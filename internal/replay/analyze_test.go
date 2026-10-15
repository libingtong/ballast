package replay

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func TestAnalyzeRuleWalksNestedConditions(t *testing.T) {
	one := 1.0
	c := cr("nested-rule", model.EventPreToolUse, func(r *model.Rule) {
		r.When = []model.Cond{
			{All: []model.Cond{
				{Any: []model.Cond{{EnvEquals: &model.EnvEqualsArg{Name: "X", Value: "1"}}}},
				{Not: &model.Cond{Not: &model.Cond{PathExists: &model.PathArg{Path: "a"}}}},
			}},
			{BranchGlob: &model.GlobArg{Patterns: model.StringList{"main"}}},
			{VerifyFailedSinceRead: &model.PathArg{Path: "$target"}},
			{PriorHit: &model.PriorHitArg{Rule: "x", Cmp: model.Cmp{Gte: &one}}},
		}
	})
	m := analyzeRule(&c)
	if !m.usesEnv || !m.usesPathExists || !m.usesBranch || !m.usesVerify || !m.usesState {
		t.Errorf("meta = %+v", m)
	}
	plain := cr("plain-rule", model.EventPreToolUse, nil)
	if m := analyzeRule(&plain); *m != (ruleMeta{id: "plain-rule", event: model.EventPreToolUse}) {
		t.Errorf("plain meta = %+v", m)
	}
}

func TestClassify(t *testing.T) {
	res := func(o model.Tri, cause string) model.RuleResult {
		return model.RuleResult{RuleID: "r", Outcome: o, UnknownCause: cause}
	}
	meta := func(mut func(*ruleMeta)) *ruleMeta {
		m := &ruleMeta{id: "r", event: model.EventPreToolUse}
		if mut != nil {
			mut(m)
		}
		return m
	}
	tests := []struct {
		name      string
		m         *ruleMeta
		r         model.RuleResult
		evidence  bool
		client    model.ClientID
		want      Class
		wantCause string
	}{
		{"plain hit", meta(nil), res(model.TriTrue, ""), false, model.ClientClaude, KnownHit, ""},
		{"plain miss", meta(nil), res(model.TriFalse, ""), false, model.ClientClaude, KnownMiss, ""},
		{"unknown outcome", meta(nil), res(model.TriUnknown, "dynamic_argv"), false, model.ClientClaude, Unknown, "dynamic_argv"},
		{"unknown without cause", meta(nil), res(model.TriUnknown, ""), false, model.ClientClaude, Unknown, "unknown"},
		{"state predicates are reconstructable", meta(func(m *ruleMeta) { m.usesState = true }), res(model.TriTrue, ""), false, model.ClientClaude, KnownHit, ""},
		{"state on cursor is approximate", meta(func(m *ruleMeta) { m.usesState = true }), res(model.TriTrue, ""), false, model.ClientCursor, Approximate, ""},
		{"env", meta(func(m *ruleMeta) { m.usesEnv = true }), res(model.TriFalse, ""), false, model.ClientClaude, Approximate, ""},
		{"branch", meta(func(m *ruleMeta) { m.usesBranch = true }), res(model.TriTrue, ""), false, model.ClientClaude, Approximate, ""},
		{"path_exists", meta(func(m *ruleMeta) { m.usesPathExists = true }), res(model.TriTrue, ""), false, model.ClientClaude, Approximate, ""},
		{"stop family", meta(func(m *ruleMeta) { m.event = model.EventStop }), res(model.TriFalse, ""), false, model.ClientClaude, Approximate, ""},
		{"subagent stop", meta(func(m *ruleMeta) { m.event = model.EventSubagentStop }), res(model.TriTrue, ""), false, model.ClientClaude, Approximate, ""},
		{"stop with unknown outcome stays unknown", meta(func(m *ruleMeta) { m.event = model.EventStop }), res(model.TriUnknown, "x"), false, model.ClientClaude, Unknown, "x"},
		{"verify without evidence even when the outcome is false", meta(func(m *ruleMeta) { m.usesVerify = true }), res(model.TriFalse, ""), false, model.ClientClaude, Unknown, "no_trusted_evidence"},
		{"verify without evidence and hit", meta(func(m *ruleMeta) { m.usesVerify = true }), res(model.TriTrue, ""), false, model.ClientClaude, Unknown, "no_trusted_evidence"},
		{"verify with evidence", meta(func(m *ruleMeta) { m.usesVerify = true }), res(model.TriTrue, ""), true, model.ClientClaude, Approximate, ""},
		{"verify with evidence but unknown outcome", meta(func(m *ruleMeta) { m.usesVerify = true }), res(model.TriUnknown, "snapshot_error"), true, model.ClientClaude, Unknown, "snapshot_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, cause := classify(tc.m, tc.r, tc.evidence, tc.client)
			if got != tc.want || cause != tc.wantCause {
				t.Errorf("classify = %s/%q, want %s/%q", got, cause, tc.want, tc.wantCause)
			}
		})
	}
}

func TestEvidenceViewSemantics(t *testing.T) {
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	rec := func(name string, tend time.Duration, out model.Outcome, s1, s2 model.TreeID) model.VerifyRecord {
		te := base.Add(tend)
		return model.VerifyRecord{Name: name, JobID: name + te.Format("150405"), TStart: te.Add(-time.Second), TEnd: te, Outcome: out, SStart: s1, SEnd: s2}
	}
	all := []model.VerifyRecord{
		rec("test", 10*time.Second, model.OutcomeFail, "A", "A"),
		rec("test", 20*time.Second, model.OutcomePass, "A", "A"),
		rec("lint", 15*time.Second, model.OutcomePass, "A", "B"), // torn
		rec("vet", 90*time.Second, model.OutcomePass, "A", "A"),  // finishes later
	}
	sortRecords(all)

	v := newEvidenceView(all, base.Add(30*time.Second), time.Time{})
	if len(v.records) != 3 {
		t.Fatalf("visible records = %d", len(v.records))
	}
	if r := v.VerifyFresh(model.FreshQuery{Names: []string{"test"}}); r.Value != model.TriTrue || r.Cause != "fresh" {
		t.Errorf("test: %+v", r)
	}
	if r := v.VerifyFresh(model.FreshQuery{Names: []string{"test", "lint"}}); r.Value != model.TriFalse || r.Cause != "not_valid_pass" || len(r.Stale) != 1 || r.Stale[0] != "lint" {
		t.Errorf("test+lint: %+v", r)
	}
	if r := v.VerifyFresh(model.FreshQuery{Names: []string{"vet"}}); r.Value != model.TriFalse || r.Cause != "no_record" {
		t.Errorf("vet (not finished yet): %+v", r)
	}
	// Without names the required list of project.json decides, not the names seen.
	if r := v.VerifyFresh(model.FreshQuery{}); r.Value != model.TriUnknown || r.Cause != causeRequiredAbsent {
		t.Errorf("no resolver: %+v", r)
	}
	req := func(names ...string) func() ([]string, string) {
		return func() ([]string, string) { return names, "" }
	}
	if r := v.withRequired(req("test")).VerifyFresh(model.FreshQuery{}); r.Value != model.TriTrue {
		t.Errorf("required test: %+v", r)
	}
	if r := v.withRequired(req("test", "vet")).VerifyFresh(model.FreshQuery{}); r.Value != model.TriFalse || r.Cause != "no_record" {
		t.Errorf("a required check that never ran must not be fresh: %+v", r)
	}
	if r := v.withRequired(func() ([]string, string) { return nil, causeNoProject }).VerifyFresh(model.FreshQuery{}); r.Value != model.TriUnknown || r.Cause != causeNoProject {
		t.Errorf("no project: %+v", r)
	}
	// A write after the pass makes it stale.
	stale := newEvidenceView(all, base.Add(30*time.Second), base.Add(25*time.Second))
	if r := stale.VerifyFresh(model.FreshQuery{Names: []string{"test"}}); r.Value != model.TriFalse || r.Cause != "stale" {
		t.Errorf("stale: %+v", r)
	}
	// Latest record decides, regardless of earlier passes.
	last := []model.VerifyRecord{rec("test", 10*time.Second, model.OutcomePass, "A", "A"), rec("test", 20*time.Second, model.OutcomeFail, "A", "A")}
	sortRecords(last)
	if r := newEvidenceView(last, base.Add(time.Minute), time.Time{}).VerifyFresh(model.FreshQuery{Names: []string{"test"}}); r.Value != model.TriFalse || r.Cause != "not_valid_pass" {
		t.Errorf("latest fail: %+v", r)
	}

	if tri, at := v.LastFailureAfter(base); tri != model.TriTrue || !at.Equal(base.Add(10*time.Second)) {
		t.Errorf("LastFailureAfter = %v %v", tri, at)
	}
	if tri, _ := v.LastFailureAfter(base.Add(10 * time.Second)); tri != model.TriFalse {
		t.Errorf("failure not after t: %v", tri)
	}
	if tri, _ := newEvidenceView(nil, base, time.Time{}).LastFailureAfter(base); tri != model.TriUnknown {
		t.Errorf("no records: %v", tri)
	}
	if got := v.FinishedSince(base.Add(12 * time.Second)); len(got) != 2 {
		t.Errorf("FinishedSince = %d records", len(got))
	}
	if r := newEvidenceView(nil, base, time.Time{}).VerifyFresh(model.FreshQuery{Names: []string{"test"}}); r.Value != model.TriFalse || r.Cause != "no_record" {
		t.Errorf("empty view: %+v", r)
	}
}

func TestTailRunes(t *testing.T) {
	long := strings.Repeat("字", 300)
	if got := tailRunes(long, 200); len([]rune(got)) != 200 {
		t.Errorf("tail = %d runes", len([]rune(got)))
	}
	if tailRunes("short", 200) != "short" {
		t.Error("short strings must be kept whole")
	}
}

func TestMemStateReadSetIsBounded(t *testing.T) {
	s := newMemState("g")
	for i := 0; i < maxReadPaths+500; i++ {
		s.addRead(strings.Repeat("p", 1)+string(rune('a'+i%26))+"/"+itoa(i), time.Unix(int64(i), 0))
	}
	if len(s.read) != maxReadPaths {
		t.Errorf("read set = %d, want %d", len(s.read), maxReadPaths)
	}
	if s.HasRead("pa/0") {
		t.Error("oldest path survived")
	}
	last := "p" + string(rune('a'+(maxReadPaths+499)%26)) + "/" + itoa(maxReadPaths+499)
	if !s.HasRead(last) {
		t.Error("newest path evicted")
	}
	if at, ok := s.LastReadAt(last); !ok || at.Unix() != int64(maxReadPaths+499) {
		t.Errorf("LastReadAt = %v %v", at, ok)
	}
	s.addRead("", time.Now())
	if s.HasRead("") {
		t.Error("empty path registered")
	}
	// Ring and counters.
	for i := 0; i < 20; i++ {
		s.touch(time.Unix(int64(i), 0), model.EventPreToolUse, "Bash")
	}
	if rec := s.Recent(100); len(rec) != maxRing || rec[len(rec)-1].Seq != 20 {
		t.Errorf("ring = %+v", rec)
	}
	if got := s.Recent(2); len(got) != 2 || got[1].Seq != 20 {
		t.Errorf("Recent(2) = %+v", got)
	}
	if s.Recent(0) != nil {
		t.Error("Recent(0)")
	}
	if at, ok := s.StartedAt(); !ok || at.Unix() != 0 {
		t.Errorf("StartedAt = %v %v", at, ok)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
