package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// shadowHits fires the log-only e2e-shadow rule n times in separate tool calls.
func shadowHits(t *testing.T, s *sandbox, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		r := s.hook("claude-code", "PreToolUse", map[string]any{
			"session_id": fmt.Sprintf("shadow-%d", i%2), "cwd": s.work, "tool_use_id": fmt.Sprintf("sh%d", i),
			"tool_name": "Bash", "tool_input": map[string]any{"command": "frobnicate --all"},
		})
		wantEmpty(t, "shadow hit (log action is silent)", r)
	}
}

func TestStatsSamplePromote(t *testing.T) {
	s := newSandbox(t)
	rulesPath := installE2ERules(t, s)
	// Only claude-code is installed (the manifest drives the capability check).
	s.mustExit(0, "", "", "install", "--client", "claude-code", "--force", "--binary", ballastBin)
	shadowHits(t, s, 6)

	r := s.mustExit(0, "", "", "stats")
	if !strings.Contains(r.stdout, "e2e-shadow") || !strings.Contains(r.stdout, "would") {
		t.Errorf("stats table:\n%s", r.stdout)
	}
	var rows []struct {
		Rule  string `json:"rule"`
		Hits  int    `json:"hits"`
		Would int    `json:"would"`
	}
	r = s.mustExit(0, "", "", "stats", "--json", "--since", "1d")
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("stats --json: %v\n%s", err, r.stdout)
	}
	found := false
	for _, row := range rows {
		if row.Rule == "e2e-shadow" {
			found = true
			if row.Would != 6 {
				t.Errorf("e2e-shadow would = %d, want 6 (%+v)", row.Would, row)
			}
		}
	}
	if !found {
		t.Errorf("no e2e-shadow row: %s", r.stdout)
	}
	s.mustExit(0, "", "", "stats", "--by", "session")
	s.mustExit(0, "", "", "stats", "--by", "repo", "--rule", "e2e-shadow")
	s.mustExit(2, "", "", "stats", "--by", "nonsense")
	s.mustExit(2, "", "", "stats", "--since", "yesterday-ish")
	s.mustExit(2, "", "", "stats", "--sample", "3") // --sample needs --rule
	s.mustExit(2, "", "", "stats", "--rule", "e2e-shadow", "--sample", "0")
	s.mustExit(2, "", "", "stats", "extra")

	// Promote: the gate wants a review newer than the rule's last change.
	s.mustExit(2, "", "", "rules", "promote", "e2e-shadow")                   // --to missing
	s.mustExit(2, "", "", "rules", "promote", "--to", "deny")                 // id missing
	s.mustExit(2, "", "", "rules", "promote", "e2e-shadow", "--to", "log")    // not a promotion target
	s.mustExit(4, "", "", "rules", "promote", "no-such-rule", "--to", "deny") // unknown rule
	s.mustExit(1, "", "", "rules", "promote", "e2e-shadow", "--to", "deny")   // no review yet
	r = s.mustExit(0, "", "", "stats", "--rule", "e2e-shadow", "--sample", "5")
	if !strings.Contains(r.stdout, "抽样 5 条") {
		t.Errorf("sample output:\n%s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(s.sb.BallastHome, "reviews", "e2e-shadow.json")); err != nil {
		t.Errorf("sample should write the review file: %v", err)
	}
	s.mustExit(0, "", "", "rules", "promote", "e2e-shadow", "--to", "deny")
	if !strings.Contains(readFile(t, rulesPath), `"action": "deny"`) {
		t.Errorf("promote did not rewrite the rule file")
	}
	// The audit trail records the promotion; the shadow rule now denies.
	out := wantJSON(t, "promoted rule", s.hook("claude-code", "PreToolUse", map[string]any{
		"session_id": "after", "cwd": s.work, "tool_name": "Bash", "tool_input": map[string]any{"command": "frobnicate"}}))
	if dig(out, "hookSpecificOutput", "permissionDecision") != "deny" {
		t.Errorf("promoted rule should deny: %v", out)
	}
	promoted := false
	for _, rec := range auditRecords(t, s) {
		if rec["type"] == "promote" {
			promoted = true
		}
	}
	if !promoted {
		t.Error("no promote record in the audit log")
	}
	// Already at deny: nothing stricter to promote to.
	s.mustExit(2, "", "", "rules", "promote", "e2e-shadow", "--to", "deny")

	// --force skips the gate (never the capability check) on a fresh rule.
	s.mustExit(1, "", "", "rules", "promote", "e2e-stop-warn", "--to", "deny")
	s.mustExit(0, "", "", "rules", "promote", "e2e-stop-warn", "--to", "deny", "--force")

	// Org rules cannot be promoted locally (4).
	writeFile(t, filepath.Join(s.sb.ManagedDir, "ballast", "rules.json"), `{"version":1,"rules":[{"id":"org-fixed","action":"log","event":"PreToolUse",
	"match":{"bash":{"cmd":"frobnicate"}},"message":"m",
	"examples":[{"name":"h","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"frobnicate"}}},
	            {"name":"m","expect":"miss","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}}]}]}`)
	s.mustExit(4, "", "", "rules", "promote", "org-fixed", "--to", "deny", "--force")
}

func TestStatsAuditReadFailure(t *testing.T) {
	s := newSandbox(t)
	// An audit "directory" that is a file cannot be read: infrastructure error (5).
	writeFile(t, filepath.Join(s.sb.BallastHome, "audit"), "not a directory")
	s.mustExit(5, "", "", "stats")
	s.mustExit(5, "", "", "audit", "lint")
}

func TestAuditLint(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	shadowHits(t, s, 2)
	r := s.mustExit(0, "", "", "audit", "lint")
	if !strings.Contains(r.stdout, "0 个问题") {
		t.Errorf("audit lint:\n%s", r.stdout)
	}
	files, _ := filepath.Glob(filepath.Join(s.sb.BallastHome, "audit", "*.jsonl"))
	if len(files) == 0 {
		t.Fatal("no audit file written by the hook")
	}
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("this line is not a record\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r = s.mustExit(1, "", "", "audit", "lint")
	if !strings.Contains(r.stdout, filepath.Base(files[0])) {
		t.Errorf("finding should name the file:\n%s", r.stdout)
	}
	r = s.mustExit(1, "", "", "audit", "lint", "--json")
	var rep struct {
		Findings []struct {
			Line int `json:"line"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || len(rep.Findings) == 0 {
		t.Errorf("audit lint --json: %v %s", err, r.stdout)
	}
	// stats skips the corrupt line and says so.
	r = s.mustExit(0, "", "", "stats")
	if !strings.Contains(r.stderr, "1 条损坏") {
		t.Errorf("stats should mention the corrupt line on stderr: %q", r.stderr)
	}
}

func TestGC(t *testing.T) {
	s := newSandbox(t)
	// The sandbox pre-creates a fresh gc.stamp; remove it so we can tell
	// whether `ballast gc` writes one.
	if err := os.Remove(filepath.Join(s.sb.BallastHome, "gc.stamp")); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(s.sb.BallastHome, "audit", "2000-01.jsonl")
	writeFile(t, old, "")
	recent := filepath.Join(s.sb.BallastHome, "audit", time.Now().UTC().Format("2006-01")+".jsonl")
	writeFile(t, recent, "")
	staleCache := filepath.Join(s.sb.BallastHome, "cache", "rules-0123456789abcdef-aaaaaaaa.json")
	writeFile(t, staleCache, "{}")
	longAgo := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(staleCache, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	// An expired session state with unflushed eval counts: gc must write
	// them as eval_summary(cause=gc) before the directory goes away.
	stale := filepath.Join(s.sb.BallastHome, "state", "repo1", "sess1", "main.json")
	writeFile(t, stale, `{"v":1,"started_at":"2000-01-01T00:00:00Z","event_seq":1,"seen":[],"read":{},"tool_count":0,"tool_count_since_inject":0,"prompt_count":0,"last_job_report_at":"0001-01-01T00:00:00Z","last_tool_failure_at":"0001-01-01T00:00:00Z","evals":{"r1":{"evals":3,"hits":0,"would":2,"unknown":0}}}`)
	if err := os.Chtimes(stale, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Dir(stale), longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	// P2: request files and quarantine entries older than 30 days, work-tree
	// leftovers of unknown jobs older than 24 hours; recent ones stay.
	oldReq := filepath.Join(s.sb.BallastHome, "requests", "repo1", "20000101T000000-aaaaaa.json")
	newReq := filepath.Join(s.sb.BallastHome, "requests", "repo1", "20261001T000000-bbbbbb.json")
	oldQuar := filepath.Join(s.sb.BallastHome, "quarantine", "20000101T000000-aaaaaa", ".claude", "settings.local.json")
	oldWork := filepath.Join(s.sb.BallastHome, "work", "20000101T000000-cccccc", "tree", "a.txt")
	newWork := filepath.Join(s.sb.BallastHome, "work", "20261001T000000-dddddd", "tree", "a.txt")
	for _, p := range []string{oldReq, newReq, oldQuar, oldWork, newWork} {
		writeFile(t, p, "x")
	}
	ancient := time.Now().Add(-90 * 24 * time.Hour)
	for _, p := range []string{oldReq, filepath.Join(s.sb.BallastHome, "quarantine", "20000101T000000-aaaaaa"), filepath.Join(s.sb.BallastHome, "work", "20000101T000000-cccccc")} {
		if err := os.Chtimes(p, ancient, ancient); err != nil {
			t.Fatal(err)
		}
	}
	s.mustExit(0, "", "", "gc")
	for _, p := range []string{oldReq, filepath.Dir(filepath.Dir(oldQuar)), filepath.Dir(filepath.Dir(oldWork))} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("gc should delete the expired P2 leftover %s", filepath.Base(p))
		}
	}
	for _, p := range []string{newReq, newWork} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("gc deleted a recent P2 file %s: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(filepath.Dir(stale)); err == nil {
		t.Error("gc should delete expired session state")
	}
	if b, err := os.ReadFile(recent); err != nil || !strings.Contains(string(b), `"type":"eval_summary"`) ||
		!strings.Contains(string(b), `"cause":"gc"`) || !strings.Contains(string(b), `"r1":{"evals":3`) {
		t.Errorf("gc should flush residual evals as eval_summary(cause=gc): %v %s", err, b)
	}
	if _, err := os.Stat(staleCache); err == nil {
		t.Error("gc should delete unreferenced rules cache files older than 7 days")
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("gc should delete audit files past retention")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("gc deleted the current audit file: %v", err)
	}
	// gc.stamp belongs to the hook (architecture.md §3.1): `ballast gc` must
	// not create it.
	if _, err := os.Stat(filepath.Join(s.sb.BallastHome, "gc.stamp")); err == nil {
		t.Error("`ballast gc` must not write gc.stamp")
	}
	s.mustExit(2, "", "", "gc", "now")
}

// TestSessionStartTouchesGCStamp: the first SessionStart claims the daily GC
// slot (gc.stamp) before spawning the detached `ballast gc`. The stamp is
// asserted only as "exists after the hook"; that the detached gc ran is
// asserted through its observable effect (an expired audit file is deleted),
// never through the stamp's mtime, which would race with the child.
func TestSessionStartTouchesGCStamp(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	stamp := filepath.Join(s.sb.BallastHome, "gc.stamp")
	if err := os.Remove(stamp); err != nil {
		t.Fatal(err)
	}
	expired := filepath.Join(s.sb.BallastHome, "audit", "2000-01.jsonl")
	writeFile(t, expired, "")
	wantJSON(t, "SessionStart", s.hook("claude-code", "SessionStart", map[string]any{"session_id": "g1", "cwd": s.work, "source": "startup"}))
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("SessionStart should touch gc.stamp: %v", err)
	}
	// Wait for the detached gc so the sandbox is not removed under a running
	// child.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(expired); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the detached `ballast gc` did not run (expired audit file still present)")
}

func TestConfigShow(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	r := s.mustExit(0, "", "", "config", "show")
	for _, want := range []string{"generation:", "read-before-edit", "e2e-prompt-warn", "user", "ok"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("config show lacks %q:\n%s", want, r.stdout)
		}
	}
	r = s.mustExit(0, "", "", "config", "show", "--json")
	var doc struct {
		Generation string `json:"generation"`
		Rules      []struct {
			ID string `json:"id"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || doc.Generation == "" || len(doc.Rules) != 10 {
		t.Errorf("config show --json: %v %d rules %s", err, len(doc.Rules), r.stdout)
	}
	r = s.mustExit(0, "", "", "config", "show", "--explain")
	if !strings.Contains(r.stdout, "结局") {
		t.Errorf("--explain should add the fate column:\n%s", r.stdout)
	}

	// A repo layer rule with the same id wins over the user layer.
	repo := s.newRepo("cfgrepo")
	writeFile(t, filepath.Join(repo, ".ballast", "rules.json"), `{"version":1,"rules":[{"id":"e2e-prompt-warn","action":"log","event":"UserPromptSubmit",
	"match":{"text":{"source":"prompt","regex":["PINGPONG"]}},"message":"repo version",
	"examples":[{"name":"h","expect":"hit","input":{"prompt":"PINGPONG"}},{"name":"m","expect":"miss","input":{"prompt":"x"}}]}]}`)
	r = s.mustExit(0, repo, "", "config", "show", "--explain")
	if !strings.Contains(r.stdout, "repo") {
		t.Errorf("repo layer rule missing:\n%s", r.stdout)
	}

	// An invalid layer is reported loudly: content still printed, exit 1.
	writeFile(t, filepath.Join(s.sb.BallastHome, "rules.json"), "{ not json")
	r = s.mustExit(1, "", "", "config", "show")
	if !strings.Contains(r.stdout, "invalid") {
		t.Errorf("invalid layer should be listed:\n%s", r.stdout)
	}
	s.mustExit(2, "", "", "config")
	s.mustExit(2, "", "", "config", "hide")
	s.mustExit(2, "", "", "config", "show", "--bogus")
	// The hook keeps working (fail open) with a broken rules file.
	wantEmpty(t, "hook with an invalid rules file", s.hook("claude-code", "PreToolUse", map[string]any{
		"session_id": "bad", "cwd": s.work, "tool_name": "Bash", "tool_input": map[string]any{"command": "sleep 300"}}))
}

func TestReplay(t *testing.T) {
	s := newSandbox(t)
	rulesPath := installE2ERules(t, s)
	ts := func(sec int) string { return fmt.Sprintf("2026-10-10T09:00:%02dZ", sec) }
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":%q,"sessionId":"rs1","cwd":%q,"message":{"role":"user","content":"wait a bit"}}`, ts(0), s.work),
		fmt.Sprintf(`{"type":"assistant","uuid":"a1","timestamp":%q,"sessionId":"rs1","cwd":%q,"message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"sleep 300"}}]}}`, ts(1), s.work),
		fmt.Sprintf(`{"type":"user","uuid":"u2","timestamp":%q,"sessionId":"rs1","cwd":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`, ts(2), s.work),
		fmt.Sprintf(`{"type":"assistant","uuid":"a2","timestamp":%q,"sessionId":"rs1","cwd":%q,"message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`, ts(3), s.work),
		"this line is not json",
	}
	tr := filepath.Join(s.sb.Root, "transcript.jsonl")
	writeFile(t, tr, strings.Join(lines, "\n")+"\n")

	r := s.mustExit(0, "", "", "replay", tr, "--format", "json", "--rules", rulesPath)
	var rep struct {
		Items        int `json:"items"`
		SkippedLines int `json:"skipped_lines"`
		Rules        []struct {
			RuleID string         `json:"rule_id"`
			Counts map[string]int `json:"counts"`
		} `json:"rules"`
		Samples []struct {
			RuleID string `json:"rule_id"`
			Class  string `json:"class"`
		} `json:"samples"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("replay json: %v\n%s", err, r.stdout)
	}
	if rep.Items == 0 || rep.SkippedLines != 1 {
		t.Errorf("replay saw %d items, skipped %d lines", rep.Items, rep.SkippedLines)
	}
	hit := false
	for _, smp := range rep.Samples {
		if smp.RuleID == "no-long-sleep" && smp.Class == "known-hit" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("replay should find a known-hit of no-long-sleep: %s", r.stdout)
	}
	// Without --rules the rules in effect (user layer) are used; md is the default format.
	r = s.mustExit(0, "", "", "replay", tr, "--subagents")
	if !strings.Contains(r.stdout, "no-long-sleep") {
		t.Errorf("markdown report:\n%s", r.stdout)
	}

	s.mustExit(2, "", "", "replay")
	s.mustExit(2, "", "", "replay", tr, "--format", "xml")
	s.mustExit(2, "", "", "replay", tr, "--client", "hermes")
	s.mustExit(2, "", "", "replay", tr, "--client", "nobody")
	s.mustExit(2, "", "", "replay", tr, "--initial-state", writeTemp(t, s, `{"exists":["a"]}`))
	s.mustExit(5, "", "", "replay", filepath.Join(s.sb.Root, "absent.jsonl"))
}

// replay loads project.json with the same strict loader as the hook's
// evidence provider: a file with an unknown field is project_invalid, not
// approximated from the lenient decoding (audit r1).
func TestReplayProjectJSONIsStrict(t *testing.T) {
	s := newSandbox(t)
	rulesPath := installE2ERules(t, s)
	repo := s.newRepo("replayrepo")
	tr := filepath.Join(s.sb.Root, "commit.jsonl")
	writeFile(t, tr, strings.Join([]string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":"2026-10-10T09:00:00Z","sessionId":"rp1","cwd":%q,"message":{"role":"user","content":"commit it"}}`, repo),
		fmt.Sprintf(`{"type":"assistant","uuid":"a1","timestamp":"2026-10-10T09:00:01Z","sessionId":"rp1","cwd":%q,"message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git commit -m x"}}]}}`, repo),
	}, "\n")+"\n")
	// One trusted record of the session, so the project.json judgement is
	// reached (without evidence the cause is always no_trusted_evidence).
	evFile := filepath.Join(s.sb.Root, "replay-evidence.json")
	rec := model.VerifyRecord{V: 1, JobID: "j1", Name: "test", RepoInst: "ri", Tier: model.TierT0, SStart: "T", SEnd: "T",
		TStart: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), TEnd: time.Date(2026, 10, 10, 9, 0, 0, 500, time.UTC),
		Outcome: model.OutcomePass, SessionID: "rp1"}
	recJSON, err := json.Marshal([]model.VerifyRecord{rec})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, evFile, string(recJSON))
	causeOf := func(project string) string {
		writeFile(t, filepath.Join(repo, ".ballast", "project.json"), project)
		r := s.mustExit(0, "", "", "replay", tr, "--format", "json", "--rules", rulesPath, "--evidence", evFile)
		var rep struct {
			Samples []struct {
				RuleID       string `json:"rule_id"`
				Class        string `json:"class"`
				UnknownCause string `json:"unknown_cause"`
			} `json:"samples"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
			t.Fatalf("replay json: %v\n%s", err, r.stdout)
		}
		for _, smp := range rep.Samples {
			if smp.RuleID == "evidence-before-commit" {
				return smp.Class + "/" + smp.UnknownCause
			}
		}
		return "no sample" // fresh pass: the rule does not fire
	}
	valid := `{"version":1,"verify":[{"name":"test","argv":["go","test","./..."],"required":true}]}`
	invalid := `{"version":1,"bogus_field":1,"verify":[{"name":"test","argv":["go","test","./..."],"required":true}]}`
	if got := causeOf(valid); got != "no sample" {
		t.Errorf("valid project.json: %s, want a fresh pass (no sample)", got)
	}
	if got := causeOf(invalid); !strings.Contains(got, "project_invalid") {
		t.Errorf("project.json with an unknown field: %s, want project_invalid", got)
	}
}

func writeTemp(t *testing.T, s *sandbox, content string) string {
	t.Helper()
	p := filepath.Join(s.sb.Root, fmt.Sprintf("tmp-%d.json", time.Now().UnixNano()))
	writeFile(t, p, content)
	return p
}

// TestPromoteRefusedByInstalledClient: a target action that an installed
// client cannot execute is refused (4) even with --force.
func TestPromoteRefusedByInstalledClient(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	s.mustExit(0, "", "", "install", "--client", "claude-code,codex", "--force", "--binary", ballastBin)
	// Codex cannot ask on PreToolUse, so e2e-shadow cannot be promoted to ask.
	r := s.mustExit(4, "", "", "rules", "promote", "e2e-shadow", "--to", "ask", "--force")
	if !strings.Contains(r.stderr, "ballast:") {
		t.Errorf("stderr: %q", r.stderr)
	}
	// deny is supported by both.
	s.mustExit(0, "", "", "rules", "promote", "e2e-shadow", "--to", "deny", "--force")
}
