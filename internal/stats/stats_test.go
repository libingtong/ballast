package stats

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

type goldenFile struct {
	Corrupt int `json:"corrupt"`
	Rows    []struct {
		Rule     string       `json:"rule"`
		Action   model.Action `json:"action"`
		Target   model.Action `json:"target"`
		Evals    int          `json:"evals"`
		Hits     int          `json:"hits"`
		Would    int          `json:"would"`
		Unknown  int          `json:"unknown"`
		Sessions int          `json:"sessions"`
	} `json:"rows"`
}

func TestComputeGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/expect.json")
	if err != nil {
		t.Fatal(err)
	}
	var want goldenFile
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	src := fileSource(t, "testdata/audit-sample.jsonl")
	rows, corrupt, err := ComputeDetailed(src, Query{Since: ts("2026-10-01T00:00:00Z"), By: ByRule})
	if err != nil {
		t.Fatal(err)
	}
	if corrupt != want.Corrupt {
		t.Errorf("corrupt = %d, want %d", corrupt, want.Corrupt)
	}
	if len(rows) != len(want.Rows) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want.Rows), rows)
	}
	for i, w := range want.Rows {
		g := rows[i]
		got := Row{Key: w.Rule, Rule: g.Rule, Action: g.Action, Target: g.Target, Evals: g.Evals, Hits: g.Hits, Would: g.Would, Unknown: g.Unknown, Sessions: g.Sessions}
		exp := Row{Key: w.Rule, Rule: w.Rule, Action: w.Action, Target: w.Target, Evals: w.Evals, Hits: w.Hits, Would: w.Would, Unknown: w.Unknown, Sessions: w.Sessions}
		if got != exp || g.Key != w.Rule {
			t.Errorf("row %d = %+v, want %+v", i, g, exp)
		}
	}
	// Compute is the same minus the corrupt count.
	rows2, err := Compute(src, Query{Since: ts("2026-10-01T00:00:00Z")})
	if err != nil || !reflect.DeepEqual(rows, rows2) {
		t.Errorf("Compute differs from ComputeDetailed: %v %+v", err, rows2)
	}
}

func TestComputeSinceWindow(t *testing.T) {
	src := fileSource(t, "testdata/audit-sample.jsonl")
	// Only the second day: the s2 records.
	rows, err := Compute(src, Query{Since: ts("2026-10-11T00:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Rule != "read-before-edit" || rows[0].Evals != 6 || rows[0].Would != 1 || rows[0].Unknown != 2 || rows[0].Sessions != 1 {
		t.Errorf("rows = %+v", rows)
	}
	rows, err = Compute(src, Query{Since: ts("2027-01-01T00:00:00Z")})
	if err != nil || len(rows) != 0 {
		t.Errorf("future since: rows=%+v err=%v", rows, err)
	}
}

func TestComputeGroupingAndFilters(t *testing.T) {
	src := fileSource(t, "testdata/audit-sample.jsonl")
	tests := []struct {
		name string
		q    Query
		want []Row
	}{
		{"by session", Query{By: BySession}, []Row{
			{Key: "s1", Rule: "no-long-sleep", Action: "deny", Target: "deny", Evals: 10, Hits: 1, Unknown: 1, Sessions: 1},
			{Key: "s1", Rule: "read-before-edit", Action: "log", Target: "deny", Evals: 4, Would: 1, Sessions: 1},
			{Key: "s2", Rule: "read-before-edit", Action: "log", Target: "deny", Evals: 6, Would: 1, Unknown: 2, Sessions: 1},
		}},
		{"by repo", Query{By: ByRepo}, []Row{
			{Key: "aaaa", Rule: "no-long-sleep", Action: "deny", Target: "deny", Evals: 10, Hits: 1, Unknown: 1, Sessions: 1},
			{Key: "aaaa", Rule: "read-before-edit", Action: "log", Target: "deny", Evals: 4, Would: 1, Sessions: 1},
			{Key: "bbbb", Rule: "read-before-edit", Action: "log", Target: "deny", Evals: 6, Would: 1, Unknown: 2, Sessions: 1},
		}},
		{"rule filter", Query{Rule: "no-long-sleep"}, []Row{
			{Key: "no-long-sleep", Rule: "no-long-sleep", Action: "deny", Target: "deny", Evals: 10, Hits: 1, Unknown: 1, Sessions: 1},
		}},
		{"current rules win", Query{Rule: "read-before-edit", Current: map[string]RuleInfo{"read-before-edit": {model.ActionWarn, model.ActionDeny}}}, []Row{
			{Key: "read-before-edit", Rule: "read-before-edit", Action: "warn", Target: "deny", Evals: 10, Would: 2, Unknown: 2, Sessions: 2},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compute(src, tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestComputeSemantics(t *testing.T) {
	mk := func(typ model.AuditType, at string, mut func(*model.AuditRecord)) model.AuditRecord {
		r := model.AuditRecord{TS: ts(at), V: 1, Type: typ, RuleID: "rule-a", SessionID: "s", RepoInst: "r"}
		mut(&r)
		return r
	}
	src := &sliceSource{recs: []model.AuditRecord{
		mk(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) { r.Decision = "deny"; r.Action = "deny"; r.Target = "deny" }),
		mk(model.AuditHit, "2026-10-10T00:01:00Z", func(r *model.AuditRecord) {
			r.Decision = "warn"
			r.Action = "warn"
			r.Target = "deny"
			r.SessionID = "s2"
		}),
		mk(model.AuditHit, "2026-10-10T00:02:00Z", func(r *model.AuditRecord) { r.Decision = "ask"; r.SessionID = "s2" }),
		mk(model.AuditHit, "2026-10-10T00:03:00Z", func(r *model.AuditRecord) { r.Decision = "log"; r.SessionID = "s3" }),
		mk(model.AuditHit, "2026-10-10T00:04:00Z", func(r *model.AuditRecord) { r.Decision = "would_warn"; r.SessionID = "" }),
		// hits/would inside eval_summary are NOT added to hit counts.
		mk(model.AuditEvalSummary, "2026-10-10T00:05:00Z", func(r *model.AuditRecord) {
			r.RuleID = ""
			r.Evals = map[string]model.EvalCount{"rule-a": {Evals: 7, Hits: 99, Would: 99, Unknown: 3}, "rule-b": {Evals: 2}}
		}),
		mk(model.AuditDiag, "2026-10-10T00:06:00Z", func(r *model.AuditRecord) {}),
		mk(model.AuditVerify, "2026-10-10T00:06:00Z", func(r *model.AuditRecord) {}),
		mk(model.AuditHit, "2026-10-10T00:07:00Z", func(r *model.AuditRecord) { r.RuleID = "" }), // malformed: no rule
	}}
	rows, err := Compute(src, Query{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		// action/target come from the most recent hit record that carries them.
		{Key: "rule-a", Rule: "rule-a", Action: "warn", Target: "deny", Evals: 7, Hits: 3, Would: 1, Unknown: 3, Sessions: 3},
		{Key: "rule-b", Rule: "rule-b", Evals: 2},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("got  %+v\nwant %+v", rows, want)
	}
}

func TestComputeErrors(t *testing.T) {
	if _, err := Compute(&sliceSource{}, Query{By: "bogus"}); !errors.Is(err, ErrUsage) {
		t.Errorf("bad --by: %v", err)
	}
	if _, err := Compute(nil, Query{}); err == nil {
		t.Error("nil source accepted")
	}
	boom := errors.New("boom")
	if _, err := Compute(&sliceSource{err: boom}, Query{}); !errors.Is(err, boom) {
		t.Errorf("source error not wrapped: %v", err)
	}
	rows, err := Compute(&sliceSource{}, Query{})
	if err != nil || len(rows) != 0 {
		t.Errorf("empty log: %v %v", rows, err)
	}
}

func TestComputeConcurrent(t *testing.T) {
	src := fileSource(t, "testdata/audit-sample.jsonl")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				rows, err := Compute(src, Query{By: BySession})
				if err != nil || len(rows) != 3 {
					t.Errorf("rows=%d err=%v", len(rows), err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestParseSince(t *testing.T) {
	now := ts("2026-10-16T12:00:00Z")
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"7d", now.Add(-7 * 24 * time.Hour), false},
		{"36h", now.Add(-36 * time.Hour), false},
		{"0d", now, false},
		{"", now.Add(-DefaultSince), false},
		{" 2d ", now.Add(-48 * time.Hour), false},
		{"2026-10-01T00:00:00Z", ts("2026-10-01T00:00:00Z"), false},
		{"2026-10-01T08:00:00+08:00", ts("2026-10-01T00:00:00Z"), false},
		{"2026-10-01T00:00:00.5Z", ts("2026-10-01T00:00:00.5Z"), false},
		{"d", time.Time{}, true},
		{"h", time.Time{}, true},
		{"-1d", time.Time{}, true},
		{"1.5d", time.Time{}, true},
		{"7", time.Time{}, true},
		{"7w", time.Time{}, true},
		{"7 d", time.Time{}, true},
		{"99999999999999999999d", time.Time{}, true},
		{"9223372036854775807h", time.Time{}, true},
		{"yesterday", time.Time{}, true},
		{"2026-10-01", time.Time{}, true},
	}
	for _, tc := range tests {
		got, err := ParseSince(tc.in, now)
		if tc.wantErr {
			if err == nil || !errors.Is(err, ErrUsage) {
				t.Errorf("ParseSince(%q) = %v, %v; want usage error", tc.in, got, err)
			}
			continue
		}
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("ParseSince(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func FuzzParseSince(f *testing.F) {
	for _, s := range []string{"7d", "36h", "", "0d", "2026-10-01T00:00:00Z", "d", "-5h", "99999999999999999999d", "1e3d", "\x00", "７d"} {
		f.Add(s)
	}
	now := ts("2026-10-16T12:00:00Z")
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseSince(s, now)
		if err != nil {
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("non-usage error %v", err)
			}
			return
		}
		if got.IsZero() {
			t.Fatalf("ParseSince(%q) returned the zero time", s)
		}
	})
}

func TestRenderTable(t *testing.T) {
	rows := []Row{{Key: "s1", Rule: "a-rule", Action: "log", Target: "deny", Evals: 10, Hits: 1, Would: 2, Unknown: 3, Sessions: 4}, {Key: "s1", Rule: "b-rule"}}
	got := string(RenderTable(rows, ByRule))
	want := "rule    action  target  evals  hits  would  unknown  sessions\n" +
		"a-rule  log     deny    10     1     2      3        4\n" +
		"b-rule  -       -       0      0     0      0        0\n"
	if got != want {
		t.Errorf("table:\n%q\nwant:\n%q", got, want)
	}
	if g := string(RenderTable(rows[:1], BySession)); g[:8] != "session " {
		t.Errorf("session table lacks key column: %q", g)
	}
}
