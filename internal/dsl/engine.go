package dsl

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

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

// Engine implements ports.RuleCompiler and ports.RuleEvaluator. It is safe
// for concurrent use.
type Engine struct {
	opt Options

	// res caches lazily compiled regular expressions by source text.
	res  sync.Map
	resN atomic.Int64
}

var (
	_ ports.RuleCompiler  = (*Engine)(nil)
	_ ports.RuleEvaluator = (*Engine)(nil)
)

// NewEngine returns an Engine.
func NewEngine(opt Options) *Engine { return &Engine{opt: opt} }

// ParseFile implements ports.RuleCompiler: decode + Validate.
func (e *Engine) ParseFile(layer model.Layer, path string, data []byte) (*model.RuleFile, []model.Diagnostic, error) {
	f, ds, err := e.parse(path, data)
	if err != nil {
		return nil, ds, err
	}
	return f, ds, nil
}

// parse decodes and validates one file. On a decode failure it returns the
// diagnostics describing it together with the error.
func (e *Engine) parse(path string, data []byte) (*model.RuleFile, []model.Diagnostic, error) {
	f, err := DecodeRuleFile(data)
	if err != nil {
		var de *decodeError
		if errors.As(err, &de) {
			ds := make([]model.Diagnostic, len(de.diags))
			copy(ds, de.diags)
			for i := range ds {
				ds[i].File = path
			}
			return nil, ds, err
		}
		return nil, []model.Diagnostic{{Severity: model.SevError, Code: CodeSchema, File: path, Message: err.Error()}}, err
	}
	return f, e.Validate(path, f), nil
}

// Compile implements ports.RuleCompiler. rules must already be merged,
// enabled-only and capped by the caller (config). Defaults (action, target)
// are applied here. Disabled rules are skipped defensively; a rule whose
// event is not a rule event, or whose action/target is invalid, is an error.
func (e *Engine) Compile(rules []model.LoadedRule, settings model.Settings, generation string) (*model.CompiledRuleSet, error) {
	rs := &model.CompiledRuleSet{Generation: generation, Settings: settings}
	for i := range rules {
		lr := rules[i]
		r := &lr.Rule
		if !r.IsEnabled() {
			continue
		}
		if !r.Event.IsRuleEvent() {
			return nil, fmt.Errorf("dsl: rule %q: bad event %q", r.ID, r.Event)
		}
		if r.Action != model.ActionNone && !r.Action.Valid() {
			return nil, fmt.Errorf("dsl: rule %q: bad action %q", r.ID, r.Action)
		}
		if r.Target != model.ActionNone && !r.Target.Valid() {
			return nil, fmt.Errorf("dsl: rule %q: bad target %q", r.ID, r.Target)
		}
		target := r.Target
		if target == model.ActionNone {
			target = model.DefaultTarget(r.Event)
		}
		rs.Rules = append(rs.Rules, model.CompiledRule{
			LoadedRule: lr,
			Action:     r.EffectiveAction(),
			Target:     target,
			Program:    e.buildProgram(&lr.Rule),
		})
	}
	return rs, nil
}
