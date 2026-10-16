package sandbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// ---------- MatchMandatory ----------

func TestMatchMandatoryTable(t *testing.T) {
	var tab struct {
		Repo []struct {
			Rel  string
			Want bool
		} `json:"repo"`
		Gitdir []struct {
			Rel  string
			Want bool
		} `json:"gitdir"`
	}
	readJSON(t, "mandatory.json", &tab)
	for _, c := range tab.Repo {
		if got := MatchMandatory(RepoMandatoryDeny, true, c.Rel); got != c.Want {
			t.Errorf("repo %q: got %v, want %v", c.Rel, got, c.Want)
		}
	}
	for _, c := range tab.Gitdir {
		if got := MatchMandatory(GitDirMandatoryDeny, false, c.Rel); got != c.Want {
			t.Errorf("gitdir %q: got %v, want %v", c.Rel, got, c.Want)
		}
	}
}

func TestMatchMandatoryContract(t *testing.T) {
	// The ten entries of docs/p2-design.md §3.4 are all present, in order,
	// and the list only ever grows after them.
	want := []string{".ballast/", ".claude/", ".git", ".git/hooks/", ".git/config", ".git/config.worktree", ".gitmodules", ".mcp.json", ".vscode/", ".idea/"}
	if len(RepoMandatoryDeny) < len(want) {
		t.Fatalf("RepoMandatoryDeny shrank: %v", RepoMandatoryDeny)
	}
	for i, w := range want {
		if RepoMandatoryDeny[i] != w {
			t.Errorf("RepoMandatoryDeny[%d] = %q, want %q", i, RepoMandatoryDeny[i], w)
		}
	}
	wantG := []string{"hooks/", "config", "config.worktree", "commondir", "modules/**/hooks/", "modules/**/config", "modules/**/config.worktree", "modules/**/commondir", "worktrees/*/config.worktree", "worktrees/*/commondir", "worktrees/*/gitdir"}
	for i, w := range wantG {
		if i >= len(GitDirMandatoryDeny) || GitDirMandatoryDeny[i] != w {
			t.Errorf("GitDirMandatoryDeny[%d] differs from the contract", i)
		}
	}
	// The sandbox-runtime "dangerous files" are all covered.
	for _, f := range []string{".gitconfig", ".gitmodules", ".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".profile", ".ripgreprc", ".mcp.json", ".vscode/x", ".idea/x", ".claude/commands/x", ".claude/agents/x", ".git/hooks/x", ".git/config"} {
		if !MatchMandatory(RepoMandatoryDeny, true, f) || !MatchMandatory(RepoMandatoryDeny, true, "deep/er/"+f) {
			t.Errorf("%s is not covered at any depth", f)
		}
	}
}

// The darwin regexes are derived from the same patterns as MatchMandatory:
// check they agree on a generated universe of paths (the regex dialect used by
// SBPL is a subset of RE2 for the constructs rePattern emits).
func TestDarwinRegexAgreesWithMatcher(t *testing.T) {
	names := []string{".git", "hooks", "config", "modules", "worktrees", "a", "b", ".claude", ".vscode", "x", "gitdir", "commondir", "config.worktree", ".gitmodules", "é ("}
	var rels []string
	var gen func(prefix []string, depth int)
	gen = func(prefix []string, depth int) {
		if len(prefix) > 0 {
			rels = append(rels, strings.Join(prefix, "/"))
		}
		if depth == 0 {
			return
		}
		for _, n := range names {
			gen(append(append([]string(nil), prefix...), n), depth-1)
		}
	}
	gen(nil, 3)
	for _, tc := range []struct {
		root    string
		pats    []string
		isRepo  bool
		comment string
	}{
		{"/w/re po (x)/é", RepoMandatoryDeny, true, "repo"},
		{"/g/dir.git", GitDirMandatoryDeny, false, "gitdir"},
	} {
		pats := parsePatterns(tc.pats)
		var res []*regexp.Regexp
		for _, p := range pats {
			res = append(res, regexp.MustCompile(rePattern(tc.root, p, tc.isRepo)))
		}
		for _, rel := range rels {
			want := MatchMandatory(tc.pats, tc.isRepo, rel)
			got := false
			for _, re := range res {
				if re.MatchString(tc.root + "/" + rel) {
					got = true
					break
				}
			}
			if got != want {
				t.Fatalf("%s %q: regex %v, matcher %v", tc.comment, rel, got, want)
			}
		}
	}
}

func FuzzMatchMandatory(f *testing.F) {
	for _, s := range []string{".git/config", "a/b/.git", "modules/a/b/config", "worktrees/w/gitdir", "", "//", "a//b", "../x", ".git/hooks/x/y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		// Never panics; agrees with the darwin regex for well-formed paths.
		r := MatchMandatory(RepoMandatoryDeny, true, rel)
		g := MatchMandatory(GitDirMandatoryDeny, false, rel)
		if _, ok := splitRel(rel); !ok {
			if r || g {
				t.Fatalf("ill-formed %q matched", rel)
			}
			return
		}
		if strings.ContainsAny(rel, "\n\x00") {
			return
		}
		root := "/r"
		any := false
		for _, p := range parsePatterns(RepoMandatoryDeny) {
			if regexp.MustCompile(rePattern(root, p, true)).MatchString(root + "/" + rel) {
				any = true
			}
		}
		if any != r {
			t.Fatalf("repo %q: regex %v, matcher %v", rel, any, r)
		}
		any = false
		for _, p := range parsePatterns(GitDirMandatoryDeny) {
			if regexp.MustCompile(rePattern(root, p, false)).MatchString(root + "/" + rel) {
				any = true
			}
		}
		if any != g {
			t.Fatalf("gitdir %q: regex %v, matcher %v", rel, any, g)
		}
	})
}

// ---------- environment helpers ----------

func TestHostEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "GIT_CONFIG_PARAMETERS='core.fsmonitor=x'", "HOME=/h",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_TRACE=/tmp/x", "GIT_EXEC_PATH=/x",
		"LD_PRELOAD=/x.so", "LD_LIBRARY_PATH=/l", "DYLD_INSERT_LIBRARIES=/y", "DYLD_LIBRARY_PATH=/z",
		"BASH_ENV=/e", "ENV=/e", "SHELLOPTS=xtrace", "BASHOPTS=x", "CDPATH=/c", "LANG=C.UTF-8",
		"GITHUB_TOKEN=notgit", "MYLD_X=1", "PYTHONPATH=/p",
	}
	want := []string{"PATH=/usr/bin", "HOME=/h", "LANG=C.UTF-8", "GITHUB_TOKEN=notgit", "MYLD_X=1", "PYTHONPATH=/p"}
	got := HostEnv(in)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("HostEnv = %v, want %v", got, want)
	}
	if HostEnv(nil) == nil {
		t.Error("HostEnv(nil) should be a non-nil empty slice")
	}
}

func TestDecide(t *testing.T) {
	boom := errors.New("boom")
	type row struct {
		policy model.SandboxPolicy
		origin model.VerifyOrigin
		avail  Kind
		err    error
		want   Kind
		wantEr bool
	}
	rows := []row{
		// hook: mandatory whatever the policy says.
		{model.SandboxRequire, model.OriginHook, KindBwrap, nil, KindBwrap, false},
		{model.SandboxPrefer, model.OriginHook, KindSandboxExec, nil, KindSandboxExec, false},
		{model.SandboxOff, model.OriginHook, KindBwrap, nil, KindBwrap, false},
		{"", model.OriginHook, KindBwrap, nil, KindBwrap, false},
		{model.SandboxRequire, model.OriginHook, KindNone, boom, KindNone, true},
		{model.SandboxPrefer, model.OriginHook, KindNone, boom, KindNone, true},
		{model.SandboxOff, model.OriginHook, KindNone, boom, KindNone, true},
		{model.SandboxOff, model.OriginHook, KindNone, nil, KindNone, true},
		{"", model.OriginHook, "", nil, KindNone, true},
		{model.SandboxOff, model.OriginHook, KindBwrap, boom, KindNone, true},
		// cli.
		{model.SandboxRequire, model.OriginCLI, KindBwrap, nil, KindBwrap, false},
		{model.SandboxRequire, model.OriginCLI, KindNone, boom, KindNone, true},
		{model.SandboxRequire, model.OriginCLI, KindNone, nil, KindNone, true},
		{model.SandboxPrefer, model.OriginCLI, KindSandboxExec, nil, KindSandboxExec, false},
		{model.SandboxPrefer, model.OriginCLI, KindNone, boom, KindNone, false},
		{model.SandboxPrefer, model.OriginCLI, KindNone, nil, KindNone, false},
		{model.SandboxOff, model.OriginCLI, KindBwrap, nil, KindNone, false},
		{model.SandboxOff, model.OriginCLI, KindNone, boom, KindNone, false},
		// fail closed: unknown policy behaves like require; unknown origin like hook.
		{"", model.OriginCLI, KindNone, boom, KindNone, true},
		{"weird", model.OriginCLI, KindBwrap, nil, KindBwrap, false},
		{model.SandboxOff, "weird", KindNone, nil, KindNone, true},
		{model.SandboxPrefer, "weird", KindBwrap, nil, KindBwrap, false},
		// an empty origin (a runner that forgot to set it) is treated like hook.
		{model.SandboxOff, "", KindBwrap, nil, KindBwrap, false},
		{model.SandboxPrefer, "", KindBwrap, nil, KindBwrap, false},
		{model.SandboxOff, "", KindNone, nil, KindNone, true},
		{model.SandboxPrefer, "", KindNone, boom, KindNone, true},
	}
	for i, r := range rows {
		got, err := Decide(r.policy, r.origin, r.avail, r.err)
		if got != r.want || (err != nil) != r.wantEr {
			t.Errorf("row %d %+v: got (%q, %v)", i, r, got, err)
		}
		if err != nil && !errors.Is(err, ErrUnavailable) {
			t.Errorf("row %d: error does not wrap ErrUnavailable: %v", i, err)
		}
	}
}
