// Command bench 是 M0c 的外部计时器(B10 的原型版):
// 从父进程 spawn hook 二进制,计时覆盖"启动到退出"(cmd.Run 前后),
// 每场景预热 -warm 次、正式 -n 次,p95 取最近秩 ceil(0.95n),输出 p50/p95/max。
// 另有 snapshot 模式,测 v0.3 快照算法在 1k/10k 文件仓库上的耗时。
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libingtong/ballast/proto/m0c/nativetree"
)

type runEnv struct {
	home    string // BALLAST_HOME
	workdir string // 非 git 的工作目录(放 target.go 等)
	rules   string
	hookBin string
}

type scenario struct {
	name    string
	group   string // core | extra | baseline
	bin     string // hook | noop | true
	procs   int
	cwd     string // 为空则用 workdir
	setup   func(re *runEnv, it int)
	payload func(re *runEnv, it, proc int) []byte
	expect  string // "empty" | "any" | 子串
	extra   []string
	post    func(re *runEnv, total int) []string
}

type result struct {
	Name    string   `json:"name"`
	Group   string   `json:"group"`
	N       int      `json:"n"`
	Procs   int      `json:"procs"`
	Samples int      `json:"samples"`
	P50     float64  `json:"p50_ms"`
	P95     float64  `json:"p95_ms"`
	Max     float64  `json:"max_ms"`
	Min     float64  `json:"min_ms"`
	Mean    float64  `json:"mean_ms"`
	Bad     int      `json:"bad"`
	Notes   []string `json:"notes,omitempty"`
	Under30 bool     `json:"p95_le_30ms"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func rank(sorted []time.Duration, q float64) time.Duration {
	n := len(sorted)
	i := int(math.Ceil(q*float64(n))) - 1
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

func summarize(s []time.Duration) (p50, p95, max, min, mean time.Duration) {
	c := append([]time.Duration(nil), s...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	var sum time.Duration
	for _, d := range c {
		sum += d
	}
	return rank(c, 0.5), rank(c, 0.95), c[len(c)-1], c[0], sum / time.Duration(len(c))
}

// ---- 载荷构造 ----

func baseEvent(re *runEnv, cwd, sess, event string) map[string]any {
	if cwd == "" {
		cwd = re.workdir
	}
	return map[string]any{"session_id": sess, "cwd": cwd, "hook_event_name": event,
		"transcript_path": "/tmp/none.jsonl", "permission_mode": "default"}
}

func mustJSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func bashEvent(re *runEnv, cwd, sess, id, command string) []byte {
	m := baseEvent(re, cwd, sess, "PreToolUse")
	m["tool_name"] = "Bash"
	m["tool_use_id"] = id
	m["tool_input"] = map[string]any{"command": command, "description": "bench"}
	return mustJSON(m)
}

const shortCmd = "go test ./... 2>&1 | tail -20 && git status --short"

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

func uid(name string, it, proc int) string { return fmt.Sprintf("tu-%s-%d-%d", name, it, proc) }

// ---- spawn ----

func baseEnv(re *runEnv, extra []string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + re.workdir, "LANG=en_US.UTF-8",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"BALLAST_HOME=" + re.home, "BALLAST_RULES=" + re.rules}
	return append(env, extra...)
}

func spawn(bin string, env []string, dir string, stdin []byte) (time.Duration, int, []byte, string) {
	cmd := exec.Command(bin)
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
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return d, code, ob.Bytes(), eb.String()
}

func okOutput(expect string, out []byte) bool {
	switch expect {
	case "", "any":
		return true
	case "empty":
		return len(out) == 0
	}
	return bytes.Contains(out, []byte(expect))
}

func runScenario(sc scenario, base string, hookBin, noopBin, rules string, n, warm int) result {
	home := filepath.Join(base, "homes", sc.name)
	_ = os.RemoveAll(home)
	_ = os.MkdirAll(home, 0o700)
	work := filepath.Join(base, "work")
	re := &runEnv{home: home, workdir: work, rules: rules, hookBin: hookBin}
	bin := map[string]string{"hook": hookBin, "noop": noopBin, "true": "/usr/bin/true"}[sc.bin]
	env := baseEnv(re, sc.extra)
	procs := sc.procs
	if procs == 0 {
		procs = 1
	}
	res := result{Name: sc.name, Group: sc.group, N: n, Procs: procs}
	var samples []time.Duration
	total := warm + n
	for it := 0; it < total; it++ {
		if sc.setup != nil {
			sc.setup(re, it)
		}
		type one struct {
			d    time.Duration
			code int
			out  []byte
			err  string
		}
		outs := make([]one, procs)
		if procs == 1 {
			d, c, o, e := spawn(bin, env, work, sc.payload(re, it, 0))
			outs[0] = one{d, c, o, e}
		} else {
			var wg sync.WaitGroup
			start := make(chan struct{})
			for p := 0; p < procs; p++ {
				wg.Add(1)
				go func(p int) {
					defer wg.Done()
					pl := sc.payload(re, it, p)
					<-start
					d, c, o, e := spawn(bin, env, work, pl)
					outs[p] = one{d, c, o, e}
				}(p)
			}
			close(start)
			wg.Wait()
		}
		for _, o := range outs {
			if it < warm {
				continue
			}
			samples = append(samples, o.d)
			if o.code != 0 || !okOutput(sc.expect, o.out) {
				res.Bad++
				if len(res.Notes) < 3 {
					res.Notes = append(res.Notes, fmt.Sprintf("异常样例: exit=%d out=%.120q err=%.160q", o.code, o.out, o.err))
				}
			}
		}
	}
	p50, p95, mx, mn, mean := summarize(samples)
	res.Samples = len(samples)
	res.P50, res.P95, res.Max, res.Min, res.Mean = ms(p50), ms(p95), ms(mx), ms(mn), ms(mean)
	res.Under30 = res.P95 <= 30
	if sc.post != nil {
		res.Notes = append(res.Notes, sc.post(re, total*procs)...)
	}
	return res
}

// ---- 场景表 ----

func scenarios(repo1k, repo10k string) []scenario {
	corruptState := func(re *runEnv, it int) {
		ms, _ := filepath.Glob(filepath.Join(re.home, "state", "*", "*", "main.json"))
		for _, m := range ms {
			_ = os.WriteFile(m, []byte(`{"v":1,"n_events":7,"seen":["a","b`), 0o600)
		}
	}
	bashShort := func(name string) func(re *runEnv, it, proc int) []byte {
		return func(re *runEnv, it, proc int) []byte {
			return bashEvent(re, "", "bench-sess", uid(name, it, proc), shortCmd)
		}
	}
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
	gitCommit := func(name, repo, cmd, expect string) scenario {
		return scenario{name: name, group: "extra", bin: "hook", cwd: repo, expect: expect,
			payload: func(re *runEnv, it, proc int) []byte {
				return bashEvent(re, repo, "bench-sess", uid(name, it, proc), cmd)
			},
			post: snapNote}
	}
	stopClaim := func(name, repo string) scenario {
		return scenario{name: name, group: "extra", bin: "hook", expect: "systemMessage",
			payload: func(re *runEnv, it, proc int) []byte {
				m := baseEvent(re, repo, "bench-sess", "Stop")
				m["last_assistant_message"] = "已完成登录功能的实现。"
				return mustJSON(m)
			}, post: snapNote}
	}
	return []scenario{
		{name: "baseline /usr/bin/true", group: "baseline", bin: "true", expect: "any", payload: bashShort("true")},
		{name: "baseline Go 空程序", group: "baseline", bin: "noop", expect: "any", payload: bashShort("noop")},
		{name: "1 SessionStart", group: "core", bin: "hook", expect: "additionalContext",
			payload: func(re *runEnv, it, proc int) []byte {
				m := baseEvent(re, "", fmt.Sprintf("sess-%d", it), "SessionStart")
				m["source"] = "startup"
				return mustJSON(m)
			}},
		{name: "2 UserPromptSubmit", group: "core", bin: "hook", expect: "additionalContext",
			payload: func(re *runEnv, it, proc int) []byte {
				m := baseEvent(re, "", "bench-sess", "UserPromptSubmit")
				m["prompt"] = "请实现登录功能,并补充对应的单元测试,完成后告诉我结果。"
				return mustJSON(m)
			}},
		{name: "3 PreToolUse Edit", group: "core", bin: "hook", expect: "deny",
			payload: func(re *runEnv, it, proc int) []byte {
				m := baseEvent(re, "", "bench-sess", "PreToolUse")
				m["tool_name"] = "Edit"
				m["tool_use_id"] = uid("edit", it, proc)
				m["tool_input"] = map[string]any{"file_path": filepath.Join(re.workdir, "target.go"),
					"old_string": "package main", "new_string": "package main // changed"}
				return mustJSON(m)
			}},
		{name: "4 PreToolUse Bash 短命令", group: "core", bin: "hook", expect: "empty", payload: bashShort("bashshort")},
		{name: "5 Bash 100KB 命令(~2500 条语句)", group: "core", bin: "hook", expect: "empty",
			payload: func(re *runEnv, it, proc int) []byte {
				return bashEvent(re, "", "bench-sess", uid("big", it, proc), big())
			}},
		{name: "5b Bash 100KB heredoc(补充)", group: "extra", bin: "hook", expect: "empty",
			payload: func(re *runEnv, it, proc int) []byte {
				return bashEvent(re, "", "bench-sess", uid("heredoc", it, proc), heredoc())
			}},
		{name: "7 PostToolUse(Read)", group: "core", bin: "hook", expect: "empty",
			payload: func(re *runEnv, it, proc int) []byte {
				m := baseEvent(re, "", "bench-sess", "PostToolUse")
				m["tool_name"] = "Read"
				m["tool_use_id"] = uid("post", it, proc)
				m["tool_input"] = map[string]any{"file_path": filepath.Join(re.workdir, fmt.Sprintf("f%02d.go", it%64))}
				m["tool_response"] = map[string]any{"type": "text", "file": map[string]any{"content": strings.Repeat("func main() {}\n", 140)}}
				return mustJSON(m)
			}},
		{name: "8 Stop", group: "core", bin: "hook", expect: "empty",
			payload: func(re *runEnv, it, proc int) []byte {
				m := baseEvent(re, "", "bench-sess", "Stop")
				m["stop_hook_active"] = false
				m["last_assistant_message"] = "我已经修改了登录模块,现在还差集成测试需要你确认环境。"
				return mustJSON(m)
			}},
		{name: "9 12 条规则冷缓存", group: "core", bin: "hook", expect: "empty", payload: bashShort("cold"),
			setup: func(re *runEnv, it int) { _ = os.RemoveAll(filepath.Join(re.home, "cache")) },
			post: func(re *runEnv, total int) []string {
				return []string{"每次迭代前删除 cache/ 目录;hook 因此读 JSON、编译全部正则与 glob、编码 gob、写缓存"}
			}},
		{name: "10 4 进程并发争锁", group: "core", bin: "hook", procs: 4, expect: "empty", payload: bashShort("lock"),
			post: func(re *runEnv, total int) []string {
				return lockCheck(re, total)
			}},
		{name: "11 损坏的状态文件", group: "core", bin: "hook", expect: "empty", payload: bashShort("corrupt"), setup: corruptState,
			post: func(re *runEnv, total int) []string {
				a := readAudit(re.home)
				c := 0
				for _, r := range a {
					if r["type"] == "eval_summary" && r["state_corrupt"] == true {
						c++
					}
				}
				return []string{fmt.Sprintf("审计中 state_corrupt=true 的行数 %d / 共 %d 次执行", c, total)}
			}},
		// 补充场景
		{name: "X1 Bash 短命令 + 状态 fsync", group: "extra", bin: "hook", expect: "empty", payload: bashShort("fsync"),
			extra: []string{"BALLAST_FSYNC=1"}},
		gitCommit("6a git commit 检查 1k 文件(C)", repo1k, `git commit -m "feat: x"`, "deny"),
		gitCommit("6b git commit 检查 10k 文件(C)", repo10k, `git commit -m "feat: x"`, "deny"),
		gitCommit("6c git commit -a 检查 1k 文件(S)", repo1k, `git commit -am "feat: x"`, "deny"),
		gitCommit("6d git commit -a 检查 10k 文件(S)", repo10k, `git commit -am "feat: x"`, "deny"),
		withEnv(gitCommit("6e git commit 检查 1k 文件(C, 原生索引解析,实验)", repo1k, `git commit -m "feat: x"`, "deny"), "BALLAST_SNAPSHOT_MODE=native"),
		withEnv(gitCommit("6f git commit 检查 10k 文件(C, 原生索引解析,实验)", repo10k, `git commit -m "feat: x"`, "deny"), "BALLAST_SNAPSHOT_MODE=native"),
		stopClaim("8b Stop 含完成声明 1k 文件(S)", repo1k),
		stopClaim("8c Stop 含完成声明 10k 文件(S)", repo10k),
	}
}

func withEnv(sc scenario, kv ...string) scenario {
	sc.extra = append(sc.extra, kv...)
	return sc
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

func snapNote(re *runEnv, total int) []string {
	a := readAudit(re.home)
	var us []float64
	for _, r := range a {
		if v, ok := r["snapshot_us"].(float64); ok {
			us = append(us, v)
		}
	}
	if len(us) == 0 {
		return []string{"审计中没有 snapshot_us(快照未执行)"}
	}
	sort.Float64s(us)
	return []string{fmt.Sprintf("hook 内快照耗时 p50=%.1fms p95=%.1fms(审计 snapshot_us,%d 次)",
		us[int(math.Ceil(0.5*float64(len(us))))-1]/1000, us[int(math.Ceil(0.95*float64(len(us))))-1]/1000, len(us))}
}

// lockCheck:并发场景下核对"无丢失更新"——成功提交的事务数必须等于状态里的 n_events。
func lockCheck(re *runEnv, total int) []string {
	a := readAudit(re.home)
	ok, to := 0, 0
	var waits []float64
	for _, r := range a {
		if r["type"] != "eval_summary" {
			continue
		}
		switch r["state"] {
		case "ok":
			ok++
		case "lock_timeout":
			to++
		}
		if w, _ := r["lock_wait_us"].(float64); w > 0 {
			waits = append(waits, w)
		}
	}
	sort.Float64s(waits)
	st := 0
	ms, _ := filepath.Glob(filepath.Join(re.home, "state", "*", "*", "main.json"))
	for _, m := range ms {
		b, _ := os.ReadFile(m)
		var s struct {
			N int `json:"n_events"`
		}
		_ = json.Unmarshal(b, &s)
		st += s.N
	}
	note := fmt.Sprintf("共 %d 次执行:状态写入成功 %d、锁超时跳过 %d;状态文件 n_events=%d;", total, ok, to, st)
	if ok == st {
		note += "无丢失更新(成功事务数 == n_events)"
	} else {
		note += "**丢失更新:成功事务数 != n_events**"
	}
	if len(waits) > 0 {
		note += fmt.Sprintf(";lock_wait_us(>0 的 %d 次)最大 %.0fus", len(waits), waits[len(waits)-1])
	}
	return []string{note}
}

// ---- 快照模式 ----

type stepStats map[string][]time.Duration

type snapResult struct {
	Repo    string                `json:"repo"`
	Files   int                   `json:"files"`
	Variant string                `json:"variant"`
	N       int                   `json:"n"`
	P50     float64               `json:"p50_ms"`
	P95     float64               `json:"p95_ms"`
	Max     float64               `json:"max_ms"`
	Steps   map[string][2]float64 `json:"steps_p50_p95_ms"`
	Tree    string                `json:"tree"`
}

func gitEnv(idx string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0"}
	if idx != "" {
		env = append(env, "GIT_INDEX_FILE="+idx)
	}
	return env
}

func gitRun(repo, idx string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = gitEnv(idx)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %v: %v: %s", args, err, e.String())
	}
	return strings.TrimSpace(o.String()), nil
}

var exemptSpecs = []string{":(glob)docs/**", ":(glob)**/*.md"}

// snapOnce 执行一次快照。literal=true 严格按 v0.3 文字(rev-parse + 外部 cp);
// literal=false 为原型优化版(直接定位 .git/index、进程内复制)。
func snapOnce(repo, tmpDir string, literal, addU bool) (string, map[string]time.Duration, time.Duration, error) {
	steps := map[string]time.Duration{}
	t0 := time.Now()
	lap := func(name string, t *time.Time) { now := time.Now(); steps[name] = now.Sub(*t); *t = now }
	t := time.Now()
	var idx string
	if literal {
		p, err := gitRun(repo, "", "rev-parse", "--git-path", "index")
		if err != nil {
			return "", nil, 0, err
		}
		idx = p
		if !filepath.IsAbs(idx) {
			idx = filepath.Join(repo, idx)
		}
	} else {
		idx = filepath.Join(repo, ".git", "index")
	}
	lap("locate", &t)
	tmp := filepath.Join(tmpDir, "idx.tmp")
	if literal {
		if out, err := exec.Command("cp", idx, tmp).CombinedOutput(); err != nil {
			return "", nil, 0, fmt.Errorf("cp: %v %s", err, out)
		}
	} else {
		in, err := os.Open(idx)
		if err != nil {
			return "", nil, 0, err
		}
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			in.Close()
			return "", nil, 0, err
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			return "", nil, 0, err
		}
	}
	lap("copy_index", &t)
	if addU {
		if _, err := gitRun(repo, tmp, "add", "-u"); err != nil {
			return "", nil, 0, err
		}
		lap("add_u", &t)
	}
	if _, err := gitRun(repo, tmp, append([]string{"rm", "--cached", "-r", "-q", "--ignore-unmatch", "--"}, exemptSpecs...)...); err != nil {
		return "", nil, 0, err
	}
	lap("rm_cached_exempt", &t)
	tree, err := gitRun(repo, tmp, "write-tree")
	if err != nil {
		return "", nil, 0, err
	}
	lap("write_tree", &t)
	total := time.Since(t0)
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + ".lock")
	return tree, steps, total, nil
}

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "?"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// gitBaseline:空转 git 子进程的启动成本(`git --version`、`git rev-parse --git-path index`)。
func gitBaseline(repo string, n int) {
	for _, args := range [][]string{{"--version"}, {"rev-parse", "--git-path", "index"}} {
		var ds []time.Duration
		for i := 0; i < n+3; i++ {
			t := time.Now()
			_, _ = gitRun(repo, "", args...)
			if i >= 3 {
				ds = append(ds, time.Since(t))
			}
		}
		p50, p95, mx, _, _ := summarize(ds)
		fmt.Printf("git %s 进程基线: p50=%.2fms p95=%.2fms max=%.2fms (n=%d)\n", strings.Join(args, " "), ms(p50), ms(p95), ms(mx), n)
	}
}

func benchSnapshot(label, repo, tmpDir string, n, warm int) []snapResult {
	files := 0
	if out, err := gitRun(repo, "", "ls-files"); err == nil {
		files = len(strings.Split(out, "\n"))
	}
	_ = os.MkdirAll(tmpDir, 0o700)
	// 复制索引不应动真实索引:前后比较哈希。
	before := fileSHA(filepath.Join(repo, ".git", "index"))
	type variant struct {
		name    string
		literal bool
		addU    bool
	}
	vs := []variant{
		{"S 字面版(rev-parse + cp + add -u + rm + write-tree)", true, true},
		{"C 普通 commit 字面版(rev-parse + cp + rm + write-tree)", true, false},
		{"S 优化版(直接定位 + 进程内复制)", false, true},
		{"C 普通 commit 优化版(直接定位 + 进程内复制)", false, false},
		{"C 普通 commit 原生版(Go 解析索引 + SHA-1,无子进程;实验)", false, false},
	}
	var rs []snapResult
	for _, v := range vs {
		var totals []time.Duration
		st := stepStats{}
		tree := ""
		for i := 0; i < warm+n; i++ {
			var tr string
			var steps map[string]time.Duration
			var tot time.Duration
			var err error
			if strings.Contains(v.name, "原生版") {
				t0 := time.Now()
				tr, err = nativetree.TreeID(filepath.Join(repo, ".git", "index"), []string{"docs/**", "**/*.md"})
				tot = time.Since(t0)
				steps = map[string]time.Duration{"native": tot}
			} else {
				tr, steps, tot, err = snapOnce(repo, tmpDir, v.literal, v.addU)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "snapshot error:", err)
				os.Exit(2)
			}
			tree = tr
			if i < warm {
				continue
			}
			totals = append(totals, tot)
			for k, d := range steps {
				st[k] = append(st[k], d)
			}
		}
		p50, p95, mx, _, _ := summarize(totals)
		r := snapResult{Repo: label, Files: files, Variant: v.name, N: n, P50: ms(p50), P95: ms(p95), Max: ms(mx), Tree: tree, Steps: map[string][2]float64{}}
		for k, ds := range st {
			a, b, _, _, _ := summarize(ds)
			r.Steps[k] = [2]float64{ms(a), ms(b)}
		}
		rs = append(rs, r)
	}
	// 原生版与 git 版的 tree id 必须一致。
	var gitC, natC string
	for _, r := range rs {
		if strings.HasPrefix(r.Variant, "C 普通 commit 优化版") {
			gitC = r.Tree
		}
		if strings.Contains(r.Variant, "原生版") {
			natC = r.Tree
		}
	}
	fmt.Printf("[%s] 原生 tree id 与 git write-tree %s (git=%s native=%s)\n", label, map[bool]string{true: "一致", false: "**不一致**"}[gitC == natC], gitC, natC)
	after := fileSHA(filepath.Join(repo, ".git", "index"))
	fmt.Printf("真实索引哈希 前=%s 后=%s (%s)\n", before, after, map[bool]string{true: "未被改动", false: "被改动!"}[before == after])
	return rs
}

func sh(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

func main() {
	hookBin := flag.String("hook", "", "hook 二进制路径")
	noopBin := flag.String("noop", "", "Go 空程序二进制路径")
	rules := flag.String("rules", "", "12 条规则 JSON")
	base := flag.String("base", "", "工作根目录(应位于 .m0tmp 下)")
	n := flag.Int("n", 300, "正式次数")
	warm := flag.Int("warm", 20, "预热次数")
	only := flag.String("only", "", "场景名正则过滤")
	mode := flag.String("mode", "hook", "hook | snapshot")
	repo1k := flag.String("repo1k", "", "1k 文件仓库")
	repo10k := flag.String("repo10k", "", "10k 文件仓库")
	snapN := flag.Int("snapn", 30, "快照正式次数")
	snapWarm := flag.Int("snapwarm", 3, "快照预热次数")
	jsonOut := flag.String("json", "", "结果 JSON 输出路径")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "需要 -base")
		os.Exit(2)
	}
	work := filepath.Join(*base, "work")
	_ = os.MkdirAll(work, 0o700)
	_ = os.WriteFile(filepath.Join(work, "target.go"), []byte("package main\n"), 0o600)

	envInfo := map[string]string{
		"go":         runtime.Version(),
		"os":         sh("sw_vers", "-productVersion") + " (" + sh("uname", "-sr") + ")",
		"cpu":        sh("sysctl", "-n", "machdep.cpu.brand_string") + ", " + sh("sysctl", "-n", "hw.ncpu") + " 核",
		"git":        sh("git", "--version"),
		"load_start": sh("sh", "-c", "uptime | sed 's/.*load averages*: //'"),
	}
	fmt.Printf("环境: %v\n", envInfo)

	out := map[string]any{"env": envInfo, "time": time.Now().Format(time.RFC3339)}

	switch *mode {
	case "hook":
		if *hookBin == "" || *rules == "" {
			fmt.Fprintln(os.Stderr, "需要 -hook -rules")
			os.Exit(2)
		}
		var filter *regexp.Regexp
		if *only != "" {
			filter = regexp.MustCompile(*only)
		}
		var rs []result
		fmt.Printf("\n| 场景 | 样本 | p50 ms | p95 ms | max ms | p95≤30ms | 异常 |\n|---|---|---|---|---|---|---|\n")
		for _, sc := range scenarios(*repo1k, *repo10k) {
			if filter != nil && !filter.MatchString(sc.name) {
				continue
			}
			if (strings.HasPrefix(sc.name, "6") || strings.HasPrefix(sc.name, "8b") || strings.HasPrefix(sc.name, "8c")) && (*repo1k == "" || *repo10k == "") {
				continue
			}
			r := runScenario(sc, *base, *hookBin, *noopBin, *rules, *n, *warm)
			rs = append(rs, r)
			fmt.Printf("| %s | %d | %.2f | %.2f | %.2f | %v | %d |\n", r.Name, r.Samples, r.P50, r.P95, r.Max, r.Under30, r.Bad)
			for _, nt := range r.Notes {
				fmt.Printf("    - %s\n", nt)
			}
		}
		out["hook"] = rs
	case "snapshot":
		var all []snapResult
		for _, rp := range []struct{ label, path string }{{"1k", *repo1k}, {"10k", *repo10k}} {
			if rp.path == "" {
				continue
			}
			if rp.label == "1k" {
				gitBaseline(rp.path, *snapN)
			}
			rs := benchSnapshot(rp.label, rp.path, filepath.Join(*base, "snaptmp"), *snapN, *snapWarm)
			all = append(all, rs...)
		}
		fmt.Printf("\n| 仓库 | 文件数 | 变体 | p50 ms | p95 ms | max ms | 分步 p50/p95 ms |\n|---|---|---|---|---|---|---|\n")
		for _, r := range all {
			var keys []string
			for k := range r.Steps {
				keys = append(keys, k)
			}
			order := map[string]int{"locate": 0, "copy_index": 1, "add_u": 2, "rm_cached_exempt": 3, "write_tree": 4, "native": 5}
			sort.Slice(keys, func(i, j int) bool { return order[keys[i]] < order[keys[j]] })
			var parts []string
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s %.2f/%.2f", k, r.Steps[k][0], r.Steps[k][1]))
			}
			fmt.Printf("| %s | %d | %s | %.2f | %.2f | %.2f | %s |\n", r.Repo, r.Files, r.Variant, r.P50, r.P95, r.Max, strings.Join(parts, "; "))
		}
		fmt.Println()
		for _, r := range all {
			fmt.Printf("tree[%s][%s] = %s\n", r.Repo, r.Variant, r.Tree)
		}
		out["snapshot"] = all
	}
	envInfo["load_end"] = sh("sh", "-c", "uptime | sed 's/.*load averages*: //'")
	fmt.Printf("\n负载 开始: %s 结束: %s\n", envInfo["load_start"], envInfo["load_end"])
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(out, "", "  ")
		_ = os.WriteFile(*jsonOut, b, 0o644)
	}
}
