package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 16, 8, 30, 0, 123456789, time.UTC)

func hitRec(i int) model.AuditRecord {
	return model.AuditRecord{
		TS: t0.Add(time.Duration(i) * time.Millisecond), Type: model.AuditHit, RuleID: "read-before-edit", Decision: "deny",
		Event: "PreToolUse", Tool: "Edit", SessionID: "s1", EventSeq: int64(i), Matched: fmt.Sprintf("Edit: src/file%d.go", i),
	}
}

func newWriter(t testing.TB, max int64) (*Writer, string) {
	t.Helper()
	lay := testenv.New(t).Layout(t)
	return NewWriter(Options{Dir: lay.AuditDir, FS: sys.OSFS{}, Clock: sys.NewFakeClock(t0), MaxFileBytes: max}), lay.AuditDir
}

func readLines(t testing.TB, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestFileNameAndParse(t *testing.T) {
	tests := []struct {
		t    time.Time
		n    int
		want string
	}{
		{t0, 0, "2026-10.jsonl"},
		{t0, 1, "2026-10.1.jsonl"},
		{t0, 12, "2026-10.12.jsonl"},
		{t0, -3, "2026-10.jsonl"},
		{time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 0, "2026-01.jsonl"},
		{time.Date(2026, 10, 31, 23, 59, 59, 0, time.FixedZone("x", -8*3600)), 0, "2026-11.jsonl"}, // month in UTC
	}
	for _, tc := range tests {
		got := FileName(tc.t, tc.n)
		if got != tc.want {
			t.Errorf("FileName(%v,%d) = %q, want %q", tc.t, tc.n, got, tc.want)
		}
	}
	valid := map[string][2]any{
		"2026-10.jsonl": {"2026-10", 0}, "2026-10.1.jsonl": {"2026-10", 1}, "1999-01.30.jsonl": {"1999-01", 30},
	}
	for name, want := range valid {
		m, n, ok := parseFileName(name)
		if !ok || m != want[0] || n != want[1] {
			t.Errorf("parseFileName(%q) = %q %d %v", name, m, n, ok)
		}
	}
	for _, name := range []string{"", "x.jsonl", "2026-13.jsonl", "2026-10.jsonl.tmp", "2026-10.0.jsonl", "2026-10.01.jsonl", "2026-10..jsonl", "2026-10.-1.jsonl", "2026-10.a.jsonl", "2026-1.jsonl", "2026-10jsonl", "2026-10x.jsonl", ".jsonl"} {
		if _, _, ok := parseFileName(name); ok {
			t.Errorf("parseFileName(%q) accepted", name)
		}
	}
}

func TestClipRunes(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "ab…"},
		{"abcd", 4, "abcd"},
		{"abcd", 1, "…"},
		{"日本語テキスト", 4, "日本語…"},
		{"日本語", 3, "日本語"},
		{"abc", 0, ""},
		{"", 0, ""},
		{"abc", -1, ""},
		{"a\xffbc", 3, "a\xff…"},
	}
	for _, tc := range tests {
		if got := ClipRunes(tc.in, tc.n); got != tc.want {
			t.Errorf("ClipRunes(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestMarshalLineBasics(t *testing.T) {
	rec := hitRec(1)
	rec.TS = time.Date(2026, 10, 16, 16, 30, 0, 5, time.FixedZone("x", 8*3600))
	rec.Matched = "token=abcdef12345 <b>&</b>"
	rec.Context = []model.EventSummary{{Seq: 1, Event: "PreToolUse", Text: "password=hunter2xyz"}}
	line, err := MarshalLine(&rec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("not exactly one line: %q", line)
	}
	s := string(line)
	for _, bad := range []string{"abcdef12345", "hunter2xyz", "\\u003c", "\\u0026"} {
		if strings.Contains(s, bad) {
			t.Errorf("line contains %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"ts":"2026-10-16T08:30:00.000000005Z"`) || !strings.Contains(s, `"v":1`) {
		t.Errorf("ts/v not normalized: %s", s)
	}
	if rec.Matched != "token=abcdef12345 <b>&</b>" || rec.Context[0].Text != "password=hunter2xyz" {
		t.Error("MarshalLine modified the caller's record")
	}
	if _, err := MarshalLine(nil); err == nil {
		t.Error("nil record accepted")
	}
}

func big(n int, s string) string { return strings.Repeat(s, n) }

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// clipCtx mirrors the first context shrink step.
func clipCtx(r *model.AuditRecord) {
	r.Context = append([]model.EventSummary(nil), r.Context...)
	for i := range r.Context {
		r.Context[i].Text = ClipRunes(r.Context[i].Text, 60)
		r.Context[i].Hits = nil
	}
}

// withOverflow pads Decision so that the record, as it looks after state
// (the shrink steps that must already have run), is over the limit by
// exactly over bytes; the step under test is then the one that makes it fit.
// A nil state means the record as given.
func withOverflow(rec model.AuditRecord, over int, state func(*model.AuditRecord)) model.AuditRecord {
	probe := rec
	if state != nil {
		state(&probe)
	}
	line, _, err := fit(&probe)
	if err != nil {
		panic(err)
	}
	rec.Decision = strings.Repeat("x", model.MaxAuditLineBytes+over-len(line))
	return rec
}

func TestMarshalLineShrinkOrder(t *testing.T) {
	ctx := func(n int) []model.EventSummary {
		out := make([]model.EventSummary, n)
		for i := range out {
			out[i] = model.EventSummary{Seq: int64(i + 1), Event: "PreToolUse", Tool: "Bash", Text: big(120, "c"), Hits: []string{"r1", "r2"}}
		}
		return out
	}
	evals := func(n int) map[string]model.EvalCount {
		m := map[string]model.EvalCount{}
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("rule-%04d", i)] = model.EvalCount{Evals: i, Hits: 1}
		}
		return m
	}
	type check func(t *testing.T, rec model.AuditRecord, raw map[string]any)
	tests := []struct {
		name  string
		rec   model.AuditRecord
		check check
	}{
		{
			name: "fits: untouched",
			rec:  model.AuditRecord{Type: model.AuditHit, Matched: big(200, "m"), Context: ctx(2), Detail: json.RawMessage(`{"a":"b"}`)},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if len(rec.Matched) != 200 || len(rec.Context) != 2 || string(rec.Detail) != `{"a":"b"}` {
					t.Errorf("fitting record was altered: %+v", rec)
				}
			},
		},
		{
			name: "detail goes first: strings clipped, other fields intact",
			rec: model.AuditRecord{Type: model.AuditDiag, Matched: big(200, "m"), Context: ctx(2),
				Detail: json.RawMessage(`{"diag":"x","note":"` + big(9000, "d") + `"}`)},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				var d map[string]any
				if err := json.Unmarshal(rec.Detail, &d); err != nil {
					t.Fatalf("detail is not JSON: %s", rec.Detail)
				}
				if d["diag"] != "x" || d["truncated"] != true || len([]rune(d["note"].(string))) != 256 {
					t.Errorf("detail = %v", d)
				}
				if len(rec.Context) != 2 || len(rec.Matched) != 200 {
					t.Errorf("later steps ran too early: ctx=%d matched=%d", len(rec.Context), len(rec.Matched))
				}
			},
		},
		{
			name: "detail replaced when clipping is not enough",
			rec:  model.AuditRecord{Type: model.AuditDiag, Matched: "keep", Detail: json.RawMessage(`[` + big(3000, `"abcdefgh",`) + `"z"]`)},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if string(rec.Detail) != `{"truncated":true}` || rec.Matched != "keep" {
					t.Errorf("detail=%s matched=%q", rec.Detail, rec.Matched)
				}
			},
		},
		{
			name: "unshrinkable field falls to the minimal record",
			rec:  model.AuditRecord{Type: model.AuditHit, Matched: big(200, "m"), Decision: big(8300, "x"), Context: ctx(2), Detail: json.RawMessage(`{"d":1}`), Evals: evals(0)},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if raw["truncated"] != true || raw["type"] != "hit" || raw["decision"] != nil || raw["matched"] != nil {
					t.Errorf("expected the minimal record, got keys %v", keysOf(raw))
				}
			},
		},
		{
			name: "context texts clipped to 60 runes, both entries kept",
			rec: withOverflow(model.AuditRecord{Type: model.AuditHit, Matched: big(200, "m"),
				Context: []model.EventSummary{{Text: big(120, "a"), Hits: []string{"h"}}, {Text: big(120, "b")}}}, 20, nil),
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if len(rec.Context) != 2 || len([]rune(rec.Context[0].Text)) != 60 || len(rec.Context[0].Hits) != 0 || len(rec.Matched) != 200 {
					t.Errorf("ctx=%d entries matched=%d", len(rec.Context), len(rec.Matched))
				}
			},
		},
		{
			name: "context reduced to the closest entry",
			rec: withOverflow(model.AuditRecord{Type: model.AuditHit, Matched: big(200, "m"),
				Context: []model.EventSummary{{Seq: 1, Text: big(120, "a")}, {Seq: 2, Text: big(120, "b")}}}, 10, clipCtx),
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if len(rec.Context) != 1 || rec.Context[0].Seq != 2 || len(rec.Matched) != 200 {
					t.Errorf("ctx=%+v matched=%d", rec.Context, len(rec.Matched))
				}
			},
		},
		{
			name: "context dropped before matched is touched",
			rec: withOverflow(model.AuditRecord{Type: model.AuditHit, Matched: big(200, "m"),
				Context: []model.EventSummary{{Seq: 1, Text: big(120, "a")}, {Seq: 2, Text: big(120, "b")}}}, 10, func(r *model.AuditRecord) {
				clipCtx(r)
				r.Context = r.Context[1:]
			}),
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if len(rec.Context) != 0 || len(rec.Matched) != 200 {
					t.Errorf("ctx=%+v matched=%d", rec.Context, len(rec.Matched))
				}
			},
		},
		{
			name: "matched is clipped last of the three",
			rec:  withOverflow(model.AuditRecord{Type: model.AuditHit, Matched: big(200, "m")}, 50, func(r *model.AuditRecord) {}),
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if n := len([]rune(rec.Matched)); n == 0 || n >= 200 || !strings.HasSuffix(rec.Matched, "…") {
					t.Errorf("matched = %q", rec.Matched)
				}
			},
		},
		{
			name: "evals entries dropped from the end, the rest kept",
			rec:  model.AuditRecord{Type: model.AuditEvalSummary, Evals: evals(300)},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if len(rec.Evals) == 0 || len(rec.Evals) >= 300 {
					t.Fatalf("kept %d evals", len(rec.Evals))
				}
				keys := make([]string, 0, len(rec.Evals))
				for k := range rec.Evals {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for i, k := range keys {
					if k != fmt.Sprintf("rule-%04d", i) {
						t.Fatalf("kept keys are not the lowest prefix: %v", keys[:i+1])
					}
				}
				if raw["truncated"] == true {
					t.Error("fell through to the minimal record")
				}
			},
		},
		{
			name: "unshrinkable record becomes minimal",
			rec:  model.AuditRecord{Type: model.AuditVerify, TS: t0, Verify: &model.VerifyRecord{Error: big(9000, "e")}, Matched: "m"},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				want := map[string]any{"ts": t0.UTC().Format(time.RFC3339Nano), "v": float64(1), "type": "verify", "truncated": true}
				if fmt.Sprint(raw) != fmt.Sprint(want) {
					t.Errorf("minimal = %v, want %v", raw, want)
				}
			},
		},
		{
			name: "multibyte matched respects the byte limit",
			rec:  model.AuditRecord{Type: model.AuditHit, Matched: big(200, "界"), Context: []model.EventSummary{{Text: big(120, "界")}, {Text: big(120, "界")}}, Detail: json.RawMessage(`{"x":"` + big(2700, "界") + `"}`)},
			check: func(t *testing.T, rec model.AuditRecord, raw map[string]any) {
				if raw["truncated"] == true {
					t.Error("should have fit after shrinking")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			if rec.TS.IsZero() {
				rec.TS = t0
			}
			line, err := MarshalLine(&rec)
			if err != nil {
				t.Fatal(err)
			}
			if len(line) > model.MaxAuditLineBytes {
				t.Fatalf("line is %d bytes", len(line))
			}
			if !utf8.Valid(line) || !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
				t.Fatalf("malformed line: %q", line)
			}
			var raw map[string]any
			if err := json.Unmarshal(line, &raw); err != nil {
				t.Fatalf("line is not JSON: %v", err)
			}
			var back lineRec
			if err := json.Unmarshal(line, &back); err != nil {
				t.Fatal(err)
			}
			tc.check(t, back.AuditRecord, raw)
		})
	}
}

func TestMarshalLineInvalidDetail(t *testing.T) {
	rec := hitRec(1)
	rec.Detail = json.RawMessage(`{not json`)
	line, err := MarshalLine(&rec)
	if err != nil {
		t.Fatal(err)
	}
	var back lineRec
	if err := json.Unmarshal(line, &back); err != nil {
		t.Fatalf("line is not valid JSON: %v\n%s", err, line)
	}
	var s string
	if err := json.Unmarshal(back.Detail, &s); err != nil || s != `{not json` {
		t.Errorf("detail = %s", back.Detail)
	}
}

func TestMarshalLineGolden(t *testing.T) {
	recs := []model.AuditRecord{
		{TS: t0, V: 1, Type: model.AuditHit, EventSeq: 7, Client: "claude-code", SessionID: "s-1", AgentID: "a-1", AgentType: "Explore",
			ToolUseID: "toolu_1", TranscriptRef: "~/.claude/projects/p/s-1.jsonl", RepoInst: "ab12", RepoRemoteHash: "0123456789abcdef",
			Event: "PreToolUse", NativeEvent: "PreToolUse", Tool: "Edit", RuleID: "read-before-edit", RuleLayer: "default",
			Generation: "gen1", Action: "deny", Target: "deny", Decision: "deny", Matched: "Edit: src/a.go password=hunter2xyz",
			Context:   []model.EventSummary{{Seq: 5, TS: t0, Event: "PostToolUse", Tool: "Bash", Text: "Bash: git status"}},
			LatencyUS: 1234, Tier: "T0", BallastVersion: "0.1.0"},
		{TS: t0, Type: model.AuditEvalSummary, Evals: map[string]model.EvalCount{"b": {Evals: 2, Hits: 1}, "a": {Evals: 5, Unknown: 1}}},
		{TS: t0, Type: model.AuditDiag, Detail: json.RawMessage(`{"diag":"lock_timeout","wait_us":20000}`)},
		{TS: t0, Type: model.AuditRuleDropped, RuleID: "x", Detail: json.RawMessage(`{"reason":"limit 12"}`)},
	}
	var got bytes.Buffer
	for i := range recs {
		line, err := MarshalLine(&recs[i])
		if err != nil {
			t.Fatal(err)
		}
		got.Write(line)
	}
	golden := filepath.Join("testdata", "lines.golden.jsonl")
	if *update {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Errorf("lines differ from %s (run -update):\n%s", golden, got.String())
	}
}

func FuzzMarshalLine(f *testing.F) {
	f.Add("Edit: a.go", "ctx text", `{"a":"b"}`, 3)
	f.Add(big(300, "界"), big(500, "x"), `{"k":"`+big(10000, "v")+`"}`, 5)
	f.Add("password=abc", "token=xyz", `[1,2,3]`, 400)
	f.Add("", "", `{broken`, 0)
	f.Add("\xff\xfe", "\x00", `"\ud800"`, 1)
	f.Fuzz(func(t *testing.T, matched, ctxText, detail string, nEvals int) {
		rec := model.AuditRecord{TS: t0, Type: model.AuditHit, RuleID: "r", Decision: "deny", Matched: matched,
			Context: []model.EventSummary{{Text: ctxText}, {Text: ctxText}}, Detail: json.RawMessage(detail)}
		if nEvals > 0 && nEvals < 2000 {
			rec.Evals = map[string]model.EvalCount{}
			for i := 0; i < nEvals; i++ {
				rec.Evals[fmt.Sprint("rule", i)] = model.EvalCount{Evals: i}
			}
		}
		line, err := MarshalLine(&rec)
		if err != nil {
			t.Fatalf("MarshalLine: %v", err)
		}
		if len(line) > model.MaxAuditLineBytes {
			t.Fatalf("line is %d bytes", len(line))
		}
		if bytes.Count(line, []byte("\n")) != 1 || line[len(line)-1] != '\n' {
			t.Fatalf("not a single line: %q", line)
		}
		var back lineRec
		if err := json.Unmarshal(line, &back); err != nil {
			t.Fatalf("line does not parse: %v\n%s", err, line)
		}
		if back.V != model.AuditVersion || back.Type != model.AuditHit {
			t.Fatalf("lost mandatory fields: %+v", back)
		}
	})
}

func TestAppendWritesParsableLinesWithPerms(t *testing.T) {
	w, dir := newWriter(t, 0)
	recs := []model.AuditRecord{hitRec(1), hitRec(2), {Type: model.AuditDiag, Detail: json.RawMessage(`{"diag":"x"}`)}}
	recs[2].TS = time.Time{} // gets the clock's now
	if err := w.Append(recs); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "2026-10.jsonl")
	lines := readLines(t, file)
	if len(lines) != 3 {
		t.Fatalf("got %d lines", len(lines))
	}
	for i, l := range lines {
		var back lineRec
		if err := json.Unmarshal([]byte(l), &back); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if back.V != 1 {
			t.Errorf("line %d: v=%d", i, back.V)
		}
	}
	if !strings.Contains(lines[2], `"ts":"2026-10-16T08:30:00.123456789Z"`) {
		t.Errorf("missing ts not filled from the clock: %s", lines[2])
	}
	if recs[2].TS != (time.Time{}) || recs[0].V != 0 {
		t.Error("Append modified the caller's records")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v %v, want 0600", fi, err)
		}
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v %v, want 0700", fi, err)
		}
	}
	if err := w.Append(nil); err != nil {
		t.Errorf("Append(nil) = %v", err)
	}
	// A second append adds to the same file.
	if err := w.Append(recs[:1]); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, file)); got != 4 {
		t.Errorf("lines after second append = %d", got)
	}
}

func TestAppendMonthsFromRecordTS(t *testing.T) {
	w, dir := newWriter(t, 0)
	a, b, c := hitRec(1), hitRec(2), hitRec(3)
	a.TS = time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC)
	b.TS = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	c.TS = time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("x", 9*3600)) // Sept 30 23:00 UTC
	if err := w.Append([]model.AuditRecord{a, b, c}); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, filepath.Join(dir, "2026-09.jsonl"))); got != 2 {
		t.Errorf("september lines = %d (record c is Sept 30 in UTC)", got)
	}
	if got := len(readLines(t, filepath.Join(dir, "2026-10.jsonl"))); got != 1 {
		t.Errorf("october lines = %d", got)
	}
}

func TestAppendRotation(t *testing.T) {
	w, dir := newWriter(t, 600)
	const total = 40
	for i := 0; i < total; i++ {
		if err := w.Append([]model.AuditRecord{hitRec(i)}); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) < 3 || names[0] != "2026-10.1.jsonl" && names[0] != "2026-10.jsonl" {
		t.Fatalf("files = %v", names)
	}
	lines := 0
	for _, n := range names {
		fi, _ := os.Stat(filepath.Join(dir, n))
		lines += len(readLines(t, filepath.Join(dir, n)))
		// A file is closed once ≥ max, so it can exceed max by at most one batch.
		if fi.Size() >= 600+model.MaxAuditLineBytes {
			t.Errorf("%s is %d bytes", n, fi.Size())
		}
	}
	if lines != total {
		t.Errorf("%d lines across %v, want %d", lines, names, total)
	}
	// Writers always continue in the highest suffix.
	last := names[len(names)-1]
	before := readLines(t, filepath.Join(dir, last))
	_ = before
	if err := w.Append([]model.AuditRecord{hitRec(99)}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, filepath.Join(dir, last)); got[len(got)-1] == "" {
		t.Error("append did not land in the highest suffix")
	}
}

func TestAppendCreatesDotOneAt64MiB(t *testing.T) {
	w, dir := newWriter(t, 0) // default 64 MiB
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, "2026-10.jsonl")
	f, err := os.Create(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes - 1); err != nil { // sparse; one byte below the limit
		t.Skipf("cannot create a sparse file: %v", err)
	}
	f.Close()
	if err := w.Append([]model.AuditRecord{hitRec(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10.1.jsonl")); err == nil {
		t.Fatal("rotated before the file reached 64 MiB")
	}
	// The line pushed the active file past 64 MiB, so the next write rotates.
	if err := w.Append([]model.AuditRecord{hitRec(2)}); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "2026-10.1.jsonl")
	if got := readLines(t, next); len(got) != 1 {
		t.Errorf("%s has %d lines, want 1", next, len(got))
	}
	if fi, _ := os.Stat(full); fi.Size() < MaxFileBytes {
		t.Errorf("rotated file shrank: %d", fi.Size())
	}
	if err := w.Append([]model.AuditRecord{hitRec(3)}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, next); len(got) != 2 {
		t.Errorf("third write did not stay in .1 (%d lines)", len(got))
	}
}

func TestAppendUsesHighestExistingSuffix(t *testing.T) {
	w, dir := newWriter(t, 0)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"2026-10.jsonl", "2026-10.2.jsonl", "2026-09.5.jsonl", "2026-10.jsonl.bak", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Append([]model.AuditRecord{hitRec(1)}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, filepath.Join(dir, "2026-10.2.jsonl")); len(got) != 1 {
		t.Errorf(".2 has %d lines (a gap at .1 must not matter)", len(got))
	}
	if got := readLines(t, filepath.Join(dir, "2026-10.jsonl")); len(got) != 0 {
		t.Errorf("plain file got %d lines", len(got))
	}
}

type shortFile struct {
	sys.File
	n int
}

func (s shortFile) Write(p []byte) (int, error) { return s.File.Write(p[:min(s.n, len(p))]) }

type shortFS struct {
	sys.FS
	n int
}

func (s shortFS) OpenFile(name string, flag int, perm fs.FileMode) (sys.File, error) {
	f, err := s.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return shortFile{File: f, n: s.n}, nil
}

func TestAppendShortWriteIsAnError(t *testing.T) {
	lay := testenv.New(t).Layout(t)
	w := NewWriter(Options{Dir: lay.AuditDir, FS: shortFS{FS: sys.OSFS{}, n: 10}, Clock: sys.NewFakeClock(t0)})
	err := w.Append([]model.AuditRecord{hitRec(1)})
	if !errors.Is(err, sys.ErrShortWrite) {
		t.Errorf("err = %v, want ErrShortWrite", err)
	}
}

func TestAppendFSErrorsAreReturned(t *testing.T) {
	boom := errors.New("disk full")
	for _, op := range []string{"OpenFile", "MkdirAll"} {
		lay := testenv.New(t).Layout(t)
		w := NewWriter(Options{Dir: lay.AuditDir, FS: sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{op: ""}, Err: boom}, Clock: sys.NewFakeClock(t0)})
		if err := w.Append([]model.AuditRecord{hitRec(1)}); !errors.Is(err, boom) {
			t.Errorf("%s fault: err = %v", op, err)
		}
	}
	// A directory squatting on the file name is an error, not a silent drop.
	w, dir := newWriter(t, 0)
	if err := os.MkdirAll(filepath.Join(dir, "2026-10.jsonl", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	// ReadDir skips directories, so the writer opens the plain name and fails.
	if err := w.Append([]model.AuditRecord{hitRec(1)}); err == nil {
		t.Error("expected an error when the target is a directory")
	}
}

func TestAppendBatchSingleFile(t *testing.T) {
	w, dir := newWriter(t, 0)
	if err := w.Append([]model.AuditRecord{hitRec(1), hitRec(2)}); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, filepath.Join(dir, "2026-10.jsonl"))); got != 2 {
		t.Errorf("lines = %d", got)
	}
}

func TestAppendMarshalErrorWritesNothing(t *testing.T) {
	w, dir := newWriter(t, 0)
	// A year beyond 9999 cannot be encoded as RFC3339, so MarshalLine fails
	// on the second record after the first one was already encoded.
	bad := hitRec(2)
	bad.TS = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := MarshalLine(&bad); err == nil {
		t.Fatal("MarshalLine accepted an unencodable timestamp")
	}
	if err := w.Append([]model.AuditRecord{hitRec(1), bad}); err == nil {
		t.Fatal("Append returned nil for an unencodable record")
	}
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("a failed batch left %d file(s) behind", len(ents))
	}
}

func TestMarshalLineCapsMatched(t *testing.T) {
	rec := model.AuditRecord{Type: model.AuditHit, Matched: big(model.MaxMatchedRunes+50, "界")}
	line, err := MarshalLine(&rec)
	if err != nil {
		t.Fatal(err)
	}
	var got model.AuditRecord
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(got.Matched); n != model.MaxMatchedRunes || !strings.HasSuffix(got.Matched, "…") {
		t.Errorf("matched has %d runes, want %d ending in an ellipsis", n, model.MaxMatchedRunes)
	}
	if rec.Matched != big(model.MaxMatchedRunes+50, "界") {
		t.Error("MarshalLine modified its input")
	}
	rec.Matched = big(model.MaxMatchedRunes, "m")
	if line, _ = MarshalLine(&rec); !strings.Contains(string(line), rec.Matched) {
		t.Error("a Matched at the limit must be kept whole")
	}
}

func TestAppendConcurrentGoroutinesNoInterleaving(t *testing.T) {
	w, dir := newWriter(t, 0)
	const writers, per = 8, 250
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				rec := hitRec(i)
				rec.SessionID = fmt.Sprint("g", g)
				rec.Matched = big(50+i%100, fmt.Sprint(g))
				if err := w.Append([]model.AuditRecord{rec}); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	seen := map[string]int{}
	for _, l := range readLines(t, filepath.Join(dir, "2026-10.jsonl")) {
		var back lineRec
		if err := json.Unmarshal([]byte(l), &back); err != nil {
			t.Fatalf("interleaved/corrupt line %q: %v", l, err)
		}
		seen[back.SessionID]++
	}
	for g := 0; g < writers; g++ {
		if seen[fmt.Sprint("g", g)] != per {
			t.Errorf("writer %d: %d lines, want %d", g, seen[fmt.Sprint("g", g)], per)
		}
	}
}

func TestWriterImplementsSink(t *testing.T) {
	var _ interface {
		Append([]model.AuditRecord) error
	} = NewWriter(Options{})
	w := NewWriter(Options{Dir: t.TempDir()})
	if w.opt.FS == nil || w.opt.Clock == nil || w.opt.MaxFileBytes != MaxFileBytes {
		t.Errorf("defaults not applied: %+v", w.opt)
	}
}

func BenchmarkAppend(b *testing.B) {
	w, _ := newWriter(b, 0)
	rec := hitRec(1)
	rec.Context = []model.EventSummary{{Seq: 1, Text: "Bash: git status"}, {Seq: 2, Text: "Edit: src/a.go"}}
	recs := []model.AuditRecord{rec}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.Append(recs); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRedactClean(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Redact("Bash: go test ./internal/... -race -count=1")
	}
}
