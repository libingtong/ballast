package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/report"
)

// requestID returns a request id in the frozen job-id format (UTC
// "20060102T150405" + "-" + 6 hex digits) that no earlier call returned.
func requestID(n int) string {
	return fmt.Sprintf("%s-%06x", time.Now().UTC().Format("20060102T150405"), 0xa1b2c0+n)
}

// TestVerifyHookStartArgs: the internal --hook-start child rejects anything
// that does not parse as the frozen argv (exit 2, nothing started).
func TestVerifyHookStartArgs(t *testing.T) {
	s := newSandbox(t)
	id := requestID(1)
	for name, args := range map[string][]string{
		"no request id":       {"verify", "--hook-start", "--cwd", s.work, "--client", "claude-code", "--"},
		"bad request id":      {"verify", "--hook-start", "--request", "nope", "--cwd", s.work, "--client", "claude-code", "--"},
		"relative cwd":        {"verify", "--hook-start", "--request", id, "--cwd", "rel", "--client", "claude-code", "--"},
		"unknown flag":        {"verify", "--hook-start", "--request", id, "--cwd", s.work, "--client", "claude-code", "--evil", "x", "--"},
		"flag-like name":      {"verify", "--hook-start", "--request", id, "--cwd", s.work, "--client", "claude-code", "--", "--status"},
		"bare --hook-start":   {"verify", "--hook-start"},
		"hook-start + status": {"verify", "--hook-start", "--status"},
	} {
		r := s.run("", "", args...)
		if r.code != 2 {
			t.Errorf("%s: exit %d, want 2\nstdout: %s\nstderr: %s", name, r.code, r.stdout, r.stderr)
		}
	}
}

// TestVerifyHookStartHelpLikeValue: the frozen --hook-start grammar takes the
// next token verbatim, so "-h" / "--help" as a session or agent id is a value,
// not a help request: the request file is written (with the config error) and
// the exit follows the argument result, never the usage text with exit 0.
func TestVerifyHookStartHelpLikeValue(t *testing.T) {
	s := newSandbox(t)
	for i, flag := range [][]string{{"--session", "-h"}, {"--session", "--help"}, {"--agent", "-help"}} {
		id := requestID(10 + i)
		args := append([]string{"verify", "--hook-start", "--request", id, "--cwd", s.work, "--client", "claude-code"}, flag...)
		args = append(args, "--")
		r := s.run("", "", args...)
		if r.code != 2 {
			t.Errorf("%v: exit %d, want 2 (not the help exit 0)\n%s%s", flag, r.code, r.stdout, r.stderr)
		}
		if strings.Contains(r.stdout, "用法") {
			t.Errorf("%v: printed the usage text: %q", flag, r.stdout)
		}
		st := s.run("", "", "verify", "--status", id)
		if st.code != 5 || !strings.Contains(st.stdout, "error=") {
			t.Errorf("%v: --status of the request: exit %d %q, want 5 with the recorded error", flag, st.code, st.stdout)
		}
	}
}

// TestVerifyStatusRequestFallback: with no job of that id,
// `verify --status <request_id>` falls back to the request file the
// hook-started verify wrote; an unknown id keeps the no-record exit (11).
func TestVerifyStatusRequestFallback(t *testing.T) {
	s := newSandbox(t)

	// Unknown id ⇒ 11, no request file.
	r := s.mustExit(11, "", "", "verify", "--status", requestID(2))
	if !strings.Contains(r.stdout, "state=none") {
		t.Errorf("unknown id output: %q", r.stdout)
	}

	// A configuration error (cwd is not a repository): exit 2, but the
	// request file is still written, carries the error, and --status
	// reports it as infra (5).
	id := requestID(3)
	r = s.run("", "", "verify", "--hook-start", "--request", id, "--cwd", s.work, "--client", "claude-code", "--")
	if r.code != 2 {
		t.Fatalf("hook-start outside a repository: exit %d, want 2\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = s.mustExit(5, "", "", "verify", "--status", id)
	if !strings.Contains(r.stdout, "request="+id) || !strings.Contains(r.stdout, "error=") {
		t.Errorf("status of a failed request: %q", r.stdout)
	}
	r = s.mustExit(5, "", "", "verify", "--status", "--json", id)
	var js struct {
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
		Exit      int    `json:"exit"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &js); err != nil || js.RequestID != id || js.Error == "" || js.Exit != 5 {
		t.Errorf("status --json = %q (%v)", r.stdout, err)
	}
}

// TestVerifyHookStartRuns: a hook-started verify in a repository starts the
// job under the request id, records origin "hook", and the request id works
// with --status. The job runs in the OS sandbox (mandatory for origin
// "hook"); without a usable backend it ends as infra, which is also fine for
// this wiring test (the boundary scripts cover the sandbox itself).
func TestVerifyHookStartRuns(t *testing.T) {
	s := newSandbox(t)
	repo := s.newRepo("proj")
	writeFile(t, filepath.Join(repo, "a.go"), "package proj\n")
	writeFile(t, filepath.Join(repo, ".ballast", "project.json"), projectJSON(t, 0, ballastBin, "version"))
	s.git(repo, "add", "-A")
	s.git(repo, "commit", "-q", "-m", "init")

	id := requestID(4)
	r := s.mustExit(0, "", "", "verify", "--hook-start", "--request", id, "--cwd", repo,
		"--client", "claude-code", "--session", "sess-1", "--", "test")
	if !strings.Contains(r.stdout, "job="+id) {
		t.Fatalf("the first job must reuse the request id: %q", r.stdout)
	}
	// Poll the request id until the job is no longer running.
	deadline := time.Now().Add(60 * time.Second)
	var st result
	for time.Now().Before(deadline) {
		st = s.run("", "", "verify", "--status", id)
		if st.code != 6 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("hook-started request %s: exit %d: %s", id, st.code, strings.TrimSpace(st.stdout))
	if st.code != 0 && st.code != 5 {
		t.Fatalf("status of the hook-started request: exit %d\n%s%s", st.code, st.stdout, st.stderr)
	}
	if !strings.Contains(st.stdout, "job="+id) || !strings.Contains(st.stdout, "name=test") {
		t.Errorf("status output: %q", st.stdout)
	}
	// The job id and the request id are the same, so the ordinary job status
	// agrees with the request status.
	if again := s.run("", "", "verify", "--status", id); again.code != st.code {
		t.Errorf("--status %s twice: %d then %d", id, st.code, again.code)
	}
	// The request file is outside jobs/ and carries the mapping.
	files, _ := filepath.Glob(filepath.Join(s.sb.BallastHome, "requests", "*", id+".json"))
	if len(files) != 1 {
		t.Fatalf("request file: %v", files)
	}
	var rf model.VerifyRequestFile
	if err := json.Unmarshal([]byte(readFile(t, files[0])), &rf); err != nil {
		t.Fatal(err)
	}
	if rf.RequestID != id || rf.Origin != model.OriginHook || rf.SessionID != "sess-1" || len(rf.Jobs) != 1 || rf.Jobs[0].JobID != id {
		t.Errorf("request file = %+v", rf)
	}
}

// writeAudit seeds the audit log with the given JSON lines.
func writeAudit(t *testing.T, s *sandbox, lines ...string) {
	t.Helper()
	dir := filepath.Join(s.sb.BallastHome, "audit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := time.Now().UTC().Format("2006-01") + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReport covers every exit code of `ballast report --out` and the
// privacy of the file it writes.
func TestReport(t *testing.T) {
	s := newSandbox(t)
	secret := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	writeAudit(t, s,
		fmt.Sprintf(`{"v":1,"ts":%q,"type":"hit","session_id":"sess-SECRETID-1","repo_inst":"repo-SECRETINST","client":"claude-code","tier":"T0","rule_id":"read-before-edit","layer":"user","action":"deny","decision":"deny","matched":%q,"reason":"reason mentions /Users/jdoe/private/file.go","latency_us":2500}`, now, "token "+secret),
		fmt.Sprintf(`{"v":1,"ts":%q,"type":"hit","session_id":"sess-SECRETID-2","repo_inst":"repo-SECRETINST","client":"codex","tier":"T0","rule_id":"my-private-rule-name","layer":"repo","action":"warn","decision":"warn","latency_us":1800}`, now),
		fmt.Sprintf(`{"v":1,"ts":%q,"type":"diag","session_id":"sess-SECRETID-1","client":"claude-code","tier":"T0","detail":{"diag":"state_corrupt"}}`, now),
		fmt.Sprintf(`{"v":1,"ts":%q,"type":"diag","session_id":"sess-SECRETID-1","client":"claude-code","tier":"T0","detail":{"diag":"not-a-frozen-diag-name-/Users/jdoe"}}`, now),
	)

	out := filepath.Join(s.sb.Root, "out", "report.json")
	// 2: usage.
	s.mustExit(2, "", "", "report")
	s.mustExit(2, "", "", "report", "--out", out, "--since", "yesterday")
	s.mustExit(2, "", "", "report", "--out", out, "extra")
	if _, err := os.Stat(out); err == nil {
		t.Fatal("a usage error wrote the report")
	}
	// 0: written (parent directory created).
	r := s.mustExit(0, "", "", "report", "--out", out)
	if !strings.Contains(r.stdout, "未发送") {
		t.Errorf("stdout should say nothing was sent: %q", r.stdout)
	}
	raw := readFile(t, out)
	var rep report.Report
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("the report is not JSON: %v", err)
	}
	if v := report.Validate(&rep); len(v) != 0 {
		t.Errorf("the written report fails Validate: %v", v)
	}
	if rep.Sessions != 2 || rep.Repos != 1 {
		t.Errorf("sessions=%d repos=%d, want 2 and 1 (counts only)", rep.Sessions, rep.Repos)
	}
	for _, leak := range []string{"SECRETID", "SECRETINST", secret, "jdoe", "/Users", "my-private-rule-name", "not-a-frozen-diag-name", "reason mentions"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the report leaks %q:\n%s", leak, raw)
		}
	}
	if !strings.Contains(raw, "read-before-edit") || !strings.Contains(raw, report.HashRuleID("my-private-rule-name")) {
		t.Errorf("shipped rule ids verbatim and custom ones hashed:\n%s", raw)
	}
	if rep.Diags["state_corrupt"] != 1 || rep.Diags[report.DiagOther] != 1 {
		t.Errorf("diags = %v", rep.Diags)
	}
	// 4: the file exists; --force overwrites.
	r = s.mustExit(4, "", "", "report", "--out", out)
	if !strings.Contains(r.stderr, "--force") {
		t.Errorf("stderr should mention --force: %q", r.stderr)
	}
	s.mustExit(0, "", "", "report", "--out", out, "--force", "--since", "7d")
	// A relative --out is resolved against the working directory.
	s.mustExit(0, s.work, "", "report", "--out", "rel.json")
	if _, err := os.Stat(filepath.Join(s.work, "rel.json")); err != nil {
		t.Errorf("relative --out: %v", err)
	}
	// 5: I/O (the parent of the target is a regular file).
	blocker := filepath.Join(s.sb.Root, "blocker")
	writeFile(t, blocker, "x")
	s.mustExit(5, "", "", "report", "--out", filepath.Join(blocker, "report.json"))
}

// TestReportNoAudit: an empty audit log still yields a valid report.
func TestReportNoAudit(t *testing.T) {
	s := newSandbox(t)
	out := filepath.Join(s.sb.Root, "empty-report.json")
	s.mustExit(0, "", "", "report", "--out", out)
	var rep report.Report
	if err := json.Unmarshal([]byte(readFile(t, out)), &rep); err != nil {
		t.Fatal(err)
	}
	if v := report.Validate(&rep); len(v) != 0 || rep.Sessions != 0 {
		t.Errorf("empty report: %v %+v", v, rep)
	}
}

// TestDoctorTierChecks: v0.6 positioning. On a T0 host that has not opted into
// the experimental T1, doctor reports exactly one tier row (tier.level, ok,
// "T0(默认模式)", no fix) even when a project layer weakens the sandbox; with
// --t1 it is wired with the working directory, so the weakened project layer
// is reported (warn on a T0 host). Native Windows has no T1 rows either way.
func TestDoctorTierChecks(t *testing.T) {
	s := newSandbox(t)
	repo := s.newRepo("proj")
	writeFile(t, filepath.Join(repo, ".claude", "settings.json"), `{"sandbox":{"excludedCommands":["npm"]}}`)
	type check struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Fix     string `json:"fix"`
	}
	run := func(args ...string) (string, map[string]check) {
		r := s.mustExit(0, repo, "", append([]string{"doctor", "--json"}, args...)...)
		var doc struct {
			Tier   string  `json:"tier"`
			Checks []check `json:"checks"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
			t.Fatal(err)
		}
		got := map[string]check{}
		for _, c := range doc.Checks {
			got[c.ID] = c
		}
		return doc.Tier, got
	}

	tierName, got := run()
	if tierName != "T0" {
		t.Errorf("tier = %q, want T0 in the test sandbox", tierName)
	}
	if c := got["tier.level"]; c.Status != "ok" || c.Message != "T0(默认模式)" || c.Fix != "" {
		t.Errorf("default tier.level = %+v", c)
	}
	for id := range got {
		if strings.HasPrefix(id, "tier.") && id != "tier.level" {
			t.Errorf("T0 default emitted %s", id)
		}
	}

	_, got = run("--t1")
	if _, ok := got["tier.level"]; !ok {
		t.Errorf("--t1: no tier.level check: %v", got)
	}
	if runtime.GOOS == "windows" {
		// Native Windows is T0 only: no per-component tier checks.
		if _, ok := got["tier.windows_t0"]; ok || got["tier.repo_sandbox_weakened"].ID != "" {
			t.Errorf("windows --t1: tier.windows_t0 / tier.repo_sandbox_weakened must be absent (checks: %v)", got)
		}
	} else if got["tier.repo_sandbox_weakened"].Status != "warn" {
		t.Errorf("--t1: tier.repo_sandbox_weakened = %q, want warn (checks: %v)", got["tier.repo_sandbox_weakened"].Status, got)
	}
}

// TestVerifyStatusMultiJobRequestTakesTheWorst: the request id is also the id
// of the request's FIRST job, so `--status <request_id>` must still aggregate
// every job of a multi-job request (worst outcome), not show the first job
// only. One entry passes and one fails; the aggregate must equal the worst of
// the per-name results (1 when the sandbox ran them, 5 when it is unusable).
func TestVerifyStatusMultiJobRequestTakesTheWorst(t *testing.T) {
	s := newSandbox(t)
	repo := s.newRepo("proj")
	writeFile(t, filepath.Join(repo, "a.go"), "package proj\n")
	pj, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"verify": []any{
			map[string]any{"name": "okjob", "argv": []string{ballastBin, "version"}, "required": true},
			map[string]any{"name": "badjob", "argv": []string{ballastBin, "no-such-subcommand"}, "required": true},
		},
		"exempt": []string{"**/*.md"},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, ".ballast", "project.json"), string(pj)+"\n")
	s.git(repo, "add", "-A")
	s.git(repo, "commit", "-q", "-m", "init")

	id := requestID(6)
	r := s.mustExit(0, "", "", "verify", "--hook-start", "--request", id, "--cwd", repo,
		"--client", "claude-code", "--session", "sess-2", "--", "okjob", "badjob")
	if !strings.Contains(r.stdout, "job="+id) {
		t.Fatalf("hook-start output: %q", r.stdout)
	}
	deadline := time.Now().Add(60 * time.Second)
	var st result
	for time.Now().Before(deadline) {
		st = s.run("", "", "verify", "--status", id)
		// The aggregate ranks infra above running, so a finished infra job
		// next to a still-running one already exits non-6: wait until no job
		// of the request is running before reading the aggregate.
		if st.code != 6 && !strings.Contains(st.stdout, "state=running") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(st.stdout, "request="+id) || !strings.Contains(st.stdout, "name=okjob") || !strings.Contains(st.stdout, "name=badjob") {
		t.Fatalf("the status of a two-job request must list both jobs:\n%s", st.stdout)
	}
	want := 0
	for _, name := range []string{"okjob", "badjob"} {
		one := s.run(repo, "", "verify", "--status", name)
		for end := time.Now().Add(60 * time.Second); one.code == 6 && time.Now().Before(end); {
			time.Sleep(100 * time.Millisecond)
			one = s.run(repo, "", "verify", "--status", name)
		}
		t.Logf("--status %s: exit %d", name, one.code)
		if one.code == 1 || (one.code == 5 && want != 1) {
			want = one.code
		}
	}
	if st.code != want {
		t.Errorf("aggregate exit = %d, want the worst per-name result %d\n%s", st.code, want, st.stdout)
	}
	if want == 1 && st.code != 1 {
		t.Errorf("a failing second job must fail the request")
	}
}
