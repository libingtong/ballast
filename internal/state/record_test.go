package state

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func TestRecordImplementsSessionState(t *testing.T) {
	var _ model.SessionState = NewRecord()
	r := NewRecord()
	if _, ok := r.StartedAt(); ok {
		t.Error("empty record has a start time")
	}
	if _, ok := r.LastToolFailureAt(); ok {
		t.Error("empty record has a tool failure time")
	}
	if r.Seen("") || r.Seen("x") || r.HasRead("/a") || r.ReportedJob("j") || r.PriorHits("r") != 0 || r.EventSeq() != 0 {
		t.Error("empty record is not empty")
	}
	if got := r.Recent(3); got != nil {
		t.Errorf("Recent on empty = %v", got)
	}
}

func TestMergeTable(t *testing.T) {
	type check func(t *testing.T, r *Record)
	tests := []struct {
		name  string
		delta model.StateDelta
		pre   func(r *Record)
		check check
	}{
		{
			name:  "first event sets start and seq",
			delta: model.StateDelta{Now: at(1), DedupeKey: "PreToolUse:a", ToolCall: true},
			check: func(t *testing.T, r *Record) {
				if s, ok := r.StartedAt(); !ok || !s.Equal(at(1)) {
					t.Errorf("StartedAt = %v %v", s, ok)
				}
				if r.EventSeq() != 1 || r.ToolCount() != 1 || r.ToolCountSinceInject() != 1 {
					t.Errorf("seq=%d tools=%d since=%d", r.EventSeq(), r.ToolCount(), r.ToolCountSinceInject())
				}
				if !r.Seen("PreToolUse:a") {
					t.Error("dedupe key not recorded")
				}
			},
		},
		{
			name:  "start time is not overwritten",
			pre:   func(r *Record) { r.StartedAtT = at(-5) },
			delta: model.StateDelta{Now: at(9)},
			check: func(t *testing.T, r *Record) {
				if s, _ := r.StartedAt(); !s.Equal(at(-5)) {
					t.Errorf("StartedAt = %v", s)
				}
			},
		},
		{
			name:  "anchor emitted resets since-inject only",
			pre:   func(r *Record) { r.Tools, r.ToolsSinceInject = 30, 25 },
			delta: model.StateDelta{Now: at(1), AnchorEmitted: true},
			check: func(t *testing.T, r *Record) {
				if r.ToolCount() != 30 || r.ToolCountSinceInject() != 0 {
					t.Errorf("tools=%d since=%d", r.ToolCount(), r.ToolCountSinceInject())
				}
			},
		},
		{
			name:  "tool call and anchor in one delta end at zero",
			pre:   func(r *Record) { r.Tools, r.ToolsSinceInject = 30, 25 },
			delta: model.StateDelta{Now: at(1), ToolCall: true, AnchorEmitted: true},
			check: func(t *testing.T, r *Record) {
				if r.ToolCount() != 31 || r.ToolCountSinceInject() != 0 {
					t.Errorf("tools=%d since=%d", r.ToolCount(), r.ToolCountSinceInject())
				}
			},
		},
		{
			name:  "prompt counter and tail",
			delta: model.StateDelta{Now: at(1), Prompt: true, PromptTail: "hello"},
			check: func(t *testing.T, r *Record) {
				if r.PromptCount() != 1 || r.PrevPromptTail() != "hello" {
					t.Errorf("prompts=%d tail=%q", r.PromptCount(), r.PrevPromptTail())
				}
			},
		},
		{
			name:  "empty prompt tail keeps previous",
			pre:   func(r *Record) { r.PromptTail = "old" },
			delta: model.StateDelta{Now: at(1), Prompt: true},
			check: func(t *testing.T, r *Record) {
				if r.PrevPromptTail() != "old" {
					t.Errorf("tail=%q", r.PrevPromptTail())
				}
			},
		},
		{
			name:  "prompt tail keeps the last 200 runes",
			delta: model.StateDelta{Now: at(1), PromptTail: strings.Repeat("a", 50) + strings.Repeat("中", 200)},
			check: func(t *testing.T, r *Record) {
				if got := r.PrevPromptTail(); got != strings.Repeat("中", 200) {
					t.Errorf("tail has %d runes: %q", len([]rune(got)), got)
				}
			},
		},
		{
			name:  "assistant text capped at 4000 runes",
			delta: model.StateDelta{Now: at(1), LastAssistantText: strings.Repeat("é", 5000)},
			check: func(t *testing.T, r *Record) {
				if n := len([]rune(r.LastAssistantText())); n != MaxAssistantRunes {
					t.Errorf("runes = %d", n)
				}
			},
		},
		{
			name:  "read paths record time and update to the newest",
			pre:   func(r *Record) { r.ReadAt = map[string]time.Time{"/a": at(-10)} },
			delta: model.StateDelta{Now: at(3), ReadPaths: []string{"/a", "/b", ""}},
			check: func(t *testing.T, r *Record) {
				if ts, ok := r.LastReadAt("/a"); !ok || !ts.Equal(at(3)) {
					t.Errorf("/a = %v %v", ts, ok)
				}
				if !r.HasRead("/b") || r.HasRead("") || len(r.ReadAt) != 2 {
					t.Errorf("read = %v", r.ReadAt)
				}
			},
		},
		{
			name:  "an older delta never rewinds a read time",
			pre:   func(r *Record) { r.ReadAt = map[string]time.Time{"/a": at(20)} },
			delta: model.StateDelta{Now: at(3), ReadPaths: []string{"/a"}},
			check: func(t *testing.T, r *Record) {
				if ts, _ := r.LastReadAt("/a"); !ts.Equal(at(20)) {
					t.Errorf("/a = %v", ts)
				}
			},
		},
		{
			name:  "hit rules count per occurrence",
			delta: model.StateDelta{Now: at(1), HitRules: []string{"r1", "r2", "r1"}},
			check: func(t *testing.T, r *Record) {
				if r.PriorHits("r1") != 2 || r.PriorHits("r2") != 1 || r.PriorHits("r3") != 0 {
					t.Errorf("hits = %v", r.Hits)
				}
			},
		},
		{
			name: "evals accumulate",
			pre:  func(r *Record) { r.EvalCounts = map[string]model.EvalCount{"r": {Evals: 2, Hits: 1}} },
			delta: model.StateDelta{Now: at(1), Evals: map[string]model.EvalCount{
				"r": {Evals: 1, Would: 1}, "s": {Evals: 1, Unknown: 1},
			}},
			check: func(t *testing.T, r *Record) {
				want := map[string]model.EvalCount{"r": {Evals: 3, Hits: 1, Would: 1}, "s": {Evals: 1, Unknown: 1}}
				if !reflect.DeepEqual(r.Evals(), want) {
					t.Errorf("evals = %v", r.Evals())
				}
			},
		},
		{
			name: "flush subtracts and keeps concurrent increments",
			pre:  func(r *Record) { r.EvalCounts = map[string]model.EvalCount{"r": {Evals: 5, Hits: 2}, "s": {Evals: 1}} },
			delta: model.StateDelta{Now: at(1), FlushEvals: true,
				FlushedEvals: map[string]model.EvalCount{"r": {Evals: 3, Hits: 2}, "s": {Evals: 1}}},
			check: func(t *testing.T, r *Record) {
				want := map[string]model.EvalCount{"r": {Evals: 2}}
				if !reflect.DeepEqual(r.Evals(), want) {
					t.Errorf("evals = %v", r.Evals())
				}
			},
		},
		{
			name: "flush floors at zero",
			pre:  func(r *Record) { r.EvalCounts = map[string]model.EvalCount{"r": {Evals: 1, Hits: 1}} },
			delta: model.StateDelta{Now: at(1), FlushEvals: true,
				FlushedEvals: map[string]model.EvalCount{"r": {Evals: 4, Hits: 0, Would: 3}, "ghost": {Evals: 9}}},
			check: func(t *testing.T, r *Record) {
				want := map[string]model.EvalCount{"r": {Hits: 1}}
				if !reflect.DeepEqual(r.Evals(), want) {
					t.Errorf("evals = %v", r.Evals())
				}
			},
		},
		{
			name: "flush without FlushEvals does nothing",
			pre:  func(r *Record) { r.EvalCounts = map[string]model.EvalCount{"r": {Evals: 5}} },
			delta: model.StateDelta{Now: at(1),
				FlushedEvals: map[string]model.EvalCount{"r": {Evals: 5}}},
			check: func(t *testing.T, r *Record) {
				if r.Evals()["r"].Evals != 5 {
					t.Errorf("evals = %v", r.Evals())
				}
			},
		},
		{
			name:  "generation, digest and full flag",
			delta: model.StateDelta{Now: at(1), Generation: "g1", RuleDigest: map[string]model.Action{"a": model.ActionDeny}, FullEmitted: true},
			check: func(t *testing.T, r *Record) {
				if r.Generation() != "g1" || r.RuleDigest()["a"] != model.ActionDeny || !r.FullInjected() {
					t.Errorf("gen=%q digest=%v full=%v", r.Generation(), r.RuleDigest(), r.FullInjected())
				}
			},
		},
		{
			name:  "reported jobs are a set with a report time",
			pre:   func(r *Record) { r.Reported = []string{"j1"} },
			delta: model.StateDelta{Now: at(1), ReportedJobs: []string{"j1", "j2", ""}, JobReportAt: at(1)},
			check: func(t *testing.T, r *Record) {
				if !reflect.DeepEqual(r.Reported, []string{"j1", "j2"}) || !r.LastJobReportAt().Equal(at(1)) {
					t.Errorf("reported=%v at=%v", r.Reported, r.LastJobReportAt())
				}
			},
		},
		{
			name:  "tool failure time",
			delta: model.StateDelta{Now: at(7), ToolFailure: true},
			check: func(t *testing.T, r *Record) {
				if ts, ok := r.LastToolFailureAt(); !ok || !ts.Equal(at(7)) {
					t.Errorf("failure = %v %v", ts, ok)
				}
			},
		},
		{
			name: "summary gets the store seq, clipped text and a timestamp",
			pre:  func(r *Record) { r.Seq = 41 },
			delta: model.StateDelta{Now: at(5), Summary: &model.EventSummary{
				Seq: 999, Event: model.EventName("PreToolUse"), Tool: "Bash", Text: strings.Repeat("x", 300), Hits: []string{"r"},
			}},
			check: func(t *testing.T, r *Record) {
				got := r.Recent(5)
				if len(got) != 1 {
					t.Fatalf("ring = %v", got)
				}
				if got[0].Seq != 42 || !got[0].TS.Equal(at(5)) || len(got[0].Text) != model.MaxEventSummaryRunes || got[0].Tool != "Bash" {
					t.Errorf("summary = %+v", got[0])
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRecord()
			if tc.pre != nil {
				tc.pre(r)
			}
			d := tc.delta
			applied, dup := r.Merge(&d)
			if !applied || dup {
				t.Fatalf("applied=%v dup=%v", applied, dup)
			}
			tc.check(t, r)
		})
	}
}

func TestMergeNilDelta(t *testing.T) {
	r := NewRecord()
	if a, d := r.Merge(nil); a || d || r.Seq != 0 {
		t.Errorf("nil delta: applied=%v dup=%v seq=%d", a, d, r.Seq)
	}
}

func TestMergeDuplicateAppliesIdempotentFieldsOnly(t *testing.T) {
	r := NewRecord()
	first := model.StateDelta{Now: at(1), DedupeKey: "PostToolUse:t1", ToolCall: true, HitRules: []string{"r"}, ReadPaths: []string{"/a"}}
	if a, d := r.Merge(&first); !a || d {
		t.Fatalf("first: %v %v", a, d)
	}
	seq := r.Seq

	dup := model.StateDelta{
		Now: at(2), DedupeKey: "PostToolUse:t1",
		ToolCall: true, Prompt: true, PromptTail: "x", HitRules: []string{"r"}, ReadPaths: []string{"/b"},
		Evals: map[string]model.EvalCount{"r": {Evals: 1}}, AnchorEmitted: true, FullEmitted: true,
		Summary: &model.EventSummary{Text: "s"}, ToolFailure: true, LastAssistantText: "t", JobReportAt: at(2),
		Generation: "g2", RuleDigest: map[string]model.Action{"a": model.ActionWarn}, ReportedJobs: []string{"j"},
	}
	applied, duplicate := r.Merge(&dup)
	if !applied || !duplicate {
		t.Fatalf("dup merge: applied=%v duplicate=%v (idempotent fields changed)", applied, duplicate)
	}
	if r.Seq != seq || r.Tools != 1 || r.Prompts != 0 || r.PromptTail != "" || r.Hits["r"] != 1 || r.HasRead("/b") ||
		len(r.EvalCounts) != 0 || r.FullDone || len(r.Ring) != 0 || !r.ToolFailureAt.IsZero() || r.AssistantText != "" || !r.JobReportAt.IsZero() {
		t.Errorf("non-idempotent fields were applied: %+v", r)
	}
	if r.Gen != "g2" || r.Digest["a"] != model.ActionWarn || !r.ReportedJob("j") {
		t.Errorf("idempotent fields missing: %+v", r)
	}

	// Replaying the same duplicate changes nothing: applied=false.
	if applied, duplicate := r.Merge(&dup); applied || !duplicate {
		t.Errorf("second replay: applied=%v duplicate=%v", applied, duplicate)
	}
}

func TestMergeEmptyKeyIsNeverDuplicate(t *testing.T) {
	r := NewRecord()
	for i := 0; i < 3; i++ {
		d := model.StateDelta{Now: at(i), ToolCall: true}
		if a, dup := r.Merge(&d); !a || dup {
			t.Fatalf("merge %d: %v %v", i, a, dup)
		}
	}
	if r.Tools != 3 || r.Seq != 3 || len(r.SeenKeys) != 0 {
		t.Errorf("tools=%d seq=%d seen=%v", r.Tools, r.Seq, r.SeenKeys)
	}
}

func TestSeenKeysFIFO(t *testing.T) {
	r := NewRecord()
	total := SeenCap + 40
	for i := 0; i < total; i++ {
		d := model.StateDelta{Now: at(i), DedupeKey: fmt.Sprintf("PreToolUse:%d", i)}
		r.Merge(&d)
	}
	if len(r.SeenKeys) != SeenCap {
		t.Fatalf("seen has %d keys", len(r.SeenKeys))
	}
	if r.Seen("PreToolUse:0") || r.Seen("PreToolUse:39") {
		t.Error("oldest keys were not evicted")
	}
	if !r.Seen("PreToolUse:40") || !r.Seen(fmt.Sprintf("PreToolUse:%d", total-1)) {
		t.Error("newest keys were evicted")
	}
	// An evicted key is applied again (documented FIFO behavior).
	d := model.StateDelta{Now: at(9999), DedupeKey: "PreToolUse:0", ToolCall: true}
	if a, dup := r.Merge(&d); !a || dup {
		t.Errorf("evicted key: applied=%v dup=%v", a, dup)
	}
}

func TestRingKeepsLastEight(t *testing.T) {
	r := NewRecord()
	for i := 1; i <= 20; i++ {
		d := model.StateDelta{Now: at(i), Summary: &model.EventSummary{Text: fmt.Sprint("e", i)}}
		r.Merge(&d)
	}
	if len(r.Ring) != RingCap {
		t.Fatalf("ring has %d entries", len(r.Ring))
	}
	if r.Ring[0].Seq != 13 || r.Ring[RingCap-1].Seq != 20 || r.Ring[0].Text != "e13" {
		t.Errorf("ring = %+v", r.Ring)
	}
	rec := r.Recent(2)
	if len(rec) != 2 || rec[0].Seq != 19 || rec[1].Seq != 20 {
		t.Errorf("Recent(2) = %+v", rec)
	}
	if got := r.Recent(100); len(got) != RingCap {
		t.Errorf("Recent(100) has %d entries", len(got))
	}
	if r.Recent(0) != nil || r.Recent(-1) != nil {
		t.Error("Recent(<=0) must be nil")
	}
}

func TestSummaryIsCopied(t *testing.T) {
	r := NewRecord()
	hits := []string{"a"}
	d := model.StateDelta{Now: at(1), Summary: &model.EventSummary{Hits: hits}}
	r.Merge(&d)
	hits[0] = "mutated"
	if r.Ring[0].Hits[0] != "a" {
		t.Error("ring entry aliases the caller's slice")
	}
	if d.Summary.Seq != 0 {
		t.Error("Merge modified the caller's summary")
	}
}

func TestReadCapEvictsOldest(t *testing.T) {
	r := NewRecord()
	for i := 0; i < ReadCap; i++ {
		d := model.StateDelta{Now: at(i), ReadPaths: []string{fmt.Sprintf("/f/%05d", i)}}
		r.Merge(&d)
	}
	if len(r.ReadAt) != ReadCap {
		t.Fatalf("read has %d entries", len(r.ReadAt))
	}
	// Re-reading an old path refreshes it, so it survives the eviction below.
	d := model.StateDelta{Now: at(ReadCap), ReadPaths: []string{"/f/00000", "/f/new1", "/f/new2"}}
	r.Merge(&d)
	if len(r.ReadAt) != ReadCap {
		t.Fatalf("read has %d entries after overflow", len(r.ReadAt))
	}
	if !r.HasRead("/f/00000") || !r.HasRead("/f/new1") || !r.HasRead("/f/new2") {
		t.Error("fresh paths were evicted")
	}
	if r.HasRead("/f/00001") || r.HasRead("/f/00002") {
		t.Error("oldest paths were not evicted")
	}
	if !r.HasRead("/f/00003") {
		t.Error("evicted more than necessary")
	}
}

func TestReadEvictionTieBreaksByPath(t *testing.T) {
	r := NewRecord()
	paths := make([]string, ReadCap+3)
	for i := range paths {
		paths[i] = fmt.Sprintf("/same/%05d", i)
	}
	d := model.StateDelta{Now: at(1), ReadPaths: paths}
	r.Merge(&d)
	if len(r.ReadAt) != ReadCap {
		t.Fatalf("len = %d", len(r.ReadAt))
	}
	for i := 0; i < 3; i++ {
		if r.HasRead(paths[i]) {
			t.Errorf("%s survived", paths[i])
		}
	}
}

func TestReportedJobsCap(t *testing.T) {
	r := NewRecord()
	var jobs []string
	for i := 0; i < ReportedJobsCap+10; i++ {
		jobs = append(jobs, fmt.Sprint("job", i))
	}
	d := model.StateDelta{Now: at(1), ReportedJobs: jobs}
	r.Merge(&d)
	if len(r.Reported) != ReportedJobsCap {
		t.Fatalf("len = %d", len(r.Reported))
	}
	if r.ReportedJob("job9") || !r.ReportedJob("job10") || !r.ReportedJob(jobs[len(jobs)-1]) {
		t.Errorf("reported = %v", r.Reported)
	}
}

func TestRuleDigestIsCopied(t *testing.T) {
	r := NewRecord()
	dig := map[string]model.Action{"a": model.ActionDeny}
	d := model.StateDelta{Now: at(1), RuleDigest: dig}
	r.Merge(&d)
	dig["a"] = model.ActionWarn
	if r.Digest["a"] != model.ActionDeny {
		t.Error("digest aliases the caller's map")
	}
}

// TestMergeCommutesDistinctKeys checks what the lock-free design relies on:
// with distinct dedupe keys every order of the same deltas gives identical
// counters, sets and totals.
func TestMergeCommutesDistinctKeys(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var deltas []model.StateDelta
	for i := 0; i < 60; i++ {
		deltas = append(deltas, model.StateDelta{
			Now:       at(i),
			DedupeKey: fmt.Sprintf("PostToolUse:%d", i),
			ToolCall:  i%2 == 0,
			Prompt:    i%5 == 0,
			ReadPaths: []string{fmt.Sprintf("/p/%d", i%9)},
			HitRules:  []string{fmt.Sprintf("r%d", i%4)},
			Evals:     map[string]model.EvalCount{fmt.Sprintf("r%d", i%3): {Evals: 1, Hits: i % 2, Would: i % 3, Unknown: i % 4}},
		})
	}
	type sig struct {
		Seq, Tools, Since, Prompts int64
		Hits                       map[string]int
		Reads                      map[string]bool
		Evals                      map[string]model.EvalCount
		Seen                       map[string]bool
	}
	get := func(r *Record) sig {
		s := sig{Seq: r.Seq, Tools: int64(r.Tools), Since: int64(r.ToolsSinceInject), Prompts: int64(r.Prompts),
			Hits: r.Hits, Evals: r.EvalCounts, Reads: map[string]bool{}, Seen: map[string]bool{}}
		for p := range r.ReadAt {
			s.Reads[p] = true
		}
		for _, k := range r.SeenKeys {
			s.Seen[k] = true
		}
		return s
	}
	base := NewRecord()
	for i := range deltas {
		base.Merge(&deltas[i])
	}
	want := get(base)
	for trial := 0; trial < 50; trial++ {
		r := NewRecord()
		for _, i := range rng.Perm(len(deltas)) {
			r.Merge(&deltas[i])
		}
		if got := get(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d diverged:\n got %+v\nwant %+v", trial, got, want)
		}
	}
}

func TestRecordGolden(t *testing.T) {
	r := NewRecord()
	steps := []model.StateDelta{
		{Now: at(0), DedupeKey: "SessionStart:", FullEmitted: true, Generation: "gen-1", RuleDigest: map[string]model.Action{"read-before-edit": model.ActionDeny, "no-long-sleep": model.ActionWarn}},
		{Now: at(1), Prompt: true, PromptTail: "please fix the bug"},
		{Now: at(2), DedupeKey: "PreToolUse:toolu_1", ToolCall: true, HitRules: []string{"read-before-edit"},
			Evals:   map[string]model.EvalCount{"read-before-edit": {Evals: 1, Hits: 1}},
			Summary: &model.EventSummary{Event: "PreToolUse", Tool: "Edit", Text: "Edit: src/a.go", Hits: []string{"read-before-edit"}}},
		{Now: at(3), DedupeKey: "PostToolUse:toolu_1", ReadPaths: []string{"/repo/src/a.go"},
			Summary: &model.EventSummary{Event: "PostToolUse", Tool: "Read", Text: "Read: src/a.go"}},
		{Now: at(4), DedupeKey: "PostToolUse:toolu_2", ToolFailure: true, ReportedJobs: []string{"job-7"}, JobReportAt: at(4)},
		{Now: at(5), LastAssistantText: "done", AnchorEmitted: true},
	}
	for i := range steps {
		r.Merge(&steps[i])
	}
	got, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "record_v1.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("record JSON differs from %s (run with -update):\n%s", golden, got)
	}
	// And the golden file decodes back to an identical record.
	back, ok := decodeRecord(want)
	if !ok {
		t.Fatal("golden file does not decode")
	}
	if !reflect.DeepEqual(back, r) {
		t.Errorf("round trip differs:\n got %+v\nwant %+v", back, r)
	}
}

func TestDecodeRecord(t *testing.T) {
	cases := []struct {
		file string
		ok   bool
	}{
		{"valid_minimal.json", true},
		{"valid_unknown_field.json", true},
		{"corrupt_truncated.json", false},
		{"corrupt_empty.json", false},
		{"corrupt_garbage.json", false},
		{"corrupt_null.json", false},
		{"corrupt_array.json", false},
		{"corrupt_wrong_version.json", false},
		{"corrupt_no_version.json", false},
		{"corrupt_trailing_data.json", false},
		{"corrupt_bad_field_type.json", false},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "decode", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			rec, ok := decodeRecord(data)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && rec.V != RecordVersion {
				t.Errorf("v = %d", rec.V)
			}
		})
	}
}

func TestDecodeEnforcesCaps(t *testing.T) {
	r := NewRecord()
	for i := 0; i < SeenCap+5; i++ {
		r.SeenKeys = append(r.SeenKeys, fmt.Sprint("k", i))
	}
	for i := 0; i < RingCap+5; i++ {
		r.Ring = append(r.Ring, model.EventSummary{Seq: int64(i)})
	}
	for i := 0; i < ReportedJobsCap+5; i++ {
		r.Reported = append(r.Reported, fmt.Sprint("j", i))
	}
	r.ReadAt = map[string]time.Time{}
	for i := 0; i < ReadCap+5; i++ {
		r.ReadAt[fmt.Sprint("/p", i)] = at(i)
	}
	r.PromptTail = strings.Repeat("p", 500)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decodeRecord(data)
	if !ok {
		t.Fatal("decode failed")
	}
	if len(got.SeenKeys) != SeenCap || len(got.Ring) != RingCap || len(got.Reported) != ReportedJobsCap || len(got.ReadAt) != ReadCap || len(got.PromptTail) != MaxPromptTailRune {
		t.Errorf("caps not enforced: seen=%d ring=%d jobs=%d read=%d tail=%d",
			len(got.SeenKeys), len(got.Ring), len(got.Reported), len(got.ReadAt), len(got.PromptTail))
	}
	if got.Ring[0].Seq != 5 || got.SeenKeys[0] != "k5" {
		t.Error("caps must keep the newest entries")
	}
}

func FuzzDecodeRecord(f *testing.F) {
	entries, _ := os.ReadDir(filepath.Join("testdata", "decode"))
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join("testdata", "decode", e.Name())); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`{"v":1,"seen":["a"],"read":{"/x":"2026-10-16T08:00:00Z"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, ok := decodeRecord(data)
		if !ok {
			return
		}
		if rec.V != RecordVersion || len(rec.SeenKeys) > SeenCap || len(rec.Ring) > RingCap || len(rec.ReadAt) > ReadCap {
			t.Fatalf("decode violated invariants: %+v", rec)
		}
		// A decoded record must survive a merge and re-encode.
		d := model.StateDelta{Now: t0, DedupeKey: "k", ToolCall: true, ReadPaths: []string{"/z"}, Summary: &model.EventSummary{Text: "x"}}
		rec.Merge(&d)
		if _, err := rec.encode(); err != nil {
			t.Fatalf("encode: %v", err)
		}
	})
}
