package evidence

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// requireGit skips (locally) or fails (in CI) when git is missing.
func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("git is required in CI: %v", err)
		}
		t.Skip("git not on PATH")
	}
}

// sandbox is an isolated home plus the environment for child processes.
type sandbox struct {
	*testenv.Sandbox
	env  sys.MapEnv
	root string // realpath of Sandbox.Root
}

func newSandbox(t testing.TB) *sandbox {
	t.Helper()
	sb := testenv.New(t)
	vars := map[string]string{}
	for k, v := range sb.Vars {
		vars[k] = v
	}
	for _, k := range []string{"PATH", "SYSTEMROOT", "TMP", "TEMP", "ComSpec"} {
		if v, ok := os.LookupEnv(k); ok {
			vars[k] = v
		}
	}
	vars["TMPDIR"] = filepath.Join(sb.Root, "tmp")
	if err := os.MkdirAll(vars["TMPDIR"], 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	return &sandbox{Sandbox: sb, env: sys.MapEnv{Vars: vars}, root: root}
}

// git runs git in dir with the sandbox environment plus extra.
func (s *sandbox) git(t testing.TB, dir string, extra []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(s.env.Environ(), extra...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out) + stderr.String(), err
	}
	return string(out), nil
}

func (s *sandbox) mustGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := s.git(t, dir, nil, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// testProjectJSON is the project.json of the freshness fixtures.
const testProjectJSON = `{"version":1,
 "verify":[{"name":"test","argv":["true"],"required":true},{"name":"lint","argv":["true"]}],
 "exempt":["**/*.md","docs/**"]}
`

func writeFile(t testing.TB, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates a fixture repository: three source files, a README, a
// docs file and project.json, all committed.
func (s *sandbox) newRepo(t testing.TB, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.mustGit(t, dir, "init", "-q", "-b", "main")
	for _, kv := range [][2]string{
		{"user.email", "t@example.invalid"}, {"user.name", "t"}, {"core.autocrlf", "false"},
		{"core.fsmonitor", "false"}, {"gc.auto", "0"}, {"maintenance.auto", "false"},
	} {
		s.mustGit(t, dir, "config", kv[0], kv[1])
	}
	writeFile(t, filepath.Join(dir, "src", "a.go"), "package src\n\nfunc A() {}\n")
	writeFile(t, filepath.Join(dir, "src", "b.go"), "package src\n\nfunc B() {}\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# fixture\n")
	writeFile(t, filepath.Join(dir, "docs", "x.txt"), "docs\n")
	writeFile(t, filepath.Join(dir, ".ballast", "project.json"), testProjectJSON)
	s.mustGit(t, dir, "add", "-A")
	s.mustGit(t, dir, "commit", "-q", "-m", "init")
	s.mustGit(t, dir, "update-index", "--refresh", "-q")
	return dir
}

// testOptions builds evidence.Options over the sandbox.
func (s *sandbox) options(t testing.TB, clock sys.Clock, ids sys.IDGen) Options {
	t.Helper()
	l, err := layout.Resolve(s.Env())
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Layout:  l,
		FS:      sys.OSFS{},
		Clock:   clock,
		IDs:     ids,
		Locator: &snapshot.Locator{FS: sys.OSFS{}},
		Project: &project.Loader{FS: sys.OSFS{}},
		Env:     s.env,
	}
}

func (s *sandbox) locate(t testing.TB, dir string) *model.RepoInfo {
	t.Helper()
	repo, err := (&snapshot.Locator{FS: sys.OSFS{}}).Locate(dir)
	if err != nil {
		t.Fatalf("Locate(%s): %v", dir, err)
	}
	return repo
}

var epoch = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// fakeProject is a ports.ProjectLoader over fixed answers.
type fakeProject struct {
	cfg map[string]*model.ProjectConfig
	err map[string]error
}

func (f fakeProject) LoadProject(root string) (*model.ProjectConfig, error) {
	if e := f.err[root]; e != nil {
		return nil, e
	}
	return f.cfg[root], nil
}
