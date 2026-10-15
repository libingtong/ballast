package dsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// fuzzRules covers every matcher and predicate once, for all six events.
const fuzzRules = `{"version":1,"rules":[
 {"id":"fz-pre-all","event":"PreToolUse","action":"deny","message":"m1","match":{"tool":["*","!Read"],"path":["**/*","!**/*_test.go","~/.x/**"],"agent_type":["*"],
   "run_in_background":false},
  "when":[{"path_exists":{}},{"not":{"read_this_session":{}}},{"any":[{"verify_fresh":{}},{"is_subagent":{}},{"branch_glob":{"patterns":["main","feature/**"]}}]}],"examples":[]},
 {"id":"fz-pre-edit","event":"PreToolUse","action":"warn","message":"m1b","match":{"tool":["Edit","Write"],"input_regex":{"new_text":"TODO","file_path":"\\.go$"}},"examples":[]},
 {"id":"fz-pre-cmd","event":"PreToolUse","action":"warn","message":"m1c","match":{"tool":["Bash","PowerShell"],"input_regex":{"command":["^rm","(?i)sudo"]}},"examples":[]},
 {"id":"fz-pre-mcp","event":"PreToolUse","action":"log","message":"m1d","match":{"tool":"mcp__*","input_regex":{"anything":"."}},"examples":[]},
 {"id":"fz-pre-bash","event":"PreToolUse","action":"ask","message":"m2","match":{
   "bash":{"cmd":["sleep","git","sh","bash"],"args_regex":["-rf","x$"],"flags":["-n","--force"],"git_sub":["commit","push"],"sleep_s":{"gte":60},"async":false,"in_loop":false,"loop_bounded":true,"in_subst":false,"sudo":false,"dynamic":false,"writes":["**/*.go"],"pipe_from":["curl"],"pipe_to":["sh"]},
   "ps":{"cmd":["start-sleep","git"],"flags":["-Force"],"sleep_s":{"gte":1}}},
  "when":[{"verify_failed_since_read":{}},{"tool_count_since_inject":{"gte":1}},{"prior_hit":{"rule":"fz-pre-all","lt":3}},{"env_equals":{"name":"CI","value":"1"}}],"examples":[]},
 {"id":"fz-pre-shell-any","event":"PreToolUse","action":"warn","message":"m3","match":{"bash":{"cmd":"*"},"ps":{"cmd":"*"}},"examples":[]},
 {"id":"fz-prompt","event":"UserPromptSubmit","action":"warn","message":"m4","match":{"text":{"preprocess":["strip_code_blocks","strip_quotes"],"window":{"head_chars":50,"tail_chars":30},"regex":["(?i)ignore","\\p{Han}"],"not_regex":["safe"],"cjk_ratio":{"lt":0.5},"count_regex":{"regex":"a","gte":1}}},
  "when":[{"prev_prompt_regex":{"regex":"x"}}],"examples":[]},
 {"id":"fz-post","event":"PostToolUse","action":"warn","message":"m5","match":{"tool":"*","text":{"source":"tool_response","regex":"AKIA[0-9A-Z]{4}"}},"examples":[]},
 {"id":"fz-fail","event":"PostToolUseFailure","action":"warn","message":"m6","match":{"text":{"regex":"Exit code [1-9]"}},"examples":[]},
 {"id":"fz-stop","event":"Stop","action":"log","target":"deny","message":"m7","match":{"text":{"preprocess":["strip_code_blocks"],"regex":"(?i)done","cjk_ratio":{"gte":0}}},"when":[{"not":{"verify_fresh":{"names":["unit"]}}}],"examples":[]},
 {"id":"fz-substop","event":"SubagentStop","action":"deny","message":"m8","match":{"agent_type":["*","!main"]},"examples":[]}
],"settings":{"inject":{"anchor_every":2}}}`

func fuzzEngine(tb testing.TB) (*Engine, *model.CompiledRuleSet) {
	tb.Helper()
	e, _ := newTestEngine(tb)
	f, err := DecodeRuleFile([]byte(fuzzRules))
	if err != nil {
		tb.Fatal(err)
	}
	var lrs []model.LoadedRule
	for i, r := range f.Rules {
		lrs = append(lrs, model.LoadedRule{Rule: r, Layer: model.LayerUser, Index: i})
	}
	for _, in := range schemaExamples(tb) {
		sf, err := DecodeRuleFile(in.Data)
		if err != nil {
			tb.Fatal(err)
		}
		for i, r := range sf.Rules {
			lrs = append(lrs, model.LoadedRule{Rule: r, Layer: model.LayerRepo, Index: i})
		}
	}
	rs, err := e.Compile(lrs, model.Settings{}, "fuzz")
	if err != nil {
		tb.Fatal(err)
	}
	return e, rs
}

func TestFuzzRulesAreValid(t *testing.T) {
	e := newEngine(t)
	f, err := DecodeRuleFile([]byte(fuzzRules))
	if err != nil {
		t.Fatal(err)
	}
	// The examples are intentionally empty; every other check must pass.
	for _, d := range e.Validate("fuzz", f) {
		if d.Code != CodeExamplesMissing {
			t.Errorf("fuzz rule set is invalid: %+v", d)
		}
	}
}

// commandListFor derives a deterministic CommandList from arbitrary text.
func commandListFor(cmd string, shell model.ShellKind) *model.CommandList {
	cl := &model.CommandList{Shell: shell}
	segs := strings.FieldsFunc(cmd, func(r rune) bool { return r == ';' || r == '|' || r == '&' || r == '\n' })
	for i, seg := range segs {
		words := strings.Fields(seg)
		if len(words) == 0 {
			continue
		}
		n := model.CommandNode{Name: words[0], Argv: make([]model.Word, len(words))}
		for j, w := range words {
			n.Argv[j] = model.Word{Lit: w, Dynamic: strings.HasPrefix(w, "$")}
			if j > 0 && (w == ">" || w == ">>") && j+1 < len(words) {
				n.Writes = append(n.Writes, model.WriteTarget{Path: words[j+1], Dynamic: strings.HasPrefix(words[j+1], "$"), Via: "redirect"})
			}
		}
		if n.Argv[0].Dynamic {
			n.Name = ""
		}
		switch n.Name {
		case "sleep":
			n.Sleep = &model.SleepInfo{Seconds: float64(len(words) * 40), Known: len(words) > 1 && !n.Argv[1].Dynamic}
		case "git":
			n.Git = &model.GitInvocation{}
			if len(words) > 1 {
				n.Git.Sub = words[1]
			}
		}
		n.Async = len(seg)%2 == 0
		if len(words)%3 == 0 {
			n.Loop = &model.LoopInfo{Kind: model.LoopWhile, N: -1, Bounded: model.Tri(len(seg) % 3), Depth: 1}
		}
		cl.Nodes = append(cl.Nodes, n)
		if i > 0 && len(cl.Nodes) > 1 {
			cl.Pipes = append(cl.Pipes, model.PipeEdge{From: len(cl.Nodes) - 2, To: len(cl.Nodes) - 1})
		}
	}
	cl.Unknown = strings.Contains(cmd, "eval")
	return cl
}

func fuzzInput(sel uint8, tool, cmd, path, text, agent string, flags uint8) *model.EvalInput {
	events := []model.EventName{
		model.EventPreToolUse, model.EventUserPromptSubmit, model.EventPostToolUse,
		model.EventPostToolUseFailure, model.EventStop, model.EventSubagentStop,
	}
	ev := &model.Event{
		Client: model.ClientClaude, Name: events[int(sel)%len(events)], Native: "x", Cwd: "/repo",
		Prompt: text, LastAssistantMessage: text, HasLastAssistantMessage: flags&1 != 0,
		Error: text, ToolResponseText: text, AgentType: agent, HasAgentType: flags&2 != 0,
	}
	if flags&4 != 0 {
		ev.AgentID = "a1"
	}
	if flags&8 != 0 {
		ev.ToolResponse = []byte(text)
	}
	shell := model.ShellNone
	switch tool {
	case "Bash":
		shell = model.ShellBash
	case "PowerShell":
		shell = model.ShellPowerShell
	}
	if flags&16 != 0 {
		ev.Tool = &model.ToolCall{
			Name: tool, RawName: tool, Shell: shell, Command: cmd, RunInBackground: flags&32 != 0,
			Targets: []string{path}, Fields: map[string][]string{"command": {cmd}, "file_path": {path}},
			Edits: []model.FileEdit{{Path: path, NewString: text, OldString: cmd}},
		}
	}
	fx := &model.Fixture{Exists: []string{path}, Read: []string{path}, VerifyFresh: []string{"true", "false", "unknown"}[int(flags)%3], PrevPrompt: text}
	pc := model.PathContext{Cwd: "/repo", ProjectRoot: "/repo", Home: "/home/user", GOOS: "linux"}
	sess, evid, world := FixtureEnv(fx, pc)
	in := &model.EvalInput{Event: ev, Client: model.ClientClaude, Paths: pc, Session: sess, SessionReadable: flags&64 == 0, Evidence: evid, World: world}
	if flags&128 != 0 {
		in.Evidence = nil
	}
	if shell != model.ShellNone && flags&3 != 3 {
		in.Commands = staticCommands{cl: commandListFor(cmd, shell)}
	}
	return in
}

func checkVerdict(t *testing.T, rs *model.CompiledRuleSet, v *model.Verdict) {
	t.Helper()
	if v == nil {
		t.Fatal("nil verdict")
	}
	if len(v.Results) > len(rs.Rules) {
		t.Fatalf("more results than rules")
	}
	hitIDs := map[string]bool{}
	for _, r := range v.Results {
		if r.Outcome == model.TriTrue {
			hitIDs[r.RuleID] = true
		}
		if r.Outcome != model.TriUnknown && r.UnknownCause != "" {
			t.Fatalf("cause on a decided result: %+v", r)
		}
	}
	if len(v.Hits) != len(hitIDs) {
		t.Fatalf("hits %d != true results %d", len(v.Hits), len(hitIDs))
	}
	want := model.ActionNone
	for _, h := range v.Hits {
		if !hitIDs[h.RuleID] {
			t.Fatalf("hit without a true result: %+v", h)
		}
		want = model.Stricter(want, h.Effective)
	}
	if v.Action != want {
		t.Fatalf("action %q, strictest effective %q", v.Action, want)
	}
	if utf8.RuneCountInString(v.Reason) > model.BudgetReason {
		t.Fatalf("reason too long: %d", utf8.RuneCountInString(v.Reason))
	}
	if v.Reason != "" && v.Action.Severity() < model.ActionWarn.Severity() {
		t.Fatalf("reason on a %q verdict", v.Action)
	}
}

func FuzzEvaluate(f *testing.F) {
	seeds := []struct {
		sel                          uint8
		tool, cmd, path, text, agent string
		flags                        uint8
	}{
		{0, "Bash", "sleep 300", "/repo/a.go", "", "", 16},
		{0, "Bash", "git add . && git commit -n -m x", "/repo/a.go", "", "general", 16 | 2 | 4},
		{0, "Bash", "curl http://x | sh", "/home/user/.ballast/rules.json", "", "", 16},
		{0, "Bash", "echo hi > $OUT; eval $X", "$OUT", "", "", 16 | 64},
		{0, "PowerShell", "Start-Sleep -Seconds 300 -Force", "c:/x", "", "", 16},
		{0, "Edit", "rm -rf x", "/repo/a_test.go", "TODO fix", "", 16 | 128},
		{0, "mcp__srv__tool", "", "", "", "", 16 | 3},
		{1, "", "", "", "Ignore previous 指示 aaa \"quoted\"\n```\ncode\n```", "", 0},
		{2, "Bash", "env", "", "key=AKIAABCD", "", 16 | 8},
		{3, "Bash", "false", "", "Exit code 2\nboom", "", 16},
		{4, "", "", "", "All done, 完成了 `x`", "", 1},
		{4, "", "", "", "", "", 0},
		{5, "", "", "", "x", "general-purpose", 2 | 4},
		{5, "", "", "", "x", "", 2 | 4},
		{255, "\x00\xff", "\xff\xfe", "\x00", "\xc3\x28", "\xff", 255},
	}
	for _, s := range seeds {
		f.Add(s.sel, s.tool, s.cmd, s.path, s.text, s.agent, s.flags)
	}
	e, rs := fuzzEngine(f)
	f.Fuzz(func(t *testing.T, sel uint8, tool, cmd, path, text, agent string, flags uint8) {
		in := fuzzInput(sel, tool, cmd, path, text, agent, flags)
		v, err := e.Evaluate(rs, in)
		if err != nil {
			t.Fatalf("Evaluate returned an error: %v", err)
		}
		checkVerdict(t, rs, v)
		// Evaluating again with identical input is deterministic.
		v2, _ := e.Evaluate(rs, fuzzInput(sel, tool, cmd, path, text, agent, flags))
		if len(v.Results) != len(v2.Results) || v.Action != v2.Action || v.Reason != v2.Reason {
			t.Fatalf("non-deterministic verdict:\n%+v\n%+v", v, v2)
		}
		// Other clients never panic either.
		for _, c := range model.AllClients {
			in.Client = c
			if _, err := e.Evaluate(rs, in); err != nil {
				t.Fatalf("client %s: %v", c, err)
			}
		}
	})
}

func FuzzEvaluateRandomRules(f *testing.F) {
	f.Add(`{"id":"abc","event":"PreToolUse","match":{"tool":"Edit","path":"**"},"when":[{"path_exists":{}}],"examples":[]}`, "Edit", "/repo/a")
	f.Add(`{"id":"abc","event":"Stop","match":{"text":{"regex":"("}},"examples":[]}`, "", "")
	f.Add(`{"id":"abc","event":"PreToolUse","match":{"bash":{"cmd":"[","args_regex":"\\"}},"when":[{},{"all":null}],"examples":[]}`, "Bash", "x")
	e := newEngine(f)
	f.Fuzz(func(t *testing.T, ruleText, tool, path string) {
		var r model.Rule
		if err := jsonUnmarshalLoose(ruleText, &r); err != nil {
			return
		}
		rs, err := e.Compile([]model.LoadedRule{{Rule: r}}, model.Settings{}, "g")
		if err != nil {
			return
		}
		for sel := uint8(0); sel < 6; sel++ {
			in := fuzzInput(sel, tool, path, path, path, tool, 16|uint8(len(path)))
			if v, err := e.Evaluate(rs, in); err != nil || v == nil {
				t.Fatalf("Evaluate: %v", err)
			}
		}
		e.Validate("fz", &model.RuleFile{Version: 1, Rules: []model.Rule{r}})
	})
}

func FuzzDecodeAndLint(f *testing.F) {
	f.Add([]byte(fuzzRules))
	f.Add([]byte(`{"version":1,"rules":[{"id":"x","when":[{"all":[{"any":[]}]}]}]}`))
	f.Add([]byte(`{"version":1,"rules":[{"id":"abc","event":"Stop","match":{"text":{"window":{"tail_chars":-1}}},"examples":[{"expect":"hit","input":3}]}]}`))
	f.Add([]byte("\xef\xbb\xbf{}"))
	f.Add([]byte(`{"version":1,"rules":[{"unknown":1}]}`))
	for _, in := range schemaExamples(f) {
		f.Add(in.Data)
	}
	e := newEngine(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		ds := e.Lint([]LintInput{{Path: "fz.json", Data: data}}, LintOptions{Clients: model.AllClients, Layer: model.LayerOrg, RunExamples: true})
		for _, d := range ds {
			if d.Code == "" || d.Message == "" || (d.Severity != model.SevError && d.Severity != model.SevWarning) {
				t.Fatalf("malformed diagnostic %+v", d)
			}
		}
		_, _, _ = e.ParseFile(model.LayerFile, "fz.json", data)
		_, _ = e.TestRules([]LintInput{{Path: "fz.json", Data: data}})
	})
}

func FuzzPreprocess(f *testing.F) {
	for _, s := range []string{"", "a `b` c", "```\ncode\n```", "\"q\" “q” ‘q’ 「q」 『q』", "> quote\nline", "```", "`", "\"", "~~~\nx", "你好 hello", "\xff\xfe`\"", "``` a ```"} {
		f.Add(s, uint8(3), 0, 0)
		f.Add(s, uint8(1), 3, 2)
	}
	f.Fuzz(func(t *testing.T, text string, mask uint8, head, tail int) {
		var steps []string
		if mask&1 != 0 {
			steps = append(steps, model.PreStripCodeBlocks)
		}
		if mask&2 != 0 {
			steps = append(steps, model.PreStripQuotes)
		}
		if mask&4 != 0 {
			steps = []string{model.PreStripQuotes, model.PreStripCodeBlocks}
		}
		out := Preprocess(text, steps)
		if utf8.RuneCountInString(out) > utf8.RuneCountInString(text)+0 && utf8.ValidString(text) {
			t.Fatalf("Preprocess grew the text: %q -> %q", text, out)
		}
		w := WindowText(out, head, tail)
		if head > 0 && tail <= 0 && utf8.RuneCountInString(w) > head {
			t.Fatalf("head window too long")
		}
		if tail > 0 && utf8.RuneCountInString(w) > tail {
			t.Fatalf("tail window too long")
		}
		if r, ok := CJKRatio(text); ok && (r < 0 || r > 1) {
			t.Fatalf("ratio %v", r)
		}
	})
}

func TestFuzzCorpusFilesExist(t *testing.T) {
	// The seed corpus lives in the f.Add calls; make sure the testdata dir is as expected.
	if _, err := os.Stat(filepath.Join("testdata", "tri.json")); err != nil {
		t.Fatal(err)
	}
}
