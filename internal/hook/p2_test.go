package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const verifyStdin = `{"session_id":"s1","cwd":"/repo","agent_id":"a1","tool_name":"Bash","tool_use_id":"tv1","tool_input":{"command":"ballast verify test"}}`

func canonicalVerify(h *harness) {
	sp := starParser{&model.CommandList{Shell: model.ShellBash, Nodes: []model.CommandNode{{
		Argv: []model.Word{{Lit: "ballast"}, {Lit: "verify"}, {Lit: "test"}}, Name: "ballast"}}}}
	h.deps.Bash, h.deps.PS = sp, sp
}

func t1Harness(t *testing.T) (*harness, *fakeTier) {
	skipWithoutPosixPaths(t)
	h := newHarness(t)
	canonicalVerify(h)
	tier := &fakeTier{info: model.TierInfo{Tier: model.TierT1, Declared: model.TierT1}}
	h.deps.Tier = tier
	h.deps.IDs = &sys.SeqID{}
	return h, tier
}

// The spawned argv is exactly what ParseHookStartArgv accepts (end to end
// between the hook and the `verify --hook-start` child).
func TestHookStartArgvIsParseableByTheChild(t *testing.T) {
	h, _ := t1Harness(t)
	h.sb.Vars["CLAUDE_PROJECT_DIR"] = "/repo"
	mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	sp := h.spawner.spawned()
	if len(sp) != 1 {
		t.Fatalf("spawned %v", sp)
	}
	if sp[0][0] != h.deps.Self {
		t.Errorf("argv[0] = %q, want Self %q (never the agent's argv[0])", sp[0][0], h.deps.Self)
	}
	req, err := model.ParseHookStartArgv(sp[0][1:])
	if err != nil {
		t.Fatalf("child cannot parse %q: %v", sp[0], err)
	}
	want := model.HookStartRequest{
		RequestID: "20261016T100000-000001", Cwd: "/repo", Names: []string{"test"}, Client: model.ClientClaude,
		SessionID: "s1", AgentID: "a1", ProjectDir: "/repo",
	}
	if !reflect.DeepEqual(req, want) {
		t.Errorf("request = %+v\nwant      %+v", req, want)
	}
	if got := h.spawner.logPaths(); len(got) != 1 ||
		got[0] != filepath.Join(h.deps.Layout.RequestsDir, "0123456789abcdef", "20261016T100000-000001.spawn.log") {
		t.Errorf("log path = %v", got)
	}
	if !strings.Contains(h.stdout.String(), "job=20261016T100000-000001") {
		t.Errorf("deny reason does not name the request id: %q", h.stdout.String())
	}
}

func TestHookStartRequestIDsAreUniquePerCall(t *testing.T) {
	h, _ := t1Harness(t)
	for i := 0; i < 3; i++ {
		mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	}
	seen := map[string]bool{}
	for _, a := range h.spawner.spawned() {
		req, err := model.ParseHookStartArgv(a[1:])
		if err != nil || seen[req.RequestID] {
			t.Fatalf("request ids not unique/valid: %v %v", req, err)
		}
		seen[req.RequestID] = true
	}
	if len(seen) != 3 {
		t.Errorf("ids = %v", seen)
	}
}

func TestHookStartOutsideARepoUsesNorepoScope(t *testing.T) {
	h, _ := t1Harness(t)
	h.repo.err = model.ErrNotRepo
	mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	logs := h.spawner.logPaths()
	if len(logs) != 1 {
		t.Fatalf("logs %v", logs)
	}
	rel, err := filepath.Rel(h.deps.Layout.RequestsDir, logs[0])
	if err != nil || !strings.HasPrefix(rel, "norepo-") || !strings.HasSuffix(rel, ".spawn.log") {
		t.Errorf("log path %q is not <RequestsDir>/norepo-…/<id>.spawn.log", logs[0])
	}
}

func TestHookStartSpawnsWithTheHookEnvironment(t *testing.T) {
	h, _ := t1Harness(t)
	h.sb.Vars["SOME_VAR"] = "kept"
	mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	h.spawner.mu.Lock()
	defer h.spawner.mu.Unlock()
	if len(h.spawner.envs) != 1 || !contains(h.spawner.envs[0], "SOME_VAR=kept") {
		t.Errorf("env = %v", h.spawner.envs)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestHookStartRefusesWithoutCapabilities(t *testing.T) {
	for name, mod := range map[string]func(*harness){
		"no spawner":    func(h *harness) { h.deps.Spawner = nil },
		"no Self":       func(h *harness) { h.deps.Self = "" },
		"relative cwd":  func(h *harness) {},
		"no IDs":        func(h *harness) { h.deps.IDs = nil },
		"spawn failure": func(h *harness) { h.spawner.err = errors.New("boom\nsecond line") },
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := t1Harness(t)
			mod(h)
			in := verifyStdin
			if name == "relative cwd" {
				in = strings.Replace(in, `"cwd":"/repo"`, `"cwd":"repo"`, 1)
			}
			mustExit0(t, h, h.claude("PreToolUse", in))
			if !strings.Contains(h.stdout.String(), denyJSON) || !strings.Contains(h.stdout.String(), "无法在后台启动 verify:") ||
				!strings.Contains(h.stdout.String(), "`ballast verify test`") {
				t.Errorf("stdout = %q", h.stdout.String())
			}
			if strings.Contains(h.stdout.String(), `\n second`) {
				t.Errorf("error is not one line: %q", h.stdout.String())
			}
			var names []string
			for _, r := range h.audit.all() {
				var m map[string]any
				_ = json.Unmarshal(r.Detail, &m)
				names = append(names, m["diag"].(string))
			}
			if !reflect.DeepEqual(names, []string{"verify_spawn_failed"}) {
				t.Errorf("diags = %v", names)
			}
			if name != "spawn failure" && len(h.spawner.spawned()) != 0 {
				t.Errorf("spawned despite %s: %v", name, h.spawner.spawned())
			}
		})
	}
}

func TestLongSpawnErrorIsClipped(t *testing.T) {
	h, _ := t1Harness(t)
	h.spawner.err = errors.New(strings.Repeat("é", 1000))
	mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	for _, r := range h.audit.all() {
		var m map[string]any
		_ = json.Unmarshal(r.Detail, &m)
		if e, ok := m["error"].(string); ok && utf8.RuneCountInString(e) > maxSpawnErrRunes {
			t.Errorf("audited error has %d runes", utf8.RuneCountInString(e))
		}
	}
}

// The synthetic reason is never truncated; the rule reasons that follow are
// clipped to the model.BudgetReason total.
func TestSyntheticReasonBudget(t *testing.T) {
	h, _ := t1Harness(t)
	h.engine.verdict = &model.Verdict{
		Action:  model.ActionDeny,
		Reason:  "[r] " + strings.Repeat("长", 5000),
		Hits:    []model.RuleHit{{RuleID: "r", Action: model.ActionDeny, Effective: model.ActionDeny, Message: "x"}},
		Results: []model.RuleResult{{RuleID: "r", Outcome: model.TriTrue}},
	}
	mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	var out struct {
		H struct {
			Reason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	head := "ballast(T1):已在沙箱外后台启动 verify,job=20261016T100000-000001(test)。"
	if !strings.HasPrefix(out.H.Reason, head) || !strings.Contains(out.H.Reason, "`ballast verify --status 20261016T100000-000001`") {
		t.Errorf("synthetic reason damaged: %q", out.H.Reason)
	}
	if n := utf8.RuneCountInString(out.H.Reason); n > model.BudgetReason {
		t.Errorf("reason has %d runes, budget %d", n, model.BudgetReason)
	}
	if !strings.Contains(out.H.Reason, "[r] 长") {
		t.Errorf("rule reason dropped entirely: %q", out.H.Reason)
	}
}

// A duplicate rule hit and a synthetic deny must not double count: the eval
// counters come from the rule verdict only.
func TestSyntheticDenyDoesNotChangeEvalCounts(t *testing.T) {
	h, _ := t1Harness(t)
	h.engine.verdict = &model.Verdict{Results: []model.RuleResult{{RuleID: "r", Outcome: model.TriFalse}}}
	mustExit0(t, h, h.claude("PreToolUse", verifyStdin))
	d := h.state.lastCommit(t).delta
	if !d.ToolCall || !reflect.DeepEqual(d.Evals, map[string]model.EvalCount{"r": {Evals: 1}}) || len(d.HitRules) != 0 {
		t.Errorf("delta = %+v", d)
	}
	for _, r := range h.audit.all() {
		if r.Type == model.AuditHit {
			t.Errorf("synthetic deny must not be audited as a rule hit: %+v", r)
		}
	}
}

func TestTierIsProbedOnlyForCanonicalVerify(t *testing.T) {
	h, tier := t1Harness(t)
	// An ordinary Bash call: parsed (Tier wired) but never probed.
	git := starParser{&model.CommandList{Shell: model.ShellBash, Nodes: []model.CommandNode{{
		Argv: []model.Word{{Lit: "git"}, {Lit: "status"}}, Name: "git"}}}}
	h.deps.Bash, h.deps.PS = git, git
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	if tier.probeCalls() != 0 {
		t.Errorf("probed for git status")
	}
	// Tier nil ⇒ the hook does not parse at all for a PreToolUse.
	h = newHarness(t)
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	if h.bash.parseCalls() != 0 {
		t.Errorf("parsed with Tier == nil")
	}
}

// ---- ConfigChange ------------------------------------------------------------

func configChangeEvent(source, file string) func(string, []byte) (*model.Event, error) {
	return func(native string, _ []byte) (*model.Event, error) {
		return &model.Event{
			Client: model.ClientClaude, Name: model.EventConfigChange, Native: native,
			SessionID: "s1", Cwd: "/repo", Source: source, ConfigFile: file,
		}, nil
	}
}

type configDetail struct {
	Source    *string `json:"source"`
	File      *string `json:"file"`
	Protected *bool   `json:"protected"`
}

func runConfigChange(t *testing.T, h *harness, source, file string) (configDetail, map[string]json.RawMessage) {
	t.Helper()
	h.adapters.ad[model.ClientClaude].parseOverride = configChangeEvent(source, file)
	mustExit0(t, h, h.claude("ConfigChange", `{}`))
	recs := h.audit.all()
	if len(recs) != 1 || recs[0].Type != model.AuditConfigChange {
		t.Fatalf("audit = %+v, want exactly one config_change", recs)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("stdout must stay empty, got %q", h.stdout.String())
	}
	var d configDetail
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recs[0].Detail, &d); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(recs[0].Detail, &raw)
	return d, raw
}

func TestConfigChangeAudit(t *testing.T) {
	str := func(s string) *string { return &s }
	yes, no := true, false
	const home = "HOME" // replaced per sub-test with the sandbox home
	cases := []struct {
		name, source, file string
		want               configDetail
		keys               []string
	}{
		{"repo project settings", "project_settings", "/repo/.claude/settings.json",
			configDetail{str("project_settings"), str("<repo>/.claude/settings.json"), &yes}, []string{"file", "protected", "source"}},
		{"repo local settings", "local_settings", "/repo/.claude/settings.local.json",
			configDetail{str("local_settings"), str("<repo>/.claude/settings.local.json"), &yes}, []string{"file", "protected", "source"}},
		{"user settings", "user_settings", home + "/.claude/settings.json",
			configDetail{str("user_settings"), str("~/.claude/settings.json"), &yes}, []string{"file", "protected", "source"}},
		{"ballast home", "user_settings", home + "/.ballast/rules.json",
			configDetail{str("user_settings"), str("~/.ballast/rules.json"), &yes}, []string{"file", "protected", "source"}},
		{"repo ballast dir", "project_settings", "/repo/.ballast/rules.json",
			configDetail{str("project_settings"), str("<repo>/.ballast/rules.json"), &yes}, []string{"file", "protected", "source"}},
		{"repo skill is not protected", "skills", "/repo/.claude/skills/x/SKILL.md",
			configDetail{str("skills"), str("<repo>/.claude/skills/x/SKILL.md"), &no}, []string{"file", "protected", "source"}},
		{"other settings-like name is not protected", "project_settings", "/repo/.claude/settings.txt",
			configDetail{str("project_settings"), str("<repo>/.claude/settings.txt"), &no}, []string{"file", "protected", "source"}},
		{"home file outside .claude", "user_settings", home + "/notes.json",
			configDetail{str("user_settings"), str("~/notes.json"), &no}, []string{"file", "protected", "source"}},
		{"outside repo and home keeps the basename only", "policy_settings", "/etc/other/managed-settings.json",
			configDetail{str("policy_settings"), str("managed-settings.json"), &no}, []string{"file", "protected", "source"}},
		{"already ~-relative", "user_settings", "~/.claude/settings.json",
			configDetail{str("user_settings"), str("~/.claude/settings.json"), &yes}, []string{"file", "protected", "source"}},
		{"file empty ⇒ file and protected omitted", "user_settings", "",
			configDetail{Source: str("user_settings")}, []string{"source"}},
		{"source empty ⇒ source omitted", "", "/repo/.claude/settings.json",
			configDetail{File: str("<repo>/.claude/settings.json"), Protected: &yes}, []string{"file", "protected"}},
		{"both empty", "", "", configDetail{}, nil},
		{"repo root itself is not under <repo>/", "x", "/repo",
			configDetail{str("x"), str("repo"), &no}, []string{"file", "protected", "source"}},
		{"dot-dot out of the repo keeps the basename only", "x", "/repo/../../Users/alice/x",
			configDetail{str("x"), str("x"), &no}, []string{"file", "protected", "source"}},
		{"dot-dot inside the repo is cleaned", "project_settings", "/repo/a/../.claude/settings.json",
			configDetail{str("project_settings"), str("<repo>/.claude/settings.json"), &yes}, []string{"file", "protected", "source"}},
		{"~ with only dot-dot leaks nothing", "x", "~/../..",
			configDetail{Source: str("x"), Protected: &no}, []string{"protected", "source"}},
		{"~ escaping upwards keeps the basename only", "x", "~/.claude/../../etc/passwd",
			configDetail{str("x"), str("passwd"), &no}, []string{"file", "protected", "source"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && (strings.Contains(c.file, home) || strings.HasPrefix(c.file, "~")) {
				// The sandbox home is a drive-letter path there, which the
				// POSIX-only fakePaths cannot normalize; the pure /repo
				// cases above still run on every platform.
				t.Skip("needs a POSIX sandbox home")
			}
			h := newHarness(t)
			got, raw := runConfigChange(t, h, c.source, strings.ReplaceAll(c.file, home, h.sb.Home))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("detail = %s\nwant %+v", mustJSON(raw), c.want)
			}
			var keys []string
			for k := range raw {
				keys = append(keys, k)
			}
			if len(keys) == 0 {
				keys = nil
			}
			sortStrings(keys)
			if !reflect.DeepEqual(keys, c.keys) {
				t.Errorf("detail keys = %v, want %v", keys, c.keys)
			}
		})
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestConfigChangeNeverBlocksAndTouchesNoState(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	h.deps.WeakenedProject = model.WeakenedProjectDeny
	h.deps.ProjectSandbox = &fakeProject{byDir: map[string][]string{"/repo": {"settings.json:env"}}}
	h.deps.RedirectsIgnored = []string{"BALLAST_HOME"}
	_, _ = runConfigChange(t, h, "project_settings", "/repo/.claude/settings.json")
	if h.engine.calls() != 0 {
		t.Error("rules evaluated for ConfigChange")
	}
	if h.state.commitCount() != 0 || h.log.index("state.load") >= 0 || h.log.index("load") >= 0 {
		t.Errorf("session state / rules touched: %v", h.log.list())
	}
	if h.log.index("inject") >= 0 {
		t.Error("injector invoked")
	}
	if len(h.spawner.spawned()) != 0 {
		t.Errorf("spawned %v", h.spawner.spawned())
	}
	r := h.audit.all()[0]
	if r.Client != model.ClientClaude || r.SessionID != "s1" || r.RepoInst != "0123456789abcdef" || r.Event != model.EventConfigChange ||
		r.NativeEvent != "ConfigChange" || r.Tier != model.TierT0 || r.BallastVersion != "9.9.9-test" || r.TS != testNow {
		t.Errorf("record common fields: %+v", r)
	}
}

func TestConfigChangeFromTheRealPayloadShape(t *testing.T) {
	// Through the fake adapter's `source` mapping only: ConfigFile stays
	// empty (P2-CC fills it), and the line still carries the source.
	h := newHarness(t)
	mustExit0(t, h, h.claude("ConfigChange", `{"session_id":"s1","cwd":"/repo","source":"user_settings"}`))
	recs := h.audit.all()
	if len(recs) != 1 || string(recs[0].Detail) != `{"source":"user_settings"}` {
		t.Errorf("audit = %+v", recs)
	}
}

func TestConfigChangeFailureProtocol(t *testing.T) {
	h := newHarness(t)
	h.adapters.ad[model.ClientClaude].parseOverride = configChangeEvent("x", "")
	h.audit.err = os.ErrPermission
	h.wantFailure(h.claude("ConfigChange", `{}`))
}

func TestConfigChangeFaultMatrix(t *testing.T) {
	for _, p := range AllFaultPoints {
		for _, m := range allFaultModes {
			t.Run(string(p)+"/"+string(m), func(t *testing.T) {
				h := newHarness(t)
				h.adapters.ad[model.ClientClaude].parseOverride = configChangeEvent("project_settings", "/repo/.claude/settings.json")
				h.deps.Faults = pointFault{p, m}
				if m == FaultHang {
					h.deps.Watchdog = 100 * time.Millisecond
				}
				h.wantFailure(h.claude("ConfigChange", `{}`))
				if h.state.commitCount() != 0 {
					t.Error("state committed")
				}
			})
		}
	}
}

func diagNames(h *harness) []string {
	var out []string
	for _, r := range h.audit.all() {
		if r.Type != model.AuditDiag {
			continue
		}
		var d struct {
			Diag string `json:"diag"`
		}
		_ = json.Unmarshal(r.Detail, &d)
		out = append(out, d.Diag)
	}
	return out
}

// ---- bounded weakened entries and first-event guard -------------------------

// The keys of a weakened project layer come from repo-controlled files: the
// deny reason and the audit detail stay bounded however many or long they are.
func TestWeakenedEntriesAreBounded(t *testing.T) {
	skipWithoutPosixPaths(t)
	var entries []string
	for i := 0; i < 40; i++ {
		entries = append(entries, fmt.Sprintf("settings.json:k%02d", i))
	}
	entries = append(entries, "settings.json:"+strings.Repeat("长", 5000)+"\nsecond line")
	h := newHarness(t)
	h.deps.ProjectSandbox = &fakeProject{byDir: map[string][]string{"/repo/proj": entries}}
	h.deps.WeakenedProject = model.WeakenedProjectDeny
	h.sb.Vars["CLAUDE_PROJECT_DIR"] = "/repo/proj"
	h.engine.verdict = &model.Verdict{
		Action: model.ActionDeny,
		Reason: "[r] " + strings.Repeat("规", 5000),
		Hits:   []model.RuleHit{{RuleID: "r", Action: model.ActionDeny, Effective: model.ActionDeny, Message: "x"}},
	}
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	var out struct {
		H struct {
			Reason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(out.H.Reason); n > model.BudgetReason {
		t.Errorf("reason has %d runes, budget %d", n, model.BudgetReason)
	}
	rest := len(entries) - maxWeakenedEntries
	if want := fmt.Sprintf("…等 %d 项", rest); !strings.Contains(out.H.Reason, want) {
		t.Errorf("reason does not say %q: %q", want, out.H.Reason)
	}
	if !strings.Contains(out.H.Reason, "[r] 规") {
		t.Errorf("rule reason dropped entirely")
	}
	var found bool
	for _, r := range h.audit.all() {
		var m struct {
			Diag    string   `json:"diag"`
			Entries []string `json:"entries"`
		}
		_ = json.Unmarshal(r.Detail, &m)
		if m.Diag != model.DiagLowerSandboxWeakened {
			continue
		}
		found = true
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(r.Detail, &raw)
		if len(raw) != 2 {
			t.Errorf("detail keys = %v, want only diag and entries", raw)
		}
		if len(m.Entries) != maxWeakenedEntries+1 {
			t.Fatalf("entries = %d, want %d plus the placeholder", len(m.Entries), maxWeakenedEntries)
		}
		if last, want := m.Entries[maxWeakenedEntries], fmt.Sprintf("…等 %d 项", rest); last != want {
			t.Errorf("placeholder = %q, want %q", last, want)
		}
		for _, e := range m.Entries {
			if utf8.RuneCountInString(e) > maxWeakenedEntryRunes || strings.ContainsAny(e, "\r\n") {
				t.Errorf("entry not bounded to one short line: %q", e)
			}
		}
	}
	if !found {
		t.Errorf("no lower_sandbox_weakened diag: %v", diagNames(h))
	}
}

// Few entries are written exactly as before: no placeholder entry.
func TestWeakenedEntriesUnderTheCapAreUntouched(t *testing.T) {
	skipWithoutPosixPaths(t)
	h := newHarness(t)
	h.deps.ProjectSandbox = &fakeProject{byDir: map[string][]string{"/repo/proj": {"settings.json:env"}}}
	h.sb.Vars["CLAUDE_PROJECT_DIR"] = "/repo/proj"
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	for _, r := range h.audit.all() {
		if strings.Contains(string(r.Detail), model.DiagLowerSandboxWeakened) &&
			string(r.Detail) != `{"diag":"lower_sandbox_weakened","entries":["settings.json:env"]}` {
			t.Errorf("detail = %s", r.Detail)
		}
	}
}

// An unreadable session state reports StartedAt() false on every event; the
// first-event diags must not be repeated for the whole session then.
func TestFirstEventDiagsSkippedOnUnreadableState(t *testing.T) {
	skipWithoutPosixPaths(t)
	h := newHarness(t)
	h.state.loadErr = errTest
	h.state.loadInfo = model.StateLoadInfo{Exists: true, Readable: false}
	h.deps.ProjectSandbox = &fakeProject{byDir: map[string][]string{"/repo/proj": {"settings.json:env"}}}
	h.deps.RedirectsIgnored = []string{"BALLAST_HOME"}
	h.sb.Vars["CLAUDE_PROJECT_DIR"] = "/repo/proj"
	for i := 0; i < 2; i++ {
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	}
	for _, n := range diagNames(h) {
		if n == model.DiagEnvRedirectIgnored || n == model.DiagLowerSandboxWeakened {
			t.Errorf("first-event diag %q written on an unreadable state: %v", n, diagNames(h))
		}
	}
	if !contains(diagNames(h), "state_unreadable") {
		t.Errorf("state_unreadable missing: %v", diagNames(h))
	}
}
