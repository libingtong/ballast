package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	doc, err := decodeNumbers([]byte(`{"a":{"b":[10,{"c":"x"}]},"n":1.5,"t":true}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"a.b.0", "10", true},
		{"a.b.1.c", "x", true},
		{"n", "1.5", true},
		{"t", "true", true},
		{"a.b.2", "", false},
		{"a.x", "", false},
		{"n.z", "", false},
	}
	for _, c := range cases {
		v, ok := lookup(doc, c.path)
		if ok != c.ok || (ok && scalarString(v) != c.want) {
			t.Errorf("lookup(%q) = %v, %v; want %q, %v", c.path, v, ok, c.want, c.ok)
		}
	}
}

func TestJSONAppendCreatesPath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(f, []byte(`{"hooks":{"Stop":[1]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdJSON([]string{"append", f, "hooks.Stop", `{"k":2}`}); code != 0 {
		t.Fatalf("append: %d", code)
	}
	if code := cmdJSON([]string{"append", f, "hooks.New.Deep", `"v"`}); code != 0 {
		t.Fatalf("append new path: %d", code)
	}
	b, _ := os.ReadFile(f)
	for _, want := range []string{`"k": 2`, `"Deep": [`, `"v"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("file lacks %q:\n%s", want, b)
		}
	}
	if code := cmdJSON([]string{"valid", f}); code != 0 {
		t.Error("appended file must stay valid JSON")
	}
}

func TestJSONLCountMatchesNestedPaths(t *testing.T) {
	dir := t.TempDir()
	lines := `{"type":"diag","detail":{"diag":"lock_timeout"},"session_id":"a"}
{"type":"diag","detail":{"diag":"state_corrupt"},"session_id":"a"}
{"type":"hit","session_id":"b"}
`
	if err := os.WriteFile(filepath.Join(dir, "2026-10.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := jsonlFiles(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("jsonlFiles: %v %v", files, err)
	}
	n := 0
	err = forEachLine(files, func(string, int, []byte) { n++ })
	if err != nil || n != 3 {
		t.Fatalf("forEachLine saw %d lines (%v)", n, err)
	}
}

func TestReportValidate(t *testing.T) {
	good := `{"v":1,"transcript":"~/t.jsonl","client":"claude-code","rules_hash":"0123456789abcdef","items":3,"skipped_lines":0,
	"rules":[{"rule_id":"no-long-sleep","counts":{"known-hit":1},"labeled":1,"precision":1}],
	"samples":[{"id":"0123456789abcdef","rule_id":"no-long-sleep","class":"known-hit","event":"PreToolUse","ts":"2026-10-10T09:00:00Z","uuid":"u","label":"tp"}]}`
	f := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(f, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := loadReport(f)
	if err != nil {
		t.Fatal(err)
	}
	if bad := rep.validate(); len(bad) != 0 {
		t.Fatalf("good report rejected: %v", bad)
	}
	// Mutations the check must notice.
	for name, mut := range map[string]func(*reportDoc){
		"wrong version":      func(r *reportDoc) { r.V = 2 },
		"unknown class":      func(r *reportDoc) { r.Samples[0].Class = "maybe" },
		"orphan sample rule": func(r *reportDoc) { r.Samples[0].RuleID = "other-rule" },
		"label count":        func(r *reportDoc) { r.Rules[0].Labeled = 0 },
		"bad sample id":      func(r *reportDoc) { r.Samples[0].ID = "xyz" },
		"null samples":       func(r *reportDoc) { r.Samples = nil },
	} {
		rep, _ := loadReport(f)
		mut(rep)
		if len(rep.validate()) == 0 {
			t.Errorf("%s: validate accepted a broken report", name)
		}
	}
	// Strict decoding: unknown fields are rejected.
	if err := os.WriteFile(f, []byte(strings.Replace(good, `"v":1`, `"v":1,"extra":true`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReport(f); err == nil {
		t.Error("an unknown top-level field must be rejected")
	}
}

func TestCtlFilesSteerTheExitCode(t *testing.T) {
	dir := t.TempDir()
	if code := cmdCtl([]string{dir}); code != 0 {
		t.Fatalf("default exit = %d", code)
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdCtl([]string{dir}); code != 3 {
		t.Fatalf("exit file: %d", code)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if got := strings.Count(string(calls), "\n"); got != 2 {
		t.Errorf("calls logged = %d, want 2", got)
	}
}

func TestStubExitFollowsEnvironment(t *testing.T) {
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("ACC_STUB_LOG", log)
	t.Setenv("ACC_STUB_EXIT", "7")
	if code := runStub("go", []string{"test", "./..."}); code != 7 {
		t.Fatalf("stub exit = %d", code)
	}
	b, _ := os.ReadFile(log)
	if strings.TrimSpace(string(b)) != "go test ./..." {
		t.Errorf("stub log = %q", b)
	}
}

func TestGenTranscriptReachesTheRequestedSize(t *testing.T) {
	out := filepath.Join(t.TempDir(), "t.jsonl")
	if code := cmdGenTranscript([]string{out, "/work", "sess", "0.2"}); code != 0 {
		t.Fatal(code)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() < 200*1024 {
		t.Fatalf("size %v err %v", fi, err)
	}
}

// TestRunStatsReportPeakRSS runs this test binary as a child that touches
// 64 MiB and checks that "run -stats" reports a peak of at least 32 MiB. On
// Windows this exercises the K32GetProcessMemoryInfo probe, elsewhere rusage.
func TestRunStatsReportPeakRSS(t *testing.T) {
	stats := filepath.Join(t.TempDir(), "stats")
	t.Setenv("ACCUTIL_TEST_ALLOC", "1")
	code := cmdRun([]string{"-timeout", "60s", "-stats", stats, "--", os.Args[0], "-test.run=^TestAllocHelper$"})
	if code != 0 {
		t.Fatalf("run exited %d", code)
	}
	b, err := os.ReadFile(stats)
	if err != nil {
		t.Fatal(err)
	}
	var kb int64
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "rss_kb="); ok {
			kb, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if kb < 32*1024 {
		t.Fatalf("peak rss_kb = %d, want >= %d (stats: %q)", kb, 32*1024, b)
	}
}

// TestAllocHelper is the child of TestRunStatsReportPeakRSS; on its own it is
// a no-op.
func TestAllocHelper(t *testing.T) {
	if os.Getenv("ACCUTIL_TEST_ALLOC") != "1" {
		t.Skip("child helper only")
	}
	buf := make([]byte, 64<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	if buf[len(buf)-4096] != 1 {
		t.Fatal("unreachable")
	}
}
