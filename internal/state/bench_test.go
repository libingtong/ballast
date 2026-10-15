package state

import (
	"fmt"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// Budgets (architecture §9): lock-free read ≤ 0.2ms, transaction ≤ 1ms.
func benchStore(b *testing.B) *Store {
	b.Helper()
	lay := testenv.New(b).Layout(b)
	s := New(Options{Layout: lay, Clock: sys.RealClock{}})
	for i := 0; i < 200; i++ {
		d := model.StateDelta{DedupeKey: fmt.Sprint("PreToolUse:", i), ToolCall: true, ReadPaths: []string{fmt.Sprint("/repo/src/file", i, ".go")},
			Summary: &model.EventSummary{Text: "Bash: git status"}}
		if _, err := s.Commit(key, &d); err != nil {
			b.Fatal(err)
		}
	}
	return s
}

func BenchmarkLoad(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.Load(key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCommit(b *testing.B) {
	s := benchStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := model.StateDelta{DedupeKey: fmt.Sprint("PostToolUse:", i), ToolCall: true, ReadPaths: []string{"/repo/src/a.go"}, HitRules: []string{"r"}}
		if _, err := s.Commit(key, &d); err != nil {
			b.Fatal(err)
		}
	}
}
