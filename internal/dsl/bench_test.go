package dsl

import (
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func benchRules(b *testing.B) (*Engine, []model.LoadedRule) {
	b.Helper()
	e, _ := newTestEngine(b)
	var lrs []model.LoadedRule
	for _, in := range schemaExamples(b) {
		f, err := DecodeRuleFile(in.Data)
		if err != nil {
			b.Fatal(err)
		}
		for i, r := range f.Rules {
			lrs = append(lrs, model.LoadedRule{Rule: r, Layer: model.LayerRepo, Index: i})
		}
	}
	return e, lrs
}

// BenchmarkCompileCold measures compiling a rule set (regexes stay lazy).
func BenchmarkCompileCold(b *testing.B) {
	_, lrs := benchRules(b)
	for i := 0; i < b.N; i++ {
		e, _ := newTestEngine(b)
		if _, err := e.Compile(lrs, model.Settings{}, "g"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateBash(b *testing.B) {
	e, lrs := benchRules(b)
	rs, err := e.Compile(lrs, model.Settings{}, "g")
	if err != nil {
		b.Fatal(err)
	}
	cl := bashList(nd("git", "commit", "-m", "x"))
	cl.Nodes[0].Git = &model.GitInvocation{Sub: "commit"}
	in := bashScn("git commit -m x", cl).input(b)
	for i := 0; i < b.N; i++ {
		if _, err := e.Evaluate(rs, in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateEditAndStop(b *testing.B) {
	e, lrs := benchRules(b)
	rs, _ := e.Compile(lrs, model.Settings{}, "g")
	edit := editScn("/repo/src/a.go").with(model.Fixture{Exists: []string{"src/a.go"}}).input(b)
	stop := scenario{native: "Stop", payload: `{"last_assistant_message":"All done, the build is green now. ` + repeat("lorem ipsum ", 100) + `"}`}.input(b)
	b.Run("edit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = e.Evaluate(rs, edit)
		}
	})
	b.Run("stop", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = e.Evaluate(rs, stop)
		}
	})
}
