package hook

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// TestEvalSummaryRule: the eval_summary record is appended ONLY when the
// commit was Applied and neither Duplicate nor LockTimeout (frozen).
func TestEvalSummaryRule(t *testing.T) {
	counts := map[string]model.EvalCount{"r": {Evals: 3, Hits: 1, Unknown: 1}, "q": {Evals: 1}}
	cases := []struct {
		name    string
		native  string
		payload string
		info    *model.StateCommitInfo
		session *fakeSession
		want    bool
		diags   int
	}{
		{"applied", "Stop", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{Applied: true}, &fakeSession{evals: counts}, true, 0},
		{"lock timeout", "Stop", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{LockTimeout: true, LockWait: 20 * time.Millisecond}, &fakeSession{evals: counts}, false, 1},
		{"lock timeout even if flagged applied", "Stop", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{Applied: true, LockTimeout: true}, &fakeSession{evals: counts}, false, 1},
		{"duplicate", "Stop", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{Applied: true, Duplicate: true}, &fakeSession{evals: counts}, false, 0},
		{"not applied", "Stop", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{}, &fakeSession{evals: counts}, false, 0},
		{"nothing to flush", "Stop", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{Applied: true}, &fakeSession{}, false, 0},
		{"subagent stop", "SubagentStop", `{"session_id":"s","cwd":"/repo","agent_id":"a","agent_type":"x"}`, &model.StateCommitInfo{Applied: true}, &fakeSession{evals: counts}, true, 0},
		{"session end", "SessionEnd", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{Applied: true}, &fakeSession{evals: counts}, true, 0},
		{"session end lock timeout", "SessionEnd", `{"session_id":"s","cwd":"/repo"}`, &model.StateCommitInfo{LockTimeout: true}, &fakeSession{evals: counts}, false, 1},
		{"non stop events never flush", "PostToolUse", preToolBash, &model.StateCommitInfo{Applied: true}, &fakeSession{evals: counts}, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.state.session = c.session
			h.state.commitInfo = c.info
			mustExit0(t, h, h.claude(c.native, c.payload))
			var summaries, diags int
			for _, r := range h.audit.all() {
				switch r.Type {
				case model.AuditEvalSummary:
					summaries++
					if !reflect.DeepEqual(r.Evals, counts) {
						t.Errorf("flushed counts = %+v", r.Evals)
					}
					if r.EventSeq != 0 || r.Event != model.EventName(c.native) {
						t.Errorf("summary record: %+v", r)
					}
				case model.AuditDiag:
					diags++
				}
			}
			if (summaries == 1) != c.want || summaries > 1 {
				t.Errorf("eval_summary records = %d, want present=%v", summaries, c.want)
			}
			if diags != c.diags {
				t.Errorf("diag records = %d, want %d", diags, c.diags)
			}
			d := h.state.lastCommit(t).delta
			wantFlush := c.native == "Stop" || c.native == "SubagentStop" || c.native == "SessionEnd"
			if d.FlushEvals != wantFlush {
				t.Errorf("FlushEvals = %v", d.FlushEvals)
			}
			if wantFlush && len(c.session.evals) > 0 && !reflect.DeepEqual(d.FlushedEvals, counts) {
				t.Errorf("FlushedEvals = %+v", d.FlushedEvals)
			}
		})
	}

	t.Run("the flush snapshot is a copy", func(t *testing.T) {
		h := newHarness(t)
		h.state.session = &fakeSession{evals: map[string]model.EvalCount{"r": {Evals: 1}}}
		mustExit0(t, h, h.claude("Stop", `{"session_id":"s","cwd":"/repo"}`))
		h.state.session.evals["r"] = model.EvalCount{Evals: 99}
		if got := h.state.lastCommit(t).delta.FlushedEvals["r"].Evals; got != 1 {
			t.Errorf("FlushedEvals aliases the state: %d", got)
		}
	})

	t.Run("unreadable state flushes nothing", func(t *testing.T) {
		h := newHarness(t)
		h.state.session = &fakeSession{evals: counts}
		h.state.loadErr = errTest
		mustExit0(t, h, h.claude("Stop", `{"session_id":"s","cwd":"/repo"}`))
		if len(h.state.lastCommit(t).delta.FlushedEvals) != 0 {
			t.Error("counts of an unreadable state must not be flushed")
		}
	})
}

func TestAuditRecordFields(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	h.state.session = &fakeSession{recent: []model.EventSummary{
		{Seq: 1, Text: "old"}, {Seq: 2, Text: "Bash: ls"}, {Seq: 3, Text: "Edit: a.go"},
	}}
	home := h.sb.Home
	in := `{"session_id":"s1","cwd":"/repo","agent_id":"ag","agent_type":"Explore","transcript_path":"` + filepath.ToSlash(filepath.Join(home, ".claude", "p", "t.jsonl")) +
		`","tool_name":"Bash","tool_use_id":"tu9","tool_input":{"command":"git status"}}`
	mustExit0(t, h, h.claude("PreToolUse", in))
	recs := h.audit.all()
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if !r.TS.Equal(testNow) || r.TS.Location() != time.UTC || r.V != model.AuditVersion || r.Type != model.AuditHit {
		t.Errorf("envelope: %+v", r)
	}
	want := model.AuditRecord{
		TS: r.TS, V: 1, Type: model.AuditHit, EventSeq: 1, Client: model.ClientClaude, SessionID: "s1", AgentID: "ag", AgentType: "Explore",
		ToolUseID: "tu9", TranscriptRef: "~/.claude/p/t.jsonl", RepoInst: "0123456789abcdef", RepoRemoteHash: "fedcba9876543210",
		Event: model.EventPreToolUse, NativeEvent: "PreToolUse", Tool: "Bash", RuleID: "r", RuleLayer: model.LayerUser,
		Generation: "gen1", Action: model.ActionDeny, Target: model.ActionDeny, Decision: "deny", Matched: "bash node 0: git status",
		Context: []model.EventSummary{{Seq: 2, Text: "Bash: ls"}, {Seq: 3, Text: "Edit: a.go"}},
		Tier:    model.TierT0, BallastVersion: "9.9.9-test",
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("record =\n%+v\nwant\n%+v", r, want)
	}

	// Matched is clipped; the transcript outside home is kept; no repo ⇒ no remote hash.
	h = newHarness(t)
	v := denyVerdict()
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'm'
	}
	v.Hits[0].Matched = string(long)
	h.engine.verdict = v
	h.repo.err = model.ErrNotRepo
	mustExit0(t, h, h.claude("PreToolUse", `{"session_id":"s1","cwd":"/repo","transcript_path":"/elsewhere/t.jsonl","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	r = h.audit.all()[0]
	if len([]rune(r.Matched)) != model.MaxMatchedRunes || r.TranscriptRef != "/elsewhere/t.jsonl" || r.RepoRemoteHash != "" {
		t.Errorf("record: matched=%d ref=%q remote=%q", len([]rune(r.Matched)), r.TranscriptRef, r.RepoRemoteHash)
	}
	if r.RepoInst != "norepo-"+hash16("/repo") {
		t.Errorf("repo_inst = %q", r.RepoInst)
	}
	// An already "~/"-relative transcript is kept.
	h = newHarness(t)
	h.engine.verdict = denyVerdict()
	mustExit0(t, h, h.claude("PreToolUse", `{"session_id":"s1","cwd":"/repo","transcript_path":"~/.claude/x.jsonl","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	if got := h.audit.all()[0].TranscriptRef; got != "~/.claude/x.jsonl" {
		t.Errorf("ref = %q", got)
	}
}

func TestNoticesAreAuditedWhenSurfaced(t *testing.T) {
	notices := []model.ConfigNotice{
		{Kind: model.NoticeConfigInvalid, Layer: model.LayerRepo, Path: "/repo/.ballast/rules.json", Message: "bad json"},
		{Kind: model.NoticeRuleDropped, Layer: model.LayerUser, RuleID: "r13", Message: "over the cap"},
		{Kind: model.NoticeRuleShadowed, Layer: model.LayerRepo, RuleID: "r1", Message: "shadowed"},
	}
	h := newHarness(t)
	h.rules.rs.Notices = notices
	h.injector.inj = &model.Injection{Text: "x", Kinds: []model.InjectKind{model.InjectFull, model.InjectNotices}, FullEmitted: true}
	mustExit0(t, h, h.claude("SessionStart", `{"session_id":"s","cwd":"/repo"}`))
	types := h.audit.types()
	want := []model.AuditType{model.AuditConfigInvalid, model.AuditRuleDropped, model.AuditRuleShadowed}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
	if r := h.audit.all()[1]; r.RuleID != "r13" || r.RuleLayer != model.LayerUser {
		t.Errorf("record: %+v", r)
	}

	// Not surfaced by the injection ⇒ not re-audited on every call.
	h = newHarness(t)
	h.rules.rs.Notices = notices
	h.injector.inj = &model.Injection{Text: "x", Kinds: []model.InjectKind{model.InjectAnchor}}
	mustExit0(t, h, h.claude("UserPromptSubmit", `{"session_id":"s","cwd":"/repo","prompt":"p"}`))
	if len(h.audit.all()) != 0 {
		t.Errorf("records: %+v", h.audit.all())
	}
}

func TestUnknownCausesAreAudited(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = &model.Verdict{Results: []model.RuleResult{
		{RuleID: "a", Outcome: model.TriFalse},
		{RuleID: "b", Outcome: model.TriUnknown, UnknownCause: "verify_fresh:job_running"},
		{RuleID: "c", Outcome: model.TriUnknown},
	}}
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	recs := h.audit.all()
	if len(recs) != 1 || recs[0].Type != model.AuditDiag ||
		!reflect.DeepEqual(string(recs[0].Detail), `{"cause":"verify_fresh:job_running","diag":"predicate_unknown","rule_id":"b"}`) {
		t.Errorf("records: %+v", recs)
	}
}

func TestGCSpawn(t *testing.T) {
	start := `{"session_id":"s","cwd":"/repo","source":"startup"}`
	stamp := func(h *harness) string { return h.deps.Layout.GCStamp }
	setStamp := func(t *testing.T, h *harness, age time.Duration) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(stamp(h)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stamp(h), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		old := h.clock.Now().Add(-age)
		if err := os.Chtimes(stamp(h), old, old); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("missing stamp spawns and touches it", func(t *testing.T) {
		h := newHarness(t)
		h.clock.Set(time.Now())
		mustExit0(t, h, h.claude("SessionStart", start))
		sp := h.spawner.spawned()
		if len(sp) != 1 || !reflect.DeepEqual(sp[0], []string{"/opt/ballast/bin/ballast", "gc"}) {
			t.Fatalf("spawned = %v", sp)
		}
		if env := h.spawner.envs[0]; len(env) == 0 {
			t.Error("the child must inherit an explicit environment")
		}
		if _, err := os.Stat(stamp(h)); err != nil {
			t.Errorf("stamp not created: %v", err)
		}
		// The next session start (same day) does not spawn again.
		mustExit0(t, h, h.claude("SessionStart", start))
		if len(h.spawner.spawned()) != 1 {
			t.Errorf("spawned twice: %v", h.spawner.spawned())
		}
	})
	t.Run("stale stamp", func(t *testing.T) {
		h := newHarness(t)
		setStamp(t, h, 25*time.Hour)
		mustExit0(t, h, h.claude("SessionStart", start))
		if len(h.spawner.spawned()) != 1 {
			t.Errorf("spawned = %v", h.spawner.spawned())
		}
	})
	t.Run("fresh stamp", func(t *testing.T) {
		h := newHarness(t)
		setStamp(t, h, 23*time.Hour)
		mustExit0(t, h, h.claude("SessionStart", start))
		if len(h.spawner.spawned()) != 0 {
			t.Errorf("spawned = %v", h.spawner.spawned())
		}
	})
	t.Run("only at SessionStart", func(t *testing.T) {
		h := newHarness(t)
		mustExit0(t, h, h.claude("UserPromptSubmit", `{"session_id":"s","cwd":"/repo","prompt":"p"}`))
		mustExit0(t, h, h.claude("SessionEnd", `{"session_id":"s","cwd":"/repo"}`))
		if len(h.spawner.spawned()) != 0 {
			t.Errorf("spawned = %v", h.spawner.spawned())
		}
	})
	t.Run("spawn failure is ignored", func(t *testing.T) {
		h := newHarness(t)
		h.spawner.err = errTest
		mustExit0(t, h, h.claude("SessionStart", start))
	})
	t.Run("no spawner or no self path disables GC", func(t *testing.T) {
		h := newHarness(t)
		h.deps.Spawner = nil
		mustExit0(t, h, h.claude("SessionStart", start))
		h = newHarness(t)
		h.deps.Self = ""
		mustExit0(t, h, h.claude("SessionStart", start))
		if len(h.spawner.spawned()) != 0 {
			t.Errorf("spawned = %v", h.spawner.spawned())
		}
	})
	t.Run("unwritable stamp location does not cause a spawn storm", func(t *testing.T) {
		h := newHarness(t)
		blocker := filepath.Join(h.sb.Root, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.deps.Layout.GCStamp = filepath.Join(blocker, "gc.stamp")
		mustExit0(t, h, h.claude("SessionStart", start))
		if len(h.spawner.spawned()) != 0 {
			t.Errorf("spawned = %v", h.spawner.spawned())
		}
	})
	t.Run("not after a failed pipeline", func(t *testing.T) {
		h := newHarness(t)
		h.state.commitErr = errTest
		h.wantFailure(h.claude("SessionStart", start))
		if len(h.spawner.spawned()) != 0 {
			t.Errorf("spawned = %v", h.spawner.spawned())
		}
	})
}
