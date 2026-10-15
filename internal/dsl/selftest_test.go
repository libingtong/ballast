package dsl

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func exampleRule(id, extra string, examples ...string) string {
	return `{"id":"` + id + `","event":"PreToolUse","action":"warn",` + extra + `"match":{"tool":"Edit"},"message":"m","examples":[` + strings.Join(examples, ",") + `]}`
}

const (
	exHit  = `{"expect":"hit","input":{"tool_name":"Edit","tool_input":{"file_path":"/repo/a"}}}`
	exMiss = `{"expect":"miss","input":{"tool_name":"Write","tool_input":{"file_path":"/repo/a"}}}`
)

func runTestRules(t *testing.T, e *Engine, data []byte) *TestReport {
	t.Helper()
	rep, err := e.TestRules([]LintInput{{Path: "t.json", Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestTestRulesReportsFailures(t *testing.T) {
	e := newEngine(t)
	rep := runTestRules(t, e, ruleFileJSON(exampleRule("rule-one", "",
		exHit, exMiss,
		`{"name":"wrongly claimed hit","expect":"hit","input":{"tool_name":"Write","tool_input":{"file_path":"/repo/a"}}}`,
		`{"expect":"unknown","input":{"tool_name":"Edit","tool_input":{"file_path":"/repo/a"}}}`,
	)))
	if rep.Passed != 2 || rep.Failed != 2 || len(rep.Cases) != 4 {
		t.Fatalf("report = %+v", rep)
	}
	c := rep.Cases[2]
	if c.Pass || c.Example != "wrongly claimed hit" || c.Got != model.TriFalse || c.Expect != model.ExpectHit || c.RuleID != "rule-one" || c.File != "t.json" || c.Client != model.ClientClaude {
		t.Errorf("failed case = %+v", c)
	}
	if rep.Cases[3].Example != "#3" || rep.Cases[3].Got != model.TriTrue {
		t.Errorf("unnamed case = %+v", rep.Cases[3])
	}
}

func TestTestRulesInvalidFileIsFailedCases(t *testing.T) {
	e := newEngine(t)
	rep, err := e.TestRules([]LintInput{
		{Path: "bad.json", Data: []byte("{nope")},
		{Path: "invalid.json", Data: ruleFileJSON(exampleRule("Bad-Id", "", exHit, exMiss))},
		{Path: "good.json", Data: ruleFileJSON(exampleRule("good-rule", "", exHit, exMiss))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 2 || rep.Passed != 2 {
		t.Fatalf("report = %+v", rep.Cases)
	}
	if c := rep.Cases[0]; c.File != "bad.json" || c.Pass || !strings.Contains(c.Detail, CodeSchema) {
		t.Errorf("undecodable case = %+v", c)
	}
	if c := rep.Cases[1]; c.File != "invalid.json" || c.Pass || !strings.Contains(c.Detail, CodeBadID) || !strings.Contains(c.Detail, "/rules/0/id") {
		t.Errorf("invalid case = %+v", c)
	}
}

func TestTestRulesSkipsMissingAdapters(t *testing.T) {
	e := newEngine(t)
	codex := `{"expect":"hit","client":"codex","input":{"tool_name":"apply_patch","tool_input":{}}}`
	rep := runTestRules(t, e, ruleFileJSON(exampleRule("rule-one", "", exHit, exMiss, codex)))
	if rep.Passed != 2 || rep.Failed != 0 || rep.Skipped != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if c := rep.Cases[2]; !c.Skipped || c.Pass || c.Client != model.ClientCodex || c.Detail == "" {
		t.Errorf("skipped case = %+v", c)
	}
	// Skipped examples are not lint failures.
	ds := e.Lint([]LintInput{{Path: "t.json", Data: ruleFileJSON(exampleRule("rule-one", "", exHit, exMiss, codex))}}, LintOptions{RunExamples: true})
	if len(ds) != 0 {
		t.Errorf("lint diagnostics: %v", diagKeys(ds))
	}
	// Without any registry everything is skipped.
	bare := NewEngine(Options{Paths: fakePaths{}})
	rep, _ = bare.TestRules([]LintInput{{Path: "t.json", Data: ruleFileJSON(exampleRule("rule-one", "", exHit, exMiss))}})
	if rep.Skipped != 2 || rep.Passed != 0 {
		t.Errorf("bare report = %+v", rep)
	}
}

func TestTestRulesBadExampleInput(t *testing.T) {
	e := newEngine(t)
	data := ruleFileJSON(exampleRule("rule-one", "", exHit, exMiss,
		`{"expect":"miss","native":"Bogus","input":{"tool_name":"Edit","tool_input":{}}}`,
		`{"expect":"miss","input":{"tool_name":"Edit","tool_input":{}},"client":"claude-code","native":"SessionStart"}`))
	rep := runTestRules(t, e, data)
	// An unknown native event is an adapter error; SessionStart parses but the rule does not apply (miss).
	if rep.Failed != 1 || rep.Passed != 3 {
		t.Fatalf("report = %+v", rep.Cases)
	}
	if c := rep.Cases[2]; c.Pass || !strings.Contains(c.Detail, "cannot parse") {
		t.Errorf("bad native = %+v", c)
	}
	ds := e.Lint([]LintInput{{Path: "t.json", Data: data}}, LintOptions{RunExamples: true})
	if got := diagKeys(ds); len(got) != 1 || got[0] != "error:example-bad-input@/rules/0/examples/2" {
		t.Errorf("lint = %v", got)
	}
}

func TestLintRunExamplesReportsFailedExamples(t *testing.T) {
	e := newEngine(t)
	data := ruleFileJSON(exampleRule("rule-one", "", exHit, exMiss,
		`{"name":"lies","expect":"miss","input":{"tool_name":"Edit","tool_input":{"file_path":"/repo/a"}}}`))
	ds := e.Lint([]LintInput{{Path: "t.json", Data: data}}, LintOptions{RunExamples: true})
	if len(ds) != 1 || ds[0].Code != CodeExampleFailed || ds[0].Pointer != "/rules/0/examples/2" || ds[0].RuleID != "rule-one" || ds[0].Severity != model.SevError {
		t.Fatalf("diagnostics = %+v", ds)
	}
	if !strings.Contains(ds[0].Message, "lies") || !strings.Contains(ds[0].Message, "expected miss") {
		t.Errorf("message = %q", ds[0].Message)
	}
	// Without RunExamples the example is not run.
	if ds := e.Lint([]LintInput{{Path: "t.json", Data: data}}, LintOptions{}); len(ds) != 0 {
		t.Errorf("examples ran without RunExamples: %v", diagKeys(ds))
	}
	// Files with errors do not run their examples (no double reporting).
	bad := ruleFileJSON(exampleRule("rule-one", `"action":"nuke",`, exHit, exMiss, `{"expect":"miss","input":{"tool_name":"Edit","tool_input":{"file_path":"/repo/a"}}}`))
	for _, d := range e.Lint([]LintInput{{Path: "t.json", Data: bad}}, LintOptions{RunExamples: true}) {
		if d.Code == CodeExampleFailed {
			t.Errorf("examples ran on an invalid file: %+v", d)
		}
	}
}

func TestTestRulesForcesDenyAndIgnoresEnabled(t *testing.T) {
	e := newEngine(t)
	// warn on codex-less registry; disabled rules are still tested; clients mismatch is a miss.
	data := ruleFileJSON(
		exampleRule("off-rule", `"enabled":false,`, exHit, exMiss),
		exampleRule("claude-only", `"clients":["codex"],`, exMiss, `{"expect":"hit","input":{"tool_name":"Edit","tool_input":{}}}`),
	)
	rep := runTestRules(t, e, data)
	if rep.Passed != 3 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep.Cases)
	}
	if c := rep.Cases[3]; c.Pass || c.Got != model.TriFalse || !strings.Contains(c.Detail, "does not apply") {
		t.Errorf("inapplicable case = %+v", c)
	}
}

func TestTestRulesUsesTheConfiguredParsers(t *testing.T) {
	e, canned := newTestEngine(t)
	rule := `{"id":"sleep-rule","event":"PreToolUse","action":"deny","match":{"bash":{"cmd":"sleep"}},"message":"m","examples":[
		{"expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"sleep 300"}}},
		{"expect":"unknown","input":{"tool_name":"Bash","tool_input":{"command":"echo \"unterminated"}}},
		{"expect":"miss","input":{"tool_name":"Read","tool_input":{"file_path":"x"}}}]}`
	rep := runTestRules(t, e, ruleFileJSON(rule))
	if rep.Failed != 0 || rep.Passed != 3 {
		t.Fatalf("report = %+v", rep.Cases)
	}
	if rep.Cases[1].Detail != "bash:parse_error" {
		t.Errorf("unknown cause = %q", rep.Cases[1].Detail)
	}
	canned.assertNoMisses(t)

	// Without parsers, shell matchers cannot decide.
	noParsers := NewEngine(Options{Paths: fakePaths{}, Adapters: fakeRegistry{}})
	rep, _ = noParsers.TestRules([]LintInput{{Path: "t.json", Data: ruleFileJSON(rule)}})
	if rep.Cases[0].Got != model.TriUnknown || rep.Cases[0].Pass {
		t.Errorf("no parser: %+v", rep.Cases[0])
	}
}

func TestFixtureEnv(t *testing.T) {
	branch := "feature/x"
	count := 7
	fx := &model.Fixture{
		Cwd: "/repo", Home: "/home/user",
		Read: []string{"src/a.go", "~/notes.md", "/abs/b.go"}, Exists: []string{"go.mod", "./docs/../README.md"},
		VerifyFresh: "false", FailedSinceRead: []string{"src/a.go"},
		Env: map[string]string{"CI": "1"}, Branch: &branch, ToolCountSinceInject: &count,
		PrevPrompt: "hello", PriorHits: map[string]int{"abc-def": 3},
	}
	pc := model.PathContext{Cwd: "/repo", ProjectRoot: "/repo", Home: "/home/user", GOOS: "linux"}
	sess, evid, world := FixtureEnv(fx, pc)

	for _, p := range []string{"/repo/src/a.go", "/home/user/notes.md", "/abs/b.go"} {
		if !sess.HasRead(p) {
			t.Errorf("HasRead(%s) = false", p)
		}
	}
	if sess.HasRead("/repo/src/other.go") || sess.HasRead("/REPO/src/a.go") {
		t.Error("HasRead over-approximates")
	}
	if sess.ToolCountSinceInject() != 7 || sess.PrevPromptTail() != "hello" || sess.PriorHits("abc-def") != 3 || sess.PriorHits("zzz") != 0 {
		t.Error("session counters")
	}
	started, ok := sess.StartedAt()
	failedRead, _ := sess.LastReadAt("/repo/src/a.go")
	okRead, _ := sess.LastReadAt("/abs/b.go")
	if !ok || !failedRead.After(started) || !okRead.After(failedRead) {
		t.Errorf("read times: started %v, failed-path read %v, other read %v", started, failedRead, okRead)
	}
	if world.PathExists("/repo/go.mod") != model.TriTrue || world.PathExists("/repo/README.md") != model.TriTrue || world.PathExists("/repo/missing") != model.TriFalse {
		t.Error("PathExists")
	}
	if v, ok := world.Getenv("CI"); !ok || v != "1" {
		t.Error("Getenv")
	}
	if b, tri := world.Branch(); b != "feature/x" || tri != model.TriTrue {
		t.Errorf("Branch = %q %v", b, tri)
	}
	if r := evid.VerifyFresh(model.FreshQuery{}); r.Value != model.TriFalse || r.Cause != "stale" {
		t.Errorf("VerifyFresh = %+v", r)
	}
	if tri, _ := evid.LastFailureAfter(failedRead); tri != model.TriTrue {
		t.Errorf("failure after the failed-path read = %v", tri)
	}
	if tri, _ := evid.LastFailureAfter(okRead); tri != model.TriFalse {
		t.Errorf("failure after a later read = %v", tri)
	}

	// Defaults, unknown branch, detached HEAD, not a repo.
	sess2, evid2, world2 := FixtureEnv(&model.Fixture{}, model.PathContext{})
	if _, tri := world2.Branch(); tri != model.TriUnknown {
		t.Error("default branch must be unknown")
	}
	if r := evid2.VerifyFresh(model.FreshQuery{}); r.Value != model.TriUnknown {
		t.Error("default verify_fresh must be unknown")
	}
	if tri, _ := evid2.LastFailureAfter(started); tri != model.TriFalse || sess2.HasRead("/repo/x") {
		t.Error("empty fixture")
	}
	empty := ""
	if _, _, w := FixtureEnv(&model.Fixture{Branch: &empty}, pc); func() model.Tri { _, t := w.Branch(); return t }() != model.TriFalse {
		t.Error("empty branch means detached HEAD")
	}
	if _, ev, _ := FixtureEnv(&model.Fixture{NotARepo: true, VerifyFresh: "true"}, pc); ev != nil {
		t.Error("not_a_repo must give a nil EvidenceView")
	}
	if s, _, _ := FixtureEnv(nil, pc); s == nil {
		t.Error("nil fixture")
	}

	// Case folding on windows/darwin.
	win := &model.Fixture{GOOS: "windows", Cwd: "c:/Repo", Read: []string{"Src/A.go"}}
	ws, _, _ := FixtureEnv(win, model.PathContext{})
	if !ws.HasRead("c:/repo/src/a.go") {
		t.Error("windows fixture paths must fold case")
	}
}

func TestFillPayload(t *testing.T) {
	out, err := fillPayload([]byte(`{"tool_name":"Edit","cwd":"/custom","session_id":"mine"}`), "/repo", "PreToolUse")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"cwd":"/custom"`, `"session_id":"mine"`, `"hook_event_name":"PreToolUse"`} {
		if !strings.Contains(s, want) {
			t.Errorf("%s missing from %s", want, s)
		}
	}
	for _, bad := range []string{`"x"`, `[]`, `null`, ``, `{`} {
		if _, err := fillPayload([]byte(bad), "/repo", "Stop"); err == nil {
			t.Errorf("fillPayload accepted %q", bad)
		}
	}
}
