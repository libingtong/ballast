package hook

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func w(lit string) model.Word   { return model.Word{Lit: lit} }
func dyn(lit string) model.Word { return model.Word{Lit: lit, Dynamic: true} }
func rd(path, via string) model.WriteTarget {
	return model.WriteTarget{Path: path, Via: via}
}
func dynRd(via string) model.WriteTarget { return model.WriteTarget{Dynamic: true, Via: via} }

func node(name string, reads ...model.WriteTarget) model.CommandNode {
	return model.CommandNode{Argv: []model.Word{w(name)}, Name: name, Reads: reads}
}

func TestShellReadRegistration(t *testing.T) {
	bashList := func(nodes ...model.CommandNode) *model.CommandList {
		return &model.CommandList{Shell: model.ShellBash, Nodes: nodes}
	}
	psList := func(nodes ...model.CommandNode) *model.CommandList {
		return &model.CommandList{Shell: model.ShellPowerShell, Nodes: nodes}
	}
	cases := []struct {
		name   string
		native string
		tool   string
		cl     *model.CommandList
		want   []string
	}{
		{"cat relative", "PostToolUse", "Bash", bashList(node("cat", rd("src/a.go", "cat"))), []string{"/repo/src/a.go"}},
		{"cat absolute", "PostToolUse", "Bash", bashList(node("cat", rd("/etc/hosts", "cat"))), []string{"/etc/hosts"}},
		{"home relative", "PostToolUse", "Bash", bashList(node("cat", rd("~/notes.md", "cat"))), []string{"HOME/notes.md"}},
		{"several nodes, order kept, duplicates removed", "PostToolUse", "Bash",
			bashList(node("head", rd("b.go", "head"), rd("a.go", "head")), node("cat", rd("b.go", "cat"), rd("./a.go", "cat"))),
			[]string{"/repo/b.go", "/repo/a.go"}},
		{"redirect read", "PostToolUse", "Bash", bashList(node("wc", rd("in.txt", "redirect"))), []string{"/repo/in.txt"}},
		{"dynamic and empty paths are skipped", "PostToolUse", "Bash", bashList(node("cat", dynRd("cat"), rd("", "cat"), rd("ok.go", "cat"))), []string{"/repo/ok.go"}},
		{"cd drops relative reads, keeps absolute", "PostToolUse", "Bash",
			bashList(node("cd"), node("cat", rd("a.go", "cat"), rd("/abs/b.go", "cat"), rd("~/c.go", "cat"))),
			[]string{"/abs/b.go", "HOME/c.go"}},
		{"pushd and popd count as cd", "PostToolUse", "Bash", bashList(node("pushd"), node("cat", rd("a.go", "cat"))), []string{}},
		{"powershell Get-Content", "PostToolUse", "PowerShell", psList(node("get-content", rd("a.txt", "get-content"))), []string{"/repo/a.txt"}},
		{"powershell Set-Location (any case) drops relative reads", "PostToolUse", "PowerShell",
			psList(node("Set-Location"), node("get-content", rd("a.txt", "get-content"), rd("/abs.txt", "get-content"))), []string{"/abs.txt"}},
		{"nothing read", "PostToolUse", "Bash", bashList(node("ls")), []string{}},
		{"PreToolUse never registers", "PreToolUse", "Bash", bashList(node("cat", rd("a.go", "cat"))), []string{}},
		{"PostToolUseFailure never registers", "PostToolUseFailure", "Bash", bashList(node("cat", rd("a.go", "cat"))), []string{}},
		{"writes are not reads", "PostToolUse", "Bash",
			bashList(model.CommandNode{Argv: []model.Word{w("tee")}, Name: "tee", Writes: []model.WriteTarget{rd("out.txt", "tee")}}), []string{}},
		{"unknown flag on the list is ignored", "PostToolUse", "Bash", &model.CommandList{Shell: model.ShellBash, Unknown: true, Nodes: []model.CommandNode{node("cat", rd("a.go", "cat"))}}, []string{"/repo/a.go"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.deps.Bash, h.deps.PS = starParser{c.cl}, starParser{c.cl}
			in := `{"session_id":"s","cwd":"/repo","tool_name":"` + c.tool + `","tool_use_id":"t1","tool_input":{"command":"x"}}`
			mustExit0(t, h, h.claude(c.native, in))
			got := h.state.lastCommit(t).delta.ReadPaths
			if got == nil {
				got = []string{}
			}
			want := make([]string, len(c.want))
			for i, p := range c.want {
				want[i] = replaceHome(p, h.sb.Home)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("read paths = %v, want %v", got, want)
			}
		})
	}
}

func replaceHome(p, home string) string {
	if len(p) >= 4 && p[:4] == "HOME" {
		return home + p[4:]
	}
	return p
}

func TestShellParsedLazilyAndOnce(t *testing.T) {
	// PreToolUse: the hook itself never parses (rules that need it do).
	h := newHarness(t)
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	if n := h.bash.parseCalls(); n != 0 {
		t.Errorf("parse calls = %d, want 0", n)
	}
	// PostToolUse: parsed once even though the evaluator also asks.
	h = newHarness(t)
	h.engine.each = func(in *model.EvalInput) *model.Verdict {
		in.Commands.Get()
		in.Commands.Get()
		return &model.Verdict{}
	}
	mustExit0(t, h, h.claude("PostToolUse", preToolBash))
	if n := h.bash.parseCalls(); n != 1 {
		t.Errorf("parse calls = %d, want 1", n)
	}
	// File tools do not parse anything.
	h = newHarness(t)
	mustExit0(t, h, h.claude("PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Edit","tool_use_id":"e","tool_input":{"file_path":"a.go"}}`))
	if n := h.bash.parseCalls(); n != 0 {
		t.Errorf("parse calls = %d, want 0", n)
	}
}

func TestShellReadsParseFailureIsNotAnError(t *testing.T) {
	for _, mod := range []func(*harness){
		func(h *harness) { h.bash.err = errTest },
		func(h *harness) { h.bash.panicv = "boom" },
		func(h *harness) { h.deps.Bash, h.deps.PS = nil, nil },
	} {
		h := newHarness(t)
		mod(h)
		mustExit0(t, h, h.claude("PostToolUse", preToolBash))
		if got := h.state.lastCommit(t).delta.ReadPaths; len(got) != 0 {
			t.Errorf("read paths = %v", got)
		}
	}
}

func TestFileToolReadRegistration(t *testing.T) {
	cases := []struct {
		name    string
		client  model.ClientID
		native  string
		payload string
		verdict *model.Verdict
		want    []string
	}{
		{"Read", model.ClientClaude, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Read","tool_use_id":"1","tool_input":{"file_path":"src/a.go"}}`, nil, []string{"/repo/src/a.go"}},
		{"Edit counts as read", model.ClientClaude, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Edit","tool_use_id":"1","tool_input":{"file_path":"/abs/a.go"}}`, nil, []string{"/abs/a.go"}},
		{"Write", model.ClientClaude, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Write","tool_use_id":"1","tool_input":{"file_path":"n.txt"}}`, nil, []string{"/repo/n.txt"}},
		{"MultiEdit", model.ClientClaude, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"MultiEdit","tool_use_id":"1","tool_input":{"file_path":"m.go"}}`, nil, []string{"/repo/m.go"}},
		{"NotebookEdit", model.ClientClaude, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"NotebookEdit","tool_use_id":"1","tool_input":{"notebook_path":"n.ipynb"}}`, nil, []string{"/repo/n.ipynb"}},
		{"several targets deduplicated", model.ClientCodex, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Edit","tool_use_id":"1","tool_input":{"targets":["a.go","./a.go","b.go"]}}`, nil, []string{"/repo/a.go", "/repo/b.go"}},
		{"Read before it ran is not a read", model.ClientClaude, "PreToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Read","tool_use_id":"1","tool_input":{"file_path":"a.go"}}`, nil, []string{}},
		{"failed Edit is not a read", model.ClientClaude, "PostToolUseFailure", `{"session_id":"s","cwd":"/repo","tool_name":"Edit","tool_use_id":"1","tool_input":{"file_path":"a.go"}}`, nil, []string{}},
		{"other tools do not register", model.ClientClaude, "PostToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Grep","tool_use_id":"1","tool_input":{"path":"a.go"}}`, nil, []string{}},
		{"cursor beforeReadFile registers", model.ClientCursor, "beforeReadFile", `{"conversation_id":"c","cwd":"/repo","path":"a.go"}`, nil, []string{"/repo/a.go"}},
		{"cursor beforeReadFile denied does not register", model.ClientCursor, "beforeReadFile", `{"conversation_id":"c","cwd":"/repo","path":"a.go"}`,
			&model.Verdict{Action: model.ActionDeny, Hits: []model.RuleHit{{RuleID: "r", Action: model.ActionDeny, Effective: model.ActionDeny}}}, []string{}},
		{"cursor beforeReadFile ask does not register", model.ClientCursor, "beforeReadFile", `{"conversation_id":"c","cwd":"/repo","path":"a.go"}`,
			&model.Verdict{Action: model.ActionAsk, Hits: []model.RuleHit{{RuleID: "r", Action: model.ActionAsk, Effective: model.ActionAsk}}}, []string{}},
		{"cursor beforeReadFile warn is fine (the verdict is not blocking)", model.ClientCursor, "beforeReadFile", `{"conversation_id":"c","cwd":"/repo","path":"a.go"}`,
			&model.Verdict{Action: model.ActionLog, Hits: []model.RuleHit{{RuleID: "r", Action: model.ActionLog, Target: model.ActionDeny, Effective: model.ActionLog}}}, []string{"/repo/a.go"}},
		{"cursor preToolUse Read does not register", model.ClientCursor, "preToolUse", `{"conversation_id":"c","cwd":"/repo","tool_name":"Read","path":"a.go"}`, nil, []string{}},
		{"claude PreToolUse Read does not register", model.ClientClaude, "PreToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Read","tool_input":{"file_path":"a.go"}}`, nil, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if c.verdict != nil {
				h.engine.verdict = c.verdict
			}
			code := h.main(c.client, c.native, c.payload)
			mustExit0(t, h, code)
			got := h.state.lastCommit(t).delta.ReadPaths
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("read paths = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWorldPathExists(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	wv := newWorld(h.deps.Env, h.deps.FS, h.repo, nil)
	for _, c := range []struct {
		name string
		path string
		want model.Tri
	}{
		{"file", filepath.ToSlash(file), model.TriTrue},
		{"dir", filepath.ToSlash(filepath.Join(dir, "d")), model.TriTrue},
		{"missing", filepath.ToSlash(filepath.Join(dir, "nope")), model.TriFalse},
		{"below a regular file (ENOTDIR)", filepath.ToSlash(filepath.Join(file, "child")), model.TriFalse},
		{"empty path", "", model.TriUnknown},
	} {
		if got := wv.PathExists(c.path); got != c.want {
			t.Errorf("%s: PathExists(%q) = %v, want %v", c.name, c.path, got, c.want)
		}
	}
	// Any other Lstat error is unknown.
	wv.fsys = errFS{h.deps.FS}
	if got := wv.PathExists("/anything"); got != model.TriUnknown {
		t.Errorf("EACCES-like error: %v", got)
	}
	if v, ok := wv.Getenv("HOME"); !ok || v != h.sb.Home {
		t.Errorf("Getenv = %q %v", v, ok)
	}
	if _, ok := wv.Getenv("NOPE_NOT_SET"); ok {
		t.Error("unset variable reported as set")
	}
	if b, tri := wv.Branch(); b != "" || tri != model.TriUnknown {
		t.Errorf("branch without repo = %q %v", b, tri)
	}
	wv = newWorld(h.deps.Env, h.deps.FS, h.repo, h.repo.info)
	h.repo.branch, h.repo.branchTr = "", model.TriFalse // detached
	if b, tri := wv.Branch(); b != "" || tri != model.TriFalse {
		t.Errorf("detached = %q %v", b, tri)
	}
}
