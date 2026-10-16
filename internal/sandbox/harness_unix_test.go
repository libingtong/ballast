//go:build unix

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// The integration harness: it plays the runner. It builds a Spec, wraps an
// argv, starts it in a new session with stdin /dev/null and a pipe for
// output, then (like the runner's wrap-up order) kills the session, runs
// PostCheck and Quarantine, and lets the test assert what is left on the host.

// requireBackend returns the real backend of this host or skips (fails when
// CI=true and the matching BALLAST_REQUIRE_* variable is 1).
func requireBackend(t *testing.T) Kind {
	t.Helper()
	var envName string
	switch runtime.GOOS {
	case "darwin":
		envName = "BALLAST_REQUIRE_SANDBOX_EXEC"
	case "linux":
		envName = "BALLAST_REQUIRE_BWRAP"
	default:
		t.Skipf("no sandbox backend on %s", runtime.GOOS)
	}
	kind, err := Probe(Options{})
	if err != nil || kind == KindNone {
		if os.Getenv("CI") == "true" && os.Getenv(envName) == "1" {
			t.Fatalf("%s=1 and CI=true but no usable sandbox backend: %v", envName, err)
		}
		t.Skipf("no usable sandbox backend: %v", err)
	}
	return kind
}

func ciTrue() bool { return os.Getenv("CI") == "true" }

// needTool skips (fails under CI=true with the backend required) when a
// helper program is missing.
func needTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		if ciTrue() {
			t.Fatalf("%s is required in CI: %v", name, err)
		}
		t.Skipf("%s not available: %v", name, err)
	}
	return p
}

// itBase returns a symlink-free scratch directory that is not under /tmp,
// /run or /var/run (on linux those are private tmpfs inside the sandbox, so a
// "write" there would not reach the host and prove nothing).
func itBase(t *testing.T) string {
	t.Helper()
	base := realTemp(t)
	if runtime.GOOS == "linux" {
		for _, p := range []string{"/tmp", "/run", "/var/run", "/var/tmp"} {
			if within(base, p) && p != "/var/tmp" {
				d, err := os.MkdirTemp(".", ".it-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(d) })
				abs, err := filepath.Abs(d)
				if err != nil {
					t.Fatal(err)
				}
				return abs
			}
		}
	}
	return base
}

func gitEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	}
}

func git(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "protocol.file.allow=always", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture is one scratch world: a fake home, a git repository with a
// submodule and a linked worktree, and an "outside" directory.
type fixture struct {
	t       *testing.T
	kind    Kind
	root    string
	home    string
	repo    string // main work tree (.git is a directory)
	wt2     string // linked worktree (.git is a gitfile)
	sub     string // repo/sub, a submodule work tree
	tmp     string // per-job temp dir (darwin)
	outside string // directory outside every W
	quar    string // quarantine directory
	extra   []string
	guards  map[string]string
	spec    Spec
}

func newFixture(t *testing.T, network model.NetworkPolicy) *fixture {
	t.Helper()
	return newFixtureIn(t, network, "")
}

// newFixtureIn builds the fixture below a sub directory of the scratch base
// (sub may contain spaces, quotes, parentheses, non-ASCII).
func newFixtureIn(t *testing.T, network model.NetworkPolicy, sub string) *fixture {
	t.Helper()
	kind := requireBackend(t)
	needTool(t, "git")
	root := itBase(t)
	if sub != "" {
		root = filepath.Join(root, sub)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, kind: kind, root: root, guards: map[string]string{}}
	f.home = filepath.Join(root, "home")
	f.repo = filepath.Join(root, "repo")
	f.wt2 = filepath.Join(root, "w2")
	f.sub = filepath.Join(f.repo, "sub")
	f.tmp = filepath.Join(root, "tmp-job")
	f.outside = filepath.Join(root, "outside")
	f.quar = filepath.Join(root, "quarantine")
	mkfiles(t, f.home, ".bashrc", ".zshrc", ".profile", ".claude.json", ".claude/settings.json", ".claude/settings.local.json",
		".ballast/evidence/e1.json", ".ballast/rules.json", ".ballast/work/")
	mkfiles(t, root, "tmp-job/", "outside/keep.txt", "subsrc/lib.go")
	git(t, f.home, filepath.Join(root, "subsrc"), "init", "-q", "-b", "main")
	git(t, f.home, filepath.Join(root, "subsrc"), "add", "-A")
	git(t, f.home, filepath.Join(root, "subsrc"), "commit", "-q", "-m", "sub")
	mkfiles(t, f.repo, "src/main.go", ".ballast/project.json", "README.md", "node_modules/.keep")
	git(t, f.home, f.repo, "init", "-q", "-b", "main")
	git(t, f.home, f.repo, "submodule", "add", "-q", filepath.Join(root, "subsrc"), "sub")
	git(t, f.home, f.repo, "add", "-A", "-f")
	git(t, f.home, f.repo, "commit", "-q", "-m", "init")
	git(t, f.home, f.repo, "worktree", "add", "-q", "-b", "w2", f.wt2)
	f.spec = f.specFor(f.repo, network)
	return f
}

// specFor builds the origin=cli Spec for workTree.
func (f *fixture) specFor(workTree string, network model.NetworkPolicy) Spec {
	gd, cd, err := ResolveGitDirs(sys.OSFS{}, workTree)
	if err != nil {
		f.t.Fatal(err)
	}
	allow := []string{workTree}
	var gitDirs []string
	for _, g := range dedupe([]string{gd, cd}) {
		gitDirs = append(gitDirs, g)
		if !within(g, workTree) {
			allow = append(allow, g)
		}
	}
	deny := []string{filepath.Join(f.home, ".ballast"), filepath.Join(f.home, ".claude.json")}
	return Spec{
		AllowWrite: allow, RepoRoots: []string{workTree}, GitDirs: gitDirs,
		DenyWrite:      deny,
		DenyWriteGlobs: []string{filepath.Join(f.home, ".claude", "settings*.json")},
		TmpDir:         f.tmp, Network: network, Home: f.home, Cwd: workTree,
	}
}

// guard records the current content of path; assertGuards fails if it changed.
func (f *fixture) guard(paths ...string) {
	f.t.Helper()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		switch {
		case err == nil:
			f.guards[p] = "content:" + string(b)
		case errors.Is(err, os.ErrNotExist):
			f.guards[p] = "absent"
		default:
			if fi, lerr := os.Lstat(p); lerr == nil && fi.IsDir() {
				f.guards[p] = "dir"
			} else {
				f.guards[p] = "unreadable"
			}
		}
	}
}

func (f *fixture) assertGuards() {
	f.t.Helper()
	for p, want := range f.guards {
		b, err := os.ReadFile(p)
		var got string
		switch {
		case err == nil:
			got = "content:" + string(b)
		case errors.Is(err, os.ErrNotExist):
			got = "absent"
		default:
			if fi, lerr := os.Lstat(p); lerr == nil && fi.IsDir() {
				got = "dir"
			} else {
				got = "unreadable"
			}
		}
		if got != want {
			f.t.Errorf("host changed: %s: was %.80q, now %.80q", p, want, got)
		}
	}
}

func (f *fixture) assertAbsent(paths ...string) {
	f.t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			f.t.Errorf("residue on the host: %s exists", p)
		}
	}
}

type runResult struct {
	Out     string
	ExitErr error
	Wrapped Wrapped
	Found   []string
	Quar    []QuarantineEntry
}

type runOpts struct {
	spec    *Spec
	env     []string // extra "K=V" in the runner environment (before Wrapped.Env)
	timeout time.Duration
	// profile overrides the darwin profile (test-only variants).
	darwinProfile func(Spec) string
	// plain runs argv with no wrapping (controls).
	plain bool
	// keepEnv skips the controlled base environment.
	base []string
}

func (f *fixture) baseEnv() []string {
	e := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + f.home, "LANG=C", "USER=ballast-it",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
	return append(e, f.extra...)
}

func (f *fixture) wrapOptions() Options {
	return Options{}
}

// start wraps argv, runs it like the runner would and finishes with the
// wrap-up sequence.
func (f *fixture) runArgv(argv []string, o runOpts) runResult {
	f.t.Helper()
	spec := f.spec
	if o.spec != nil {
		spec = *o.spec
	}
	var res runResult
	base := f.baseEnv()
	if o.base != nil {
		base = o.base
	}
	base = append(base, o.env...)
	if o.plain {
		res.Wrapped = Wrapped{Kind: KindNone, Argv: argv}
	} else {
		w, err := Wrap(Options{Env: sys.MapEnv{Vars: envMap(base)}}, f.kind, spec, argv)
		if err != nil {
			f.t.Fatalf("Wrap: %v", err)
		}
		if o.darwinProfile != nil {
			if f.kind != KindSandboxExec {
				f.t.Fatal("darwinProfile override on a non-darwin backend")
			}
			rs := resolveSpec(sys.OSFS{}, spec)
			w.Argv[2] = o.darwinProfile(rs)
		}
		res.Wrapped = w
	}
	timeout := o.timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, res.Wrapped.Argv[0], res.Wrapped.Argv[1:]...)
	cmd.Dir = spec.Cwd
	cmd.Env = applyEnv(base, res.Wrapped.Env)
	setsid(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// cmd.Stdin == nil is /dev/null.
	res.ExitErr = cmd.Run()
	// Wrap-up order: kill the session, then PostCheck, Quarantine.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	res.Out = out.String()
	if !o.plain {
		found, err := PostCheck(sys.OSFS{}, spec, res.Wrapped)
		if err != nil && !errors.Is(err, ErrProtectedCreated) {
			f.t.Fatalf("PostCheck: %v", err)
		}
		res.Found = found
		if len(found) > 0 {
			roots := append(append([]string(nil), spec.RepoRoots...), spec.GitDirs...)
			res.Quar = Quarantine(sys.OSFS{}, found, roots, filepath.Join(f.quar, f.t.Name()))
			for _, q := range res.Quar {
				if q.Err != "" {
					f.t.Errorf("Quarantine %s: %s", q.Path, q.Err)
				}
			}
		}
	}
	return res
}

func (f *fixture) sh(script string, o runOpts) runResult {
	f.t.Helper()
	return f.runArgv([]string{"/bin/sh", "-c", script}, o)
}

func envMap(kv []string) map[string]string {
	m := map[string]string{}
	for _, e := range kv {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

func relFound(root string, found []string) []string {
	out := rels(root, found)
	sort.Strings(out)
	return out
}

// listener helpers -------------------------------------------------------

func hasPython(t *testing.T) string { return needTool(t, "python3") }

// pyConnect is a python3 one-liner that exits 0 iff the connection succeeds.
const pyTCP = `import socket,sys
s=socket.socket(socket.AF_INET, socket.SOCK_STREAM); s.settimeout(3)
try:
    s.connect((sys.argv[1], int(sys.argv[2]))); print("CONNECTED")
except Exception as e:
    print("FAILED", type(e).__name__, e); sys.exit(1)
`

const pyUnix = `import socket,sys
s=socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.settimeout(3)
try:
    s.connect(sys.argv[1]); s.send(b"hello"); print("CONNECTED")
except Exception as e:
    print("FAILED", type(e).__name__, e); sys.exit(1)
`

func fmtSh(format string, a ...any) string { return fmt.Sprintf(format, a...) }
