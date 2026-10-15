package dsl

import (
	"errors"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func newEngine(t testing.TB) *Engine {
	t.Helper()
	e, _ := newTestEngine(t)
	return e
}

func TestFlatMatchers(t *testing.T) {
	pre := func(match string) string { return ruleJSON("PreToolUse", match, "") }
	mcp := scenario{payload: toolPayload("mcp__srv__tool", `{"q":"x"}`)}
	sub := func(typ string) scenario {
		return scenario{payload: `{"agent_id":"a1","agent_type":"` + typ + `","tool_name":"Read","tool_input":{"file_path":"/repo/a"}}`}
	}
	runRows(t, newEngine(t), []row{
		{"tool exact", pre(`{"tool":"Edit"}`), editScn("/repo/a.go"), T, ""},
		{"tool is case sensitive", pre(`{"tool":"edit"}`), editScn("/repo/a.go"), F, ""},
		{"tool list is OR", pre(`{"tool":["Write","Edit"]}`), editScn("/repo/a.go"), T, ""},
		{"tool brace glob", pre(`{"tool":"{Edit,Write}"}`), editScn("/repo/a.go"), T, ""},
		{"tool mcp glob", pre(`{"tool":"mcp__*"}`), mcp, T, ""},
		{"tool mcp glob misses builtin", pre(`{"tool":"mcp__*"}`), editScn("/repo/a.go"), F, ""},
		{"tool negation excludes", pre(`{"tool":["*","!Edit"]}`), editScn("/repo/a.go"), F, ""},
		{"tool negation keeps others", pre(`{"tool":["*","!Edit"]}`), mcp, T, ""},
		{"tool matches raw name", pre(`{"tool":"apply_patch"}`),
			scenario{payload: toolPayload("Edit", `{"file_path":"/repo/a"}`)}, F, ""},
		{"tool absent on non-tool event", ruleJSON("Stop", `{"tool":"Edit"}`, ""), scenario{native: "Stop", payload: `{"last_assistant_message":"x"}`}, F, ""},

		{"agent_type main", pre(`{"agent_type":"main"}`), editScn("/repo/a"), T, ""},
		{"agent_type subagent glob", pre(`{"agent_type":"general-*"}`), sub("general-purpose"), T, ""},
		{"agent_type excludes main", pre(`{"agent_type":"general-*"}`), editScn("/repo/a"), F, ""},
		{"agent_type negation", pre(`{"agent_type":["*","!main"]}`), editScn("/repo/a"), F, ""},
		{"agent_type case sensitive", pre(`{"agent_type":"General-purpose"}`), sub("general-purpose"), F, ""},

		{"run_in_background true", pre(`{"run_in_background":true}`),
			scenario{payload: toolPayload("Bash", `{"command":"x","run_in_background":true}`)}, T, ""},
		{"run_in_background true needs the flag", pre(`{"run_in_background":true}`), bashScn("x", nil), F, ""},
		{"run_in_background false accepts missing", pre(`{"run_in_background":false}`), bashScn("x", nil), T, ""},
		{"run_in_background false rejects true", pre(`{"run_in_background":false}`),
			scenario{payload: toolPayload("Bash", `{"command":"x","run_in_background":true}`)}, F, ""},

		{"input_regex command", pre(`{"tool":"Bash","input_regex":{"command":"^rm\\s"}}`), bashScn("rm -rf x", nil), T, ""},
		{"input_regex no match", pre(`{"tool":"Bash","input_regex":{"command":"^rm\\s"}}`), bashScn("ls", nil), F, ""},
		{"input_regex list is OR", pre(`{"tool":"Bash","input_regex":{"command":["^rm","^ls"]}}`), bashScn("ls", nil), T, ""},
		{"input_regex keys are AND", pre(`{"tool":"Bash","input_regex":{"command":"ls","description":"zzz"}}`),
			scenario{payload: toolPayload("Bash", `{"command":"ls","description":"list"}`)}, F, ""},
		{"input_regex missing field is false", pre(`{"tool":"Bash","input_regex":{"description":"x"}}`), bashScn("ls", nil), F, ""},
		{"input_regex virtual new_text", pre(`{"tool":"Edit","input_regex":{"new_text":"TODO"}}`),
			scenario{payload: toolPayload("Edit", `{"file_path":"/repo/a","old_string":"x","new_string":"// TODO"}`)}, T, ""},
		{"input_regex virtual old_text", pre(`{"tool":"Edit","input_regex":{"old_text":"^x$"}}`),
			scenario{payload: toolPayload("Edit", `{"file_path":"/repo/a","old_string":"x","new_string":"y"}`)}, T, ""},
		{"input_regex any array element", pre(`{"tool":"MultiEdit","input_regex":{"edits.new_string":"^second$"}}`),
			scenario{payload: toolPayload("MultiEdit", `{"file_path":"/repo/a","edits":[{"old_string":"a","new_string":"first"},{"old_string":"b","new_string":"second"}]}`)}, T, ""},
		{"input_regex bad regex is unknown", pre(`{"tool":"Bash","input_regex":{"command":"("}}`), bashScn("ls", nil), U, "input_regex:command:bad_regex"},
	})
}

func TestShellMatchers(t *testing.T) {
	bash := func(m string) string { return ruleJSON("PreToolUse", `{"bash":`+m+`}`, "") }
	sleep300 := withSleep(nd("sleep", "300"), 300, true)
	runRows(t, newEngine(t), []row{
		{"cmd glob", bash(`{"cmd":"sl*"}`), bashScn("sleep 300", bashList(sleep300)), T, ""},
		{"cmd case sensitive", bash(`{"cmd":"Sleep"}`), bashScn("sleep 300", bashList(sleep300)), F, ""},
		{"cmd list OR", bash(`{"cmd":["cat","sleep"]}`), bashScn("sleep 300", bashList(sleep300)), T, ""},
		{"cmd negation", bash(`{"cmd":["*","!sleep"]}`), bashScn("sleep 300", bashList(sleep300)), F, ""},
		{"any node of the command", bash(`{"cmd":"git"}`), bashScn("cd x && git status", bashList(nd("cd", "x"), nd("git", "status"))), T, ""},
		{"dynamic command is unknown", bash(`{"cmd":"git"}`),
			bashScn("$X status", bashList(model.CommandNode{Argv: []model.Word{{Lit: "$X", Dynamic: true}}})), U, "bash:dynamic_command"},
		{"empty command name without argv is false", bash(`{"cmd":"git"}`), bashScn("FOO=1", bashList(model.CommandNode{})), F, ""},
		{"all fields on the same node", bash(`{"cmd":"git","flags":"--force"}`),
			bashScn("git status && rm --force", bashList(nd("git", "status"), nd("rm", "--force"))), F, ""},
		{"all fields satisfied by one node", bash(`{"cmd":"git","flags":"--force"}`),
			bashScn("git push --force", bashList(nd("git", "push", "--force"))), T, ""},

		{"args_regex joins literals", bash(`{"cmd":"rm","args_regex":"^-rf /tmp"}`), bashScn("rm -rf /tmp/x", bashList(nd("rm", "-rf", "/tmp/x"))), T, ""},
		{"args_regex no match", bash(`{"cmd":"rm","args_regex":"^-rf /etc"}`), bashScn("rm -rf /tmp/x", bashList(nd("rm", "-rf", "/tmp/x"))), F, ""},
		{"args_regex dynamic and no match is unknown", bash(`{"cmd":"rm","args_regex":"/etc"}`),
			bashScn("rm -rf $D", bashList(withDyn(nd("rm", "-rf"), "$D"))), U, "bash:dynamic_args"},
		{"args_regex dynamic but literal match is true", bash(`{"cmd":"rm","args_regex":"-rf"}`),
			bashScn("rm -rf $D", bashList(withDyn(nd("rm", "-rf"), "$D"))), T, ""},
		{"args_regex bad regex is unknown", bash(`{"args_regex":"("}`), bashScn("x", bashList(nd("x"))), U, "bash:bad_regex"},

		{"flag long exact", bash(`{"flags":"--no-verify"}`), bashScn("git commit --no-verify", bashList(nd("git", "commit", "--no-verify"))), T, ""},
		{"flag long with value", bash(`{"flags":"--message"}`), bashScn("git commit --message=x", bashList(nd("git", "commit", "--message=x"))), T, ""},
		{"flag long prefix is not a match", bash(`{"flags":"--no"}`), bashScn("git commit --no-verify", bashList(nd("git", "commit", "--no-verify"))), F, ""},
		{"flag short standalone", bash(`{"flags":"-n"}`), bashScn("git commit -n", bashList(nd("git", "commit", "-n"))), T, ""},
		{"flag short combined", bash(`{"flags":"-n"}`), bashScn("git commit -anm", bashList(nd("git", "commit", "-anm"))), T, ""},
		{"flag short absent from combined", bash(`{"flags":"-n"}`), bashScn("git commit -am", bashList(nd("git", "commit", "-am"))), F, ""},
		{"flag short is not a long option", bash(`{"flags":"-n"}`), bashScn("git commit --no", bashList(nd("git", "commit", "--no"))), F, ""},
		{"flag short combined needs letters only", bash(`{"flags":"-n"}`), bashScn("x -n1", bashList(nd("x", "-n1"))), F, ""},
		{"flag ignores dynamic words", bash(`{"flags":"-n"}`), bashScn("x $F", bashList(withDyn(nd("x"), "-n"))), F, ""},
		{"flag list is OR", bash(`{"flags":["-f","--force"]}`), bashScn("x --force", bashList(nd("x", "--force"))), T, ""},

		{"git_sub in list", bash(`{"git_sub":["commit","push"]}`), bashScn("git push", bashList(withGit(nd("git", "push"), "push"))), T, ""},
		{"git_sub other", bash(`{"git_sub":"commit"}`), bashScn("git push", bashList(withGit(nd("git", "push"), "push"))), F, ""},
		{"git_sub on non-git node", bash(`{"git_sub":"commit"}`), bashScn("ls", bashList(nd("ls"))), F, ""},
		{"git_sub dynamic is unknown", bash(`{"git_sub":"commit"}`),
			bashScn("git $S", bashList(model.CommandNode{Name: "git", Git: &model.GitInvocation{SubDynamic: true}})), U, "bash:dynamic_git_sub"},

		{"sleep_s gte", bash(`{"sleep_s":{"gte":60}}`), bashScn("sleep 300", bashList(sleep300)), T, ""},
		{"sleep_s range", bash(`{"sleep_s":{"gte":1,"lt":5}}`), bashScn("sleep 3", bashList(withSleep(nd("sleep", "3"), 3, true))), T, ""},
		{"sleep_s below", bash(`{"sleep_s":{"gte":60}}`), bashScn("sleep 3", bashList(withSleep(nd("sleep", "3"), 3, true))), F, ""},
		{"sleep_s on non-sleep node", bash(`{"sleep_s":{"gte":0}}`), bashScn("ls", bashList(nd("ls"))), F, ""},
		{"sleep_s unknown duration", bash(`{"sleep_s":{"gte":60}}`), bashScn("sleep $X", bashList(withSleep(nd("sleep"), 0, false))), U, "bash:dynamic_sleep"},

		{"async true", bash(`{"async":true}`), bashScn("x &", bashList(func() model.CommandNode { n := nd("x"); n.Async = true; return n }())), T, ""},
		{"async false", bash(`{"async":false}`), bashScn("x", bashList(nd("x"))), T, ""},
		{"async false rejects background", bash(`{"async":false}`), bashScn("x &", bashList(func() model.CommandNode { n := nd("x"); n.Async = true; return n }())), F, ""},
		{"in_loop true", bash(`{"in_loop":true}`), bashScn("x", bashList(withLoop(nd("x"), model.LoopWhile, model.TriFalse))), T, ""},
		{"in_loop false", bash(`{"in_loop":false}`), bashScn("x", bashList(withLoop(nd("x"), model.LoopWhile, model.TriFalse))), F, ""},
		{"loop_bounded true", bash(`{"loop_bounded":true}`), bashScn("x", bashList(withLoop(nd("x"), model.LoopForList, model.TriTrue))), T, ""},
		{"loop_bounded true rejects infinite", bash(`{"loop_bounded":true}`), bashScn("x", bashList(withLoop(nd("x"), model.LoopWhile, model.TriFalse))), F, ""},
		{"loop_bounded false accepts infinite", bash(`{"loop_bounded":false}`), bashScn("x", bashList(withLoop(nd("x"), model.LoopWhile, model.TriFalse))), T, ""},
		{"loop_bounded unknown", bash(`{"loop_bounded":true}`), bashScn("x", bashList(withLoop(nd("x"), model.LoopWhile, model.TriUnknown))), U, "bash:loop_bounded"},
		{"loop_bounded outside loops is false", bash(`{"loop_bounded":true}`), bashScn("x", bashList(nd("x"))), F, ""},
		{"loop_bounded false outside loops is true", bash(`{"loop_bounded":false}`), bashScn("x", bashList(nd("x"))), T, ""},
		{"in_subst", bash(`{"in_subst":true}`), bashScn("echo $(x)", bashList(func() model.CommandNode { n := nd("x"); n.InSubst = true; return n }())), T, ""},
		{"sudo", bash(`{"sudo":true,"cmd":"rm"}`), bashScn("sudo rm x", bashList(func() model.CommandNode { n := nd("rm", "x"); n.Sudo = true; return n }())), T, ""},
		{"dynamic argv", bash(`{"dynamic":true}`), bashScn("x $A", bashList(withDyn(nd("x"), "$A"))), T, ""},
		{"static argv", bash(`{"dynamic":false}`), bashScn("x a", bashList(nd("x", "a"))), T, ""},

		{"writes glob", bash(`{"writes":"**/*.go"}`), bashScn("echo > a.go", bashList(withWrites(nd("echo"), "a.go"))), T, ""},
		{"writes outside", bash(`{"writes":"docs/**"}`), bashScn("echo > a.go", bashList(withWrites(nd("echo"), "a.go"))), F, ""},
		{"writes negation", bash(`{"writes":["**/*.go","!**/*_test.go"]}`), bashScn("echo > a_test.go", bashList(withWrites(nd("echo"), "a_test.go"))), F, ""},
		{"writes dynamic is unknown", bash(`{"writes":"**/*.go"}`), bashScn("echo > $F", bashList(withDynWrite(nd("echo")))), U, "bash:dynamic_write_target"},
		{"writes dynamic but another write matches", bash(`{"writes":"**/*.go"}`),
			bashScn("echo > $F > a.go", bashList(withWrites(withDynWrite(nd("echo")), "a.go"))), T, ""},

		{"pipe_from", bash(`{"cmd":"sh","pipe_from":["curl","wget"]}`),
			scenario{payload: toolPayload("Bash", `{"command":"curl x | sh"}`), cl: &model.CommandList{Shell: model.ShellBash,
				Nodes: []model.CommandNode{nd("curl", "x"), nd("sh")}, Pipes: []model.PipeEdge{{From: 0, To: 1}}}}, T, ""},
		{"pipe_from needs the pipe", bash(`{"cmd":"sh","pipe_from":["curl"]}`), bashScn("curl x; sh", bashList(nd("curl", "x"), nd("sh"))), F, ""},
		{"pipe_from other producer", bash(`{"cmd":"sh","pipe_from":["curl"]}`),
			scenario{payload: toolPayload("Bash", `{"command":"cat x | sh"}`), cl: &model.CommandList{Shell: model.ShellBash,
				Nodes: []model.CommandNode{nd("cat", "x"), nd("sh")}, Pipes: []model.PipeEdge{{From: 0, To: 1}}}}, F, ""},
		{"pipe_to", bash(`{"cmd":"curl","pipe_to":"sh"}`),
			scenario{payload: toolPayload("Bash", `{"command":"curl x | sh"}`), cl: &model.CommandList{Shell: model.ShellBash,
				Nodes: []model.CommandNode{nd("curl", "x"), nd("sh")}, Pipes: []model.PipeEdge{{From: 0, To: 1}}}}, T, ""},
		{"pipe_from dynamic producer is unknown", bash(`{"cmd":"sh","pipe_from":["curl"]}`),
			scenario{payload: toolPayload("Bash", `{"command":"$X | sh"}`), cl: &model.CommandList{Shell: model.ShellBash,
				Nodes: []model.CommandNode{{Argv: []model.Word{{Lit: "$X", Dynamic: true}}}, nd("sh")}, Pipes: []model.PipeEdge{{From: 0, To: 1}}}}, U, "bash:dynamic_pipe_command"},
		{"pipe edge out of range is ignored", bash(`{"cmd":"sh","pipe_from":["curl"]}`),
			scenario{payload: toolPayload("Bash", `{"command":"x"}`), cl: &model.CommandList{Shell: model.ShellBash,
				Nodes: []model.CommandNode{nd("sh")}, Pipes: []model.PipeEdge{{From: 7, To: 0}}}}, F, ""},
	})
}

func TestShellDispatchAndParseFailures(t *testing.T) {
	bothRule := ruleJSON("PreToolUse", `{"bash":{"cmd":"git"},"ps":{"cmd":"start-sleep"}}`, "")
	bashOnly := ruleJSON("PreToolUse", `{"bash":{"cmd":"git"}}`, "")
	psOnly := ruleJSON("PreToolUse", `{"ps":{"cmd":"git"}}`, "")
	runRows(t, newEngine(t), []row{
		{"bash matcher on bash tool", bothRule, bashScn("git x", bashList(nd("git", "x"))), T, ""},
		{"ps matcher on powershell tool", bothRule, psScn("Start-Sleep 3", psList(nd("start-sleep", "3"))), T, ""},
		{"ps matcher ignored for bash tool", psOnly, bashScn("git x", bashList(nd("git", "x"))), F, ""},
		{"bash matcher ignored for powershell tool", bashOnly, psScn("git x", psList(nd("git", "x"))), F, ""},
		{"bash matcher on non-shell tool", bashOnly, editScn("/repo/a"), F, ""},
		{"bash matcher on non-tool event", ruleJSON("Stop", `{"bash":{"cmd":"git"}}`, ""), scenario{native: "Stop", payload: `{"last_assistant_message":"x"}`}, F, ""},
		{"parse error is unknown", bashOnly, bashScn(`echo "x`, nil).withErr(errors.New("unterminated")), U, "bash:parse_error"},
		{"powershell parse error cause", psOnly, psScn(`x`, nil).withErr(errors.New("bad")), U, "ps:parse_error"},
		{"missing commands for a non-empty command", bashOnly, bashScn("git x", nil), U, "bash:no_commands"},
		{"empty command is false", bashOnly, scenario{payload: toolPayload("Bash", `{}`)}, F, ""},
		{"Unknown list without a positive node", bashOnly,
			bashScn("eval $X", &model.CommandList{Shell: model.ShellBash, Unknown: true, UnknownReason: "eval", Nodes: []model.CommandNode{withDyn(nd("eval"), "$X")}}), U, "bash:eval"},
		{"Unknown list with a positive node still hits", bashOnly,
			bashScn("git x; eval $X", &model.CommandList{Shell: model.ShellBash, Unknown: true, Nodes: []model.CommandNode{nd("git", "x"), nd("eval")}}), T, ""},
		{"Unknown list and a failing when stays unknown", ruleJSON("PreToolUse", `{"bash":{"cmd":"git"}}`, `[{"is_subagent":{}}]`),
			bashScn("git x; eval $X", &model.CommandList{Shell: model.ShellBash, Unknown: true, UnknownReason: "eval", Nodes: []model.CommandNode{nd("git", "x"), nd("eval")}}), U, "bash:eval"},
		{"positive node beats a parse-unknown sibling in when", ruleJSON("PreToolUse", `{"bash":{"cmd":"git"}}`, `[{"is_subagent":{}}]`),
			bashScn("git x", bashList(nd("git", "x"))), F, ""},
	})
}

func (s scenario) withErr(err error) scenario { s.cmdErr = err; return s }

func TestPathBindings(t *testing.T) {
	tamper := ruleJSON("PreToolUse", `{"tool":["Edit","Write","Bash"],"path":["**/.ballast/**","~/.claude/settings*.json"]}`, "")
	runRows(t, newEngine(t), []row{
		{"file tool target matches", tamper, editScn("/home/user/.ballast/rules.json"), T, ""},
		{"file tool target is cleaned", tamper, editScn("src/../.ballast/rules.json"), T, ""},
		{"file tool target outside via dotdot", tamper, editScn("../elsewhere/a.go"), F, ""},
		{"file tool relative target", tamper, editScn(".ballast/rules.json"), T, ""},
		{"home glob", tamper, editScn("/home/user/.claude/settings.local.json"), T, ""},
		{"file tool target misses", tamper, editScn("/repo/src/a.go"), F, ""},
		{"shell write target", tamper, bashScn("echo > ~/.ballast/x", bashList(withWrites(nd("echo"), "~/.ballast/x"))), T, ""},
		{"shell without write targets", tamper, bashScn("cat x", bashList(nd("cat", "x"))), F, ""},
		{"shell dynamic target is unknown", tamper, bashScn("echo > $X", bashList(withDynWrite(nd("echo")))), U, "bash:dynamic_write_target"},
		{"shell parse error is unknown", tamper, bashScn("x", nil).withErr(errors.New("e")), U, "bash:parse_error"},
		{"path with a tool without targets is false", ruleJSON("PreToolUse", `{"path":"**"}`, ""),
			scenario{payload: toolPayload("WebFetch", `{"url":"http://x"}`)}, F, ""},
		{"path on a non-tool event is false", ruleJSON("UserPromptSubmit", `{"path":"**"}`, ""), scenario{native: "UserPromptSubmit", payload: `{"prompt":"x"}`}, F, ""},
		{"bare filename pattern matches any depth", ruleJSON("PreToolUse", `{"path":"*.md"}`, ""), editScn("/repo/docs/deep/a.md"), T, ""},
		{"no path: every target is a binding", ruleJSON("PreToolUse", `{"tool":"Edit"}`, `[{"path_exists":{}}]`), editScn("/repo/a").with(model.Fixture{Exists: []string{"a"}}), T, ""},
		{"multi-target: any binding hits", ruleJSON("PreToolUse", `{"tool":"MultiEdit"}`, `[{"path_exists":{}},{"not":{"read_this_session":{}}}]`),
			scenario{payload: toolPayload("MultiEdit", `{"file_path":"/repo/a","edits":[]}`)}.with(model.Fixture{Exists: []string{"a"}}), T, ""},
	})
}

func TestShellToolTargetBindings(t *testing.T) {
	// No shell matcher and no match.path: a shell tool's targets are its
	// parsed write targets (dsl-semantics §5.2, §9).
	rule := ruleJSON("PreToolUse", `{"tool":["Edit","Bash"]}`, `[{"path_exists":{}},{"not":{"read_this_session":{}}}]`)
	exists := model.Fixture{Exists: []string{"a.go"}}
	runRows(t, newEngine(t), []row{
		{"write target is bound", rule, bashScn("echo x > a.go", bashList(withWrites(nd("echo", "x"), "a.go"))).with(exists), T, ""},
		{"write target is bound (miss)", rule, bashScn("echo x > b.go", bashList(withWrites(nd("echo", "x"), "b.go"))).with(exists), F, ""},
		{"write target already read", rule, bashScn("echo x > a.go", bashList(withWrites(nd("echo", "x"), "a.go"))).
			with(model.Fixture{Exists: []string{"a.go"}, Read: []string{"a.go"}}), F, ""},
		{"no write targets: empty binding", rule, bashScn("ls", bashList(nd("ls"))), U, "path_exists:no_target"},
		{"dynamic write target is unknown", rule, bashScn("echo x > $F", bashList(withDynWrite(nd("echo", "x")))), U, "bash:dynamic_write_target"},
		{"parse error is unknown", rule, bashScn("x", nil).withErr(errors.New("e")), U, "bash:parse_error"},
		{"dynamic target does not mask a bound hit", rule,
			bashScn("echo x > a.go > $F", bashList(withDynWrite(withWrites(nd("echo", "x"), "a.go")))).with(exists), T, ""},
	})
}

func TestShellTargetGateUsesTarget(t *testing.T) {
	// Shell write targets are parsed only when when references $target
	// (dsl-semantics §9, contract v1.3): an unrelated when must not turn a
	// parse failure or an eval into unknown.
	bare := ruleJSON("PreToolUse", `{"tool":["Bash"],"run_in_background":true}`, "")
	unrelated := ruleJSON("PreToolUse", `{"tool":["Bash"],"run_in_background":true}`, `[{"not":{"is_subagent":{}}}]`)
	literal := ruleJSON("PreToolUse", `{"tool":["Bash"],"run_in_background":true}`, `[{"not":{"path_exists":{"path":"go.mod"}}}]`)
	nested := ruleJSON("PreToolUse", `{"tool":["Bash"]}`, `[{"any":[{"is_subagent":{}},{"not":{"path_exists":{"path":"$target"}}}]}]`)
	bg := `{"tool_name":"Bash","tool_input":{"command":"x","run_in_background":true}}`
	parseErr := scenario{native: "PreToolUse", payload: bg}.withErr(errors.New("e"))
	evalCL := bashList(nd("eval", "x"))
	evalCL.Unknown, evalCL.UnknownReason = true, "eval"
	evalScn := scenario{native: "PreToolUse", payload: bg, cl: evalCL}
	runRows(t, newEngine(t), []row{
		{"bare rule, parse error", bare, parseErr, T, ""},
		{"unrelated when, parse error", unrelated, parseErr, T, ""},
		{"unrelated when, eval", unrelated, evalScn, T, ""},
		{"literal path when, parse error", literal, parseErr, T, ""},
		{"nested $target when, parse error", nested, scenario{native: "PreToolUse", payload: `{"tool_name":"Bash","tool_input":{"command":"x"}}`}.withErr(errors.New("e")), U, "bash:parse_error"},
	})
}

func TestShellPathBinding(t *testing.T) {
	// bash node x path binding: the binding target is the matching write.
	rule := ruleJSON("PreToolUse", `{"bash":{"cmd":"cp"},"path":"**/*.go"}`, `[{"path_exists":{}}]`)
	n := withWrites(nd("cp", "a", "b"), "/repo/ignored.txt", "/repo/b.go")
	runRows(t, newEngine(t), []row{
		{"binding target is the write that matched", rule, bashScn("cp a b", bashList(n)).with(model.Fixture{Exists: []string{"b.go"}}), T, ""},
		{"binding target is not the other write", rule, bashScn("cp a b", bashList(n)).with(model.Fixture{Exists: []string{"ignored.txt"}}), F, ""},
		{"shell writes binds the matching write", ruleJSON("PreToolUse", `{"bash":{"cmd":"cp","writes":"**/*.go"}}`, `[{"path_exists":{}}]`),
			bashScn("cp a b", bashList(n)).with(model.Fixture{Exists: []string{"b.go"}}), T, ""},
		{"no path binding: $target is unknown", ruleJSON("PreToolUse", `{"bash":{"cmd":"cp"}}`, `[{"path_exists":{}}]`),
			bashScn("cp a b", bashList(n)), U, "path_exists:no_target"},
		{"literal path works without a binding target", ruleJSON("PreToolUse", `{"bash":{"cmd":"cp"}}`, `[{"path_exists":{"path":"go.mod"}}]`),
			bashScn("cp a b", bashList(n)).with(model.Fixture{Exists: []string{"go.mod"}}), T, ""},
	})
}

func TestTextMatchers(t *testing.T) {
	stop := func(text string) string { return ruleJSON("Stop", `{"text":`+text+`}`, "") }
	msg := func(m string) scenario {
		return scenario{native: "Stop", payload: `{"last_assistant_message":` + jsonStr(m) + `}`}
	}
	runRows(t, newEngine(t), []row{
		{"regex any", stop(`{"regex":["done","finished"]}`), msg("we are finished"), T, ""},
		{"regex none", stop(`{"regex":["done","finished"]}`), msg("working"), F, ""},
		{"not_regex exempts", stop(`{"regex":"done","not_regex":["not done"]}`), msg("not done"), F, ""},
		{"not_regex alone", stop(`{"not_regex":"forbidden"}`), msg("fine"), T, ""},
		{"not_regex alone hits exemption", stop(`{"not_regex":"forbidden"}`), msg("forbidden"), F, ""},
		{"tail window", stop(`{"window":{"tail_chars":4},"regex":"done"}`), msg("done... later"), F, ""},
		{"tail window keeps the end", stop(`{"window":{"tail_chars":4},"regex":"done"}`), msg("later: done"), T, ""},
		{"head window", stop(`{"window":{"head_chars":4},"regex":"done"}`), msg("done, later"), T, ""},
		{"head then tail", stop(`{"window":{"head_chars":6,"tail_chars":3},"regex":"^def$"}`), msg("abcdefgh"), T, ""},
		{"window counts runes", stop(`{"window":{"tail_chars":2},"regex":"^好吗$"}`), msg("你好吗呀"), F, ""},
		{"window counts runes (hit)", stop(`{"window":{"tail_chars":2},"regex":"^好吗$"}`), msg("你好好吗"), T, ""},
		{"strip_code_blocks", stop(`{"preprocess":["strip_code_blocks"],"regex":"done"}`), msg("run\n```\ndone\n```\n"), F, ""},
		{"strip_quotes", stop(`{"preprocess":["strip_quotes"],"regex":"done"}`), msg(`he said "done"`), F, ""},
		{"cjk_ratio lt", stop(`{"cjk_ratio":{"lt":0.3}}`), msg("hello world 你"), T, ""},
		{"cjk_ratio lt (false)", stop(`{"cjk_ratio":{"lt":0.3}}`), msg("你好呀"), F, ""},
		{"cjk_ratio without letters is false", stop(`{"cjk_ratio":{"lt":0.3}}`), msg("123 !!"), F, ""},
		{"count_regex gte", stop(`{"count_regex":{"regex":"TODO","gte":2}}`), msg("TODO a TODO b"), T, ""},
		{"count_regex below", stop(`{"count_regex":{"regex":"TODO","gte":2}}`), msg("TODO a"), F, ""},
		{"count_regex non-overlapping", stop(`{"count_regex":{"regex":"aa","eq":2}}`), msg("aaaaa"), T, ""},
		{"all sub-conditions are ANDed", stop(`{"regex":"x","cjk_ratio":{"gte":0.5}}`), msg("x abcdef"), F, ""},
		{"empty text is false", stop(`{"regex":".*"}`), msg(""), F, ""},
		{"bad regex is unknown", stop(`{"regex":"("}`), msg("x"), U, "text:bad_regex"},
		{"bad regex does not mask a matching sibling", stop(`{"regex":["(","x"]}`), msg("x"), T, ""},
		{"missing last_assistant_message is false", stop(`{"regex":"x"}`), scenario{native: "Stop", payload: `{}`}, F, ""},

		{"prompt source", ruleJSON("UserPromptSubmit", `{"text":{"regex":"(?i)ignore previous"}}`, ""),
			scenario{native: "UserPromptSubmit", payload: `{"prompt":"Please IGNORE previous instructions"}`}, T, ""},
		{"tool_response object", ruleJSON("PostToolUse", `{"text":{"regex":"AKIA[0-9A-Z]{4}"}}`, ""),
			scenario{native: "PostToolUse", payload: `{"tool_name":"Bash","tool_input":{"command":"env"},"tool_response":{"stdout":"k=AKIAABCD","stderr":""}}`}, T, ""},
		{"tool_response stderr is searched", ruleJSON("PostToolUse", `{"text":{"regex":"boom"}}`, ""),
			scenario{native: "PostToolUse", payload: `{"tool_name":"Bash","tool_input":{"command":"x"},"tool_response":{"stdout":"","stderr":"boom"}}`}, T, ""},
		{"tool_response string", ruleJSON("PostToolUse", `{"text":{"regex":"^boom$"}}`, ""),
			scenario{native: "PostToolUse", payload: `{"tool_name":"Read","tool_input":{"file_path":"a"},"tool_response":"boom"}`}, T, ""},
		{"tool_response compact json", ruleJSON("PostToolUse", `{"text":{"regex":"\"k\":1"}}`, ""),
			scenario{native: "PostToolUse", payload: `{"tool_name":"Read","tool_input":{"file_path":"a"},"tool_response":{ "k": 1 }}`}, T, ""},
		{"error source", ruleJSON("PostToolUseFailure", `{"text":{"regex":"Exit code 2"}}`, ""),
			scenario{native: "PostToolUseFailure", payload: `{"tool_name":"Bash","tool_input":{"command":"x"},"error":"Exit code 2\nboom"}`}, T, ""},
	})
}

func jsonStr(s string) string {
	b := []byte{'"'}
	for _, r := range s {
		switch {
		case r == '"':
			b = append(b, '\\', '"')
		case r == '\\':
			b = append(b, '\\', '\\')
		case r == '\n':
			b = append(b, '\\', 'n')
		case r < 0x20:
			b = append(b, ' ')
		default:
			b = append(b, string(r)...)
		}
	}
	return string(append(b, '"'))
}

func TestPowerShellFlags(t *testing.T) {
	ps := func(flags string) string { return ruleJSON("PreToolUse", `{"ps":{"flags":`+flags+`}}`, "") }
	runRows(t, newEngine(t), []row{
		{"parameters are case-insensitive", ps(`"-Force"`), psScn("rm x -force", psList(nd("remove-item", "x", "-force"))), T, ""},
		{"parameter with a value", ps(`"-Force"`), psScn("rm -Force:$true", psList(nd("remove-item", "-Force:$true"))), T, ""},
		{"different parameter", ps(`"-Force"`), psScn("rm -Forced", psList(nd("remove-item", "-Forced"))), F, ""},
		{"no combined short flags in powershell", ps(`"-n"`), psScn("x -an", psList(nd("x", "-an"))), F, ""},
	})
}

func TestPathServiceProblems(t *testing.T) {
	rule := ruleJSON("PreToolUse", `{"tool":"Edit","path":"**/*.go"}`, "")
	s := editScn("/repo/a.go")

	noPaths := NewEngine(Options{})
	rr, _ := evalRuleJSON(t, noPaths, rule, s)
	if rr.Outcome != U || rr.UnknownCause != "paths:unavailable" {
		t.Errorf("no PathService: %v %q", rr.Outcome, rr.UnknownCause)
	}
	rr, _ = evalRuleJSON(t, noPaths, ruleJSON("PreToolUse", `{"tool":"Edit"}`, `[{"path_exists":{}}]`), s)
	if rr.Outcome != U || rr.UnknownCause != "paths:unavailable" {
		t.Errorf("no PathService, predicate: %v %q", rr.Outcome, rr.UnknownCause)
	}

	rr, _ = evalRuleJSON(t, newEngine(t), ruleJSON("PreToolUse", `{"tool":"Edit","path":"src/[a"}`, ""), s)
	if rr.Outcome != U || rr.UnknownCause != "paths:bad_glob" {
		t.Errorf("uncompilable glob: %v %q", rr.Outcome, rr.UnknownCause)
	}
}

func TestPathCaseFolding(t *testing.T) {
	rule := ruleJSON("PreToolUse", `{"tool":"Edit","path":"**/README.md"}`, "")
	s := editScn("/repo/readme.md")
	for _, c := range []struct {
		name, optGOOS, ctxGOOS string
		want                   model.Tri
	}{
		{"linux is case-sensitive", "linux", "", F},
		{"context GOOS wins", "linux", "windows", T},
		{"darwin from Options.GOOS", "darwin", "", T},
		{"windows from Options.GOOS", "windows", "", T},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := NewEngine(Options{Paths: fakePaths{}, GOOS: c.optGOOS})
			rr, _ := evalWith(t, e, rule, s, func(in *model.EvalInput) { in.Paths.GOOS = c.ctxGOOS })
			if rr.Outcome != c.want {
				t.Errorf("outcome %v, want %v", rr.Outcome, c.want)
			}
		})
	}
}

func TestNameGlobsAreCaseSensitiveEverywhere(t *testing.T) {
	// Name globs go straight to doublestar: the PathContext GOOS must not fold them.
	e := NewEngine(Options{Paths: fakePaths{}, GOOS: "windows"})
	rr, _ := evalWith(t, e, ruleJSON("PreToolUse", `{"tool":"edit","agent_type":"MAIN"}`, ""), editScn("/repo/a"), func(in *model.EvalInput) { in.Paths.GOOS = "windows" })
	if rr.Outcome != F {
		t.Errorf("name globs folded case: %v", rr.Outcome)
	}
	rr, _ = evalWith(t, e, ruleJSON("PreToolUse", `{"bash":{"cmd":"Git"}}`, ""), bashScn("git x", bashList(nd("git", "x"))), func(in *model.EvalInput) { in.Paths.GOOS = "windows" })
	if rr.Outcome != F {
		t.Errorf("cmd glob folded case: %v", rr.Outcome)
	}
}
