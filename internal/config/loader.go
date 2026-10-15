package config

import (
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Options configures a Loader.
type Options struct {
	Layout   layout.Layout
	FS       sys.FS
	Clock    sys.Clock
	Compiler ports.RuleCompiler
	// Version participates in the cache key (cache invalidates on upgrade).
	Version string
	// NoCache disables reading/writing cache/ (tests, `config show`).
	NoCache bool
}

// Loader implements ports.RulesLoader.
type Loader struct {
	opt Options
}

var _ ports.RulesLoader = (*Loader)(nil)

// NewLoader returns a Loader.
func NewLoader(opt Options) *Loader { return &Loader{opt: opt} }

// Load implements ports.RulesLoader.
func (l *Loader) Load(req model.LoadRequest) (*model.CompiledRuleSet, error) {
	panic("unimplemented: config.Loader.Load")
}

// LayerPath is one layer's file location.
type LayerPath struct {
	Layer model.Layer
	Path  string
}

// LayerPaths returns the org, repo, user paths for a request (repo root =
// Repo.Toplevel, or Cwd when Repo is nil).
func LayerPaths(lay layout.Layout, req model.LoadRequest) []LayerPath {
	panic("unimplemented: config.LayerPaths")
}

// StatFile returns the StatKey of path (Exists=false when missing).
func StatFile(fsys sys.FS, path string) (model.StatKey, error) {
	panic("unimplemented: config.StatFile")
}

// LayerRules is one parsed layer (after LKG substitution).
type LayerRules struct {
	Source model.LayerSource
	File   *model.RuleFile // nil when missing or invalid without LKG
	Diags  []model.Diagnostic
}

// MergeResult is the outcome of Merge.
type MergeResult struct {
	Rules    []model.LoadedRule // enabled, ≤12, load order
	Settings model.Settings
	Notices  []model.ConfigNotice
	// All lists every rule seen with its fate, for --explain.
	All []RuleFate
}

// Fate of a rule during merge.
type Fate string

const (
	FateActive   Fate = "active"
	FateDisabled Fate = "disabled"
	FateShadowed Fate = "shadowed"
	FateDropped  Fate = "dropped"
)

// RuleFate pairs a rule with its merge outcome.
type RuleFate struct {
	Rule     model.LoadedRule
	Fate     Fate
	ShadowBy model.Layer // set when Fate == FateShadowed
}

// Merge applies the frozen merge semantics (package doc) to layers given in
// org, repo, user order. Pure function.
func Merge(layers []LayerRules) MergeResult {
	panic("unimplemented: config.Merge")
}

// ExplainReport backs `ballast config show --explain`.
type ExplainReport struct {
	Generation string               `json:"generation"`
	Sources    []model.LayerSource  `json:"sources"`
	Rules      []ExplainRule        `json:"rules"`
	Notices    []model.ConfigNotice `json:"notices,omitempty"`
}

// ExplainRule is one row of the explain table.
type ExplainRule struct {
	ID     string          `json:"id"`
	Layer  model.Layer     `json:"layer"`
	Source string          `json:"source"`
	Event  model.EventName `json:"event"`
	Action model.Action    `json:"action"`
	Target model.Action    `json:"target"`
	Fate   Fate            `json:"fate"`
	Note   string          `json:"note,omitempty"`
}

// Explain loads all layers WITHOUT the cache and reports every rule's fate.
func (l *Loader) Explain(req model.LoadRequest) (*ExplainReport, error) {
	panic("unimplemented: config.Loader.Explain")
}

// Generation computes rules_hash for a merged rule list + settings:
// hex(sha256(canonical JSON))[:16]. Canonical JSON = encoding/json of
// struct{Rules []model.Rule `json:"rules"`; Settings model.Settings
// `json:"settings"`} where each rule has Enabled/Action/Target defaults made
// explicit and Examples/Description set to nil/"" (they do not change
// behaviour). encoding/json map keys are sorted, so the encoding is stable.
func Generation(rules []model.Rule, settings model.Settings) string {
	panic("unimplemented: config.Generation")
}
