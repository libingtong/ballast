package testenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/sys"
)

// Sandbox is an isolated fake home.
type Sandbox struct {
	Root        string // t.TempDir()
	Home        string // fake $HOME / %USERPROFILE%
	BallastHome string // BALLAST_HOME
	ManagedDir  string // BALLAST_MANAGED_DIR
	Vars        map[string]string
}

// New creates the sandbox directories without touching the process environment.
func New(t testing.TB) *Sandbox {
	t.Helper()
	root := t.TempDir()
	s := &Sandbox{
		Root:        root,
		Home:        filepath.Join(root, "home"),
		BallastHome: filepath.Join(root, "home", ".ballast"),
		ManagedDir:  filepath.Join(root, "managed"),
	}
	for _, d := range []string{s.Home, s.ManagedDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("testenv: %v", err)
		}
	}
	s.Vars = map[string]string{
		"HOME":                 s.Home,
		"USERPROFILE":          s.Home,
		layout.EnvHome:         s.BallastHome,
		layout.EnvInstallRoot:  s.Home,
		layout.EnvManagedDir:   s.ManagedDir,
		layout.EnvClaudeDir:    filepath.Join(s.Home, ".claude"),
		layout.EnvCodexHome:    filepath.Join(s.Home, ".codex"),
		layout.EnvHermesHome:   filepath.Join(s.Home, ".hermes"),
		"XDG_CONFIG_HOME":      filepath.Join(s.Home, ".config"),
		"GIT_CONFIG_NOSYSTEM":  "1",
		"GIT_CONFIG_GLOBAL":    filepath.Join(s.Home, ".gitconfig"),
		"GIT_AUTHOR_NAME":      "ballast-test",
		"GIT_AUTHOR_EMAIL":     "test@example.invalid",
		"GIT_COMMITTER_NAME":   "ballast-test",
		"GIT_COMMITTER_EMAIL":  "test@example.invalid",
		"BALLAST_TEST_SANDBOX": "1",
	}
	return s
}

// Env returns a sys.MapEnv over the sandbox variables (does not modify the process).
func (s *Sandbox) Env() sys.MapEnv { return sys.MapEnv{Vars: s.Vars} }

// Layout resolves the sandbox layout.
func (s *Sandbox) Layout(t testing.TB) layout.Layout {
	t.Helper()
	l, err := layout.Resolve(s.Env())
	if err != nil {
		t.Fatalf("testenv: %v", err)
	}
	return l
}

// Isolate creates a sandbox AND sets the process environment with t.Setenv
// (so the test cannot run in parallel with t.Parallel). Use for e2e tests
// that exec the ballast binary.
func Isolate(t *testing.T) *Sandbox {
	t.Helper()
	s := New(t)
	for k, v := range s.Vars {
		t.Setenv(k, v)
	}
	return s
}
