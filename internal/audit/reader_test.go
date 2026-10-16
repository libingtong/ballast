package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// copyScanFixture copies testdata/scan into a fresh sandbox audit dir.
func copyScanFixture(t *testing.T) string {
	t.Helper()
	dir := testenv.New(t).Layout(t).AuditDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join("testdata", "scan"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("testdata", "scan", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func scanAll(t *testing.T, r *Reader, opt ScanOptions) ([]string, []Location, ScanStats) {
	t.Helper()
	var ids []string
	var locs []Location
	st, err := r.Scan(opt, func(rec *model.AuditRecord, loc Location) error {
		id := rec.RuleID
		if id == "" {
			id = string(rec.Type)
		}
		ids = append(ids, id)
		loc.File = filepath.Base(loc.File)
		locs = append(locs, loc)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids, locs, st
}

func TestScanChronologicalAcrossRotation(t *testing.T) {
	r := &Reader{Dir: copyScanFixture(t)}
	ids, locs, st := scanAll(t, r, ScanOptions{})
	want := []string{"r-sep1", "r-sep2", "r-oct1", "diag", "r-oct2", "eval_summary", "r-oct3", "r-nov1"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v\nwant    %v", ids, want)
	}
	wantLoc := []Location{{"2026-09.jsonl", 1}, {"2026-09.jsonl", 2}, {"2026-10.jsonl", 1}, {"2026-10.jsonl", 2},
		{"2026-10.1.jsonl", 1}, {"2026-10.2.jsonl", 1}, {"2026-10.10.jsonl", 1}, {"2026-11.jsonl", 1}}
	for i := range wantLoc {
		if locs[i] != wantLoc[i] {
			t.Errorf("loc[%d] = %v, want %v", i, locs[i], wantLoc[i])
		}
	}
	// Corrupt: the clipped tail of .1 and "garbage line" of .2; skipped: the v=2 line; the blank line is ignored.
	if st.Files != 6 || st.Corrupt != 2 || st.Skipped != 1 || st.Lines != 11 {
		t.Errorf("stats = %+v", st)
	}
}

func TestScanFilters(t *testing.T) {
	r := &Reader{Dir: copyScanFixture(t)}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name string
		opt  ScanOptions
		want string
	}{
		{"since", ScanOptions{Since: day(10, 6)}, "r-oct2,eval_summary,r-oct3,r-nov1"},
		{"until is exclusive", ScanOptions{Until: day(10, 5)}, "r-sep1,r-sep2,r-oct1"},
		{"window", ScanOptions{Since: day(9, 15), Until: day(10, 11)}, "r-sep2,r-oct1,diag,r-oct2"},
		{"since is inclusive", ScanOptions{Since: time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC), Until: day(10, 6)}, "diag"},
		{"types", ScanOptions{Types: []model.AuditType{model.AuditDiag, model.AuditEvalSummary}}, "diag,eval_summary"},
		{"types and since", ScanOptions{Since: day(10, 1), Types: []model.AuditType{model.AuditHit}}, "r-oct1,r-oct2,r-oct3,r-nov1"},
		{"empty window", ScanOptions{Since: day(12, 1)}, ""},
		{"before everything", ScanOptions{Until: day(1, 1)}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids, _, _ := scanAll(t, r, tc.opt)
			if got := strings.Join(ids, ","); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScanSkipsFilesOutsideTheWindow(t *testing.T) {
	r := &Reader{Dir: copyScanFixture(t)}
	_, _, st := scanAll(t, r, ScanOptions{Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	if st.Files != 1 {
		t.Errorf("opened %d files for a November-only window", st.Files)
	}
	_, _, st = scanAll(t, r, ScanOptions{Until: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	if st.Files != 1 {
		t.Errorf("opened %d files for a September-only window", st.Files)
	}
}

func TestScanCallbackErrorAborts(t *testing.T) {
	r := &Reader{Dir: copyScanFixture(t)}
	stop := errors.New("stop")
	n := 0
	_, err := r.Scan(ScanOptions{}, func(*model.AuditRecord, Location) error {
		n++
		if n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 3 {
		t.Errorf("err=%v n=%d", err, n)
	}
}

func TestRecordsPortWrapper(t *testing.T) {
	r := &Reader{Dir: copyScanFixture(t)}
	var got []string
	corrupt, err := r.Records(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		func(rec *model.AuditRecord) error { got = append(got, rec.RuleID); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if corrupt != 2 || strings.Join(got, ",") != "r-oct1,,r-oct2,,r-oct3" {
		t.Errorf("corrupt=%d got=%v", corrupt, got)
	}
	stop := errors.New("stop")
	if _, err := r.Records(time.Time{}, time.Time{}, func(*model.AuditRecord) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("err = %v", err)
	}
}

func TestScanMissingDirAndEmptyFiles(t *testing.T) {
	r := &Reader{Dir: filepath.Join(t.TempDir(), "nope")}
	ids, _, st := scanAll(t, r, ScanOptions{})
	if len(ids) != 0 || st != (ScanStats{}) {
		t.Errorf("ids=%v st=%+v", ids, st)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-10.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r = &Reader{Dir: dir, FS: sys.OSFS{}}
	ids, _, st = scanAll(t, r, ScanOptions{})
	if len(ids) != 0 || st.Files != 1 || st.Lines != 0 {
		t.Errorf("ids=%v st=%+v", ids, st)
	}
}

func TestScanTolerance(t *testing.T) {
	good := `{"ts":"2026-10-01T08:00:00Z","v":1,"type":"hit","rule_id":"ok","decision":"deny"}` + "\n"
	big := `{"ts":"2026-10-01T08:00:00Z","v":1,"type":"hit","rule_id":"big","matched":"` + strings.Repeat("z", 3<<20) + `"}` + "\n"
	cases := []struct {
		name    string
		content string
		ids     string
		corrupt int
	}{
		{"corrupt tail without newline", good + `{"ts":"2026-10-01T08:00:01Z","v":1,"ty`, "ok", 1},
		{"valid tail without newline", strings.TrimSuffix(good, "\n"), "ok", 0},
		{"corrupt in the middle", good + "{{{\n" + good, "ok,ok", 1},
		{"non-object lines", good + "null\n[1]\n\"s\"\n42\n" + good, "ok,ok", 4},
		{"crlf endings", strings.ReplaceAll(good+good, "\n", "\r\n"), "ok,ok", 0},
		{"binary junk", good + "\x00\x01\x02\xff\n" + good, "ok,ok", 1},
		{"oversize line is skipped, next line survives", big + good, "ok", 1},
		{"unknown version skipped", `{"ts":"2026-10-01T08:00:00Z","v":9,"type":"hit"}` + "\n" + good, "ok", 0},
		{"missing version skipped", `{"ts":"2026-10-01T08:00:00Z","type":"hit"}` + "\n" + good, "ok", 0},
		{"truncated marker record is delivered", `{"ts":"2026-10-01T08:00:00Z","v":1,"type":"hit","truncated":true}` + "\n", "hit", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "2026-10.jsonl"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			ids, _, st := scanAll(t, &Reader{Dir: dir}, ScanOptions{})
			if strings.Join(ids, ",") != tc.ids || st.Corrupt != tc.corrupt {
				t.Errorf("ids=%v corrupt=%d, want %q corrupt=%d", ids, st.Corrupt, tc.ids, tc.corrupt)
			}
		})
	}
}

func TestScanRoundTripsWriterOutput(t *testing.T) {
	w, dir := newWriter(t, 700)
	var want []model.AuditRecord
	for i := 0; i < 30; i++ {
		rec := hitRec(i)
		rec.TS = rec.TS.Add(time.Duration(i) * time.Hour * 24)
		want = append(want, rec)
	}
	for i := range want {
		if err := w.Append(want[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	var got []model.AuditRecord
	st, err := (&Reader{Dir: dir}).Scan(ScanOptions{}, func(rec *model.AuditRecord, _ Location) error {
		got = append(got, *rec)
		return nil
	})
	if err != nil || st.Corrupt != 0 || st.Skipped != 0 {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].EventSeq != want[i].EventSeq || !got[i].TS.Equal(want[i].TS) || got[i].RuleID != want[i].RuleID || got[i].Matched != want[i].Matched {
			t.Errorf("record %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// lintFixture builds a file whose violations are at known line numbers.
func lintFixture(t *testing.T) (dir string, wantMsgs map[int]string) {
	t.Helper()
	dir = testenv.New(t).Layout(t).AuditDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := func(ts, rest string) string {
		return fmt.Sprintf(`{"ts":%q,"v":1,%s}`, ts, rest)
	}
	oversize := line("2026-10-01T08:31:00Z", `"type":"hit","rule_id":"r","decision":"deny","matched":"`+strings.Repeat("a", 9000)+`"`)
	lines := []string{
		/*1*/ line("2026-10-01T08:30:00Z", `"type":"hit","rule_id":"r","decision":"deny"`),
		/*2*/ line("2026-10-01T08:30:01Z", `"type":"eval_summary","evals":{"r":{"evals":1,"hits":0,"would":0,"unknown":0}}`),
		/*3*/ `this is not json`,
		/*4*/ ``,
		/*5*/ line("2026-10-01T08:30:02Z", `"type":"hit","decision":"deny"`),
		/*6*/ `{"ts":"2026-10-01T08:30:03Z","v":2,"type":"hit","rule_id":"r","decision":"deny"}`,
		/*7*/ line("2026-10-01T08:30:04Z", `"type":"bogus"`),
		/*8*/ line("2026-10-01T08:15:00Z", `"type":"hit","rule_id":"r","decision":"deny"`),
		/*9*/ line("2026-10-01T08:26:00Z", `"type":"hit","rule_id":"r","decision":"deny"`),
		/*10*/ `{"v":1,"type":"hit","rule_id":"r","decision":"deny"}`,
		/*11*/ line("2026-10-01T08:30:05Z", `"type":"diag"`),
		/*12*/ line("2026-10-01T08:30:06Z", `"type":"verify","truncated":true`),
		/*13*/ line("2026-10-01T08:30:07Z", `"type":"verify"`),
		/*14*/ oversize,
		/*15*/ line("2026-10-01T08:30:08Z", `"type":"eval_summary"`),
		/*16*/ `[1,2]`,
		/*17*/ line("2026-10-01T08:30:09Z", `"type":"rule_dropped"`),
		/*18*/ line("2026-10-01T08:30:10Z", `"type":"hit","rule_id":"r","decision":"deny"`),
		/*19*/ `{"ts":"2026-10-01T08:30:11Z","v":1,"type":"hi`,
	}
	content := strings.Join(lines, "\n") // the last line has no newline
	if err := os.WriteFile(filepath.Join(dir, "2026-10.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, map[int]string{
		3:  "invalid JSON",
		4:  "empty line",
		5:  "missing rule_id",
		6:  "unknown or missing v: 2",
		7:  `unknown type "bogus"`,
		8:  "more than 5m0s before",
		10: "missing ts",
		11: "missing detail",
		13: "missing verify",
		14: "exceeds 8192 bytes",
		15: "missing evals",
		16: "invalid JSON",
		17: "missing rule_id",
		19: "corrupt trailing line",
	}
}

func TestLintReportsFileAndLine(t *testing.T) {
	dir, want := lintFixture(t)
	findings, st, err := Lint(&Reader{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, f := range findings {
		if filepath.Base(f.Loc.File) != "2026-10.jsonl" {
			t.Errorf("finding %v has the wrong file", f)
		}
		got[f.Loc.Line] += f.Message + "; "
	}
	for line, sub := range want {
		if !strings.Contains(got[line], sub) {
			t.Errorf("line %d: findings %q do not contain %q", line, got[line], sub)
		}
	}
	for line, msg := range got {
		if _, ok := want[line]; !ok {
			t.Errorf("unexpected finding at line %d: %s", line, msg)
		}
	}
	// Clean lines produce nothing: 1, 2, 9 (within skew), 12 (truncated marker), 18.
	for _, line := range []int{1, 2, 9, 12, 18} {
		if got[line] != "" {
			t.Errorf("line %d flagged: %s", line, got[line])
		}
	}
	if st.Files != 1 || st.Skipped != 1 {
		t.Errorf("stats = %+v", st)
	}
	s := findings[0].String()
	if !strings.HasSuffix(strings.Split(s, ": ")[0], ".jsonl:3") {
		t.Errorf("String() = %q", s)
	}
	b, _ := json.Marshal(findings[0])
	if !bytes.Contains(b, []byte(`"loc":{"File":`)) || !bytes.Contains(b, []byte(`"message"`)) {
		t.Errorf("json = %s", b)
	}
}

func TestLintAcrossFilesResetsTimestampTracking(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, ts string) {
		l := fmt.Sprintf(`{"ts":%q,"v":1,"type":"install"}`+"\n", ts)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(l), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("2026-10.jsonl", "2026-10-31T23:00:00Z")
	mk("2026-10.1.jsonl", "2026-10-31T10:00:00Z") // different file: the monotonic check is per file
	findings, _, err := Lint(&Reader{Dir: dir})
	if err != nil || len(findings) != 0 {
		t.Errorf("findings=%v err=%v", findings, err)
	}
}

func TestLintCleanWriterOutput(t *testing.T) {
	w, dir := newWriter(t, 2000)
	var recs []model.AuditRecord
	for i := 0; i < 50; i++ {
		recs = append(recs, hitRec(i))
	}
	recs = append(recs,
		model.AuditRecord{Type: model.AuditEvalSummary, Evals: map[string]model.EvalCount{"r": {Evals: 1}}},
		model.AuditRecord{Type: model.AuditDiag, Detail: json.RawMessage(`{"diag":"state_corrupt"}`)},
		model.AuditRecord{Type: model.AuditVerify, Verify: &model.VerifyRecord{Name: "test"}},
		model.AuditRecord{Type: model.AuditInstall, Detail: json.RawMessage(`{"action":"install"}`)},
		model.AuditRecord{Type: model.AuditHit, RuleID: "r", Decision: "deny", Detail: json.RawMessage(`{"x":"` + big(20000, "y") + `"}`)}, // shrunk
		model.AuditRecord{Type: model.AuditVerify, Verify: &model.VerifyRecord{Error: big(20000, "e")}},                                    // minimal fallback
	)
	for i := range recs {
		if err := w.Append(recs[i : i+1]); err != nil {
			t.Fatal(err)
		}
	}
	findings, st, err := Lint(&Reader{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("unexpected finding %s", f)
	}
	if st.Lines != len(recs) || st.Corrupt != 0 || st.Files < 2 {
		t.Errorf("stats = %+v, want %d lines in several files", st, len(recs))
	}
}

func TestGC(t *testing.T) {
	dir := t.TempDir()
	names := []string{"2025-12.jsonl", "2026-06.jsonl", "2026-06.3.jsonl", "2026-07.jsonl", "2026-08.jsonl", "2026-10.jsonl", "2026-10.1.jsonl", "keep.txt", "2026-07.jsonl.bak"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "2020-01.jsonl.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 2026-07 ended 2026-08-01; now - 90d = 2026-07-18 ⇒ 2026-06 ended 07-01 (>90d ago? no).
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	removed, err := GC(sys.OSFS{}, dir, now, Retention)
	if err != nil {
		t.Fatal(err)
	}
	var base []string
	for _, p := range removed {
		if filepath.Dir(p) != dir {
			t.Errorf("removed path %q is not inside dir", p)
		}
		base = append(base, filepath.Base(p))
	}
	// Month end + 90d < now: 2025-12 (ended 2026-01-01), 2026-06 (ended 2026-07-01 ⇒ 2026-09-29 < 10-16).
	// 2026-07 ended 2026-08-01 ⇒ 2026-10-30 > 10-16: kept.
	wantRemoved := "2025-12.jsonl,2026-06.3.jsonl,2026-06.jsonl"
	sortStrings(base)
	if strings.Join(base, ",") != wantRemoved {
		t.Errorf("removed %v, want %s", base, wantRemoved)
	}
	for _, n := range []string{"2026-07.jsonl", "2026-08.jsonl", "2026-10.jsonl", "2026-10.1.jsonl", "keep.txt", "2026-07.jsonl.bak", "2020-01.jsonl.d"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was removed", n)
		}
	}
}

func TestGCBoundaryAndDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-01.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	monthEnd := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	// Exactly retention after the month ended ⇒ not yet "more than".
	if removed, _ := GC(nil, dir, monthEnd.Add(Retention), Retention); len(removed) != 0 {
		t.Errorf("removed at the boundary: %v", removed)
	}
	if removed, _ := GC(nil, dir, monthEnd.Add(Retention+time.Second), 0); len(removed) != 1 { // 0 ⇒ Retention, nil FS ⇒ real
		t.Errorf("not removed just past the boundary: %v", removed)
	}
	if removed, err := GC(sys.OSFS{}, filepath.Join(dir, "missing"), monthEnd, Retention); err != nil || removed != nil {
		t.Errorf("missing dir: %v %v", removed, err)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func FuzzScanLine(f *testing.F) {
	f.Add([]byte(`{"ts":"2026-10-01T08:00:00Z","v":1,"type":"hit","rule_id":"r"}` + "\n"))
	f.Add([]byte("garbage\n\n{\n"))
	f.Add([]byte(`{"ts":"2026-10-01T08:00:00Z","v":1,"type":"hit"`))
	f.Add([]byte("\x00\xff\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "2026-10.jsonl"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		r := &Reader{Dir: dir}
		st, err := r.Scan(ScanOptions{}, func(rec *model.AuditRecord, loc Location) error {
			if rec.V != model.AuditVersion || loc.Line < 1 {
				t.Fatalf("bad record delivered: %+v at %v", rec, loc)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if st.Corrupt+st.Skipped > st.Lines {
			t.Fatalf("stats inconsistent: %+v", st)
		}
		if _, _, err := Lint(r); err != nil {
			t.Fatalf("Lint: %v", err)
		}
	})
}

// A regular file where the audit directory should be is an error on every
// platform (Windows reports it as "path not found", which must not be taken
// for a missing, i.e. empty, directory), while a really missing directory is
// just empty.
func TestFileInPlaceOfAuditDirIsAnError(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "audit")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Reader{Dir: file}).Scan(ScanOptions{}, func(*model.AuditRecord, Location) error { return nil }); err == nil {
		t.Error("Scan over a file: want an error")
	}
	if _, err := GC(nil, file, time.Now(), 0); err == nil {
		t.Error("GC over a file: want an error")
	}
	if _, err := (&Reader{Dir: filepath.Join(root, "missing")}).Scan(ScanOptions{}, func(*model.AuditRecord, Location) error { return nil }); err != nil {
		t.Errorf("Scan over a missing directory: %v", err)
	}
}
