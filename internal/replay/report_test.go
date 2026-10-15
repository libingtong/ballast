package replay

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func goldenReport() *Report {
	p := 0.75
	return &Report{
		V: 1, Transcript: "~/.claude/projects/p/s.jsonl", Client: model.ClientClaude, RulesHash: "abc123",
		Items: 120, SkippedLines: 2,
		Rules: []RuleSummary{
			{RuleID: "read-before-edit", Counts: map[Class]int{KnownHit: 4, KnownMiss: 90, Approximate: 0, Unknown: 1}, Labeled: 4, Precision: &p},
			{RuleID: "claims-done", Counts: map[Class]int{Approximate: 3}},
			{RuleID: "quiet-rule", Counts: map[Class]int{KnownMiss: 7}},
		},
		Samples: []Sample{
			{ID: "0123456789abcdef", RuleID: "read-before-edit", Class: KnownHit, Event: model.EventPreToolUse, TS: time.Date(2026, 10, 10, 9, 0, 4, 0, time.UTC), UUID: "u004", Matched: "path: /repo/a.go | b", Label: "tp"},
			{ID: "fedcba9876543210", RuleID: "read-before-edit", Class: Unknown, Event: model.EventPreToolUse, UUID: "line-9", UnknownCause: "dynamic\nargument"},
			{ID: "1111222233334444", RuleID: "claims-done", Class: Approximate, Event: model.EventStop, TS: time.Date(2026, 10, 10, 9, 5, 0, 0, time.UTC), UUID: "u050#1", Matched: "all done"},
		},
	}
}

func TestRenderMarkdownGolden(t *testing.T) {
	got := RenderMarkdown(goldenReport())
	path := filepath.Join("testdata", "report.golden.md")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("markdown differs from %s:\n%s", path, got)
	}
	// Deterministic.
	if string(RenderMarkdown(goldenReport())) != string(got) {
		t.Error("rendering is not deterministic")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	out := string(RenderMarkdown(&Report{V: 1, Transcript: "x", Client: model.ClientCodex}))
	for _, want := range []string{"# Ballast replay report", "_No rules._", "_No samples._"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestRunMemoryOn40MB replays a synthetic 40MB transcript and checks that
// the live heap stays far below the 200MB acceptance bound (streaming).
func TestRunMemoryOn40MB(t *testing.T) {
	if testing.Short() {
		t.Skip("40MB replay skipped with -short")
	}
	e := newEnv(t, rset("g", readBeforeEdit()))
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.dir, "big.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	x := newTx("big", e.workdir)
	out := strings.Repeat("0123456789abcdef", 512) // 8 KiB tool output
	var written int64
	for i := 0; written < 40<<20; i++ {
		x.lines = x.lines[:0]
		id := "toolu_" + string(rune('a'+i%26)) + strings.Repeat("x", i%7)
		if i%50 == 0 {
			x.user("prompt number")
		}
		file := e.workdir + "/f" + string(rune('a'+i%26)) + ".go"
		if i%3 == 0 {
			x.tool(id, "Edit", map[string]any{"file_path": file})
		} else {
			x.tool(id, "Read", map[string]any{"file_path": file})
		}
		x.result(id, out, false)
		x.text(out[:1024])
		n, err := f.Write(x.bytes())
		if err != nil {
			t.Fatal(err)
		}
		written += int64(n)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}
		}
	}()
	rep, err := Run(Options{Transcript: path}, e.deps)
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if rep.Items < 1000 {
		t.Fatalf("only %d items replayed", rep.Items)
	}
	const limit = 200 << 20
	if p := peak.Load(); p > limit {
		t.Errorf("peak heap %d MiB exceeds %d MiB", p>>20, limit>>20)
	} else {
		t.Logf("replayed %d items from %d MiB, peak heap %d MiB", rep.Items, written>>20, p>>20)
	}
}
