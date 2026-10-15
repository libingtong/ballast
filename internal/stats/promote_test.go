package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

const userRules = `{
  "version": 1,
  "rules": [
    {"id": "read-before-edit", "action": "log", "target": "deny", "event": "PreToolUse", "match": {"tool": "Edit"}, "examples": [{"expect": "hit", "input": {}}]},
    {"id": "post-warner", "action": "log", "target": "warn", "event": "PostToolUse", "examples": [{"expect": "hit", "input": {}}]},
    {"id": "stop-checker", "event": "Stop", "clients": ["codex"], "examples": [{"expect": "hit", "input": {}}]}
  ]
}
`

type promoteEnv struct {
	sb       *testenv.Sandbox
	lay      layout.Layout
	repoRoot string
	clock    *sys.FakeClock
	sink     *captureSink
	deps     PromoteDeps
	userFile string
	repoFile string
}

func newPromoteEnv(t *testing.T, recs []model.AuditRecord) *promoteEnv {
	t.Helper()
	sb := testenv.New(t)
	lay := sb.Layout(t)
	e := &promoteEnv{sb: sb, lay: lay, repoRoot: filepath.Join(sb.Root, "repo"), clock: sys.NewFakeClock(ts("2026-10-16T12:00:00Z")), sink: &captureSink{}}
	e.userFile = lay.UserRules
	e.repoFile = layout.RepoRules(e.repoRoot)
	e.write(t, e.userFile, userRules)
	e.deps = PromoteDeps{
		Layout: lay, FS: sys.OSFS{}, Clock: e.clock, IDs: &sys.SeqID{},
		Audit: e.sink, Source: &sliceSource{recs: recs}, Compiler: strictCompiler{},
		RepoRoot: e.repoRoot,
	}
	return e
}

func (e *promoteEnv) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Rule files were last changed well before the fake "now".
	old := e.clock.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func (e *promoteEnv) hits(rule string, n int, within time.Duration) []model.AuditRecord {
	var recs []model.AuditRecord
	for i := 0; i < n; i++ {
		at := e.clock.Now().Add(-within + time.Duration(i)*time.Minute)
		recs = append(recs, hitRec(rule, at, "s1", fmt.Sprintf("tu%d", i), "would_deny"))
	}
	return recs
}

// review writes a review credential for rule at the given time.
func (e *promoteEnv) review(t *testing.T, rule string, at time.Time) {
	t.Helper()
	rv := Review{V: 1, RuleID: rule, ReviewedAt: at, SampleIDs: []string{"x"}, Samples: []SampleHit{{ID: "x", Record: hitRec(rule, at, "s", "t", "would_deny")}}}
	data, _ := json.Marshal(rv)
	e.write(t, ReviewPath(e.lay, rule), string(data))
}

func (e *promoteEnv) readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPromoteRefusesWithoutReview(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.deps.Source = &sliceSource{recs: e.hits("read-before-edit", 10, time.Hour)}
	before := e.readFile(t, e.userFile)
	_, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps)
	if !errors.Is(err, ErrGate) || !strings.Contains(err.Error(), "no review file") {
		t.Fatalf("err = %v", err)
	}
	if e.readFile(t, e.userFile) != before {
		t.Error("rule file modified on refusal")
	}
	if len(e.sink.recs) != 0 {
		t.Errorf("refusal audited as a promotion: %+v", e.sink.recs)
	}
}

func TestPromoteRefusesWithTooFewHits(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.deps.Source = &sliceSource{recs: e.hits("read-before-edit", 4, time.Hour)}
	e.review(t, "read-before-edit", e.clock.Now().Add(-time.Hour))
	_, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps)
	if !errors.Is(err, ErrGate) || !strings.Contains(err.Error(), "4 hits") {
		t.Fatalf("err = %v", err)
	}
	// Custom threshold.
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, MinHits: 4}, e.deps); err != nil {
		t.Fatalf("MinHits=4: %v", err)
	}
}

func TestPromoteWindowAndRuleFilterCountHits(t *testing.T) {
	e := newPromoteEnv(t, nil)
	recs := e.hits("read-before-edit", 3, time.Hour)
	recs = append(recs, e.hits("read-before-edit", 3, 9*24*time.Hour)...) // outside the 7d window
	recs = append(recs, e.hits("post-warner", 10, time.Hour)...)          // other rule
	diag := hitRec("read-before-edit", e.clock.Now().Add(-time.Minute), "s", "d", "none")
	recs = append(recs, diag) // decision none is not a hit
	// A realistic source filters by since; emulate by sorting.
	sortByTS(recs)
	e.deps.Source = &sliceSource{recs: recs}
	e.review(t, "read-before-edit", e.clock.Now().Add(-time.Hour))
	_, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps)
	if !errors.Is(err, ErrGate) || !strings.Contains(err.Error(), "3 hits") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Window: 10 * 24 * time.Hour}, e.deps); err != nil {
		t.Fatalf("wide window: %v", err)
	}
}

func sortByTS(recs []model.AuditRecord) {
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j].TS.Before(recs[j-1].TS); j-- {
			recs[j], recs[j-1] = recs[j-1], recs[j]
		}
	}
}

func TestPromoteSucceedsWithReviewAndHits(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.deps.Source = &sliceSource{recs: e.hits("read-before-edit", 6, time.Hour)}
	// The real flow: Sample writes the credential, then Promote.
	if _, err := Sample(e.deps.Source, e.lay, e.deps.FS, e.deps.IDs, e.clock, "read-before-edit", 5); err != nil {
		t.Fatal(err)
	}
	res, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.File != e.userFile || res.Layer != model.LayerUser || res.From != model.ActionLog || res.To != model.ActionDeny || res.Forced || res.GateHits != 6 || !res.ReviewAt.Equal(e.clock.Now()) {
		t.Errorf("result = %+v", res)
	}
	out := e.readFile(t, e.userFile)
	rf, diags, err := strictCompiler{}.ParseFile(model.LayerUser, e.userFile, []byte(out))
	if err != nil || len(diags) != 0 {
		t.Fatalf("rewritten file: %v %v", err, diags)
	}
	if rf.Rules[0].Action != model.ActionDeny || rf.Rules[0].Target != model.ActionDeny || rf.Rules[1].Action != model.ActionLog {
		t.Errorf("rules after promote: %+v", rf.Rules)
	}
	if !strings.Contains(out, "\n  \"rules\": [\n    {\n      \"id\": \"read-before-edit\",\n      \"action\": \"deny\"") {
		t.Errorf("not 2-space indented with key order kept:\n%s", out)
	}
	if st, _ := os.Stat(e.userFile); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode())
	}

	if len(e.sink.recs) != 1 {
		t.Fatalf("audit records = %+v", e.sink.recs)
	}
	rec := e.sink.recs[0]
	if rec.Type != model.AuditPromote || rec.RuleID != "read-before-edit" || rec.Action != model.ActionDeny || rec.RuleLayer != model.LayerUser || !rec.TS.Equal(e.clock.Now()) {
		t.Errorf("audit record = %+v", rec)
	}
	var d map[string]any
	if err := json.Unmarshal(rec.Detail, &d); err != nil || d["forced"] != false || d["from"] != "log" || d["to"] != "deny" || d["gate_hits"] != float64(6) {
		t.Errorf("audit detail = %s (%v)", rec.Detail, err)
	}

	// The change invalidates the credential: promoting again needs a new review.
	later := e.clock.Now()
	if err := os.Chtimes(e.userFile, later.Add(time.Hour), later.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.deps.Layout.UserRules = e.userFile
	_, err = Promote(PromoteOptions{RuleID: "post-warner", To: model.ActionWarn}, e.deps)
	if !errors.Is(err, ErrGate) {
		t.Errorf("rule file changed after review: %v", err)
	}
}

func TestPromoteReviewMustBeNewerThanRuleChange(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.deps.Source = &sliceSource{recs: e.hits("read-before-edit", 6, time.Hour)}
	e.review(t, "read-before-edit", e.clock.Now().Add(-100*time.Hour)) // older than the 72h-old file
	_, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps)
	if !errors.Is(err, ErrGate) || !strings.Contains(err.Error(), "older than the last change") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromoteInvalidReviewCredentials(t *testing.T) {
	cases := map[string]string{
		"garbage":       `not json`,
		"wrong rule":    `{"v":1,"rule_id":"other-rule","reviewed_at":"2026-10-16T11:00:00Z","samples":[{"id":"x"}]}`,
		"wrong version": `{"v":2,"rule_id":"read-before-edit","reviewed_at":"2026-10-16T11:00:00Z","samples":[{"id":"x"}]}`,
		"no samples":    `{"v":1,"rule_id":"read-before-edit","reviewed_at":"2026-10-16T11:00:00Z","samples":[]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			e := newPromoteEnv(t, nil)
			e.deps.Source = &sliceSource{recs: e.hits("read-before-edit", 6, time.Hour)}
			e.write(t, ReviewPath(e.lay, "read-before-edit"), content)
			if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps); !errors.Is(err, ErrGate) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestPromoteForceIsAuditedAndSkipsGateOnly(t *testing.T) {
	e := newPromoteEnv(t, nil) // no hits, no review
	res, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Forced || res.GateHits != 0 || !res.ReviewAt.IsZero() {
		t.Errorf("result = %+v", res)
	}
	if len(e.sink.recs) != 1 || e.sink.recs[0].Type != model.AuditPromote {
		t.Fatalf("audit = %+v", e.sink.recs)
	}
	var d map[string]any
	_ = json.Unmarshal(e.sink.recs[0].Detail, &d)
	if d["forced"] != true {
		t.Errorf("detail.forced = %v", d["forced"])
	}
	if !strings.Contains(e.readFile(t, e.userFile), `"action": "deny"`) {
		t.Error("file not rewritten")
	}
}

func TestPromoteForceWithoutAuditSinkChangesNothing(t *testing.T) {
	e := newPromoteEnv(t, nil)
	before := e.readFile(t, e.userFile)
	e.sink.err = errors.New("audit disk full")
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps); err == nil {
		t.Fatal("forced promote succeeded although the audit write failed")
	}
	if e.readFile(t, e.userFile) != before {
		t.Error("rule file changed although the forced promotion could not be audited")
	}
	e.deps.Audit = nil
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps); err == nil {
		t.Fatal("nil audit sink accepted for --force")
	}
}

func TestPromoteAuditFailureAfterRewriteReportsResult(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.deps.Source = &sliceSource{recs: e.hits("read-before-edit", 6, time.Hour)}
	e.review(t, "read-before-edit", e.clock.Now().Add(-time.Hour))
	e.sink.err = errors.New("audit disk full")
	res, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps)
	if err == nil || res == nil || !strings.Contains(e.readFile(t, e.userFile), `"action": "deny"`) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestPromoteCapabilityCheckIsNeverSkipped(t *testing.T) {
	e := newPromoteEnv(t, nil)
	// PreToolUse "ask" is not executable on Codex.
	opt := PromoteOptions{RuleID: "read-before-edit", To: model.ActionAsk, Force: true, Clients: []model.ClientID{model.ClientClaude, model.ClientCodex}}
	before := e.readFile(t, e.userFile)
	_, err := Promote(opt, e.deps)
	if !errors.Is(err, ErrCapability) || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v", err)
	}
	if e.readFile(t, e.userFile) != before || len(e.sink.recs) != 0 {
		t.Error("state changed on capability refusal")
	}
	// Claude alone supports ask on PreToolUse.
	opt.Clients = []model.ClientID{model.ClientClaude}
	if _, err := Promote(opt, e.deps); err != nil {
		t.Fatalf("claude only: %v", err)
	}
}

func TestPromoteCapabilityUsesInjectedCheck(t *testing.T) {
	e := newPromoteEnv(t, nil)
	var seen []string
	e.deps.CheckCapabilities = func(r *model.Rule, c model.ClientID) []model.Diagnostic {
		seen = append(seen, fmt.Sprintf("%s/%s/%s", r.ID, r.Action, c))
		if c == model.ClientCursor {
			return []model.Diagnostic{{Severity: model.SevWarning, Message: "unverified"}, {Severity: model.SevError, Code: "action-unsupported", Message: "no deny on cursor here"}}
		}
		return []model.Diagnostic{{Severity: model.SevWarning, Message: "unverified"}}
	}
	opt := PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true, Clients: []model.ClientID{model.ClientClaude, model.ClientCursor}}
	_, err := Promote(opt, e.deps)
	if !errors.Is(err, ErrCapability) || !strings.Contains(err.Error(), "no deny on cursor here") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(seen, ",") != "read-before-edit/deny/claude-code,read-before-edit/deny/cursor" {
		t.Errorf("seen = %v", seen)
	}
	// Warnings alone pass; the check sees the candidate action, not the file's.
	opt.Clients = []model.ClientID{model.ClientClaude}
	if _, err := Promote(opt, e.deps); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteClientsFilterRuleClients(t *testing.T) {
	e := newPromoteEnv(t, nil)
	// stop-checker applies to codex only; promoting to deny on Stop is fine there,
	// and claude is skipped because the rule does not apply to it.
	opt := PromoteOptions{RuleID: "stop-checker", To: model.ActionDeny, Force: true, Clients: []model.ClientID{model.ClientClaude, model.ClientCodex}}
	if _, err := Promote(opt, e.deps); err != nil {
		t.Fatal(err)
	}
	// Absent "action" key was inserted.
	if !strings.Contains(e.readFile(t, e.userFile), "\"id\": \"stop-checker\",\n      \"action\": \"deny\",\n      \"event\": \"Stop\"") {
		t.Errorf("file:\n%s", e.readFile(t, e.userFile))
	}
}

func TestPromoteWarnOnUnsupportedEventRefused(t *testing.T) {
	e := newPromoteEnv(t, nil)
	// PostToolUse cannot deny on any client.
	_, err := Promote(PromoteOptions{RuleID: "post-warner", To: model.ActionDeny, Force: true}, e.deps)
	if !errors.Is(err, ErrCapability) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Promote(PromoteOptions{RuleID: "post-warner", To: model.ActionWarn, Force: true}, e.deps); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteLookup(t *testing.T) {
	e := newPromoteEnv(t, nil)
	force := func(id string) PromoteOptions {
		return PromoteOptions{RuleID: id, To: model.ActionDeny, Force: true}
	}
	if _, err := Promote(force("no-such-rule"), e.deps); !errors.Is(err, ErrRuleNotFound) {
		t.Errorf("missing rule: %v", err)
	}

	// Org rules are refused, even when a lower layer defines the same id.
	e.write(t, e.lay.OrgRules, strings.ReplaceAll(userRules, `"id": "read-before-edit"`, `"id": "org-rule"`))
	e.write(t, e.userFile, strings.ReplaceAll(userRules, `"id": "read-before-edit"`, `"id": "org-rule"`))
	if _, err := Promote(force("org-rule"), e.deps); !errors.Is(err, ErrOrgRule) {
		t.Errorf("org rule: %v", err)
	}
	if errors.Is(ErrOrgRule, ErrRuleNotFound) {
		t.Error("sentinels alias each other")
	}

	// Repo layer wins over the user layer for the same id.
	e.write(t, e.userFile, userRules)
	e.write(t, e.repoFile, strings.ReplaceAll(userRules, `"action": "log", "target": "deny", "event": "PreToolUse"`, `"action": "warn", "target": "deny", "event": "PreToolUse"`))
	res, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionAsk, Force: true}, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != model.LayerRepo || res.File != e.repoFile || res.From != model.ActionWarn {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(e.readFile(t, e.userFile), `"action": "log"`) {
		t.Error("user layer touched")
	}
}

func TestPromoteArgumentErrors(t *testing.T) {
	e := newPromoteEnv(t, nil)
	cases := []struct {
		name string
		opt  PromoteOptions
	}{
		{"bad id", PromoteOptions{RuleID: "../x", To: model.ActionDeny}},
		{"to log", PromoteOptions{RuleID: "read-before-edit", To: model.ActionLog}},
		{"to empty", PromoteOptions{RuleID: "read-before-edit"}},
		{"to bogus", PromoteOptions{RuleID: "read-before-edit", To: "nuke"}},
	}
	for _, tc := range cases {
		if _, err := Promote(tc.opt, e.deps); !errors.Is(err, ErrUsage) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// Not stricter: rule already warn.
	e.write(t, e.userFile, strings.ReplaceAll(userRules, `"action": "log", "target": "deny"`, `"action": "warn", "target": "deny"`))
	for _, to := range []model.Action{model.ActionWarn} {
		if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: to, Force: true}, e.deps); !errors.Is(err, ErrUsage) {
			t.Errorf("not stricter (%s): %v", to, err)
		}
	}
	d := e.deps
	d.Compiler = nil
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, d); err == nil {
		t.Error("nil compiler accepted")
	}
}

func TestPromoteRefusesInvalidRuleFile(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.write(t, e.userFile, `{"version": 2, "rules": [{"id": "read-before-edit", "event": "PreToolUse", "examples": []}]}`)
	_, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps)
	if err == nil || !strings.Contains(err.Error(), "bad-version") {
		t.Errorf("err = %v", err)
	}
	e.write(t, e.userFile, `{broken`)
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps); err == nil || errors.Is(err, ErrRuleNotFound) {
		t.Errorf("undecodable file: %v", err)
	}
}

func TestPromoteWriteFailureLeavesFileIntact(t *testing.T) {
	e := newPromoteEnv(t, nil)
	before := e.readFile(t, e.userFile)
	e.deps.FS = sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"Rename": ""}, Err: errors.New("EXDEV")}
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps); err == nil {
		t.Fatal("expected error")
	}
	if e.readFile(t, e.userFile) != before {
		t.Error("original damaged")
	}
	ents, _ := os.ReadDir(filepath.Dir(e.userFile))
	for _, ent := range ents {
		if strings.Contains(ent.Name(), ".tmp.") {
			t.Errorf("temp file left behind: %s", ent.Name())
		}
	}
}

func TestPromoteSourceErrorBlocksGateButNotForce(t *testing.T) {
	e := newPromoteEnv(t, nil)
	e.deps.Source = &sliceSource{err: errors.New("audit unreadable")}
	e.review(t, "read-before-edit", e.clock.Now().Add(-time.Hour))
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny}, e.deps); err == nil || errors.Is(err, ErrGate) {
		t.Errorf("err = %v", err)
	}
	if _, err := Promote(PromoteOptions{RuleID: "read-before-edit", To: model.ActionDeny, Force: true}, e.deps); err != nil {
		t.Errorf("force: %v", err)
	}
}

func TestPromoteConcurrentDistinctRules(t *testing.T) {
	// Two promotions on different files in parallel must not interfere.
	e := newPromoteEnv(t, nil)
	e.write(t, e.repoFile, strings.ReplaceAll(userRules, `"id": "read-before-edit"`, `"id": "repo-only-rule"`))
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"read-before-edit", "repo-only-rule"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := Promote(PromoteOptions{RuleID: id, To: model.ActionDeny, Force: true}, e.deps)
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if len(e.sink.recs) != 2 {
		t.Errorf("audit records = %d", len(e.sink.recs))
	}
}
