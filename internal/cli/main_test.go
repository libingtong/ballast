package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/testenv"
)

// ballastBin is the ballast binary built once by TestMain into a temporary
// directory; every end-to-end test runs it as a subprocess with a fully
// redirected environment (no real home, client config or git config).
var ballastBin string

// repoRoot is the module root (the directory with go.mod).
var repoRoot string

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli tests:", err)
		return 1
	}
	repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))
	dir, err := os.MkdirTemp("", "ballast-cli-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cli tests:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	exe := "ballast"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	ballastBin = filepath.Join(dir, exe)
	goTool := "go"
	if p, lerr := exec.LookPath("go"); lerr == nil {
		goTool = p
	} else if rt := filepath.Join(runtime.GOROOT(), "bin", "go"); fileExists(rt) || fileExists(rt+".exe") {
		goTool = rt
	}
	// The binary carries the ballast_fault switches so the e2e tests can relax
	// the hook's 150 ms watchdog (BALLAST_TEST_WATCHDOG): under -race and a
	// loaded machine the production wall clock is not a stable assertion.
	// Production builds ignore every switch (faults_off_test.go; scripts/
	// fault.sh and accept-m*.sh exercise the production binary).
	build := exec.Command(goTool, "build", "-tags", "ballast_fault", "-o", ballastBin, "./cmd/ballast")
	build.Dir = repoRoot
	// Never reach the network (the module has no dependencies to fetch).
	build.Env = append(filterEnv(os.Environ(), "GOPROXY"), "GOPROXY=off")
	if out, berr := build.CombinedOutput(); berr != nil {
		fmt.Fprintf(os.Stderr, "cli tests: building ballast failed: %v\n%s\n", berr, out)
		return 1
	}
	return m.Run()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func filterEnv(env []string, key string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// sandbox is one isolated world: a fake home, redirected client config roots
// and a working directory.
type sandbox struct {
	t    *testing.T
	sb   *testenv.Sandbox
	work string
	env  []string
}

// newSandbox creates the redirected environment (HOME, BALLAST_HOME,
// BALLAST_INSTALL_ROOT, BALLAST_MANAGED_DIR, CLAUDE_CONFIG_DIR, CODEX_HOME,
// HERMES_HOME, GIT_CONFIG_GLOBAL, ...) and an empty working directory.
func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	sb := testenv.New(t)
	s := &sandbox{t: t, sb: sb, work: filepath.Join(sb.Root, "work")}
	if err := os.MkdirAll(s.work, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range sb.Vars {
		s.env = append(s.env, k+"="+v)
	}
	// Only what child processes need to run: PATH (git, the shell) and, on
	// Windows, the system root. Everything else of the caller's environment
	// is dropped so no real configuration can leak in.
	for _, k := range []string{"PATH", "SYSTEMROOT", "SystemRoot", "COMSPEC", "PATHEXT", "TMPDIR", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(k); ok {
			s.env = append(s.env, k+"="+v)
		}
	}
	s.env = append(s.env, "GOPROXY=off", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "BALLAST_TEST_WATCHDOG=5s")
	// A fresh gc.stamp keeps SessionStart from spawning the detached
	// `ballast gc`, which would otherwise race the removal of the sandbox.
	// The tests of that mechanism remove the stamp themselves.
	if err := os.MkdirAll(sb.BallastHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sb.BallastHome, "gc.stamp"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// setenv adds or replaces an environment variable for later runs.
func (s *sandbox) setenv(k, v string) {
	s.env = append(filterEnv(s.env, k), k+"="+v)
}

// unsetenv removes an environment variable for later runs.
func (s *sandbox) unsetenv(k string) { s.env = filterEnv(s.env, k) }

// result is one finished ballast run.
type result struct {
	stdout, stderr string
	code           int
}

// run executes ballast in dir (the work directory when empty).
func (s *sandbox) run(dir, stdin string, args ...string) result {
	s.t.Helper()
	cmd := exec.Command(ballastBin, args...)
	cmd.Env = s.env
	if dir == "" {
		dir = s.work
	}
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		s.t.Fatalf("starting ballast %v: %v", args, err)
	}
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		s.t.Fatalf("ballast %v timed out\nstdout: %s\nstderr: %s", args, so.String(), se.String())
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			s.t.Fatalf("ballast %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return result{stdout: so.String(), stderr: se.String(), code: code}
}

// mustExit runs ballast and fails unless the exit code is want.
func (s *sandbox) mustExit(want int, dir, stdin string, args ...string) result {
	s.t.Helper()
	r := s.run(dir, stdin, args...)
	if r.code != want {
		s.t.Fatalf("ballast %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, r.code, want, r.stdout, r.stderr)
	}
	return r
}

// git runs git in dir with the sandbox environment.
func (s *sandbox) git(dir string, args ...string) string {
	s.t.Helper()
	out, err := newGitCmd(s, dir, args...).CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newGitCmd(s *sandbox, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = s.env
	return cmd
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("git is required in CI")
		}
		t.Skip("git not found")
	}
}

// newRepo initializes a git repository with one committed file.
func (s *sandbox) newRepo(name string) string {
	s.t.Helper()
	requireGit(s.t)
	dir := filepath.Join(s.sb.Root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	s.git(dir, "init", "-q", "-b", "main")
	s.git(dir, "config", "user.name", "ballast-test")
	s.git(dir, "config", "user.email", "test@example.invalid")
	s.git(dir, "config", "commit.gpgsign", "false")
	s.git(dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// jsonOut decodes the JSON object a hook wrote to stdout.
func jsonOut(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%q", err, stdout)
	}
	return m
}

// dig walks nested JSON objects.
func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}
