package dsl

import (
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// SchemaVersion is the only accepted RuleFile.Version.
const SchemaVersion = 1

// Options wires the engine's collaborators.
type Options struct {
	Paths ports.PathService
	// Bash/PS are used by TestRules to build EvalInput.Commands; Evaluate
	// uses EvalInput.Commands as given.
	Bash ports.BashParser
	PS   ports.PowerShellParser
	// Adapters parse example inputs (TestRules) through the real client adapters.
	Adapters ports.AdapterRegistry
	// GOOS controls case folding for path globs; "" ⇒ runtime.GOOS.
	GOOS string
}

// Engine implements ports.RuleCompiler and ports.RuleEvaluator.
type Engine struct {
	opt Options
}

var (
	_ ports.RuleCompiler  = (*Engine)(nil)
	_ ports.RuleEvaluator = (*Engine)(nil)
)

// NewEngine returns an Engine.
func NewEngine(opt Options) *Engine { return &Engine{opt: opt} }

// DecodeRuleFile strictly decodes data (unknown fields are errors) without
// semantic validation.
func DecodeRuleFile(data []byte) (*model.RuleFile, error) {
	panic("unimplemented: dsl.DecodeRuleFile")
}

// ParseFile implements ports.RuleCompiler: decode + Validate.
func (e *Engine) ParseFile(layer model.Layer, path string, data []byte) (*model.RuleFile, []model.Diagnostic, error) {
	panic("unimplemented: dsl.Engine.ParseFile")
}

// Validate performs the per-file semantic checks that do not depend on
// clients or other layers (ids, events, actions, applicability, regex/glob
// compile, Cond arity, Cmp non-empty, text budgets, examples presence).
func (e *Engine) Validate(path string, f *model.RuleFile) []model.Diagnostic {
	panic("unimplemented: dsl.Engine.Validate")
}

// Compile implements ports.RuleCompiler. rules must already be merged,
// enabled-only and capped by the caller (config). Defaults (action, target)
// are applied here.
func (e *Engine) Compile(rules []model.LoadedRule, settings model.Settings, generation string) (*model.CompiledRuleSet, error) {
	panic("unimplemented: dsl.Engine.Compile")
}

// Evaluate implements ports.RuleEvaluator.
func (e *Engine) Evaluate(rs *model.CompiledRuleSet, in *model.EvalInput) (*model.Verdict, error) {
	panic("unimplemented: dsl.Engine.Evaluate")
}

// Arbitrate combines per-rule hits into a Verdict for client c (exported
// for replay, which re-arbitrates recorded hits).
func Arbitrate(c model.ClientID, ev model.EventName, results []model.RuleResult, hits []model.RuleHit) *model.Verdict {
	panic("unimplemented: dsl.Arbitrate")
}
