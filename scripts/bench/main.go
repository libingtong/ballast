// Command bench is the external timer of plan B10 (ported from proto/m0c/bench).
//
// It spawns the real ballast binary (`ballast hook --client X --event E`) from
// this parent process and times every run from process start to process exit
// (cmd.Run, so process creation, Go runtime start-up, package init and the
// whole hook pipeline are all inside the measured interval). Each scenario
// runs -warm warm-up iterations and then -n measured ones; p95 is the
// nearest rank ceil(0.95*n).
//
// Gate (per scenario, all three must hold):
//
//   - Bad == 0     every measured and every warm-up run exited 0 with empty
//     stderr and with the stdout the scenario expects;
//   - samples complete     exactly n*procs samples were recorded;
//   - p95 <= 30ms.
//
// A run is only green when every scenario of a full run passes, every
// supported client has at least one PreToolUse scenario, and no scenario is
// missing. The exit code is 0 only then (1 gate failed, 2 usage/setup error).
//
// Isolation: every scenario gets its own BALLAST_HOME under -base, and the
// child environment is built from scratch (only PATH and the Windows system
// variables are inherited); HOME, CLAUDE_CONFIG_DIR, CODEX_HOME, HERMES_HOME,
// BALLAST_INSTALL_ROOT, BALLAST_MANAGED_DIR and GIT_CONFIG_GLOBAL all point
// inside -base. Nothing outside -base is written; the network is never used.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// productBudget is the gate of plan B10 (architecture.md section 9). It is
	// the default and what developer machines and the Linux CI job enforce.
	// It is deliberately not a flag; the only way to move it is the
	// BALLAST_BENCH_P95_MS environment variable (see parseBudget), which can
	// only raise it, for CI platforms with a measured, documented floor.
	productBudget = 30 * time.Millisecond

	defaultN    = 300
	defaultWarm = 20

	// runTimeout bounds a single spawned run; the hook's own watchdog is
	// 150ms, so this only fires when the process hangs for real.
	runTimeout = 20 * time.Second
)

// budget is the p95 bound in force for this run: productBudget unless a CI
// platform raised it through BALLAST_BENCH_P95_MS. heavyBudget is the bound of
// the scenarios that scan the whole work tree (see heavyScenarios): equal to
// budget unless BALLAST_BENCH_P95_HEAVY_MS raised it further.
var budget, heavyBudget = productBudget, productBudget

// heavyScenarios walk every file of a 1k / 10k file work tree (the S-form
// snapshot of `git commit -a` and the freshness check of a Stop claim), so
// their time is dominated by the file system, not by the hook: on a hosted
// Windows runner (Defender scans every stat/read) they cost 2-4x the others.
// Giving them their own bound keeps the bound of every other scenario tight.
var heavyScenarios = map[string]bool{"6c": true, "6d": true, "8b": true, "8c": true}

// limitFor returns the p95 bound of a result.
func limitFor(r *result) time.Duration {
	if r.Heavy {
		return heavyBudget
	}
	return budget
}

// ---- scenario model ----

// runCtx is the per-scenario environment handed to payload and setup hooks.
type runCtx struct {
	home string // BALLAST_HOME of the scenario
	work string // plain (non-git) working directory holding target.go
	root string // module root (for rules/)
}

type scenario struct {
	id     string // stable short id used by -only and the report ("1", "6a", "C-cx-patch")
	name   string
	group  string // core | extra | client
	client string
	event  string
	procs  int    // concurrent processes per iteration (default 1)
	cwd    string // "work" (default) or "repo1k" / "repo10k"
	// expect validates stdout: "" or "empty" means stdout must be empty,
	// otherwise stdout must contain the substring.
	expect  string
	setup   func(rc *runCtx, it int)
	payload func(rc *runCtx, it, proc int) []byte
	// post runs after the scenario and may return notes plus extra problems
	// (each problem counts as one Bad).
	post func(rc *runCtx, runs int) (notes []string, problems []string)
	// hitRule, when set, requires an audit "hit" record of that rule for the
	// measured runs (proves the intended code path really ran).
	hitRule string
	preTool bool // counts as the client's PreToolUse coverage
}

type result struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Group    string   `json:"group"`
	Client   string   `json:"client"`
	Event    string   `json:"event"`
	N        int      `json:"n"`
	Warm     int      `json:"warm"`
	Procs    int      `json:"procs"`
	Want     int      `json:"samples_expected"`
	Samples  int      `json:"samples"`
	P50      float64  `json:"p50_ms"`
	P95      float64  `json:"p95_ms"`
	Max      float64  `json:"max_ms"`
	Min      float64  `json:"min_ms"`
	Mean     float64  `json:"mean_ms"`
	Bad      int      `json:"bad"`
	WarmBad  int      `json:"warm_bad"`
	Pass     bool     `json:"pass"`
	Failures []string `json:"failures,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	LoadFrom string   `json:"load_start"`
	LoadTo   string   `json:"load_end"`
	// Heavy: a whole-work-tree scenario, judged against heavyBudget.
	Heavy bool `json:"heavy,omitempty"`
}

// processFloor is p95 of a bare `ballast version` run (process creation, Go
// runtime and package initialisation, no hook pipeline), measured once per
// run and written to the report. It documents how much of a scenario's time
// the platform spends before the hook starts: ~1.5 ms on a Linux runner but
// ~12-13 ms on the hosted macOS and Windows runners. It is informational: the
// bound is budget, not floor-relative (a Windows hook costs ~30 ms more than
// its floor, so a floor-relative bound would not fit there either).
var processFloor time.Duration

// BenchP95MsEnv raises the p95 bound above the product's 30 ms. Unset or empty
// means 30 ms. CI sets it per platform where the same binary measurably cannot
// reach 30 ms on a hosted runner (3-vCPU macOS virtual machine; Windows with
// Defender scanning every file operation), to a fixed number derived from the
// measurements in README.md, so that an order-of-magnitude regression still
// fails there. It can only relax (values below 30 are rejected, as is anything
// that is not a whole number of milliseconds up to maxBudgetMs); a bad value is
// a usage error rather than silently ignored.
const BenchP95MsEnv = "BALLAST_BENCH_P95_MS"

// BenchP95HeavyMsEnv is the same for the heavyScenarios only; it defaults to
// BALLAST_BENCH_P95_MS and cannot be below it.
const BenchP95HeavyMsEnv = "BALLAST_BENCH_P95_HEAVY_MS"

const maxBudgetMs = 1000

func parseBudget(v string) (time.Duration, error) { return parseBound(BenchP95MsEnv, v, productBudget) }

// parseBound parses one of the budget variables: empty means floor, anything
// else must be a whole number of milliseconds in floor..maxBudgetMs.
func parseBound(name, v string, floor time.Duration) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return floor, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: not a whole number of milliseconds", name, v)
	}
	d := time.Duration(n) * time.Millisecond
	if d < floor || n > maxBudgetMs {
		return 0, fmt.Errorf("%s=%d: must be %d..%d (it can only relax the bound)", name, n, floor/time.Millisecond, maxBudgetMs)
	}
	return d, nil
}

// budgetText describes the p95 bound for the reports.
func budgetText() string {
	if budget == productBudget && heavyBudget == productBudget {
		return fmt.Sprintf("%dms", budget/time.Millisecond)
	}
	t := fmt.Sprintf("%dms(本环境的平台上限,产品门槛 %dms;%s", budget/time.Millisecond, productBudget/time.Millisecond, BenchP95MsEnv)
	if heavyBudget != budget {
		t += fmt.Sprintf(";遍历整个工作区的场景 6c/6d/8b/8c 为 %dms,%s", heavyBudget/time.Millisecond, BenchP95HeavyMsEnv)
	}
	return t + ")"
}

// gate applies the pass/fail rules to a finished scenario. It is the only
// place that decides pass/fail (so a partly failed spawn can never read as
// green: M0c audit finding).
func gate(r *result) {
	r.Failures = nil
	if r.Bad != 0 {
		r.Failures = append(r.Failures, fmt.Sprintf("Bad=%d (must be 0)", r.Bad))
	}
	if r.WarmBad != 0 {
		r.Failures = append(r.Failures, fmt.Sprintf("warm-up Bad=%d (must be 0)", r.WarmBad))
	}
	if r.Samples != r.Want || r.Want == 0 {
		r.Failures = append(r.Failures, fmt.Sprintf("samples=%d, expected %d", r.Samples, r.Want))
	}
	if limit := limitFor(r); r.Samples > 0 && ms2dur(r.P95) > limit {
		r.Failures = append(r.Failures, fmt.Sprintf("p95=%.2fms > %dms", r.P95, limit/time.Millisecond))
	}
	r.Pass = len(r.Failures) == 0
}

func ms2dur(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }
func ms(d time.Duration) float64      { return float64(d.Microseconds()) / 1000 }

// rank is the nearest-rank percentile: the ceil(q*n)-th smallest sample.
func rank(sorted []time.Duration, q float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	i := int(math.Ceil(q*float64(n))) - 1
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	return sorted[i]
}

func summarize(s []time.Duration) (p50, p95, max, min, mean time.Duration) {
	if len(s) == 0 {
		return
	}
	c := append([]time.Duration(nil), s...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	var sum time.Duration
	for _, d := range c {
		sum += d
	}
	return rank(c, 0.5), rank(c, 0.95), c[len(c)-1], c[0], sum / time.Duration(len(c))
}

// ---- payload helpers ----

func mustJSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func uid(tag string, it, proc int) string { return fmt.Sprintf("tu-%s-%d-%d", tag, it, proc) }

// claudeBase is the common Claude Code payload head.
func claudeBase(cwd, sess, event string) map[string]any {
	return map[string]any{"session_id": sess, "cwd": cwd, "hook_event_name": event,
		"transcript_path": filepath.Join(cwd, "none.jsonl"), "permission_mode": "default"}
}

func claudeBash(cwd, sess, id, command string) []byte {
	m := claudeBase(cwd, sess, "PreToolUse")
	m["tool_name"] = "Bash"
	m["tool_use_id"] = id
	m["tool_input"] = map[string]any{"command": command, "description": "bench"}
	return mustJSON(m)
}

const shortCmd = "go test ./... 2>&1 | tail -20 && git status --short"

// bigStatements is a ~100KB command made of ~2500 statements (the parser's
// worst case); bigHeredoc is the same size as one heredoc.
func bigStatements() string {
	var sb strings.Builder
	for i := 0; sb.Len() < 100*1024; i++ {
		fmt.Fprintf(&sb, "printf '%%s\\n' item_%05d >> /tmp/bench_out_%d.log; ", i, i%7)
	}
	return sb.String()
}

func bigHeredoc() string {
	var sb strings.Builder
	sb.WriteString("cat <<'EOF' > /tmp/bench_big.txt\n")
	for i := 0; sb.Len() < 100*1024; i++ {
		fmt.Fprintf(&sb, "line %06d: the quick brown fox jumps over the lazy dog\n", i)
	}
	sb.WriteString("EOF\n")
	return sb.String()
}

const patchText = "*** Begin Patch\n*** Update File: target.go\n@@\n-package main\n+package main // changed\n*** End Patch\n"

// ---- scenario table ----

func scenarios() []scenario {
	var bigS, bigH string
	big := func() string {
		if bigS == "" {
			bigS = bigStatements()
		}
		return bigS
	}
	heredoc := func() string {
		if bigH == "" {
			bigH = bigHeredoc()
		}
		return bigH
	}
	bashShort := func(tag string) func(rc *runCtx, it, proc int) []byte {
		return func(rc *runCtx, it, proc int) []byte {
			return claudeBash(rc.work, "bench-sess", uid(tag, it, proc), shortCmd)
		}
	}
	commit := func(id, name, cwd, cmd string) scenario {
		return scenario{id: id, name: name, group: "core", client: "claude-code", event: "PreToolUse",
			cwd: cwd, expect: "deny", hitRule: "evidence-before-commit", preTool: true,
			payload: func(rc *runCtx, it, proc int) []byte {
				return claudeBash(rc.repoFor(cwd), "bench-sess", uid(id, it, proc), cmd)
			}}
	}
	stopClaim := func(id, name, cwd string) scenario {
		return scenario{id: id, name: name, group: "extra", client: "claude-code", event: "Stop",
			cwd: cwd, expect: "empty", hitRule: "claim-needs-evidence",
			payload: func(rc *runCtx, it, proc int) []byte {
				m := claudeBase(rc.repoFor(cwd), "bench-sess", "Stop")
				m["stop_hook_active"] = false
				m["last_assistant_message"] = "已完成登录功能的实现,所有测试均已通过。"
				return mustJSON(m)
			}}
	}
	return []scenario{
		{id: "1", name: "SessionStart", group: "core", client: "claude-code", event: "SessionStart",
			expect: "additionalContext",
			payload: func(rc *runCtx, it, proc int) []byte {
				m := claudeBase(rc.work, fmt.Sprintf("sess-%d", it), "SessionStart")
				m["source"] = "startup"
				return mustJSON(m)
			}},
		{id: "2", name: "UserPromptSubmit", group: "core", client: "claude-code", event: "UserPromptSubmit",
			expect: "additionalContext",
			payload: func(rc *runCtx, it, proc int) []byte {
				m := claudeBase(rc.work, "bench-sess", "UserPromptSubmit")
				m["prompt"] = "请实现登录功能,并补充对应的单元测试,完成后告诉我结果。"
				return mustJSON(m)
			}},
		{id: "3", name: "PreToolUse Edit", group: "core", client: "claude-code", event: "PreToolUse",
			expect: "deny", preTool: true, hitRule: "read-before-edit",
			payload: func(rc *runCtx, it, proc int) []byte {
				m := claudeBase(rc.work, "bench-sess", "PreToolUse")
				m["tool_name"] = "Edit"
				m["tool_use_id"] = uid("edit", it, proc)
				m["tool_input"] = map[string]any{"file_path": filepath.Join(rc.work, "target.go"),
					"old_string": "package main", "new_string": "package main // changed"}
				return mustJSON(m)
			}},
		{id: "4", name: "PreToolUse Bash 短命令", group: "core", client: "claude-code", event: "PreToolUse",
			expect: "empty", preTool: true, payload: bashShort("bashshort")},
		{id: "5", name: "PreToolUse Bash 100KB 命令(约 2500 条语句)", group: "core", client: "claude-code", event: "PreToolUse",
			expect: "empty", preTool: true,
			payload: func(rc *runCtx, it, proc int) []byte {
				return claudeBash(rc.work, "bench-sess", uid("big", it, proc), big())
			}},
		commit("6a", "git commit 检查,1k 文件仓库(C)", "repo1k", `git commit -m "feat: x"`),
		commit("6b", "git commit 检查,10k 文件仓库(C)", "repo10k", `git commit -m "feat: x"`),
		{id: "7", name: "PostToolUse(Read)", group: "core", client: "claude-code", event: "PostToolUse",
			expect: "empty",
			payload: func(rc *runCtx, it, proc int) []byte {
				m := claudeBase(rc.work, "bench-sess", "PostToolUse")
				m["tool_name"] = "Read"
				m["tool_use_id"] = uid("post", it, proc)
				m["tool_input"] = map[string]any{"file_path": filepath.Join(rc.work, fmt.Sprintf("f%02d.go", it%64))}
				m["tool_response"] = map[string]any{"type": "text", "file": map[string]any{"content": strings.Repeat("func main() {}\n", 140)}}
				return mustJSON(m)
			}},
		{id: "8", name: "Stop(无完成声明)", group: "core", client: "claude-code", event: "Stop",
			expect: "empty",
			payload: func(rc *runCtx, it, proc int) []byte {
				m := claudeBase(rc.work, "bench-sess", "Stop")
				m["stop_hook_active"] = false
				m["last_assistant_message"] = "我已经修改了登录模块,现在还差集成测试需要你确认环境。"
				return mustJSON(m)
			}},
		{id: "9", name: "12 条规则且缓存冷启动", group: "core", client: "claude-code", event: "PreToolUse",
			expect: "empty", preTool: true, payload: bashShort("cold"),
			setup: func(rc *runCtx, it int) { _ = os.RemoveAll(filepath.Join(rc.home, "cache")) }},
		{id: "10", name: "4 个进程并发争锁", group: "core", client: "claude-code", event: "PreToolUse",
			procs: 4, expect: "empty", preTool: true, payload: bashShort("lock"), post: lockCheck},
		{id: "11", name: "损坏的状态文件", group: "core", client: "claude-code", event: "PreToolUse",
			expect: "empty", preTool: true, payload: bashShort("corrupt"), setup: corruptState, post: corruptCheck},

		// Supplementary scenarios.
		{id: "5b", name: "PreToolUse Bash 100KB heredoc", group: "extra", client: "claude-code", event: "PreToolUse",
			expect: "empty", preTool: true,
			payload: func(rc *runCtx, it, proc int) []byte {
				return claudeBash(rc.work, "bench-sess", uid("heredoc", it, proc), heredoc())
			}},
		commit("6c", "git commit -a 检查,1k 文件仓库(S,工作区快照)", "repo1k", `git commit -am "feat: x"`),
		commit("6d", "git commit -a 检查,10k 文件仓库(S,工作区快照)", "repo10k", `git commit -am "feat: x"`),
		stopClaim("8b", "Stop 含完成声明,1k 文件仓库(verify_fresh)", "repo1k"),
		stopClaim("8c", "Stop 含完成声明,10k 文件仓库(verify_fresh)", "repo10k"),

		// One PreToolUse scenario per supported --client besides claude-code
		// (scenarios 3/4/5/9/10/11 cover claude-code).
		{id: "C-codex-patch", name: "codex PreToolUse apply_patch(未读文件)", group: "client", client: "codex", event: "PreToolUse",
			expect: "deny", preTool: true, hitRule: "read-before-edit",
			payload: func(rc *runCtx, it, proc int) []byte {
				return mustJSON(map[string]any{"session_id": "bench-sess", "cwd": rc.work, "tool_use_id": uid("cxp", it, proc),
					"tool_name": "apply_patch", "tool_input": map[string]any{"input": patchText}})
			}},
		{id: "C-codex-exec", name: "codex PreToolUse exec_command 短命令", group: "client", client: "codex", event: "PreToolUse",
			expect: "empty", preTool: true,
			payload: func(rc *runCtx, it, proc int) []byte {
				return mustJSON(map[string]any{"session_id": "bench-sess", "cwd": rc.work, "tool_use_id": uid("cxe", it, proc),
					"tool_name": "exec_command", "tool_input": map[string]any{"cmd": shortCmd}})
			}},
		{id: "C-cursor-shell", name: "cursor beforeShellExecution(sleep 命中)", group: "client", client: "cursor", event: "beforeShellExecution",
			expect: `"permission":"deny"`, preTool: true, hitRule: "no-long-sleep",
			payload: func(rc *runCtx, it, proc int) []byte {
				return mustJSON(map[string]any{"conversation_id": "bench-sess", "cwd": rc.work, "command": "sleep 300"})
			}},
		{id: "C-cursor-pre", name: "cursor preToolUse Shell 短命令", group: "client", client: "cursor", event: "preToolUse",
			expect: "empty", preTool: true,
			payload: func(rc *runCtx, it, proc int) []byte {
				return mustJSON(map[string]any{"conversation_id": "bench-sess", "cwd": rc.work, "tool_use_id": uid("cup", it, proc),
					"tool_name": "Shell", "tool_input": map[string]any{"command": shortCmd}})
			}},
		{id: "C-hermes", name: "hermes pre_tool_call terminal(sleep 命中)", group: "client", client: "hermes", event: "pre_tool_call",
			expect: `"decision":"block"`, preTool: true, hitRule: "no-long-sleep",
			payload: func(rc *runCtx, it, proc int) []byte {
				return mustJSON(map[string]any{"session_id": "bench-sess", "cwd": rc.work,
					"tool_name": "terminal", "tool_input": map[string]any{"command": "sleep 300"}})
			}},
	}
}

func (rc *runCtx) repoFor(cwd string) string {
	if cwd == "repo10k" {
		return rc.repo10k()
	}
	return rc.repo1k()
}

// The repo paths are resolved by name so the scenario table stays static.
var repoPaths = map[string]string{}

func (rc *runCtx) repo1k() string  { return repoPaths["repo1k"] }
func (rc *runCtx) repo10k() string { return repoPaths["repo10k"] }

// ---- scenario hooks ----

func stateFiles(home string) []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(home, "state"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func corruptState(rc *runCtx, it int) {
	for _, f := range stateFiles(rc.home) {
		_ = os.WriteFile(f, []byte(`{"v":1,"event_seq":7,"seen":["a","b`), 0o600)
	}
}

func readAudit(home string) []map[string]any {
	fs, _ := filepath.Glob(filepath.Join(home, "audit", "*.jsonl"))
	var out []map[string]any
	for _, f := range fs {
		b, _ := os.ReadFile(f)
		for _, ln := range bytes.Split(b, []byte("\n")) {
			if len(ln) == 0 {
				continue
			}
			var m map[string]any
			if json.Unmarshal(ln, &m) == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

func diagName(r map[string]any) string {
	if r["type"] != "diag" {
		return ""
	}
	switch d := r["detail"].(type) {
	case map[string]any:
		s, _ := d["diag"].(string)
		return s
	}
	return ""
}

func countDiag(recs []map[string]any, name string) int {
	n := 0
	for _, r := range recs {
		if diagName(r) == name {
			n++
		}
	}
	return n
}

// corruptCheck: the corrupt state file must have been detected and reset on
// every run after the first (setup corrupts an existing file); fewer
// detections would mean the scenario did not test the corrupt-state path.
func corruptCheck(rc *runCtx, runs int) ([]string, []string) {
	ids := map[string]bool{}
	for _, r := range readAudit(rc.home) {
		if diagName(r) == "state_corrupt" {
			ids[fmt.Sprint(r["tool_use_id"])] = true
		}
	}
	notes := []string{fmt.Sprintf("%d 次执行中检测到并重置损坏状态 %d 次", runs, len(ids))}
	var problems []string
	if len(ids) < runs-1 {
		problems = append(problems, fmt.Sprintf("损坏状态只被检测到 %d 次,至少应为 %d 次", len(ids), runs-1))
	}
	return notes, problems
}

// lockCheck: concurrent runs must not lose updates. Every run either applied
// its state transaction (counted by event_seq in the state file) or reported
// a lock timeout / duplicate diag; applied + timeouts must cover all runs.
func lockCheck(rc *runCtx, runs int) ([]string, []string) {
	recs := readAudit(rc.home)
	timeouts := countDiag(recs, "lock_timeout")
	var seq int64
	for _, f := range stateFiles(rc.home) {
		b, _ := os.ReadFile(f)
		var s struct {
			Seq int64 `json:"event_seq"`
		}
		if json.Unmarshal(b, &s) == nil && s.Seq > seq {
			seq = s.Seq
		}
	}
	notes := []string{fmt.Sprintf("共 %d 次执行:状态 event_seq=%d,锁超时 diag=%d", runs, seq, timeouts)}
	var problems []string
	if int(seq)+timeouts != runs {
		problems = append(problems, fmt.Sprintf("丢失更新:event_seq(%d)+锁超时(%d) != 执行次数(%d)", seq, timeouts, runs))
	}
	return notes, problems
}

// ---- environment ----

func baseEnv(base, home, work string) []string {
	root := filepath.Join(base, "fakehome")
	env := []string{
		"HOME=" + root, "USERPROFILE=" + root,
		"BALLAST_HOME=" + home,
		"BALLAST_INSTALL_ROOT=" + root,
		"BALLAST_MANAGED_DIR=" + filepath.Join(base, "managed"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(root, ".claude"),
		"CODEX_HOME=" + filepath.Join(root, ".codex"),
		"HERMES_HOME=" + filepath.Join(root, ".hermes"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, ".config"),
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"LANG=en_US.UTF-8", "GOPROXY=off",
	}
	// BALLAST_TEST_WATCHDOG_MS is the CI-only relaxation of the 150 ms
	// watchdog; only a binary built with -tags ballast_test honours it
	// (scripts/bench.sh builds that way when the variable is set).
	for _, k := range []string{"PATH", "SYSTEMROOT", "SystemRoot", "COMSPEC", "PATHEXT", "TMPDIR", "TEMP", "TMP", "BALLAST_TEST_WATCHDOG_MS"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// spawn runs one ballast process and returns its wall time.
func spawn(bin string, args []string, env []string, dir string, stdin []byte) (time.Duration, int, []byte, string) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &ob, &eb
	t := time.Now()
	err := cmd.Run()
	d := time.Since(t)
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return d, code, ob.Bytes(), eb.String()
}

// measureFloor times a bare `ballast version` (see processFloor) and returns
// its p95; 0 when the runs fail. The value is only written to the report.
func measureFloor(o *options) time.Duration {
	home := filepath.Join(o.base, "homes", "floor")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return 0
	}
	env := baseEnv(o.base, home, filepath.Join(o.base, "work"))
	var samples []time.Duration
	for i := 0; i < 70; i++ {
		d, code, _, _ := spawn(o.bin, []string{"version"}, env, filepath.Join(o.base, "work"), nil)
		if code != 0 {
			return 0
		}
		if i >= 10 {
			samples = append(samples, d)
		}
	}
	_, p95, _, _, _ := summarize(samples)
	fmt.Fprintf(os.Stderr, "bench: process floor (ballast version) p95 = %.2f ms\n", ms(p95))
	return p95
}

func okOutput(expect string, out []byte) bool {
	if expect == "" || expect == "empty" {
		return len(out) == 0
	}
	return bytes.Contains(out, []byte(expect))
}

type oneRun struct {
	d    time.Duration
	code int
	out  []byte
	err  string
}

func (o oneRun) bad(expect string) bool {
	return o.code != 0 || o.err != "" || !okOutput(expect, o.out)
}

func runScenario(sc scenario, o *options, rules []byte) result {
	home := filepath.Join(o.base, "homes", strings.ReplaceAll(sc.id, "/", "_"))
	_ = os.RemoveAll(home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return failedSetup(sc, err)
	}
	if err := os.WriteFile(filepath.Join(home, "rules.json"), rules, 0o600); err != nil {
		return failedSetup(sc, err)
	}
	// A fresh gc.stamp keeps SessionStart from spawning the detached
	// `ballast gc` (a once-a-day side effect that would race the clean-up).
	_ = os.WriteFile(filepath.Join(home, "gc.stamp"), nil, 0o600)

	work := filepath.Join(o.base, "work")
	rc := &runCtx{home: home, work: work, root: o.root}
	env := append(baseEnv(o.base, home, work), o.extraEnv...)
	procs := sc.procs
	if procs == 0 {
		procs = 1
	}
	cwd := work
	switch sc.cwd {
	case "repo1k":
		cwd = repoPaths["repo1k"]
	case "repo10k":
		cwd = repoPaths["repo10k"]
	}
	args := []string{"hook", "--client", sc.client, "--event", sc.event}

	res := result{ID: sc.id, Name: sc.name, Group: sc.group, Client: sc.client, Event: sc.event,
		N: o.n, Warm: o.warm, Procs: procs, Want: o.n * procs, LoadFrom: loadAvg()}
	var samples []time.Duration
	total := o.warm + o.n
	for it := 0; it < total; it++ {
		if sc.setup != nil {
			sc.setup(rc, it)
		}
		outs := make([]oneRun, procs)
		if procs == 1 {
			d, c, so, se := spawn(o.bin, args, env, cwd, sc.payload(rc, it, 0))
			outs[0] = oneRun{d, c, so, se}
		} else {
			var wg sync.WaitGroup
			start := make(chan struct{})
			for p := 0; p < procs; p++ {
				wg.Add(1)
				go func(p int) {
					defer wg.Done()
					pl := sc.payload(rc, it, p)
					<-start
					d, c, so, se := spawn(o.bin, args, env, cwd, pl)
					outs[p] = oneRun{d, c, so, se}
				}(p)
			}
			close(start)
			wg.Wait()
		}
		for _, r := range outs {
			bad := r.bad(sc.expect)
			if it < o.warm {
				if bad {
					res.WarmBad++
					noteBad(&res, r)
				}
				continue
			}
			samples = append(samples, r.d)
			if bad {
				res.Bad++
				noteBad(&res, r)
			}
		}
	}
	res.LoadTo = loadAvg()
	p50, p95, mx, mn, mean := summarize(samples)
	res.Samples = len(samples)
	res.P50, res.P95, res.Max, res.Min, res.Mean = ms(p50), ms(p95), ms(mx), ms(mn), ms(mean)

	// Post checks: every problem is one Bad, so it fails the gate.
	if sc.post != nil {
		notes, problems := sc.post(rc, total*procs)
		res.Notes = append(res.Notes, notes...)
		for _, p := range problems {
			res.Bad++
			res.Notes = append(res.Notes, "问题: "+p)
		}
	}
	if sc.hitRule != "" {
		recs := readAudit(home)
		hits := 0
		for _, r := range recs {
			if r["type"] == "hit" && fmt.Sprint(r["rule_id"]) == sc.hitRule {
				hits++
			}
		}
		res.Notes = append(res.Notes, fmt.Sprintf("审计中 %s 命中 %d 次(执行 %d 次)", sc.hitRule, hits, total*procs))
		if hits == 0 {
			res.Bad++
			res.Notes = append(res.Notes, "问题: 预期规则 "+sc.hitRule+" 没有任何命中记录")
		}
	}
	res.Heavy = heavyScenarios[sc.id]
	if limit := limitFor(&res); limit != productBudget && ms2dur(res.P95) > productBudget {
		res.Notes = append(res.Notes, fmt.Sprintf("p95 %.2fms 超过产品门槛 %dms,在本环境的平台上限 %dms 之内", res.P95, productBudget/time.Millisecond, limit/time.Millisecond))
	}
	gate(&res)
	return res
}

func noteBad(res *result, r oneRun) {
	if len(res.Notes) < 3 {
		res.Notes = append(res.Notes, fmt.Sprintf("异常样例: exit=%d out=%.120q err=%.160q", r.code, r.out, r.err))
	}
}

func failedSetup(sc scenario, err error) result {
	r := result{ID: sc.id, Name: sc.name, Group: sc.group, Client: sc.client, Event: sc.event, Bad: 1,
		Notes: []string{"场景准备失败: " + err.Error()}}
	gate(&r)
	return r
}

// ---- repositories ----

func gitCmd(base, dir string, args ...string) error {
	full := append([]string{"-c", "user.name=bench", "-c", "user.email=bench@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "gc.auto=0"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = baseEnv(base, filepath.Join(base, "homes", "gen"), dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func gitOut(base, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = baseEnv(base, filepath.Join(base, "homes", "gen"), dir)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

const projectJSON = `{
  "version": 1,
  "verify": [{"name": "test", "argv": ["go", "version"], "timeout_s": 60, "required": true}],
  "exempt": ["**/*.md", "docs/**"]
}
`

// ensureRepo creates (or reuses) a repository of total tracked files: about
// 80% .go, 10% .md and 10% docs/** files in 100 top-level directories, with
// 5 unstaged modifications left in the work tree (so `commit -a` snapshots
// have something to hash).
func ensureRepo(base, name string, total int) (string, error) {
	dir := filepath.Join(base, "repos", name)
	marker := filepath.Join(dir, ".bench-ok")
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == strconv.Itoa(total) {
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := gitCmd(base, dir, "init", "-q", "-b", "main"); err != nil {
		return "", err
	}
	var tracked []string
	for i := 0; i < total; i++ {
		d := fmt.Sprintf("pkg%03d/sub%02d", i%100, (i/100)%10)
		var f, content string
		switch i % 10 {
		case 0:
			f, content = fmt.Sprintf("%s/README%d.md", d, i), fmt.Sprintf("# doc %d\n", i)
		case 1:
			f = fmt.Sprintf("docs/%s/note%d.txt", d, i)
			content = fmt.Sprintf("note %d\n", i)
		default:
			f = fmt.Sprintf("%s/f%d.go", d, i)
			content = fmt.Sprintf("package %s\n\nfunc F%d() int { return %d }\n", filepath.Base(filepath.Dir(f)), i, i)
		}
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
		tracked = append(tracked, f)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ballast"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, ".ballast", "project.json"), []byte(projectJSON), 0o644); err != nil {
		return "", err
	}
	if err := gitCmd(base, dir, "add", "-A"); err != nil {
		return "", err
	}
	if err := gitCmd(base, dir, "commit", "-q", "-m", "init"); err != nil {
		return "", err
	}
	// Let the index timestamps age past the racy-git window, then refresh
	// so entries have no racy risk, then dirty five tracked files.
	time.Sleep(2100 * time.Millisecond)
	if err := gitCmd(base, dir, "update-index", "--refresh", "-q"); err != nil {
		return "", err
	}
	touched := 0
	for _, f := range tracked {
		if touched == 5 {
			break
		}
		if strings.HasSuffix(f, ".go") && touched < 3 || strings.HasSuffix(f, ".md") && touched == 3 || strings.HasPrefix(f, "docs/") && touched == 4 {
			p := filepath.Join(dir, filepath.FromSlash(f))
			fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				return "", err
			}
			_, _ = fh.WriteString("// local edit\n")
			_ = fh.Close()
			touched++
		}
	}
	if touched != 5 {
		return "", fmt.Errorf("repo %s: only %d files modified", name, touched)
	}
	n, err := gitOut(base, dir, "ls-files")
	if err != nil {
		return "", err
	}
	if got := len(strings.Split(n, "\n")); got != total+1 { // +1: .ballast/project.json
		return "", fmt.Errorf("repo %s: %d tracked files, want %d", name, got, total+1)
	}
	if err := os.WriteFile(marker, []byte(strconv.Itoa(total)), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// ---- rules ----

// twelveRules builds the user-layer rule file: the 12 rules the plan asks for
// (default pack + long-term pack + keep-pushing + claim-needs-evidence).
// read-before-edit, no-long-sleep and evidence-before-commit are promoted to
// deny so the scenarios hit the blocking render path.
func twelveRules(root string) ([]byte, error) {
	files := []string{"default.json", "optional/long-term.json", "optional/keep-pushing.json", "optional/claim-needs-evidence.json"}
	var all []map[string]any
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, "rules", filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		var pack struct {
			Rules []map[string]any `json:"rules"`
		}
		if err := json.Unmarshal(b, &pack); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, r := range pack.Rules {
			id, _ := r["id"].(string)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			switch id {
			case "read-before-edit", "no-long-sleep", "evidence-before-commit":
				r["action"] = "deny"
				delete(r, "target")
			}
			all = append(all, r)
		}
	}
	if len(all) != 12 {
		return nil, fmt.Errorf("expected 12 rules, built %d", len(all))
	}
	return json.MarshalIndent(map[string]any{"version": 1, "rules": all}, "", "  ")
}

// ---- machine info ----

func sh(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return "?"
}

func readFileTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func osRelease() string {
	for _, ln := range strings.Split(readFileTrim("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(ln, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func machineInfo() map[string]string {
	info := map[string]string{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "go": runtime.Version(), "num_cpu": strconv.Itoa(runtime.NumCPU())}
	switch runtime.GOOS {
	case "darwin":
		info["model"] = firstNonEmpty(sh("sysctl", "-n", "hw.model"))
		info["cpu"] = firstNonEmpty(sh("sysctl", "-n", "machdep.cpu.brand_string"))
		info["os"] = "macOS " + sh("sw_vers", "-productVersion") + " (" + sh("uname", "-sr") + ")"
		if mem, err := strconv.ParseInt(sh("sysctl", "-n", "hw.memsize"), 10, 64); err == nil {
			info["memory_gb"] = strconv.FormatInt(mem>>30, 10)
		}
	case "linux":
		info["model"] = firstNonEmpty(readFileTrim("/sys/devices/virtual/dmi/id/product_name"), "unknown (virtual or no DMI)")
		cpu := ""
		for _, ln := range strings.Split(readFileTrim("/proc/cpuinfo"), "\n") {
			if k, v, ok := strings.Cut(ln, ":"); ok && strings.TrimSpace(k) == "model name" {
				cpu = strings.TrimSpace(v)
				break
			}
		}
		info["cpu"] = firstNonEmpty(cpu)
		info["os"] = firstNonEmpty(osRelease()) + " (" + sh("uname", "-sr") + ")"
	case "windows":
		info["model"] = "unknown"
		info["cpu"] = firstNonEmpty(os.Getenv("PROCESSOR_IDENTIFIER"))
		info["os"] = firstNonEmpty(sh("cmd", "/c", "ver"))
	default:
		info["os"] = runtime.GOOS
	}
	info["git"] = sh("git", "--version")
	return info
}

// loadAvg returns the 1/5/15-minute load averages ("n/a" where the platform
// has no such figure). It is called outside the timed regions.
func loadAvg() string {
	switch runtime.GOOS {
	case "darwin":
		s := strings.Trim(sh("sysctl", "-n", "vm.loadavg"), "{} ")
		return firstNonEmpty(s)
	case "linux":
		f := strings.Fields(readFileTrim("/proc/loadavg"))
		if len(f) >= 3 {
			return strings.Join(f[:3], " ")
		}
	}
	return "n/a"
}

// ---- main ----

type options struct {
	bin, base, root string
	n, warm         int
	only            *regexp.Regexp
	jsonOut, mdOut  string
	list            bool
	extraEnv        []string // KEY=VALUE pairs appended to the child environment (experiments)
}

func main() {
	var o options
	var only string
	flag.StringVar(&o.bin, "bin", "", "path of the ballast binary (required)")
	flag.StringVar(&o.base, "base", "", "work directory for all temporary state (required, created if missing)")
	flag.StringVar(&o.root, "root", ".", "module root (for rules/)")
	flag.IntVar(&o.n, "n", defaultN, "measured iterations per scenario")
	flag.IntVar(&o.warm, "warm", defaultWarm, "warm-up iterations per scenario")
	flag.StringVar(&only, "only", "", "regexp on scenario ids (partial run; the report is marked incomplete)")
	flag.StringVar(&o.jsonOut, "json", "", "write the raw result JSON here")
	flag.StringVar(&o.mdOut, "md", "", "write the markdown report here")
	flag.Func("env", "extra KEY=VALUE for the ballast child environment (repeatable; recorded in the report)", func(v string) error {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("want KEY=VALUE, got %q", v)
		}
		o.extraEnv = append(o.extraEnv, v)
		return nil
	})
	flag.BoolVar(&o.list, "list", false, "list scenario ids and exit")
	flag.Parse()

	if o.list {
		for _, sc := range scenarios() {
			fmt.Printf("%-14s %-6s %-12s %s\n", sc.id, sc.group, sc.client, sc.name)
		}
		return
	}
	if o.bin == "" || o.base == "" {
		fmt.Fprintln(os.Stderr, "bench: -bin and -base are required")
		os.Exit(2)
	}
	if o.n < 1 || o.warm < 0 {
		fmt.Fprintln(os.Stderr, "bench: -n must be >= 1 and -warm >= 0")
		os.Exit(2)
	}
	if only != "" {
		re, err := regexp.Compile(only)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bench: bad -only:", err)
			os.Exit(2)
		}
		o.only = re
	}
	var err error
	if o.bin, err = filepath.Abs(o.bin); err != nil {
		fatal(err)
	}
	if o.base, err = filepath.Abs(o.base); err != nil {
		fatal(err)
	}
	if o.root, err = filepath.Abs(o.root); err != nil {
		fatal(err)
	}
	if _, err := os.Stat(o.bin); err != nil {
		fatal(fmt.Errorf("ballast binary: %w", err))
	}
	for _, d := range []string{"work", "homes", "repos", "fakehome", "managed"} {
		if err := os.MkdirAll(filepath.Join(o.base, d), 0o700); err != nil {
			fatal(err)
		}
	}
	if g := enclosingRepo(filepath.Join(o.base, "work")); g != "" {
		fatal(fmt.Errorf("-base must be outside any git repository (found %s); use a temporary directory", g))
	}
	if err := os.WriteFile(filepath.Join(o.base, "work", "target.go"), []byte("package main\n"), 0o600); err != nil {
		fatal(err)
	}
	rules, err := twelveRules(o.root)
	if err != nil {
		fatal(err)
	}
	if err := preflightLint(&o, rules); err != nil {
		fatal(err)
	}
	b, berr := parseBudget(os.Getenv(BenchP95MsEnv))
	if berr == nil {
		var h time.Duration
		if h, berr = parseBound(BenchP95HeavyMsEnv, os.Getenv(BenchP95HeavyMsEnv), b); berr == nil {
			budget, heavyBudget = b, h
		}
	}
	if berr != nil {
		fmt.Fprintln(os.Stderr, "bench:", berr)
		os.Exit(2)
	}
	processFloor = measureFloor(&o)

	all := scenarios()
	var picked []scenario
	for _, sc := range all {
		if o.only == nil || o.only.MatchString(sc.id) {
			picked = append(picked, sc)
		}
	}
	if len(picked) == 0 {
		fmt.Fprintln(os.Stderr, "bench: -only matched no scenario")
		os.Exit(2)
	}
	needRepos := false
	for _, sc := range picked {
		if sc.cwd != "" {
			needRepos = true
		}
	}
	if needRepos {
		fmt.Fprintln(os.Stderr, "bench: preparing git repositories (1k / 10k files) ...")
		for name, total := range map[string]int{"repo1k": 1000, "repo10k": 10000} {
			p, err := ensureRepo(o.base, name, total)
			if err != nil {
				fatal(err)
			}
			repoPaths[name] = p
		}
	}

	info := machineInfo()
	info["ballast"] = sh(o.bin, "version")
	if st, err := os.Stat(o.bin); err == nil {
		info["binary_bytes"] = strconv.FormatInt(st.Size(), 10)
	}
	info["load_start"] = loadAvg()
	info["process_floor_p95_ms"] = fmt.Sprintf("%.2f", ms(processFloor))
	info["n"], info["warm"] = strconv.Itoa(o.n), strconv.Itoa(o.warm)
	if len(o.extraEnv) > 0 {
		info["extra_env"] = strings.Join(o.extraEnv, " ")
	}
	if v := os.Getenv("BALLAST_TEST_WATCHDOG_MS"); v != "" {
		info["watchdog_override_ms"] = v
	}
	fmt.Printf("环境: %s | %s | %s | %s 核\n", info["model"], info["cpu"], info["os"], info["num_cpu"])
	fmt.Printf("负载(开始): %s\n\n", info["load_start"])
	fmt.Printf("| 场景 | 客户端/事件 | 样本 | p50 ms | p95 ms | max ms | Bad | 结果 |\n|---|---|---|---|---|---|---|---|\n")

	var results []result
	for _, sc := range picked {
		r := runScenario(sc, &o, rules)
		results = append(results, r)
		fmt.Printf("| %s %s | %s/%s | %d/%d | %.2f | %.2f | %.2f | %d | %s |\n",
			r.ID, r.Name, r.Client, r.Event, r.Samples, r.Want, r.P50, r.P95, r.Max, r.Bad, verdict(r))
		for _, nt := range r.Notes {
			fmt.Printf("    - %s\n", nt)
		}
		for _, f := range r.Failures {
			fmt.Printf("    - 未通过: %s\n", f)
		}
	}
	info["load_end"] = loadAvg()

	problems := coverage(all, results, o.only != nil)
	ok := len(problems) == 0
	for _, r := range results {
		ok = ok && r.Pass
	}
	for _, p := range problems {
		fmt.Printf("\n覆盖检查未通过: %s\n", p)
	}
	fmt.Printf("\n负载(结束): %s\n", info["load_end"])
	if ok {
		fmt.Printf("\n结论: 通过(%d 个场景,门槛 p95 <= %s,Bad == 0,样本齐全)\n", len(results), budgetText())
	} else {
		fmt.Printf("\n结论: 未通过\n")
	}

	complete := o.only == nil
	out := map[string]any{"env": info, "time": time.Now().Format(time.RFC3339), "complete": complete,
		"budget_ms": int(budget / time.Millisecond), "product_budget_ms": int(productBudget / time.Millisecond), "heavy_budget_ms": int(heavyBudget / time.Millisecond), "pass": ok, "results": results, "coverage_problems": problems}
	if o.jsonOut != "" {
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(o.jsonOut, b, 0o644); err != nil {
			fatal(err)
		}
	}
	if o.mdOut != "" {
		if err := os.WriteFile(o.mdOut, []byte(markdown(info, results, problems, complete, ok)), 0o644); err != nil {
			fatal(err)
		}
	}
	if !ok {
		os.Exit(1)
	}
}

// enclosingRepo returns the directory holding a .git entry at or above dir,
// or "" when there is none (the non-git work directory must really be one).
func enclosingRepo(dir string) string {
	for d := dir; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	os.Exit(2)
}

func verdict(r result) string {
	if r.Pass {
		return "通过"
	}
	return "**未通过**"
}

// preflightLint makes sure the generated rule file is valid for every
// client, so a broken rule set cannot silently turn scenarios into no-ops.
func preflightLint(o *options, rules []byte) error {
	dir := filepath.Join(o.base, "homes", "lint")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(p, rules, 0o600); err != nil {
		return err
	}
	env := baseEnv(o.base, dir, o.base)
	_, code, out, errs := spawn(o.bin, []string{"lint", "--client", "all", p}, env, o.base, nil)
	if code != 0 {
		return fmt.Errorf("ballast lint rejected the benchmark rule set (exit %d): %s %s", code, out, errs)
	}
	return nil
}

// requiredClients are the --client values that need a PreToolUse scenario.
var requiredClients = []string{"claude-code", "codex", "cursor", "hermes"}

// coverage checks the structural parts of the gate: all scenarios present
// (full run) and a PreToolUse scenario per client.
func coverage(all []scenario, got []result, partial bool) []string {
	var problems []string
	have := map[string]bool{}
	for _, r := range got {
		have[r.ID] = true
	}
	if !partial {
		for _, sc := range all {
			if !have[sc.id] {
				problems = append(problems, "缺少场景 "+sc.id)
			}
		}
		// plan B10: the 11 numbered scenarios (6 = 6a and 6b).
		for _, id := range []string{"1", "2", "3", "4", "5", "6a", "6b", "7", "8", "9", "10", "11"} {
			if !have[id] {
				problems = append(problems, "缺少 B10 场景 "+id)
			}
		}
		cover := map[string]bool{}
		for _, sc := range all {
			if sc.preTool && have[sc.id] {
				cover[sc.client] = true
			}
		}
		for _, c := range requiredClients {
			if !cover[c] {
				problems = append(problems, "客户端 "+c+" 没有 PreToolUse 场景")
			}
		}
	}
	return problems
}

func markdown(info map[string]string, rs []result, problems []string, complete, ok bool) string {
	var b strings.Builder
	st := "通过"
	if !ok {
		st = "未通过"
	}
	fmt.Fprintf(&b, "# ballast hook 性能报告\n\n结论: **%s**(门槛: 每个场景 p95 <= %s,Bad == 0,样本齐全;完整运行: %v)\n\n", st, budgetText(), complete)
	b.WriteString("## 环境\n\n| 项 | 值 |\n|---|---|\n")
	for _, k := range []string{"model", "cpu", "num_cpu", "memory_gb", "os", "goos", "goarch", "go", "git", "ballast", "binary_bytes", "extra_env", "watchdog_override_ms", "process_floor_p95_ms", "n", "warm", "load_start", "load_end"} {
		if v := info[k]; v != "" {
			fmt.Fprintf(&b, "| %s | %s |\n", k, v)
		}
	}
	b.WriteString("\n## 结果(毫秒)\n\n| 场景 | 客户端/事件 | 样本 | p50 | p95 | max | Bad | 结果 |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %s %s | %s/%s | %d/%d | %.2f | %.2f | %.2f | %d | %s |\n", r.ID, r.Name, r.Client, r.Event, r.Samples, r.Want, r.P50, r.P95, r.Max, r.Bad, verdict(r))
	}
	for _, r := range rs {
		if len(r.Notes) == 0 && len(r.Failures) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s %s\n\n负载 %s -> %s\n\n", r.ID, r.Name, r.LoadFrom, r.LoadTo)
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		for _, f := range r.Failures {
			fmt.Fprintf(&b, "- 未通过: %s\n", f)
		}
	}
	for _, p := range problems {
		fmt.Fprintf(&b, "\n覆盖检查未通过: %s\n", p)
	}
	return b.String()
}
