//go:build unix

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// ---------- helpers ----------

// realTemp returns a symlink-free temporary directory (t.TempDir on macOS is
// under /var, a symlink to /private/var).
func realTemp(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	r, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func expandR(root, s string) string {
	if s == "" || s == "/" {
		return s
	}
	return strings.ReplaceAll(s, "R:", root+"/")
}

func expandAll(root string, ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, expandR(root, s))
	}
	return out
}

type fileSpec struct {
	P string  `json:"p"`
	C string  `json:"c"`
	L *string `json:"l"`
	D bool    `json:"d"`
}

func buildFiles(t *testing.T, root string, files []fileSpec) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, f.P)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		switch {
		case f.L != nil:
			if err := os.Symlink(expandR(root, *f.L), p); err != nil {
				t.Fatal(err)
			}
		case f.D:
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.WriteFile(p, []byte(expandR(root, f.C)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCacheEnv(t *testing.T) {
	cur := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	// Default: every CacheVars entry points below tmp, in order.
	got := CacheEnv("/tmp/job", []string{"/w/repo"}, cur(nil))
	if len(got) != len(CacheVars) {
		t.Fatalf("got %v", got)
	}
	for i, k := range CacheVars {
		if !strings.HasPrefix(got[i], k+"=/tmp/job/cache/") {
			t.Errorf("entry %d = %q", i, got[i])
		}
	}
	if got[0] != "GOCACHE=/tmp/job/cache/go-build" || got[1] != "XDG_CACHE_HOME=/tmp/job/cache/xdg" {
		t.Errorf("unexpected layout: %v", got)
	}
	// A value inside an allowWrite root is kept (variable omitted); outside it,
	// relative, or a sibling-prefix path is redirected.
	got = CacheEnv("/tmp/job", []string{"/w/repo", "/c/cache"}, cur(map[string]string{
		"GOCACHE":          "/c/cache/go",
		"XDG_CACHE_HOME":   "/c/cache",
		"npm_config_cache": "/c/cachex/npm", // sibling prefix, not inside
		"PIP_CACHE_DIR":    "relative/pip",
	}))
	joined := strings.Join(got, "\n")
	for _, k := range []string{"GOCACHE=", "XDG_CACHE_HOME="} {
		if strings.Contains(joined, k) {
			t.Errorf("%s should have been kept: %v", k, got)
		}
	}
	for _, k := range []string{"npm_config_cache=/tmp/job/cache/npm", "YARN_CACHE_FOLDER=", "PIP_CACHE_DIR=/tmp/job/cache/pip"} {
		if !strings.Contains(joined, k) {
			t.Errorf("%s missing: %v", k, got)
		}
	}
	// nil cur is allowed.
	if len(CacheEnv("/t", nil, nil)) != len(CacheVars) {
		t.Error("nil cur")
	}
}

// ---------- ResolveGitDirs / ValidateSpec ----------

func TestResolveGitDirsTable(t *testing.T) {
	var tab struct {
		Resolve []struct {
			Name          string     `json:"name"`
			Files         []fileSpec `json:"files"`
			WorkTree      string     `json:"workTree"`
			WantGitDir    string     `json:"wantGitDir"`
			WantCommonDir string     `json:"wantCommonDir"`
			WantErr       bool       `json:"wantErr"`
		} `json:"resolve"`
	}
	readJSON(t, "gitdirs.json", &tab)
	for _, c := range tab.Resolve {
		t.Run(c.Name, func(t *testing.T) {
			root := realTemp(t)
			buildFiles(t, root, c.Files)
			gd, cd, err := ResolveGitDirs(sys.OSFS{}, expandR(root, c.WorkTree))
			if c.WantErr {
				if !errors.Is(err, ErrInvalidSpec) {
					t.Fatalf("want ErrInvalidSpec, got gd=%q cd=%q err=%v", gd, cd, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gd != expandR(root, c.WantGitDir) || cd != expandR(root, c.WantCommonDir) {
				t.Errorf("got (%q, %q), want (%q, %q)", gd, cd, expandR(root, c.WantGitDir), expandR(root, c.WantCommonDir))
			}
		})
	}
}

type specJSON struct {
	AllowWrite []string `json:"allowWrite"`
	RepoRoots  []string `json:"repoRoots"`
	GitDirs    []string `json:"gitDirs"`
	DenyWrite  []string `json:"denyWrite"`
	DenyGlobs  []string `json:"denyGlobs"`
	TmpDir     string   `json:"tmpDir"`
	Home       string   `json:"home"`
	Cwd        string   `json:"cwd"`
	Tree       string   `json:"tree"`
	WorkRoot   string   `json:"workRoot"`
	LoaderEnv  []string `json:"loaderEnv"`
}

func (s specJSON) spec(root string) Spec {
	return Spec{
		AllowWrite: expandAll(root, s.AllowWrite), RepoRoots: expandAll(root, s.RepoRoots), GitDirs: expandAll(root, s.GitDirs),
		DenyWrite: expandAll(root, s.DenyWrite), DenyWriteGlobs: expandAll(root, s.DenyGlobs),
		TmpDir: expandR(root, s.TmpDir), Home: expandR(root, s.Home), Cwd: expandR(root, s.Cwd),
		Tree: expandR(root, s.Tree), WorkRoot: expandR(root, s.WorkRoot), LoaderEnv: s.LoaderEnv,
	}
}

func TestValidateSpecTable(t *testing.T) {
	var tab struct {
		Validate struct {
			Files []fileSpec `json:"files"`
			Cases []struct {
				Name    string   `json:"name"`
				Spec    specJSON `json:"spec"`
				WantErr bool     `json:"wantErr"`
			} `json:"cases"`
		} `json:"validate"`
	}
	readJSON(t, "gitdirs.json", &tab)
	for _, c := range tab.Validate.Cases {
		t.Run(c.Name, func(t *testing.T) {
			root := realTemp(t)
			buildFiles(t, root, tab.Validate.Files)
			err := ValidateSpec(sys.OSFS{}, c.Spec.spec(root))
			if c.WantErr {
				if !errors.Is(err, ErrInvalidSpec) {
					t.Fatalf("want ErrInvalidSpec, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// ValidateSpec rechecks the git dirs through fsys at call time: an agent that
// rewrites the gitfile after the runner computed the spec cannot widen W.
func TestValidateSpecRechecksGitDirs(t *testing.T) {
	root := realTemp(t)
	buildFiles(t, root, []fileSpec{
		{P: "main/.git/HEAD", C: "x"},
		{P: "main/.git/worktrees/w1/commondir", C: "../..\n"},
		{P: "main/.git/worktrees/w1/gitdir", C: root + "/wt/.git\n"},
		{P: "wt/.git", C: "gitdir: " + root + "/main/.git/worktrees/w1\n"},
		{P: "victim/.git/HEAD", C: "x"},
		{P: "home/x", C: "x"},
	})
	gd, cd, err := ResolveGitDirs(sys.OSFS{}, root+"/wt")
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		AllowWrite: []string{root + "/wt", gd, cd}, RepoRoots: []string{root + "/wt"}, GitDirs: []string{gd, cd},
		Home: root + "/home", Cwd: root + "/wt",
	}
	if err := ValidateSpec(sys.OSFS{}, spec); err != nil {
		t.Fatal(err)
	}
	// The agent redirects the gitfile at another repository.
	if err := os.WriteFile(root+"/wt/.git", []byte("gitdir: "+root+"/victim/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpec(sys.OSFS{}, spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("redirected gitfile accepted: %v", err)
	}
}

// ---------- AncestorPins, goldens ----------

type goldenCase struct {
	Name     string   `json:"name"`
	Spec     Spec     `json:"spec"`
	Existing []string `json:"existing"` // "p" exists, "p/" marks a directory
	Pins     []string `json:"pins"`
	Darwin   []string `json:"darwin"`
	Bwrap    []string `json:"bwrap"`
}

func existingMap(ex []string) map[string]bool {
	m := map[string]bool{}
	for _, e := range ex {
		m[e] = true
		if strings.HasSuffix(e, "/") && len(e) > 1 {
			m[strings.TrimSuffix(e, "/")] = true
		}
	}
	return m
}

func goldenSpecs() []goldenCase {
	cli := func(network model.NetworkPolicy) Spec {
		return Spec{
			AllowWrite: []string{"/w/repo"}, RepoRoots: []string{"/w/repo"}, GitDirs: []string{"/w/repo/.git"},
			DenyWrite:      []string{"/h/.ballast", "/h/.claude.json", "/h/.claude/settings.json"},
			DenyWriteGlobs: []string{"/h/.claude/settings*.json"},
			TmpDir:         "/t/job-1", Network: network, Home: "/h", Cwd: "/w/repo",
		}
	}
	cliExisting := []string{"/w/repo", "/w/repo/", "/w/repo/.git", "/w/repo/.git/", "/w/repo/.git/hooks", "/w/repo/.git/hooks/", "/w/repo/.git/config",
		"/w/repo/.claude", "/w/repo/.claude/", "/w/repo/sub/.git", "/w/repo/sub/.git/", "/w/repo/sub/.git/config", "/h/.ballast", "/h/.ballast/", "/h/.claude.json", "/h/.claude", "/h/.claude/"}
	worktree := Spec{
		AllowWrite: []string{"/w/wt", "/m/.git/worktrees/n", "/m/.git"}, RepoRoots: []string{"/w/wt"},
		GitDirs:   []string{"/m/.git/worktrees/n", "/m/.git"},
		DenyWrite: []string{"/h/.ballast"}, TmpDir: "/t/job-2", Network: model.NetworkDeny, Home: "/h", Cwd: "/w/wt/pkg",
	}
	weird := Spec{
		AllowWrite: []string{`/w/re po "q" (x)/é+ü`, "/w/cache[1]"}, RepoRoots: []string{`/w/re po "q" (x)/é+ü`},
		GitDirs:   []string{`/w/re po "q" (x)/é+ü/.git`},
		DenyWrite: []string{`/h/x y/.ballast`, `/h/back\slash/.claude.json`}, DenyWriteGlobs: []string{`/h/x y/settings*.json`},
		TmpDir: `/t/j (1)`, Network: model.NetworkLoopback, Home: "/h", Cwd: `/w/re po "q" (x)/é+ü`,
	}
	tree := Spec{
		AllowWrite: []string{"/h/.ballast/work/j1/tree", "/c/cache"}, RepoRoots: []string{"/h/.ballast/work/j1/tree"},
		DenyWrite: []string{"/h/.ballast", "/hr/.ballast", "/h/.claude.json"}, DenyWriteGlobs: []string{"/h/.claude/settings*.json"},
		TmpDir: "/t/job-3", Network: model.NetworkDeny, Home: "/h", Cwd: "/h/.ballast/work/j1/tree/pkg",
		Tree: "/h/.ballast/work/j1/tree", WorkRoot: "/h/.ballast/work",
		LoaderEnv: []string{"LD_LIBRARY_PATH=/opt/lib:/x y"},
	}
	treeExisting := []string{"/h/.ballast", "/h/.ballast/", "/h/.ballast/work/j1/tree", "/h/.ballast/work/j1/tree/", "/h/.claude.json", "/c/cache", "/c/cache/",
		"/h/.ballast/work/j1/tree/.claude", "/h/.ballast/work/j1/tree/.claude/", "/h/.ballast/work/j1/tree/a/.vscode", "/h/.ballast/work/j1/tree/a/.vscode/"}
	return []goldenCase{
		{Name: "cli deny", Spec: cli(model.NetworkDeny), Existing: cliExisting},
		{Name: "cli loopback", Spec: cli(model.NetworkLoopback), Existing: cliExisting},
		{Name: "cli allow", Spec: cli(model.NetworkAllow), Existing: cliExisting},
		{Name: "cli default network", Spec: cli(""), Existing: cliExisting},
		{Name: "linked worktree", Spec: worktree, Existing: []string{"/w/wt", "/w/wt/", "/w/wt/.git", "/m/.git", "/m/.git/", "/m/.git/worktrees/n", "/m/.git/worktrees/n/",
			"/m/.git/hooks", "/m/.git/hooks/", "/m/.git/config", "/m/.git/worktrees/n/config.worktree", "/m/.git/worktrees/n/commondir", "/m/.git/worktrees/n/gitdir", "/h/.ballast", "/h/.ballast/"}},
		{Name: "awkward paths", Spec: weird, Existing: []string{`/w/re po "q" (x)/é+ü`, `/w/re po "q" (x)/é+ü/`, `/w/re po "q" (x)/é+ü/.git`, `/w/re po "q" (x)/é+ü/.git/`, `/h/x y/.ballast`, "/w/cache[1]"}},
		{Name: "job private tree", Spec: tree, Existing: treeExisting},
	}
}

func TestProfilesGolden(t *testing.T) {
	path := filepath.Join("testdata", "profiles.json")
	cases := goldenSpecs()
	for i := range cases {
		c := &cases[i]
		c.Pins = AncestorPins(c.Spec)
		c.Darwin = strings.Split(strings.TrimRight(DarwinProfile(c.Spec), "\n"), "\n")
		c.Bwrap = BwrapArgs(c.Spec, existingMap(c.Existing))
	}
	if os.Getenv("BALLAST_UPDATE_GOLDEN") == "1" {
		b, err := json.MarshalIndent(cases, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	var want []goldenCase
	readJSON(t, "profiles.json", &want)
	if len(want) != len(cases) {
		t.Fatalf("golden has %d cases, test has %d (BALLAST_UPDATE_GOLDEN=1 regenerates)", len(want), len(cases))
	}
	for i, c := range cases {
		w := want[i]
		t.Run(c.Name, func(t *testing.T) {
			if w.Name != c.Name {
				t.Fatalf("case order: %q vs %q", w.Name, c.Name)
			}
			eq := func(what string, got, want []string) {
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Errorf("%s differs from the golden.\n--- got\n%s\n--- want\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
			}
			eq("AncestorPins", c.Pins, w.Pins)
			eq("DarwinProfile", c.Darwin, w.Darwin)
			eq("BwrapArgs", c.Bwrap, w.Bwrap)
		})
	}
}

func TestAncestorPinsSemantics(t *testing.T) {
	spec := Spec{
		AllowWrite: []string{"/w/repo", "/w/cache"}, RepoRoots: []string{"/w/repo"}, GitDirs: []string{"/w/repo/.git"},
		DenyWrite:      []string{"/h/.ballast", "/w/repo/gen/locked", "/w/cache/a/b/secret.txt"},
		DenyWriteGlobs: []string{"/w/repo/conf/app*.json"},
	}
	got := AncestorPins(spec)
	want := []string{"/w/cache", "/w/cache/a", "/w/cache/a/b", "/w/repo", "/w/repo/.git", "/w/repo/conf", "/w/repo/gen"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("AncestorPins = %v, want %v", got, want)
	}
	if got := AncestorPins(Spec{}); len(got) != 0 {
		t.Errorf("empty spec: %v", got)
	}
	// Sorted and deduplicated for a spec with overlapping roots.
	spec.DenyWrite = append(spec.DenyWrite, "/w/repo/gen/locked")
	got = AncestorPins(spec)
	if !sort.StringsAreSorted(got) {
		t.Error("not sorted")
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Error("duplicate pin")
		}
	}
}

func TestDarwinProfileProperties(t *testing.T) {
	for _, c := range goldenSpecs() {
		t.Run(c.Name, func(t *testing.T) {
			p := DarwinProfile(c.Spec)
			idx := func(s string) int { return strings.Index(p, s) }
			must := func(s string) int {
				i := idx(s)
				if i < 0 {
					t.Fatalf("profile lacks %q", s)
				}
				return i
			}
			if !strings.HasPrefix(p, "(version 1)\n(allow default)\n(deny file-write*)\n(allow file-write*") {
				t.Errorf("profile preamble: %q", p[:80])
			}
			if strings.Contains(p, "/dev/tty") || strings.Contains(p, "dtracehelper") {
				t.Error("terminal or dtrace devices allowed")
			}
			// Network.
			must("(deny network-outbound)")
			switch c.Spec.Network {
			case model.NetworkLoopback:
				if strings.Count(p, "remote ip") != 1 || !strings.Contains(p, `(remote ip "localhost:*")`) || strings.Contains(p, `"*:*"`) {
					t.Error("loopback must allow exactly localhost:*")
				}
				if strings.Contains(p, "mDNSResponder") {
					t.Error("mDNSResponder outside allow")
				}
			case model.NetworkAllow:
				must(`(remote ip "*:*")`)
				must(`(path-literal "/private/var/run/mDNSResponder")`)
				if strings.Contains(p, "localhost:*") && !strings.Contains(p, `(local ip "localhost:*")`) {
					t.Error("unexpected localhost rule")
				}
			default:
				if strings.Contains(p, "remote ip") || strings.Contains(p, "mDNSResponder") || strings.Contains(p, "local ip") {
					t.Error("deny must allow no IP at all, loopback included")
				}
			}
			if c.Spec.Network == model.NetworkLoopback || c.Spec.Network == model.NetworkAllow {
				must(`(allow network-bind (local ip "localhost:*"))`)
				must(`(allow network-inbound (local ip "localhost:*"))`)
			}
			must("(deny network-bind)")
			must("(deny network-inbound)")
			// Unix sockets: only inside W (AllowWrite and TmpDir), every policy.
			w := append(append([]string(nil), c.Spec.AllowWrite...), c.Spec.TmpDir)
			var outs, binds []string
			for _, l := range strings.Split(p, "\n") {
				if strings.HasPrefix(l, "(allow network-outbound (remote unix-socket (subpath ") {
					outs = append(outs, l)
				}
				if strings.HasPrefix(l, "(allow network-bind (local unix-socket (subpath ") {
					binds = append(binds, l)
				}
			}
			if len(outs) != len(w) || len(binds) != len(w) {
				t.Errorf("unix socket allowances: %d/%d for %d roots", len(outs), len(binds), len(w))
			}
			for _, r := range w {
				if !strings.Contains(strings.Join(outs, "\n"), sbplString(r)) {
					t.Errorf("no socket allowance for %s", r)
				}
			}
			// Order of the file rules: allow W, deny D, (Tree), deny mandatory, pins.
			iAllow := must("(allow file-write*")
			iNet := must("(deny network-outbound)")
			iIPC := must("(deny mach-lookup)")
			if !(iAllow < iNet && iNet < iIPC) {
				t.Error("section order")
			}
			if !strings.HasSuffix(p, DarwinEscapeDenials()) {
				t.Error("profile does not end with DarwinEscapeDenials()")
			}
			if c.Spec.Tree != "" {
				iDenyBallast := must(`(subpath "/h/.ballast")`)
				iTree := must("(allow file-write* (subpath " + sbplString(c.Spec.Tree) + "))")
				iMand := must(`(regex "^/h/\\.ballast/work/j1/tree/(.+/)?\\.claude(/.*)?$")`)
				if !(iDenyBallast < iTree && iTree < iMand) {
					t.Errorf("Tree carve-out order: deny %d, tree %d, mandatory %d", iDenyBallast, iTree, iMand)
				}
				// Tree is not part of the first allow rule.
				first := p[iAllow : strings.Index(p[iAllow:], ")\n")+iAllow]
				if strings.Contains(first, "/tree") {
					t.Error("Tree must not be in the first allow rule")
				}
			} else if strings.Count(p, "(allow file-write* (subpath") != 0 {
				t.Error("carve-out without Tree")
			}
			// Mandatory rules are anchored regexes per root.
			for _, r := range c.Spec.RepoRoots {
				must(sbplString("^" + reQuote(r) + `/(.+/)?\.git$`))
			}
			for _, g := range c.Spec.GitDirs {
				must(sbplString("^" + reQuote(g) + `/hooks(/.*)?$`))
				must(sbplString("^" + reQuote(g) + `/modules/[^/]+(/[^/]+)*/config$`))
			}
		})
	}
}

func TestDarwinIPCSection(t *testing.T) {
	frag := DarwinEscapeDenials()
	lines := strings.Split(strings.TrimRight(frag, "\n"), "\n")
	if lines[0] != "(deny mach-lookup)" {
		t.Fatalf("first line %q", lines[0])
	}
	// DarwinMachAllow, in order, right after the default-deny.
	for i, n := range DarwinMachAllow {
		if lines[1+i] != `(allow mach-lookup (global-name "`+n+`"))` {
			t.Errorf("line %d = %q, want allow of %s", 1+i, lines[1+i], n)
		}
	}
	rest := lines[1+len(DarwinMachAllow):]
	want := []string{"(deny mach-register)", "(deny job-creation)", "(deny user-preference-write)", "(deny lsopen)", "(deny appleevent-send)", "(deny signal)", "(allow signal (target same-sandbox))"}
	for i, w := range want {
		if rest[i] != w {
			t.Errorf("rest[%d] = %q, want %q", i, rest[i], w)
		}
	}
	tail := strings.Join(rest[len(want):], "\n")
	for _, p := range []string{`(literal "/usr/bin/open")`, `(literal "/usr/bin/osascript")`, `(literal "/bin/launchctl")`} {
		if !strings.Contains(tail, p) {
			t.Errorf("process-exec denial lacks %s", p)
		}
	}
	// No forbidden service, exact or prefix, in the allowlist; the forbidden
	// list itself contains the entries the contract names.
	for _, a := range DarwinMachAllow {
		for _, f := range DarwinMachForbidden {
			if a == f || (strings.HasSuffix(f, ".") && strings.HasPrefix(a, f)) {
				t.Errorf("DarwinMachAllow contains forbidden %s (matches %s)", a, f)
			}
		}
		if strings.Contains(a, "SecurityServer") || strings.Contains(a, "securityd") {
			t.Errorf("credential proxy allowed: %s", a)
		}
	}
	for _, f := range []string{"com.apple.SecurityServer", "com.apple.coreservices.launchservicesd", "com.apple.coreservices.appleevents", "com.apple.tccd"} {
		found := false
		for _, g := range DarwinMachForbidden {
			if g == f {
				found = true
			}
		}
		if !found {
			t.Errorf("DarwinMachForbidden lost %s", f)
		}
	}
	for _, f := range DarwinMachForbidden {
		if strings.Contains(frag, `"`+f+`"`) {
			t.Errorf("fragment mentions forbidden %s", f)
		}
	}
	// The test-only variant differs by exactly the process-exec rule.
	spec := goldenSpecs()[0].Spec
	if strings.Contains(darwinProfile(spec, false), "process-exec") || !strings.Contains(darwinProfile(spec, true), "process-exec") {
		t.Error("process-exec toggle")
	}
}

func TestBwrapArgsProperties(t *testing.T) {
	has := func(args []string, seq ...string) int {
		for i := 0; i+len(seq) <= len(args); i++ {
			ok := true
			for j, s := range seq {
				if args[i+j] != s {
					ok = false
					break
				}
			}
			if ok {
				return i
			}
		}
		return -1
	}
	for _, c := range goldenSpecs() {
		t.Run(c.Name, func(t *testing.T) {
			a := BwrapArgs(c.Spec, existingMap(c.Existing))
			if has(a, "--die-with-parent") != 0 || has(a, "--new-session") < 0 || has(a, "--unshare-pid") < 0 {
				t.Error("base flags")
			}
			if (has(a, "--unshare-net") >= 0) == (c.Spec.Network == model.NetworkAllow) {
				t.Errorf("--unshare-net with network %q", c.Spec.Network)
			}
			if has(a, "--tmpfs", "/run", "--tmpfs", "/var/run") < 0 {
				t.Error("socket directories not masked")
			}
			if has(a, "--ro-bind", "/", "/") < 0 || has(a, "--setenv", "TMPDIR", "/tmp") < 0 {
				t.Error("root bind / TMPDIR")
			}
			if has(a, "--chdir", c.Spec.Cwd) < 0 {
				t.Error("--chdir")
			}
			for _, kv := range c.Spec.LoaderEnv {
				k, v, _ := strings.Cut(kv, "=")
				if has(a, "--setenv", k, v) < 0 {
					t.Errorf("LoaderEnv %s not rendered", kv)
				}
			}
			if c.Spec.Tree != "" {
				iRO := has(a, "--ro-bind", "/h/.ballast", "/h/.ballast")
				iTree := has(a, "--bind", c.Spec.Tree, c.Spec.Tree)
				if iRO < 0 || iTree < 0 || iTree < iRO {
					t.Errorf("Tree bind must follow the ~/.ballast ro-bind (ro %d, tree %d)", iRO, iTree)
				}
				if has(a, "--bind", "/c/cache", "/c/cache") < 0 || has(a, "--bind", "/c/cache", "/c/cache") > iRO {
					t.Error("other W roots come before the D binds")
				}
			}
			// A nonexistent path is never bound.
			for i := 0; i+2 < len(a); i++ {
				if strings.HasPrefix(a[i], "--") && strings.HasSuffix(a[i], "bind") && a[i+1] == a[i+2] && a[i+1] != "/" {
					if !existingMap(c.Existing)[a[i+1]] {
						t.Errorf("%s %s: path does not exist", a[i], a[i+1])
					}
				}
			}
		})
	}
	// The container entry: a top-level .git directory is pinned (bind), never
	// ro-bound as a whole; a gitfile is ro-bound and not pinned.
	spec := Spec{AllowWrite: []string{"/r"}, RepoRoots: []string{"/r"}, GitDirs: []string{"/r/.git"}, Cwd: "/r"}
	dirForm := BwrapArgs(spec, existingMap([]string{"/r", "/r/", "/r/.git", "/r/.git/", "/r/.git/hooks", "/r/.git/hooks/", "/r/.git/config", "/r/n/.git", "/r/n/.git/"}))
	if has(dirForm, "--ro-bind", "/r/.git", "/r/.git") >= 0 || has(dirForm, "--bind", "/r/.git", "/r/.git") < 0 {
		t.Errorf(".git directory must be a writable mount point: %v", dirForm)
	}
	if has(dirForm, "--bind", "/r/n/.git", "/r/n/.git") < 0 {
		t.Errorf("nested .git directory must be a writable mount point: %v", dirForm)
	}
	if has(dirForm, "--ro-bind", "/r/.git/hooks", "/r/.git/hooks") < 0 || has(dirForm, "--ro-bind", "/r/.git/config", "/r/.git/config") < 0 {
		t.Errorf("hooks and config must be read-only: %v", dirForm)
	}
	fileForm := BwrapArgs(Spec{AllowWrite: []string{"/r"}, RepoRoots: []string{"/r"}, Cwd: "/r"}, existingMap([]string{"/r", "/r/", "/r/.git"}))
	if has(fileForm, "--ro-bind", "/r/.git", "/r/.git") < 0 || has(fileForm, "--bind", "/r/.git", "/r/.git") >= 0 {
		t.Errorf("a gitfile must be read-only: %v", fileForm)
	}
}

// ---------- scan / post-check / quarantine ----------

func mkfiles(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		p := filepath.Join(root, r)
		if strings.HasSuffix(r, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(r), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rels(root string, abs []string) []string {
	out := make([]string, len(abs))
	for i, a := range abs {
		r, _ := filepath.Rel(root, a)
		out[i] = filepath.ToSlash(r)
	}
	return out
}

func TestScanMandatory(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	mkfiles(t, repo,
		".git/HEAD", ".git/index", ".git/config", ".git/hooks/pre-commit", ".git/objects/ab/cd", ".git/modules/sub/config", ".git/modules/sub/hooks/post-checkout", ".git/worktrees/w2/gitdir", ".git/worktrees/w2/HEAD",
		".claude/settings.json", ".claude/commands/x.md", ".vscode/tasks.json", "src/main.go", "src/config",
		"node_modules/pkg/.git/config", "node_modules/pkg/.git/index", "node_modules/pkg/.git/hooks/h", "a/b/.mcp.json", ".gitmodules")
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	mkfiles(t, root, "outside/.claude/secret", "outside/x")
	spec := Spec{RepoRoots: []string{repo}, GitDirs: []string{filepath.Join(repo, ".git")}}
	got, err := ScanMandatory(sys.OSFS{}, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".claude", ".git", ".git/config", ".git/hooks", ".git/modules/sub/config", ".git/modules/sub/hooks", ".git/worktrees/w2/gitdir", ".gitmodules", ".vscode",
		"a/b/.mcp.json", "node_modules/pkg/.git", "node_modules/pkg/.git/config", "node_modules/pkg/.git/hooks",
	}
	if g := rels(repo, got); strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("ScanMandatory:\n got %v\nwant %v", g, want)
	}
	// A symlink is never followed (outside/.claude is not reported) and a
	// missing root is not an error.
	if _, err := ScanMandatory(sys.OSFS{}, Spec{RepoRoots: []string{filepath.Join(root, "absent")}}); err != nil {
		t.Errorf("missing root: %v", err)
	}
}

func TestScanMandatoryLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("creates thousands of directories")
	}
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	for i := 0; i < MaxMandatoryMatches+1; i++ {
		if err := os.MkdirAll(filepath.Join(repo, fmt.Sprintf("d%05d", i), ".vscode"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := ScanMandatory(sys.OSFS{}, Spec{RepoRoots: []string{repo}})
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("want ErrInvalidSpec, got %v", err)
	}
	// Exactly at the limit is fine.
	if err := os.RemoveAll(filepath.Join(repo, fmt.Sprintf("d%05d", MaxMandatoryMatches))); err != nil {
		t.Fatal(err)
	}
	got, err := ScanMandatory(sys.OSFS{}, Spec{RepoRoots: []string{repo}})
	if err != nil || len(got) != MaxMandatoryMatches {
		t.Fatalf("at the limit: %d matches, err %v", len(got), err)
	}
}

func TestPostCheck(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	mkfiles(t, repo, ".git/HEAD", ".git/hooks/h", ".git/config", ".claude/settings.json", "sub/.vscode/x", "sub/src/a.go", "gen/")
	spec := Spec{RepoRoots: []string{repo}, GitDirs: []string{filepath.Join(repo, ".git")}}
	base, err := ScanMandatory(sys.OSFS{}, spec)
	if err != nil {
		t.Fatal(err)
	}
	w := Wrapped{Baseline: base, MissingProtected: []string{filepath.Join(repo, "gen", "locked")}}

	// Nothing new (writes inside baseline subtrees and elsewhere are not matches).
	mkfiles(t, repo, "src/new.go", ".git/index")
	if found, err := PostCheck(sys.OSFS{}, spec, w); err != nil || found != nil {
		t.Fatalf("clean run: %v %v", found, err)
	}

	// New protected paths at several depths, plus a missing explicit one, plus
	// a baseline entry under a renamed ancestor.
	mkfiles(t, repo, ".mcp.json", "src/deep/.vscode/tasks.json", "gen/locked")
	if err := os.Rename(filepath.Join(repo, "sub"), filepath.Join(repo, "sub2")); err != nil {
		t.Fatal(err)
	}
	found, err := PostCheck(sys.OSFS{}, spec, w)
	if !errors.Is(err, ErrProtectedCreated) {
		t.Fatalf("want ErrProtectedCreated, got %v (%v)", err, found)
	}
	want := []string{".mcp.json", "gen/locked", "src/deep/.vscode", "sub2/.vscode"}
	if g := rels(repo, found); strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("found %v, want %v", g, want)
	}
	if !sort.StringsAreSorted(found) {
		t.Error("found not sorted")
	}
}

// noChmodFS is the OS file system without any way to change a mode.
type noChmodFS struct{ sys.OSFS }

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
}

// PostCheck fails closed: a directory the scan cannot enter does not hide a
// protected path the child created (it used to return found == nil and an
// error, so the runner never quarantined anything).
func TestPostCheckFailsClosed(t *testing.T) {
	skipIfRoot(t)
	setup := func(t *testing.T) (repo string, spec Spec, w Wrapped) {
		root := realTemp(t)
		repo = filepath.Join(root, "repo")
		mkfiles(t, repo, ".git/HEAD", "pkg/a.go", "pkg/in/b.go", "src/c.go")
		spec = Spec{RepoRoots: []string{repo}, GitDirs: []string{filepath.Join(repo, ".git")}}
		base, err := ScanMandatory(sys.OSFS{}, spec)
		if err != nil {
			t.Fatal(err)
		}
		return repo, spec, Wrapped{Baseline: base}
	}

	t.Run("directory without permission is opened up and scanned", func(t *testing.T) {
		repo, spec, w := setup(t)
		mkfiles(t, repo, ".claude/settings.local.json", "pkg/in/.vscode/tasks.json")
		if err := os.Chmod(filepath.Join(repo, "pkg"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(repo, "pkg"), 0o755) })
		found, err := PostCheck(sys.OSFS{}, spec, w)
		if !errors.Is(err, ErrProtectedCreated) || errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("want ErrProtectedCreated only, got %v", err)
		}
		if g := rels(repo, found); strings.Join(g, "\n") != ".claude\npkg/in/.vscode" {
			t.Errorf("found %v", g)
		}
	})

	t.Run("directory that stays unreadable is itself reported", func(t *testing.T) {
		repo, spec, w := setup(t)
		mkfiles(t, repo, ".claude/settings.local.json", "pkg/in/.vscode/tasks.json")
		if err := os.Chmod(filepath.Join(repo, "pkg"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(repo, "pkg"), 0o755) })
		w.MissingProtected = []string{filepath.Join(repo, "pkg", "locked")} // lstat fails with EACCES
		found, err := PostCheck(noChmodFS{}, spec, w)
		if !errors.Is(err, ErrProtectedCreated) {
			t.Fatalf("want ErrProtectedCreated, got %v", err)
		}
		if g := rels(repo, found); strings.Join(g, "\n") != ".claude\npkg\npkg/locked" {
			t.Errorf("found %v", g)
		}
	})

	t.Run("more matches than MaxMandatoryMatches", func(t *testing.T) {
		if testing.Short() {
			t.Skip("creates thousands of directories")
		}
		repo, spec, w := setup(t)
		mkfiles(t, repo, ".claude/settings.local.json")
		for i := 0; i <= MaxMandatoryMatches; i++ {
			if err := os.MkdirAll(filepath.Join(repo, "x", fmt.Sprint(i), ".idea"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ScanMandatory(sys.OSFS{}, spec); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("ScanMandatory keeps its bound, got %v", err)
		}
		found, err := PostCheck(sys.OSFS{}, spec, w)
		if !errors.Is(err, ErrProtectedCreated) || errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("want ErrProtectedCreated only, got %v", err)
		}
		if len(found) != MaxMandatoryMatches+2 || rels(repo, found)[0] != ".claude" {
			t.Errorf("found %d paths, first %v", len(found), rels(repo, found)[:1])
		}
	})
}

// A cache variable survives only inside runner_allow_write; a value inside the
// repository (also an AllowWrite entry for origin=cli) is redirected.
func TestWrapCacheEnvIgnoresRepoRoots(t *testing.T) {
	spec, root := wrapFixture(t, model.NetworkDeny)
	cache := filepath.Join(root, "shared-cache")
	mkfiles(t, root, "shared-cache/")
	spec.AllowWrite = append(spec.AllowWrite, cache)
	repo := spec.RepoRoots[0]
	env := sys.MapEnv{Vars: map[string]string{
		"GOCACHE":          filepath.Join(repo, ".cache", "go"),
		"XDG_CACHE_HOME":   filepath.Join(repo, ".git", "x"),
		"npm_config_cache": filepath.Join(cache, "npm"),
		"PIP_CACHE_DIR":    repo,
	}}
	w, err := Wrap(Options{Env: env, Bwrap: "/usr/bin/bwrap"}, KindSandboxExec, spec, []string{"go", "test"})
	if err != nil {
		t.Fatal(err)
	}
	kv := map[string]string{}
	for _, e := range w.Env {
		if k, v, ok := strings.Cut(e, "="); ok {
			kv[k] = v
		}
	}
	tmp := spec.TmpDir
	for k, sub := range map[string]string{"GOCACHE": "go-build", "XDG_CACHE_HOME": "xdg", "PIP_CACHE_DIR": "pip", "YARN_CACHE_FOLDER": "yarn"} {
		if kv[k] != tmp+"/cache/"+sub {
			t.Errorf("%s = %q, want it redirected to %s/cache/%s", k, kv[k], tmp, sub)
		}
	}
	if _, set := kv["npm_config_cache"]; set {
		t.Errorf("npm_config_cache inside runner_allow_write was redirected: %q", kv["npm_config_cache"])
	}
}

// renameFailFS fails Rename (as EXDEV) and optionally RemoveAll of a path.
type renameFailFS struct {
	sys.OSFS
	failRename    bool
	failRemoveAll string
}

func (f renameFailFS) Rename(o, n string) error {
	if f.failRename {
		return &os.LinkError{Op: "rename", Old: o, New: n, Err: syscall.EXDEV}
	}
	return os.Rename(o, n)
}

func (f renameFailFS) RemoveAll(p string) error {
	if f.failRemoveAll != "" && strings.Contains(p, f.failRemoveAll) {
		return syscall.EPERM
	}
	return os.RemoveAll(p)
}

func TestQuarantine(t *testing.T) {
	setup := func(t *testing.T) (root, repo, qdir string) {
		root = realTemp(t)
		repo = filepath.Join(root, "repo")
		mkfiles(t, repo, ".mcp.json", ".claude/settings.local.json", ".claude/sub/deep.txt", "a/.vscode/tasks.json")
		mkfiles(t, root, "victim/secret.txt")
		if err := os.Symlink(filepath.Join(root, "victim"), filepath.Join(repo, ".claude", "lnk")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "victim", "secret.txt"), filepath.Join(repo, "a", ".vscode", "flink")); err != nil {
			t.Fatal(err)
		}
		return root, repo, filepath.Join(root, "q", "job-1")
	}
	paths := func(repo string) []string {
		return []string{filepath.Join(repo, ".mcp.json"), filepath.Join(repo, ".claude"), filepath.Join(repo, "a", ".vscode")}
	}
	check := func(t *testing.T, root, repo, qdir string, ents []QuarantineEntry, copied bool) {
		t.Helper()
		for _, e := range ents {
			if e.Err != "" || e.MovedTo == "" {
				t.Errorf("entry %+v", e)
			}
			if _, err := os.Lstat(e.Path); err == nil {
				t.Errorf("%s still exists", e.Path)
			}
		}
		for rel, want := range map[string]string{".mcp.json": ".mcp.json", ".claude/settings.local.json": ".claude/settings.local.json", "a/.vscode/tasks.json": "a/.vscode/tasks.json"} {
			b, err := os.ReadFile(filepath.Join(qdir, rel))
			if err != nil || string(b) != want {
				t.Errorf("quarantined %s: %q %v", rel, b, err)
			}
		}
		// Symlinks were moved/copied as links, never followed.
		for _, l := range []string{".claude/lnk", "a/.vscode/flink"} {
			fi, err := os.Lstat(filepath.Join(qdir, l))
			if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("%s is not a symlink in quarantine: %v", l, err)
			}
		}
		if b, err := os.ReadFile(filepath.Join(root, "victim", "secret.txt")); err != nil || string(b) != "victim/secret.txt" {
			t.Errorf("symlink target damaged: %q %v", b, err)
		}
	}
	t.Run("rename", func(t *testing.T) {
		root, repo, q := setup(t)
		ents := Quarantine(sys.OSFS{}, paths(repo), []string{repo}, q)
		check(t, root, repo, q, ents, false)
	})
	t.Run("EXDEV falls back to copy and remove", func(t *testing.T) {
		root, repo, q := setup(t)
		ents := Quarantine(renameFailFS{failRename: true}, paths(repo), []string{repo}, q)
		check(t, root, repo, q, ents, true)
	})
	t.Run("everything fails but removal in place", func(t *testing.T) {
		root, repo, q := setup(t)
		// Copy fails (destination cannot be created: it is a file), rename fails.
		if err := os.MkdirAll(filepath.Dir(q), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(q, []byte("not a dir"), 0o600); err != nil {
			t.Fatal(err)
		}
		ents := Quarantine(renameFailFS{failRename: true}, paths(repo), []string{repo}, q)
		for _, e := range ents {
			if e.MovedTo != "" || e.Err != "" {
				t.Errorf("entry %+v: want removed in place without error", e)
			}
			if _, err := os.Lstat(e.Path); err == nil {
				t.Errorf("%s survived", e.Path)
			}
		}
		if b, err := os.ReadFile(filepath.Join(root, "victim", "secret.txt")); err != nil || len(b) == 0 {
			t.Errorf("symlink target damaged: %v", err)
		}
	})
	t.Run("a path that survives is reported", func(t *testing.T) {
		_, repo, q := setup(t)
		fs2 := renameFailFS{failRename: true, failRemoveAll: ".mcp.json"}
		ents := Quarantine(fs2, []string{filepath.Join(repo, ".mcp.json")}, []string{repo}, q)
		if len(ents) != 1 || ents[0].Err == "" || ents[0].MovedTo != "" {
			t.Errorf("entry %+v", ents)
		}
	})
	t.Run("outside the roots is refused", func(t *testing.T) {
		root, repo, q := setup(t)
		ents := Quarantine(sys.OSFS{}, []string{filepath.Join(root, "victim", "secret.txt"), repo}, []string{repo}, q)
		for _, e := range ents {
			if e.Err == "" {
				t.Errorf("%+v should be refused", e)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "victim", "secret.txt")); err != nil {
			t.Error("victim moved")
		}
	})
	t.Run("name collisions are kept apart", func(t *testing.T) {
		root := realTemp(t)
		repo := filepath.Join(root, "repo")
		gd := filepath.Join(root, "gd")
		mkfiles(t, repo, "hooks/x")
		mkfiles(t, gd, "hooks/y")
		ents := Quarantine(sys.OSFS{}, []string{filepath.Join(repo, "hooks"), filepath.Join(gd, "hooks")}, []string{repo, gd}, filepath.Join(root, "q"))
		if ents[0].MovedTo == ents[1].MovedTo || ents[0].Err != "" || ents[1].Err != "" {
			t.Errorf("%+v", ents)
		}
	})
}

// ---------- Wrap ----------

func TestWrapKindNone(t *testing.T) {
	argv := []string{"go", "test", "./..."}
	w, err := Wrap(Options{}, KindNone, Spec{}, argv)
	if err != nil || w.Kind != KindNone || strings.Join(w.Argv, " ") != "go test ./..." || w.NewSession {
		t.Fatalf("%+v %v", w, err)
	}
	if _, err := Wrap(Options{}, KindNone, Spec{}, nil); err == nil {
		t.Error("empty argv")
	}
}

func wrapFixture(t *testing.T, network model.NetworkPolicy) (Spec, string) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	mkfiles(t, repo, ".git/HEAD", ".git/config", ".git/hooks/h", "src/a.go")
	mkfiles(t, root, "home/.ballast/x", "tmp/")
	return Spec{
		AllowWrite: []string{repo}, RepoRoots: []string{repo}, GitDirs: []string{filepath.Join(repo, ".git")},
		DenyWrite: []string{filepath.Join(root, "home", ".ballast")}, TmpDir: filepath.Join(root, "tmp"),
		Network: network, Home: filepath.Join(root, "home"), Cwd: repo,
	}, root
}

func TestWrapEnvAllPolicies(t *testing.T) {
	for _, network := range []model.NetworkPolicy{"", model.NetworkDeny, model.NetworkLoopback, model.NetworkAllow} {
		for _, kind := range []Kind{KindSandboxExec, KindBwrap} {
			t.Run(string(kind)+"/"+string(network), func(t *testing.T) {
				spec, root := wrapFixture(t, network)
				env := sys.MapEnv{Vars: map[string]string{
					"PATH": "/usr/bin", "HOME": "/h", "SSH_AUTH_SOCK": "/s", "DOCKER_HOST": "unix:///d", "DBUS_SESSION_BUS_ADDRESS": "x", "DBUS_SYSTEM_BUS_ADDRESS": "y",
					"LD_PRELOAD": "/x", "LD_LIBRARY_PATH": "/l", "DYLD_INSERT_LIBRARIES": "/y", "GOCACHE": "/home/u/.cache/go-build",
				}}
				w, err := Wrap(Options{Env: env, Bwrap: "/usr/bin/bwrap"}, kind, spec, []string{"go", "test"})
				if err != nil {
					t.Fatal(err)
				}
				if !w.NewSession || w.Kind != kind {
					t.Errorf("%+v", w)
				}
				has := map[string]bool{}
				kv := map[string]string{}
				for _, e := range w.Env {
					has[e] = true
					if k, v, ok := strings.Cut(e, "="); ok {
						kv[k] = v
					}
				}
				for _, k := range []string{"SSH_AUTH_SOCK", "DOCKER_HOST", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES"} {
					if !has[k] {
						t.Errorf("%s is not unset in Env %v", k, w.Env)
					}
				}
				tmp := spec.TmpDir
				if kind == KindBwrap {
					tmp = "/tmp"
				}
				if kv["TMPDIR"] != tmp {
					t.Errorf("TMPDIR = %q, want %q", kv["TMPDIR"], tmp)
				}
				for _, c := range CacheEnv(tmp, spec.AllowWrite, func(k string) string { v, _ := env.Getenv(k); return v }) {
					if !has[c] {
						t.Errorf("Env lacks %s", c)
					}
				}
				if kv["GOCACHE"] != tmp+"/cache/go-build" {
					t.Errorf("GOCACHE = %q", kv["GOCACHE"])
				}
				if _, ok := kv["GIT_CEILING_DIRECTORIES"]; ok {
					t.Error("GIT_CEILING_DIRECTORIES without Tree")
				}
				if kind == KindSandboxExec {
					if w.Argv[0] != "/usr/bin/sandbox-exec" || w.Argv[1] != "-p" || w.Argv[3] != "--" || w.Argv[len(w.Argv)-1] != "test" {
						t.Errorf("argv %v", w.Argv)
					}
				} else {
					if w.Argv[0] != "/usr/bin/bwrap" || w.Argv[len(w.Argv)-3] != "--" {
						t.Errorf("argv %v", w.Argv)
					}
					a := strings.Join(w.Argv, " ")
					if !strings.Contains(a, "--new-session") || !strings.Contains(a, "--tmpfs /tmp") ||
						strings.Contains(a, "--unshare-net") == (network == model.NetworkAllow) {
						t.Errorf("bwrap flags: %s", a)
					}
					if strings.Contains(a, "LD_") {
						t.Errorf("LD_* leaked into the bwrap command line: %s", a)
					}
				}
				// Baseline holds the existing mandatory matches.
				wantBase := []string{filepath.Join(spec.RepoRoots[0], ".git"), filepath.Join(spec.RepoRoots[0], ".git", "config"), filepath.Join(spec.RepoRoots[0], ".git", "hooks")}
				if strings.Join(w.Baseline, "\n") != strings.Join(wantBase, "\n") {
					t.Errorf("Baseline %v, want %v", w.Baseline, wantBase)
				}
				_ = root
			})
		}
	}
}

func TestWrapTreeEnvAndLoaderEnv(t *testing.T) {
	root := realTemp(t)
	work := filepath.Join(root, "home", ".ballast", "work")
	tree := filepath.Join(work, "j1", "tree")
	mkfiles(t, tree, "src/a.go")
	mkfiles(t, root, "tmp/")
	spec := Spec{
		AllowWrite: []string{tree}, RepoRoots: []string{tree}, DenyWrite: []string{filepath.Join(root, "home", ".ballast")},
		TmpDir: filepath.Join(root, "tmp"), Home: filepath.Join(root, "home"), Cwd: tree, Tree: tree, WorkRoot: work,
		LoaderEnv: []string{"LD_LIBRARY_PATH=/opt/lib"},
	}
	for _, kind := range []Kind{KindSandboxExec, KindBwrap} {
		w, err := Wrap(Options{Bwrap: "/usr/bin/bwrap", Env: sys.MapEnv{Vars: map[string]string{"LD_LIBRARY_PATH": "/opt/lib"}}}, kind, spec, []string{"true"})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range w.Env {
			if e == "GIT_CEILING_DIRECTORIES="+filepath.Join(work, "j1") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: GIT_CEILING_DIRECTORIES missing: %v", kind, w.Env)
		}
		if kind == KindBwrap {
			a := strings.Join(w.Argv, "\x00")
			if !strings.Contains(a, "--setenv\x00LD_LIBRARY_PATH\x00/opt/lib") {
				t.Errorf("LoaderEnv not passed to the child: %v", w.Argv)
			}
		}
		// The backend environment (runner side) has no LD_* even though the
		// runner's own environment does.
		be := applyEnv([]string{"PATH=/bin", "LD_PRELOAD=/x", "LD_LIBRARY_PATH=/opt/lib", "DYLD_INSERT_LIBRARIES=/y"}, w.Env)
		for _, e := range be {
			if strings.HasPrefix(e, "LD_") || strings.HasPrefix(e, "DYLD_") {
				t.Errorf("%s: backend environment has %s", kind, e)
			}
		}
	}
	// A Tree outside WorkRoot/<job>/tree is refused by Wrap.
	bad := spec
	bad.WorkRoot = filepath.Join(root, "other")
	if _, err := Wrap(Options{Bwrap: "/usr/bin/bwrap"}, KindBwrap, bad, []string{"true"}); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("tree outside the work root: %v", err)
	}
}

func TestWrapRefusesInvalidSpec(t *testing.T) {
	spec, root := wrapFixture(t, "")
	spec.Home = filepath.Dir(spec.AllowWrite[0]) // the repo's parent: an ancestor of the repo, but the repo is below home
	spec.AllowWrite = []string{root}
	spec.Cwd = root
	spec.RepoRoots = nil
	spec.GitDirs = nil
	spec.Home = filepath.Join(root, "home")
	for _, kind := range []Kind{KindSandboxExec, KindBwrap} {
		if _, err := Wrap(Options{Bwrap: "/usr/bin/bwrap"}, kind, spec, []string{"true"}); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: root containing home accepted: %v", kind, err)
		}
	}
}

func TestWrapLinuxMissingProtected(t *testing.T) {
	spec, root := wrapFixture(t, "")
	repo := spec.AllowWrite[0]
	spec.DenyWrite = append(spec.DenyWrite, filepath.Join(repo, "gen", "locked.txt"), filepath.Join(repo, "src", "a.go"))
	spec.DenyWriteGlobs = []string{filepath.Join(repo, "conf", "app*.json")}
	w, err := Wrap(Options{Bwrap: "/usr/bin/bwrap"}, KindBwrap, spec, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(repo, "conf"), filepath.Join(repo, "gen", "locked.txt")}
	if strings.Join(w.MissingProtected, "\n") != strings.Join(want, "\n") {
		t.Errorf("MissingProtected %v, want %v", w.MissingProtected, want)
	}
	// An existing explicit protected file is ro-bound, a missing one is not.
	a := strings.Join(w.Argv, " ")
	if !strings.Contains(a, "--ro-bind "+filepath.Join(repo, "src", "a.go")+" ") || strings.Contains(a, filepath.Join(repo, "gen")) {
		t.Errorf("argv: %s", a)
	}
	_ = root
}

func TestSocketDirArgs(t *testing.T) {
	root := realTemp(t)
	run := filepath.Join(root, "run")
	varDir := filepath.Join(root, "var")
	mkfiles(t, root, "run/", "var/")
	pair := func(a, b string) []string {
		return []string{"--ro-bind", "/", "/", "--tmpfs", a, "--tmpfs", b, "--chdir", "/x"}
	}
	j := func(a []string) string { return strings.Join(a, " ") }
	dirs := []string{run, filepath.Join(varDir, "run")}

	// /var/run is a symlink to /run (Debian 12): masked once, through /run.
	if err := os.Symlink(run, filepath.Join(varDir, "run")); err != nil {
		t.Fatal(err)
	}
	got := socketDirArgs(sys.OSFS{}, pair(dirs[0], dirs[1]), dirs...)
	if want := "--ro-bind / / --tmpfs " + run + " --chdir /x"; j(got) != want {
		t.Errorf("symlinked /var/run:\n got %s\nwant %s", j(got), want)
	}
	// A real /var/run directory is masked too.
	os.Remove(filepath.Join(varDir, "run"))
	mkfiles(t, varDir, "run/")
	got = socketDirArgs(sys.OSFS{}, pair(dirs[0], dirs[1]), dirs...)
	if want := "--ro-bind / / --tmpfs " + run + " --tmpfs " + dirs[1] + " --chdir /x"; j(got) != want {
		t.Errorf("real /var/run:\n got %s\nwant %s", j(got), want)
	}
	// A missing directory has nothing to mask.
	os.RemoveAll(filepath.Join(varDir, "run"))
	got = socketDirArgs(sys.OSFS{}, pair(dirs[0], dirs[1]), dirs...)
	if want := "--ro-bind / / --tmpfs " + run + " --chdir /x"; j(got) != want {
		t.Errorf("missing /var/run:\n got %s\nwant %s", j(got), want)
	}
	// Other --tmpfs arguments are left alone.
	got = socketDirArgs(sys.OSFS{}, []string{"--tmpfs", "/tmp", "--tmpfs", run}, dirs...)
	if j(got) != "--tmpfs /tmp --tmpfs "+run {
		t.Errorf("unrelated tmpfs: %s", j(got))
	}
}

// A protected entry that is replaced at its own path (the child renames a
// non-pinned ancestor away and recreates the directory) is found through its
// identity, although its path is in the Baseline.
func TestPostCheckFindsReplacedBaselineEntry(t *testing.T) {
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	mkfiles(t, repo, "a/b/.vscode/settings.json", "a/b/src.go", ".git/HEAD")
	spec := Spec{RepoRoots: []string{repo}}
	w, err := Wrap(Options{Bwrap: "/usr/bin/bwrap"}, KindBwrap, Spec{
		AllowWrite: []string{repo}, RepoRoots: []string{repo}, GitDirs: []string{filepath.Join(repo, ".git")},
		Home: filepath.Join(root, "home"), Cwd: repo,
	}, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ids) == 0 && runtime.GOOS != "windows" {
		t.Fatal("Wrap recorded no fingerprints")
	}
	if found, err := PostCheck(sys.OSFS{}, spec, w); err != nil || found != nil {
		t.Fatalf("untouched: %v %v", found, err)
	}
	// Same path, another object; the moved original is a new path.
	if err := os.Rename(filepath.Join(repo, "a"), filepath.Join(repo, "a2")); err != nil {
		t.Fatal(err)
	}
	mkfiles(t, repo, "a/b/.vscode/tasks.json")
	found, err := PostCheck(sys.OSFS{}, spec, w)
	if !errors.Is(err, ErrProtectedCreated) {
		t.Fatalf("want ErrProtectedCreated, got %v (%v)", err, found)
	}
	want := []string{"a/b/.vscode", "a2/b/.vscode"}
	if g := rels(repo, found); strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("found %v, want %v", g, want)
	}
	// Path-only behaviour without fingerprints (a hand-built Wrapped).
	only := Wrapped{Baseline: w.Baseline}
	found, _ = PostCheck(sys.OSFS{}, spec, only)
	if g := rels(repo, found); strings.Join(g, "\n") != "a2/b/.vscode" {
		t.Errorf("without fingerprints found %v", g)
	}
}

func TestWrapRejectsTooManyMandatoryMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("creates thousands of directories")
	}
	root := realTemp(t)
	repo := filepath.Join(root, "repo")
	for i := 0; i < MaxMandatoryMatches+1; i++ {
		if err := os.MkdirAll(filepath.Join(repo, fmt.Sprintf("d%05d", i), ".idea"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkfiles(t, repo, ".git/HEAD")
	spec := Spec{AllowWrite: []string{repo}, RepoRoots: []string{repo}, GitDirs: []string{filepath.Join(repo, ".git")}, Home: filepath.Join(root, "home"), Cwd: repo, TmpDir: filepath.Join(root, "tmp")}
	for _, kind := range []Kind{KindSandboxExec, KindBwrap} {
		if _, err := Wrap(Options{Bwrap: "/usr/bin/bwrap"}, kind, spec, []string{"true"}); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: want ErrInvalidSpec, got %v", kind, err)
		}
	}
}
