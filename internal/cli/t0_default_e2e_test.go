package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestT0HostDoesNotInterceptCanonicalVerify pins the v0.6 positioning (T0 is
// the default guarantee, hook-started verify is an experimental T1 feature):
// on a host that has neither declared T1 nor been locked, PreToolUse must NOT
// intercept the canonical `ballast verify <name>` (exit 0, empty stdout, no
// request file, no spawned job), and the verify the agent then runs itself
// takes the original detached background runner path, whose evidence carries
// tier "T0" and origin "cli".
func TestT0HostDoesNotInterceptCanonicalVerify(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	repo := s.newRepo("proj")
	writeFile(t, filepath.Join(repo, "a.go"), "package proj\n")
	writeFile(t, filepath.Join(repo, ".ballast", "project.json"), projectJSON(t, 0, ballastBin, "version"))
	s.git(repo, "add", "-A")
	s.git(repo, "commit", "-q", "-m", "init")

	for _, cmd := range []string{"ballast verify test", "ballast verify", "/usr/local/bin/ballast verify test"} {
		r := s.hook("claude-code", "PreToolUse", map[string]any{
			"session_id": "t0-1", "cwd": repo, "tool_name": "Bash",
			"tool_input": map[string]any{"command": cmd},
		})
		wantEmpty(t, "PreToolUse "+cmd+" on a T0 host", r)
	}
	// Nothing was started on the agent's behalf by the hook.
	if _, err := os.Stat(filepath.Join(s.sb.BallastHome, "requests")); err == nil {
		t.Error("the hook created a verify request on a T0 host")
	}
	if _, err := os.Stat(filepath.Join(s.sb.BallastHome, "evidence")); err == nil {
		t.Error("the hook produced evidence on a T0 host")
	}

	// The agent then runs it: the original detached runner.
	r := s.mustExit(0, repo, "", "verify", "test")
	if !strings.Contains(r.stdout, "job=") {
		t.Fatalf("verify should print the started job: %q", r.stdout)
	}
	waitStatus(t, s, repo, 0, "test")

	var recs int
	err := filepath.Walk(filepath.Join(s.sb.BallastHome, "evidence"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var rec struct {
			Tier    string `json:"tier"`
			Origin  string `json:"origin"`
			Sandbox string `json:"sandbox"`
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			return err
		}
		recs++
		if rec.Tier != "T0" || rec.Origin == "hook" || rec.Outcome != "pass" {
			t.Errorf("evidence %s: tier %q origin %q outcome %q, want T0, not hook, pass", filepath.Base(p), rec.Tier, rec.Origin, rec.Outcome)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if recs != 1 {
		t.Errorf("evidence records = %d, want 1", recs)
	}
}
