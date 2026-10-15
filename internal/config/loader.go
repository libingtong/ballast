package config

import (
	"fmt"
	"io"
	"os"
	"sync"

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
	// NoCache disables reading/writing cache/ (tests, `config show`): the
	// stat index and the merged-rules cache are neither read nor written and
	// no LKG file is written. LKG files are still READ, so an invalid layer
	// is resolved exactly as in a cached run.
	NoCache bool
	// IDs names atomic-write temp files; nil means sys.RandomID.
	IDs sys.IDGen
	// Stderr receives load diagnostics (cache/LKG write failures, invalid
	// layers); nil means os.Stderr.
	Stderr io.Writer
}

// Loader implements ports.RulesLoader.
type Loader struct {
	opt Options
	mu  sync.Mutex // serializes diagnostics written to Options.Stderr
}

var _ ports.RulesLoader = (*Loader)(nil)

// NewLoader returns a Loader.
func NewLoader(opt Options) *Loader { return &Loader{opt: opt} }

// Load implements ports.RulesLoader.
func (l *Loader) Load(req model.LoadRequest) (*model.CompiledRuleSet, error) {
	if l.opt.Compiler == nil {
		return nil, fmt.Errorf("config: no rule compiler configured")
	}
	paths, stats, err := l.statLayers(req)
	if err != nil {
		return nil, err
	}
	var key string
	var idx statIndex
	if !l.opt.NoCache {
		key = cacheKey(l.opt.Version, stats)
		idx = l.readStatIndex()
		if rs, ok := l.cacheLookup(idx, key, stats); ok {
			return rs, nil
		}
	}
	layers, err := l.readLayers(paths, stats, loadMode{writeLKG: !l.opt.NoCache, report: true})
	if err != nil {
		return nil, err
	}
	res := Merge(layers)
	rules := make([]model.Rule, len(res.Rules))
	for i := range res.Rules {
		rules[i] = res.Rules[i].Rule
	}
	gen := Generation(rules, res.Settings)
	sources := make([]model.LayerSource, len(layers))
	for i := range layers {
		sources[i] = layers[i].Source
	}
	rs, err := l.compile(res.Rules, res.Settings, gen, sources, res.Notices)
	if err != nil {
		return nil, err
	}
	if !l.opt.NoCache {
		l.cacheStore(idx, key, res, gen, sources)
	}
	return rs, nil
}

// compile runs the injected compiler and stamps the merge outputs onto the
// result, so that fresh and cached loads are indistinguishable.
func (l *Loader) compile(rules []model.LoadedRule, settings model.Settings, gen string, sources []model.LayerSource, notices []model.ConfigNotice) (*model.CompiledRuleSet, error) {
	rs, err := l.opt.Compiler.Compile(rules, settings, gen)
	if err != nil {
		return nil, fmt.Errorf("config: compile: %w", err)
	}
	if rs == nil {
		return nil, fmt.Errorf("config: compiler returned no rule set")
	}
	rs.Generation = gen
	rs.Settings = settings
	rs.Sources = sources
	rs.Notices = notices
	return rs, nil
}

func (l *Loader) stderr() io.Writer {
	if l.opt.Stderr != nil {
		return l.opt.Stderr
	}
	return os.Stderr
}

func (l *Loader) diag(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.stderr(), "ballast: config: "+format+"\n", args...)
}

// LayerPath is one layer's file location.
type LayerPath struct {
	Layer model.Layer
	Path  string
}

// LayerPaths returns the org, repo, user paths for a request (repo root =
// Repo.Toplevel, or Cwd when Repo is nil).
func LayerPaths(lay layout.Layout, req model.LoadRequest) []LayerPath {
	root := req.Cwd
	if req.Repo != nil && req.Repo.Toplevel != "" {
		root = req.Repo.Toplevel
	}
	repo := ""
	if root != "" {
		repo = layout.RepoRules(root)
	}
	return []LayerPath{
		{Layer: model.LayerOrg, Path: lay.OrgRules},
		{Layer: model.LayerRepo, Path: repo},
		{Layer: model.LayerUser, Path: lay.UserRules},
	}
}

// StatFile returns the StatKey of path (Exists=false when missing).
func StatFile(fsys sys.FS, path string) (model.StatKey, error) {
	key := model.StatKey{Path: path}
	if path == "" {
		return key, nil
	}
	fi, err := fsys.Stat(path)
	if err != nil {
		if isNotExist(err) {
			return key, nil
		}
		return key, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return key, fmt.Errorf("config: %s is a directory", path)
	}
	key.Exists = true
	key.Size = fi.Size()
	key.MtimeNS = fi.ModTime().UnixNano()
	key.Inode = inodeOf(fi)
	return key, nil
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
	return merge(layers)
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
	paths, stats, err := l.statLayers(req)
	if err != nil {
		return nil, err
	}
	layers, err := l.readLayers(paths, stats, loadMode{})
	if err != nil {
		return nil, err
	}
	res := Merge(layers)
	rules := make([]model.Rule, len(res.Rules))
	for i := range res.Rules {
		rules[i] = res.Rules[i].Rule
	}
	rep := &ExplainReport{
		Generation: Generation(rules, res.Settings),
		Notices:    res.Notices,
	}
	for i := range layers {
		rep.Sources = append(rep.Sources, layers[i].Source)
	}
	for _, f := range res.All {
		r := &f.Rule.Rule
		target := r.Target
		if target == model.ActionNone {
			target = model.DefaultTarget(r.Event)
		}
		rep.Rules = append(rep.Rules, ExplainRule{
			ID:     r.ID,
			Layer:  f.Rule.Layer,
			Source: f.Rule.Source,
			Event:  r.Event,
			Action: r.EffectiveAction(),
			Target: target,
			Fate:   f.Fate,
			Note:   fateNote(f),
		})
	}
	return rep, nil
}

// Generation computes rules_hash for a merged rule list + settings:
// hex(sha256(canonical JSON))[:16]. Canonical JSON = encoding/json of
// struct{Rules []model.Rule `json:"rules"`; Settings model.Settings
// `json:"settings"`} where each rule has Enabled/Action/Target defaults made
// explicit and Examples/Description set to nil/"" (they do not change
// behaviour). encoding/json map keys are sorted, so the encoding is stable.
func Generation(rules []model.Rule, settings model.Settings) string {
	return generation(rules, settings)
}
