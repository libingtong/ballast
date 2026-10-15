package hook

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/sys"
)

const childEnv = "BALLAST_U7_CHILD"

// TestMain doubles as the re-exec entry for the multi-process tests (the
// child runs hook.Main with fake ports and the real process stdio, exit code
// and watchdog). model.ShellReadPaths is U2's real implementation; the
// earlier test-only stand-in was removed in contract v1.3.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Exit(childMain())
	}
	os.Exit(m.Run())
}

// childMain is `ballast hook` with fake ports and the real process stdio.
func childMain() int {
	lay, err := layout.Resolve(sys.OSEnv{})
	if err != nil {
		os.Stderr.WriteString("child: " + err.Error() + "\n")
		return 70
	}
	h := newCoreHarness(sys.OSEnv{}, lay)
	h.engine.verdict = denyVerdict()
	h.deps.Exit = os.Exit
	h.deps.Faults = FaultsFromEnv(os.LookupEnv)
	if ms, err := strconv.Atoi(os.Getenv("BALLAST_U7_WD_MS")); err == nil && ms > 0 {
		h.deps.Watchdog = time.Duration(ms) * time.Millisecond
	} else {
		h.deps.Watchdog = 5 * time.Second // generous: the test machine may be loaded
	}
	return Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, h.deps)
}

type childResult struct {
	stdout, stderr string
	code           int
}

// runChild re-executes the test binary as a hook process in a sandboxed
// environment (nothing from the caller's environment leaks in).
func runChild(t *testing.T, stdin string, extraEnv []string, args ...string) childResult {
	t.Helper()
	root := t.TempDir()
	env := []string{
		childEnv + "=1",
		"HOME=" + root,
		"USERPROFILE=" + root,
		"BALLAST_HOME=" + root + "/.ballast",
		"BALLAST_INSTALL_ROOT=" + root,
		"BALLAST_MANAGED_DIR=" + root + "/managed",
		"CLAUDE_CONFIG_DIR=" + root + "/.claude",
		"CODEX_HOME=" + root + "/.codex",
		"HERMES_HOME=" + root + "/.hermes",
		"GOCOVERDIR=" + t.TempDir(), // a -cover test binary warns on stderr otherwise
	}
	if p := os.Getenv("SystemRoot"); p != "" { // Windows needs it to start a process
		env = append(env, "SystemRoot="+p)
	}
	env = append(env, extraEnv...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("run child: %v", err)
	}
	return childResult{stdout: so.String(), stderr: se.String(), code: code}
}

func TestChildProcessHook(t *testing.T) {
	r := runChild(t, preToolBash, nil, "--client", "claude-code", "--event", "PreToolUse")
	if r.code != 0 || !strings.Contains(r.stdout, denyJSON) || r.stderr != "" {
		t.Fatalf("child = %+v", r)
	}
	// No decision ⇒ empty stdout, exit 0 (SessionEnd renders nothing).
	r = runChild(t, `{"session_id":"s"}`, nil, "--client", "claude-code", "--event", "SessionEnd")
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("session end child = %+v", r)
	}
	// Exceptions: bad stdin, bad args, unknown event.
	for _, tc := range []struct {
		stdin string
		args  []string
	}{
		{"not json", []string{"--client", "claude-code", "--event", "PreToolUse"}},
		{preToolBash, []string{"--client", "claude-code"}},
		{preToolBash, []string{"--client", "claude-code", "--event", "Nope"}},
		{preToolBash, []string{"--client", "nope", "--event", "PreToolUse"}},
	} {
		r := runChild(t, tc.stdin, nil, tc.args...)
		if r.code != 1 || r.stdout != "" || !strings.HasPrefix(r.stderr, "ballast: ") {
			t.Errorf("%v: child = %+v", tc.args, r)
		}
	}
}
