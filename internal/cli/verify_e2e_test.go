package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// projectJSON renders a project.json whose verify commands are the ballast
// binary itself, so the tests need no other toolchain: `ballast version`
// exits 0 (pass) and `ballast frobnicate` exits 2 (fail).
func projectJSON(t *testing.T, timeoutS int, argv ...string) string {
	t.Helper()
	spec := map[string]any{"name": "test", "argv": argv, "required": true}
	if timeoutS > 0 {
		spec["timeout_s"] = timeoutS
	}
	b, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"verify":  []any{spec},
		"exempt":  []string{"**/*.md"},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// waitStatus polls `verify --status args...` until its exit code is want.
func waitStatus(t *testing.T, s *sandbox, dir string, want int, args ...string) result {
	t.Helper()
	full := append([]string{"verify", "--status"}, args...)
	deadline := time.Now().Add(45 * time.Second)
	var last result
	for time.Now().Before(deadline) {
		last = s.run(dir, "", full...)
		if last.code == want {
			return last
		}
		if last.code != 6 && last.code != 11 {
			break // a final state other than the wanted one: no point waiting
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("ballast %v: exit %d, want %d\nstdout: %s\nstderr: %s", full, last.code, want, last.stdout, last.stderr)
	return last
}

// TestVerifyFullFlow is the U8 integration acceptance of architecture §8.3:
// init --yes, background verify, --status polled to pass, --check fresh, and
// a modified file makes --check stale (10).
func TestVerifyFullFlow(t *testing.T) {
	s := newSandbox(t)
	repo := s.newRepo("proj")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.invalid/proj\n\ngo 1.21\n")
	writeFile(t, filepath.Join(repo, "a.go"), "package proj\n")
	writeFile(t, filepath.Join(repo, "README.md"), "# proj\n")

	// init: dry-run writes nothing, --yes writes, a second run is refused.
	r := s.mustExit(0, repo, "", "init", "--dry-run")
	if !strings.Contains(r.stdout, `"./..."`) {
		t.Errorf("init --dry-run should propose go test:\n%s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(repo, ".ballast", "project.json")); err == nil {
		t.Fatal("init --dry-run wrote project.json")
	}
	s.mustExit(0, repo, "", "init", "--yes")
	pj := filepath.Join(repo, ".ballast", "project.json")
	if !strings.Contains(readFile(t, pj), `"go"`) {
		t.Errorf("project.json should hold the go suggestion:\n%s", readFile(t, pj))
	}
	s.mustExit(3, repo, "", "init", "--yes")
	s.mustExit(0, repo, "", "init", "--yes", "--force")

	// Replace the suggestion by a command that needs no toolchain.
	writeFile(t, pj, projectJSON(t, 0, ballastBin, "version"))
	s.git(repo, "add", "-A")
	s.git(repo, "commit", "-q", "-m", "init")

	// Before any verify there is no evidence.
	s.mustExit(11, repo, "", "verify", "--check")
	s.mustExit(11, repo, "", "verify", "--status")

	// Background verify: returns immediately, prints job=<id> name=<name>.
	r = s.mustExit(0, repo, "", "verify")
	if !strings.Contains(r.stdout, "job=") || !strings.Contains(r.stdout, "name=test") {
		t.Fatalf("verify should print the started job: %q", r.stdout)
	}
	st := waitStatus(t, s, repo, 0)
	if !strings.Contains(st.stdout, "outcome=pass") {
		t.Errorf("status output: %q", st.stdout)
	}
	jobID := ""
	for _, f := range strings.Fields(r.stdout) {
		if strings.HasPrefix(f, "job=") {
			jobID = strings.TrimPrefix(f, "job=")
		}
	}
	s.mustExit(0, repo, "", "verify", "--status", jobID)
	js := s.mustExit(0, repo, "", "verify", "--status", "--json", "test")
	var parsed struct {
		Jobs []struct {
			Name      string `json:"name"`
			Outcome   string `json:"outcome"`
			ValidPass bool   `json:"valid_pass"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &parsed); err != nil || len(parsed.Jobs) != 1 || parsed.Jobs[0].Outcome != "pass" || !parsed.Jobs[0].ValidPass {
		t.Errorf("status --json: %v %s", err, js.stdout)
	}

	// The evidence matches the committed tree.
	s.mustExit(0, repo, "", "verify", "--check")
	s.mustExit(0, repo, "", "verify", "--check", "--commit")
	s.mustExit(0, repo, "", "verify", "--check", "--worktree")

	// An exempt file does not invalidate it.
	writeFile(t, filepath.Join(repo, "README.md"), "# proj changed\n")
	s.git(repo, "add", "README.md")
	s.mustExit(0, repo, "", "verify", "--check")
	s.git(repo, "reset", "-q", "--hard")

	// An unstaged edit: commit mode (the index) still matches, worktree mode does not.
	writeFile(t, filepath.Join(repo, "a.go"), "package proj\n\nvar changed = 1\n")
	s.mustExit(0, repo, "", "verify", "--check")
	s.mustExit(10, repo, "", "verify", "--check", "--worktree")

	// Staged edit: the tree to be committed changed ⇒ stale (10).
	s.git(repo, "add", "a.go")
	r = s.mustExit(10, repo, "", "verify", "--check")
	if !strings.Contains(r.stderr, "stale") {
		t.Errorf("--check stale message should be on stderr: %q", r.stderr)
	}
	s.mustExit(10, repo, "", "verify", "--check", "--commit")

	// GIT_INDEX_FILE: a relative path is resolved against the cwd and wins
	// over the real index (as under a pre-commit hook).
	s.git(repo, "reset", "-q", "--hard") // index == worktree == HEAD == verified tree again
	s.mustExit(0, repo, "", "verify", "--check")
	altIndex := filepath.Join(repo, ".git", "alt-index")
	if b, err := os.ReadFile(filepath.Join(repo, ".git", "index")); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(altIndex, b, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "a.go"), "package proj\n\nvar viaAlt = 2\n")
	s.setenv("GIT_INDEX_FILE", filepath.Join(".git", "alt-index"))
	s.git(repo, "add", "a.go") // goes into the alternate index only
	s.mustExit(10, repo, "", "verify", "--check")
	// --worktree ignores GIT_INDEX_FILE; the worktree differs, so it is stale too.
	s.mustExit(10, repo, "", "verify", "--check", "--worktree")
	// A subdirectory of the repository still finds the repo, but the
	// relative GIT_INDEX_FILE then resolves against that cwd: no such file is
	// an empty index, which cannot match the verified tree (stale, never fresh).
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.mustExit(10, filepath.Join(repo, "sub"), "", "verify", "--check")
	s.unsetenv("GIT_INDEX_FILE")
	s.mustExit(0, repo, "", "verify", "--check") // real index is untouched
}

func TestVerifyConfigurationErrors(t *testing.T) {
	s := newSandbox(t)
	// Outside a git repository: start is a configuration error, check cannot decide.
	s.mustExit(2, "", "", "verify")
	s.mustExit(12, "", "", "verify", "--check")
	s.mustExit(2, "", "", "verify", "--status")

	repo := s.newRepo("cfg")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	s.mustExit(2, repo, "", "verify")             // no project.json
	s.mustExit(12, repo, "", "verify", "--check") // no project.json
	s.mustExit(2, repo, "", "verify", "--status") // no project.json
	s.mustExit(2, repo, "", "verify", "--foreground", "test")
	writeFile(t, filepath.Join(repo, ".ballast", "project.json"), "{not json")
	s.mustExit(2, repo, "", "verify")
	s.mustExit(12, repo, "", "verify", "--check")
	writeFile(t, filepath.Join(repo, ".ballast", "project.json"), projectJSON(t, 0, ballastBin, "version"))
	s.mustExit(2, repo, "", "verify", "no-such-entry")
	s.mustExit(11, repo, "", "verify", "--status", "no-such-entry")
	s.mustExit(11, repo, "", "verify", "--status", "20260101T000000-abcdef")
	s.mustExit(2, repo, "", "verify", "--run-job", "20260101T000000-abcdef")

	// Usage errors.
	s.mustExit(2, repo, "", "verify", "--status", "--check")
	s.mustExit(2, repo, "", "verify", "--commit")
	s.mustExit(2, repo, "", "verify", "--check", "--commit", "--worktree")
	s.mustExit(2, repo, "", "verify", "--foreground")
	s.mustExit(2, repo, "", "verify", "--check", "name")
	s.mustExit(2, repo, "", "verify", "--bogus")
}

func TestVerifyForegroundOutcomes(t *testing.T) {
	s := newSandbox(t)
	repo := s.newRepo("fg")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	pj := filepath.Join(repo, ".ballast", "project.json")

	writeFile(t, pj, projectJSON(t, 0, ballastBin, "version"))
	r := s.mustExit(0, repo, "", "verify", "--foreground", "test")
	if !strings.Contains(r.stdout, "ballast ") {
		t.Errorf("foreground should stream the command output: %q", r.stdout)
	}
	waitStatus(t, s, repo, 0)

	writeFile(t, pj, projectJSON(t, 0, ballastBin, "frobnicate"))
	s.mustExit(1, repo, "", "verify", "--foreground", "test")
	s.mustExit(1, repo, "", "verify", "--status")

	// A command that cannot be started is an infrastructure outcome (5).
	writeFile(t, pj, projectJSON(t, 0, filepath.Join(repo, "no-such-binary")))
	s.mustExit(5, repo, "", "verify", "--foreground", "test")
	s.mustExit(5, repo, "", "verify", "--status")
}

// TestVerifyTimeoutAndSupersede covers exit codes 3 (timeout) and 7
// (superseded); it needs a shell to sleep with.
func TestVerifyTimeoutAndSupersede(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh -c sleep")
	}
	s := newSandbox(t)
	repo := s.newRepo("slow")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	pj := filepath.Join(repo, ".ballast", "project.json")

	writeFile(t, pj, projectJSON(t, 1, "sh", "-c", "sleep 30"))
	s.mustExit(3, repo, "", "verify", "--foreground", "test")
	s.mustExit(3, repo, "", "verify", "--status")

	// Two overlapping runs: the first becomes superseded (7).
	writeFile(t, pj, projectJSON(t, 0, "sh", "-c", "sleep 30"))
	r := s.mustExit(0, repo, "", "verify")
	first := strings.TrimPrefix(strings.Fields(r.stdout)[0], "job=")
	s.mustExit(6, repo, "", "verify", "--status")
	s.mustExit(6, repo, "", "verify", "--status", first)
	// While the first one is still running --check cannot be fresh (13).
	s.mustExit(13, repo, "", "verify", "--check", "--worktree")
	writeFile(t, pj, projectJSON(t, 0, ballastBin, "version"))
	s.mustExit(0, repo, "", "verify")
	waitStatus(t, s, repo, 7, first)
	waitStatus(t, s, repo, 0)
}

// TestFreshnessWithPreCommitHook is the scripts/freshness.sh scenario with
// a real git pre-commit hook calling `ballast verify --check`.
func TestFreshnessWithPreCommitHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook script needs a POSIX sh")
	}
	s := newSandbox(t)
	repo := s.newRepo("hooked")
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")
	writeFile(t, filepath.Join(repo, "b.go"), "package a\n")
	writeFile(t, filepath.Join(repo, ".ballast", "project.json"), projectJSON(t, 0, ballastBin, "version"))
	s.git(repo, "add", "-A")
	s.git(repo, "commit", "-q", "-m", "init")
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\nexec '"+ballastBin+"' verify --check --commit\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	tryCommit := func(args ...string) error {
		t.Helper()
		_, err := gitErr(s, repo, append([]string{"commit", "-q"}, args...)...)
		return err
	}

	// Verified tree == tree to commit: allowed (no change, allow-empty).
	s.mustExit(0, repo, "", "verify")
	waitStatus(t, s, repo, 0)
	if err := tryCommit("--allow-empty", "-m", "empty with evidence"); err != nil {
		t.Fatalf("commit with fresh evidence must pass: %v", err)
	}

	// commit -a with an unstaged edit: the hook sees the temporary index.
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n\nvar x = 1\n")
	if err := tryCommit("-a", "-m", "edit"); err == nil {
		t.Fatal("commit -a on an unverified edit must be blocked")
	}
	s.mustExit(0, repo, "", "verify")
	waitStatus(t, s, repo, 0)
	if err := tryCommit("-a", "-m", "edit"); err != nil {
		t.Fatalf("commit -a after verify must pass: %v", err)
	}

	// commit <paths>: only that path is committed; evidence of the whole
	// work tree (including the other unstaged edit) is stale for it.
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n\nvar x = 2\n")
	writeFile(t, filepath.Join(repo, "b.go"), "package a\n\nvar y = 2\n")
	s.mustExit(0, repo, "", "verify")
	waitStatus(t, s, repo, 0)
	if err := tryCommit("-m", "only a", "a.go"); err == nil {
		t.Fatal("path commit differs from the verified work tree: must be blocked")
	}
}

func gitErr(s *sandbox, dir string, args ...string) (string, error) {
	cmd := newGitCmd(s, dir, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
