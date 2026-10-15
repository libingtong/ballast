package dsl

import "github.com/libingtong/ballast/internal/model"

// TestCase is the result of one example.
type TestCase struct {
	File    string           `json:"file"`
	RuleID  string           `json:"rule_id"`
	Example string           `json:"example"` // name or "#<index>"
	Client  model.ClientID   `json:"client"`
	Expect  model.ExpectKind `json:"expect"`
	Got     model.Tri        `json:"got"`
	Pass    bool             `json:"pass"`
	Detail  string           `json:"detail,omitempty"` // UnknownCause or error
}

// TestReport aggregates test-rules results.
type TestReport struct {
	Cases  []TestCase `json:"cases"`
	Passed int        `json:"passed"`
	Failed int        `json:"failed"`
}

// TestRules runs every example of every rule in the given files. Each
// example is evaluated in isolation: its rule alone (as a one-rule
// CompiledRuleSet, action forced to deny so capability degradation cannot
// mask a hit), the example input parsed by the real adapter of
// Example.Client, and FixtureEnv for predicates. Files are decoded and
// validated first; invalid files produce failed cases, not an error.
func (e *Engine) TestRules(inputs []LintInput) (*TestReport, error) {
	panic("unimplemented: dsl.Engine.TestRules")
}

// FixtureEnv builds fake SessionState / EvidenceView / WorldView from a
// fixture (exported so other units' tests can reuse it).
func FixtureEnv(fx *model.Fixture, ctx model.PathContext) (model.SessionState, model.EvidenceView, model.WorldView) {
	panic("unimplemented: dsl.FixtureEnv")
}
