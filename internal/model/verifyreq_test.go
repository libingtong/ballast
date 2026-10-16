package model_test

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/psparse"
)

const goodID = "20261016T100000-0a1b2c"

func parseBash(t testing.TB, src string) *model.CommandList {
	t.Helper()
	cl, err := bashparse.Parse(src, bashparse.Options{})
	if err != nil {
		return nil
	}
	return cl
}

func TestParseVerifyRequestBash(t *testing.T) {
	tests := []struct {
		src   string
		ok    bool
		names []string
	}{
		{"ballast verify", true, nil},
		{"ballast verify test", true, []string{"test"}},
		{"ballast verify test lint build.go_2-x", true, []string{"test", "lint", "build.go_2-x"}},
		{"ballast verify test test lint", true, []string{"test", "lint"}},
		{"/usr/local/bin/ballast verify test", true, []string{"test"}},
		{"./ballast verify test", true, []string{"test"}},
		{"'ballast' verify 'test'", true, []string{"test"}},
		{"ballast.exe verify test", true, []string{"test"}},
		{"ballast verify " + strings.Repeat("a", 64), true, []string{strings.Repeat("a", 64)}},
		{"ballast verify " + strings.Repeat("a", 65), false, nil},
		{"ballast verify " + strings.Join(strings.Fields(strings.Repeat("n ", 16)), " "), true, []string{"n"}},
		{"ballast verify " + strings.Join(strings.Fields(strings.Repeat("n ", 17)), " "), false, nil},
		// not canonical
		{"", false, nil},
		{"ballast", false, nil},
		{"ballast status", false, nil},
		{"ballast verify --status 20261016T100000-0a1b2c", false, nil},
		{"ballast verify --foreground test", false, nil},
		{"ballast verify --check", false, nil},
		{"ballast verify -", false, nil},
		{"ballast verify .hidden", false, nil},
		{"ballast verify _x", false, nil},
		{"ballast verify a/b", false, nil},
		{"ballast verify test | cat", false, nil},
		{"ballast verify test > out", false, nil},
		{"ballast verify test < in", false, nil},
		{"ballast verify test &", false, nil},
		{"ballast verify test; ls", false, nil},
		{"ballast verify test && ls", false, nil},
		{"ls && ballast verify test", false, nil},
		{"env X=1 ballast verify test", false, nil},
		{"sudo ballast verify test", false, nil},
		{"timeout 5 ballast verify test", false, nil},
		{"nohup ballast verify test", false, nil},
		{"ballast verify $NAME", false, nil},
		{"ballast verify \"$NAME\"", false, nil},
		{"ballast verify $(echo test)", false, nil},
		{"ballast verify `echo test`", false, nil},
		{"ballast verify te*", false, nil},
		{"$B verify test", false, nil},
		{"ballastx verify test", false, nil},
		{"xballast verify test", false, nil},
		{"BALLAST verify test", false, nil},
		{"ballast Verify test", false, nil},
		{"for i in 1 2; do ballast verify test; done", false, nil},
		{"while true; do ballast verify test; done", false, nil},
		{"(ballast verify test)", true, []string{"test"}}, // a lone subshell is still exactly one command
		{"bash -c 'ballast verify test'", false, nil},
		{"echo $(ballast verify test)", false, nil},
		{"ballast verify 'a b'", false, nil},
		{"ballast verify é", false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			cl := parseBash(t, tc.src)
			req, ok := model.ParseVerifyRequest(cl)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (list %+v)", ok, tc.ok, cl)
			}
			if !ok {
				if !reflect.DeepEqual(req, model.VerifyRequest{}) {
					t.Errorf("rejected request not zero: %+v", req)
				}
				return
			}
			if !reflect.DeepEqual(req.Names, tc.names) {
				t.Errorf("names = %v, want %v", req.Names, tc.names)
			}
		})
	}
}

func TestParseVerifyRequestPowerShell(t *testing.T) {
	tests := []struct {
		src string
		ok  bool
	}{
		{"ballast verify test", true},
		{"ballast.exe verify test lint", true},
		{`C:\bin\ballast.exe verify test`, true},
		{"ballast verify", true},
		{"ballast verify test | Out-Null", false},
		{"ballast verify test > out.txt", false},
		{"ballast verify $env:X", false},
		{"ballast verify -Name test", false},
		{"ballast verify test; ls", false},
		{"Start-Job { ballast verify test }", false},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			cl, err := psparse.Parse(tc.src)
			if err != nil {
				cl = nil
			}
			if _, ok := model.ParseVerifyRequest(cl); ok != tc.ok {
				t.Fatalf("ok = %v, want %v (list %+v)", ok, tc.ok, cl)
			}
		})
	}
}

// TestParseVerifyRequestStructural exercises every structural precondition on
// hand-built command lists (independent of what the parsers emit).
func TestParseVerifyRequestStructural(t *testing.T) {
	good := func() *model.CommandList {
		return &model.CommandList{Shell: model.ShellBash, Nodes: []model.CommandNode{{
			Argv: []model.Word{{Lit: "ballast"}, {Lit: "verify"}, {Lit: "test"}}, Name: "ballast",
		}}}
	}
	if _, ok := model.ParseVerifyRequest(good()); !ok {
		t.Fatal("baseline not accepted")
	}
	mut := map[string]func(*model.CommandList){
		"unknown":  func(c *model.CommandList) { c.Unknown = true },
		"two":      func(c *model.CommandList) { c.Nodes = append(c.Nodes, c.Nodes[0]) },
		"none":     func(c *model.CommandList) { c.Nodes = nil },
		"pipes":    func(c *model.CommandList) { c.Pipes = []model.PipeEdge{{From: 0, To: 0}} },
		"depth":    func(c *model.CommandList) { c.Nodes[0].Depth = 1 },
		"async":    func(c *model.CommandList) { c.Nodes[0].Async = true },
		"in_subst": func(c *model.CommandList) { c.Nodes[0].InSubst = true },
		"loop":     func(c *model.CommandList) { c.Nodes[0].Loop = &model.LoopInfo{Kind: model.LoopForList} },
		"redirect": func(c *model.CommandList) { c.Nodes[0].Redirects = []model.Redirect{{Op: ">"}} },
		"wrapper":  func(c *model.CommandList) { c.Nodes[0].Wrappers = []string{"env"} },
		"sudo":     func(c *model.CommandList) { c.Nodes[0].Sudo = true },
		"params":   func(c *model.CommandList) { c.Nodes[0].Params = map[string][]model.Word{"-x": nil} },
		"dyn0":     func(c *model.CommandList) { c.Nodes[0].Argv[0].Dynamic = true },
		"dyn1":     func(c *model.CommandList) { c.Nodes[0].Argv[1].Dynamic = true },
		"dyn2":     func(c *model.CommandList) { c.Nodes[0].Argv[2].Dynamic = true },
		"short":    func(c *model.CommandList) { c.Nodes[0].Argv = c.Nodes[0].Argv[:1] },
		"empty":    func(c *model.CommandList) { c.Nodes[0].Argv = nil },
		"emptyarg": func(c *model.CommandList) { c.Nodes[0].Argv[2].Lit = "" },
	}
	for name, f := range mut {
		t.Run(name, func(t *testing.T) {
			c := good()
			f(c)
			if _, ok := model.ParseVerifyRequest(c); ok {
				t.Errorf("accepted %+v", c)
			}
		})
	}
	if _, ok := model.ParseVerifyRequest(nil); ok {
		t.Error("nil accepted")
	}
}

func TestHookStartArgvShape(t *testing.T) {
	got := model.HookStartArgv("/opt/ballast", model.HookStartRequest{
		RequestID: goodID, Cwd: "/repo", Names: []string{"test", "lint"}, Client: model.ClientClaude,
		SessionID: "s1", AgentID: "a1", ProjectDir: "/repo/sub",
	})
	want := []string{"/opt/ballast", "verify", "--hook-start", "--request", goodID, "--cwd", "/repo", "--client", "claude-code",
		"--session", "s1", "--agent", "a1", "--project-dir", "/repo/sub", "--", "test", "lint"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %q\nwant   %q", got, want)
	}
	min := model.HookStartArgv("/b", model.HookStartRequest{RequestID: goodID, Cwd: "/r", Client: model.ClientClaude})
	wantMin := []string{"/b", "verify", "--hook-start", "--request", goodID, "--cwd", "/r", "--client", "claude-code", "--"}
	if !reflect.DeepEqual(min, wantMin) {
		t.Errorf("minimal argv = %q, want %q", min, wantMin)
	}
}

func TestHookStartArgvRoundTrip(t *testing.T) {
	reqs := []model.HookStartRequest{
		{RequestID: goodID, Cwd: "/repo", Client: model.ClientClaude},
		{RequestID: goodID, Cwd: "/repo", Client: model.ClientClaude, Names: []string{"test"}},
		{RequestID: goodID, Cwd: "/my repo/with space", Client: model.ClientClaude, Names: []string{"a", "b.c"}, SessionID: "s 1"},
		{RequestID: goodID, Cwd: "/-leading-dash", Client: model.ClientClaude, SessionID: "-s", AgentID: "--agent", ProjectDir: "/--x"},
		{RequestID: goodID, Cwd: "/--", Client: model.ClientClaude, SessionID: "--", AgentID: "--", ProjectDir: "/p"},
		{RequestID: goodID, Cwd: "/r", Client: model.ClientClaude, ProjectDir: "/repo/sub dir"},
		{RequestID: goodID, Cwd: "/ünï/路径", Client: model.ClientClaude, Names: []string{"x"}},
		{RequestID: goodID, Cwd: "/r", Client: model.ClientClaude, SessionID: "--hook-start", AgentID: "--cwd"},
	}
	for _, r := range reqs {
		argv := model.HookStartArgv("/opt/ballast", r)
		got, err := model.ParseHookStartArgv(argv[1:])
		if err != nil {
			t.Errorf("parse %q: %v", argv, err)
			continue
		}
		if !reflect.DeepEqual(got, r) {
			t.Errorf("round trip:\n got %+v\nwant %+v", got, r)
		}
	}
}

func TestParseHookStartArgvRejects(t *testing.T) {
	base := func() []string {
		return model.HookStartArgv("/b", model.HookStartRequest{
			RequestID: goodID, Cwd: "/r", Client: model.ClientClaude, Names: []string{"test"}})[1:]
	}
	if _, err := model.ParseHookStartArgv(base()); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	replace := func(old, nw string) []string {
		a := base()
		for i, s := range a {
			if s == old {
				a[i] = nw
				return a
			}
		}
		t.Fatalf("no %q in %q", old, a)
		return nil
	}
	cases := map[string][]string{
		"empty":             nil,
		"wrong verb":        append([]string{"status"}, base()[1:]...),
		"no hook-start":     append([]string{"verify"}, base()[2:]...),
		"bad id (short)":    replace(goodID, "20261016T100000-0a1b2"),
		"bad id (upper)":    replace(goodID, "20261016T100000-0A1B2C"),
		"bad id (T)":        replace(goodID, "20261016X100000-0a1b2c"),
		"bad id (path)":     replace(goodID, "../../etc/passwd-aaaaaa"),
		"relative cwd":      replace("/r", "r"),
		"empty cwd":         replace("/r", ""),
		"bad name flag":     replace("test", "--status"),
		"bad name slash":    replace("test", "a/b"),
		"bad name dollar":   replace("test", "$X"),
		"bad name empty":    replace("test", ""),
		"unknown flag":      append([]string{"verify", "--hook-start", "--bogus", "x"}, base()[2:]...),
		"dup flag":          append(base()[:len(base())-2:len(base())-2], "--cwd", "/z", "--", "test"),
		"missing request":   {"verify", "--hook-start", "--cwd", "/r", "--client", "claude-code", "--"},
		"missing cwd":       {"verify", "--hook-start", "--request", goodID, "--client", "claude-code", "--"},
		"missing client":    {"verify", "--hook-start", "--request", goodID, "--cwd", "/r", "--"},
		"flag without val":  {"verify", "--hook-start", "--request", goodID, "--cwd", "/r", "--client"},
		"relative projdir":  {"verify", "--hook-start", "--request", goodID, "--cwd", "/r", "--client", "claude-code", "--project-dir", "sub", "--"},
		"empty projdir":     {"verify", "--hook-start", "--request", goodID, "--cwd", "/r", "--client", "claude-code", "--project-dir", "", "--"},
		"too many names":    append(base()[:len(base())-1:len(base())-1], strings.Fields(strings.Repeat("n ", 17))...),
		"loose positionals": {"verify", "--hook-start", "--request", goodID, "--cwd", "/r", "--client", "claude-code", "stray"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := model.ParseHookStartArgv(args); err == nil {
				t.Errorf("accepted %q → %+v", args, got)
			}
		})
	}
}

// Anything ParseVerifyRequest accepts must survive the HookStartArgv round
// trip, and anything the round trip yields must satisfy the same name rules.
func TestVerifyRequestToArgvConsistency(t *testing.T) {
	for _, src := range []string{"ballast verify", "ballast verify test lint", "ballast verify a.b-c_d x"} {
		req, ok := model.ParseVerifyRequest(parseBash(t, src))
		if !ok {
			t.Fatalf("%q rejected", src)
		}
		hs := model.HookStartRequest{RequestID: goodID, Cwd: "/r", Client: model.ClientClaude, Names: req.Names}
		got, err := model.ParseHookStartArgv(model.HookStartArgv("/b", hs)[1:])
		if err != nil || !reflect.DeepEqual(got, hs) {
			t.Errorf("%q: got %+v err %v, want %+v", src, got, err, hs)
		}
	}
}

func FuzzParseVerifyRequest(f *testing.F) {
	for _, s := range []string{
		"ballast verify", "ballast verify test", "ballast verify --status x", "ballast verify test | cat",
		"env X=1 ballast verify test", "ballast verify $X", "a; ballast verify b", "ballast verify '", "\xff",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		for _, cl := range []*model.CommandList{parseBash(t, src), psList(src)} {
			req, ok := model.ParseVerifyRequest(cl)
			if !ok {
				if len(req.Names) != 0 {
					t.Fatalf("rejected but names set: %+v", req)
				}
				continue
			}
			if len(cl.Nodes) != 1 || len(req.Names) > model.MaxVerifyRequestNames {
				t.Fatalf("accepted a non-canonical list: %+v → %+v", cl, req)
			}
			seen := map[string]bool{}
			for _, n := range req.Names {
				if !validName(n) || seen[n] {
					t.Fatalf("bad or duplicate name %q in %+v", n, req)
				}
				seen[n] = true
			}
			hs := model.HookStartRequest{RequestID: goodID, Cwd: "/r", Client: model.ClientClaude, Names: req.Names}
			got, err := model.ParseHookStartArgv(model.HookStartArgv("/b", hs)[1:])
			if err != nil || !reflect.DeepEqual(got, hs) {
				t.Fatalf("argv round trip failed: %+v err %v", got, err)
			}
		}
	})
}

func psList(src string) *model.CommandList {
	cl, err := psparse.Parse(src)
	if err != nil {
		return nil
	}
	return cl
}

func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			i > 0 && (c == '.' || c == '_' || c == '-')
		if !ok {
			return false
		}
	}
	return true
}

func FuzzHookStartArgv(f *testing.F) {
	f.Add(goodID, "/repo", "claude-code", "s", "a", "/p", "test")
	f.Add(goodID, "/-x y", "c", "--", "--cwd", "/--", "n")
	f.Add("bad", "rel", "", "", "", "rel", "$x")
	f.Fuzz(func(t *testing.T, id, cwd, client, sid, aid, pdir, name string) {
		var names []string
		if name != "" {
			names = []string{name}
		}
		req := model.HookStartRequest{RequestID: id, Cwd: cwd, Names: names, Client: model.ClientID(client), SessionID: sid, AgentID: aid, ProjectDir: pdir}
		argv := model.HookStartArgv("/opt/ballast", req)
		got, err := model.ParseHookStartArgv(argv[1:])
		valid := validRequestID(id) && strings.HasPrefix(cwd, "/") &&
			(pdir == "" || strings.HasPrefix(pdir, "/")) && (name == "" || validName(name))
		if valid {
			if err != nil || !reflect.DeepEqual(got, req) {
				t.Fatalf("valid request failed round trip: %+v → %q → %+v, %v", req, argv, got, err)
			}
			return
		}
		if err == nil && !reflect.DeepEqual(got, req) {
			t.Fatalf("accepted but changed: %+v → %+v", req, got)
		}
		if err == nil {
			// Accepted although my predicate said invalid: only legal when
			// the predicate is stricter (non-POSIX absolute paths).
			if !validRequestID(got.RequestID) || got.Cwd == "" {
				t.Fatalf("accepted an invalid request: %+v", got)
			}
		}
	})
}

var requestIDRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{6}$`)

func validRequestID(id string) bool { return requestIDRE.MatchString(id) }
