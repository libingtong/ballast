package dsl

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func schemaExamples(t testing.TB) []LintInput {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "schema", "examples", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no schema examples found: %v", err)
	}
	sort.Strings(files)
	var out []LintInput
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, LintInput{Path: filepath.Base(f), Layer: model.LayerFile, Data: data})
	}
	return out
}

// TestSchemaExamplesTestRules runs every example of schema/examples/* through
// the fake adapter, path service and canned shell parsers. Non-claude-code
// examples are skipped (they need the real adapters; U12 runs them).
func TestSchemaExamplesTestRules(t *testing.T) {
	e, canned := newTestEngine(t)
	rep, err := e.TestRules(schemaExamples(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Cases {
		if !c.Pass && !c.Skipped {
			t.Errorf("%s %s/%s [%s]: expected %s got %s (%s)", c.File, c.RuleID, c.Example, c.Client, c.Expect, c.Got, c.Detail)
		}
	}
	canned.assertNoMisses(t)
	if rep.Failed != 0 {
		t.Errorf("failed=%d", rep.Failed)
	}
	if rep.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the codex apply_patch example of 01-read-before-edit)", rep.Skipped)
	}
	if rep.Passed < 40 {
		t.Errorf("passed = %d, expected at least 40 claude-code examples", rep.Passed)
	}
}

func TestSchemaExamplesLint(t *testing.T) {
	e, _ := newTestEngine(t)
	ds := e.Lint(schemaExamples(t), LintOptions{Clients: []model.ClientID{model.ClientClaude}, RunExamples: true})
	for _, d := range ds {
		if d.Severity == model.SevError {
			t.Errorf("unexpected lint error: %+v", d)
		}
	}
}
