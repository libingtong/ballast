package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Promote gate defaults.
const (
	DefaultMinHits = 5
	DefaultWindow  = 7 * 24 * time.Hour
)

// PromoteOptions configures Promote.
type PromoteOptions struct {
	RuleID  string
	To      model.Action
	Force   bool
	MinHits int           // default 5
	Window  time.Duration // default 7d
	Clients []model.ClientID
}

// PromoteDeps are the collaborators of Promote.
type PromoteDeps struct {
	Layout   layout.Layout
	FS       sys.FS
	Clock    sys.Clock
	IDs      sys.IDGen
	Audit    ports.AuditSink
	Source   ports.AuditSource
	Compiler ports.RuleCompiler
	// CheckCapabilities is dsl.CheckCapabilities (injected).
	CheckCapabilities func(r *model.Rule, c model.ClientID) []model.Diagnostic
	// RepoRoot locates the repo layer file.
	RepoRoot string
}

// PromoteResult reports what happened.
type PromoteResult struct {
	File     string       `json:"file"`
	Layer    model.Layer  `json:"layer"`
	From     model.Action `json:"from"`
	To       model.Action `json:"to"`
	Forced   bool         `json:"forced"`
	GateHits int          `json:"gate_hits"`
	ReviewAt time.Time    `json:"review_at"`
}

// located is the rule being promoted and the file that defines it.
type located struct {
	layer model.Layer
	path  string
	data  []byte
	rule  model.Rule
}

// Promote performs `ballast rules promote`.
func Promote(opt PromoteOptions, deps PromoteDeps) (*PromoteResult, error) {
	if !validRuleID(opt.RuleID) {
		return nil, fmt.Errorf("%w: invalid rule id %q", ErrUsage, opt.RuleID)
	}
	if opt.To != model.ActionWarn && opt.To != model.ActionAsk && opt.To != model.ActionDeny {
		return nil, fmt.Errorf("%w: --to must be warn, ask or deny, got %q", ErrUsage, opt.To)
	}
	if deps.FS == nil || deps.Clock == nil || deps.IDs == nil || deps.Compiler == nil {
		return nil, errors.New("stats: promote: missing dependency (FS, Clock, IDs or Compiler)")
	}
	minHits := opt.MinHits
	if minHits <= 0 {
		minHits = DefaultMinHits
	}
	window := opt.Window
	if window <= 0 {
		window = DefaultWindow
	}
	now := deps.Clock.Now()

	loc, err := locate(opt.RuleID, deps)
	if err != nil {
		return nil, err
	}
	from := loc.rule.EffectiveAction()
	if opt.To.Severity() <= from.Severity() {
		return nil, fmt.Errorf("%w: --to %s is not stricter than the current action %s", ErrUsage, opt.To, from)
	}
	if err := checkCapabilities(loc.rule, opt.To, opt.Clients, deps.CheckCapabilities); err != nil {
		return nil, err
	}

	res := &PromoteResult{File: loc.path, Layer: loc.layer, From: from, To: opt.To, Forced: opt.Force}
	hits, hitsErr := countHits(deps.Source, opt.RuleID, now.Add(-window))
	res.GateHits = hits
	rvAt, rvErr := currentReview(deps, loc)
	res.ReviewAt = rvAt

	if !opt.Force {
		var why []string
		if hitsErr != nil {
			return nil, hitsErr
		}
		if hits < minHits {
			why = append(why, fmt.Sprintf("%d hits in the last %s, need at least %d", hits, window, minHits))
		}
		if rvErr != nil {
			why = append(why, rvErr.Error())
		}
		if len(why) > 0 {
			return nil, fmt.Errorf("%w: %s (review samples with `ballast stats --rule %s --sample N`, or use --force)", ErrGate, strings.Join(why, "; "), opt.RuleID)
		}
	}

	newData, err := RewriteRuleActions(loc.data, opt.RuleID, opt.To)
	if err != nil {
		return nil, err
	}
	if err := verifyRewrite(deps.Compiler, loc, newData, opt.RuleID, opt.To); err != nil {
		return nil, err
	}

	rec := auditRecord(now, loc, res)
	if opt.Force {
		// A forced promotion must never happen unaudited: record first.
		if err := appendAudit(deps.Audit, rec); err != nil {
			return nil, err
		}
	}
	perm := fs.FileMode(0o600)
	if st, err := deps.FS.Stat(loc.path); err == nil {
		perm = st.Mode().Perm()
	}
	if err := sys.WriteFileAtomic(deps.FS, deps.IDs, loc.path, newData, perm, false); err != nil {
		return nil, fmt.Errorf("stats: rewriting %s: %w", loc.path, err)
	}
	if !opt.Force {
		if err := appendAudit(deps.Audit, rec); err != nil {
			return res, err
		}
	}
	return res, nil
}

func appendAudit(sink ports.AuditSink, rec model.AuditRecord) error {
	if sink == nil {
		return errors.New("stats: promote: no audit sink")
	}
	if err := sink.Append([]model.AuditRecord{rec}); err != nil {
		return fmt.Errorf("stats: writing promote audit record: %w", err)
	}
	return nil
}

func auditRecord(now time.Time, loc *located, res *PromoteResult) model.AuditRecord {
	detail, _ := json.Marshal(map[string]any{
		"from":      res.From,
		"to":        res.To,
		"forced":    res.Forced,
		"gate_hits": res.GateHits,
		"review_at": res.ReviewAt,
		"file":      loc.path,
	})
	target := loc.rule.Target
	if target == model.ActionNone {
		target = model.DefaultTarget(loc.rule.Event)
	} else if target == res.From {
		target = res.To // RewriteRuleActions moves an explicit target equal to the old action
	}
	return model.AuditRecord{
		TS:        now.UTC(),
		V:         model.AuditVersion,
		Type:      model.AuditPromote,
		Event:     loc.rule.Event,
		RuleID:    loc.rule.ID,
		RuleLayer: loc.layer,
		Action:    res.To,
		Target:    target,
		Detail:    detail,
	}
}

// locate finds the rule in the org, repo or user layer (first definition in
// that order is the effective one); an org definition refuses promotion.
func locate(id string, deps PromoteDeps) (*located, error) {
	type layerFile struct {
		layer model.Layer
		path  string
	}
	files := []layerFile{{model.LayerOrg, deps.Layout.OrgRules}}
	if deps.RepoRoot != "" {
		files = append(files, layerFile{model.LayerRepo, layout.RepoRules(deps.RepoRoot)})
	}
	files = append(files, layerFile{model.LayerUser, deps.Layout.UserRules})

	for _, lf := range files {
		if lf.path == "" {
			continue
		}
		data, err := deps.FS.ReadFile(lf.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stats: reading %s: %w", lf.path, err)
		}
		rf, diags, err := deps.Compiler.ParseFile(lf.layer, lf.path, data)
		if err != nil {
			if lf.layer == model.LayerOrg {
				continue // an unreadable org file cannot shadow anything we can show
			}
			return nil, fmt.Errorf("stats: parsing %s: %w", lf.path, err)
		}
		if rf == nil {
			continue
		}
		for i := range rf.Rules {
			if rf.Rules[i].ID != id {
				continue
			}
			if lf.layer == model.LayerOrg {
				return nil, fmt.Errorf("%w: %q is defined in %s", ErrOrgRule, id, lf.path)
			}
			for _, d := range diags {
				if d.Severity == model.SevError {
					return nil, fmt.Errorf("stats: %s has errors (%s: %s); fix them before promoting", lf.path, d.Code, d.Message)
				}
			}
			return &located{layer: lf.layer, path: lf.path, data: data, rule: rf.Rules[i]}, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrRuleNotFound, id)
}

// checkCapabilities rejects a promotion that some installed client cannot
// execute. It is never skipped, not even by --force.
func checkCapabilities(r model.Rule, to model.Action, clients []model.ClientID, check func(*model.Rule, model.ClientID) []model.Diagnostic) error {
	if len(clients) == 0 {
		clients = []model.ClientID{model.ClientClaude}
	}
	var bad []string
	for _, c := range clients {
		if !r.AppliesTo(c) {
			continue
		}
		cand := r
		cand.Action = to
		if check != nil {
			for _, d := range check(&cand, c) {
				if d.Severity == model.SevError {
					bad = append(bad, fmt.Sprintf("%s: %s", c, d.Message))
				}
			}
			continue
		}
		if _, ok := model.Capabilities(c).Lookup(r.Event, to); !ok {
			bad = append(bad, fmt.Sprintf("%s: %s is not supported on %s", c, to, r.Event))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrCapability, strings.Join(bad, "; "))
	}
	return nil
}

// countHits counts hit records of the rule (effective or would_*) since.
func countHits(src ports.AuditSource, ruleID string, since time.Time) (int, error) {
	if src == nil {
		return 0, errors.New("stats: promote: no audit source")
	}
	n := 0
	_, err := src.Records(since, time.Time{}, func(rec *model.AuditRecord) error {
		if rec.Type == model.AuditHit && rec.RuleID == ruleID &&
			(isEffective(rec.Decision) || strings.HasPrefix(rec.Decision, "would_")) {
			n++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("stats: reading audit: %w", err)
	}
	return n, nil
}

// currentReview loads reviews/<rule>.json and checks that it is newer than
// the last change of the rule file (its mtime). The returned error describes
// why the credential does not count.
func currentReview(deps PromoteDeps, loc *located) (time.Time, error) {
	path := ReviewPath(deps.Layout, loc.rule.ID)
	data, err := deps.FS.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, errors.New("no review file")
		}
		return time.Time{}, fmt.Errorf("review file unreadable: %w", err)
	}
	var rv Review
	if err := json.Unmarshal(data, &rv); err != nil || rv.V != ReviewVersion || rv.RuleID != loc.rule.ID {
		return time.Time{}, errors.New("review file is invalid")
	}
	if len(rv.Samples) == 0 {
		return rv.ReviewedAt, errors.New("review file contains no samples")
	}
	st, err := deps.FS.Stat(loc.path)
	if err != nil {
		return rv.ReviewedAt, fmt.Errorf("cannot stat %s: %w", loc.path, err)
	}
	if !rv.ReviewedAt.After(st.ModTime()) {
		return rv.ReviewedAt, errors.New("review is older than the last change to the rule file")
	}
	return rv.ReviewedAt, nil
}

// verifyRewrite makes sure the rewritten file still decodes and carries the
// new action for the promoted rule (and only that rule changed its action).
func verifyRewrite(c ports.RuleCompiler, loc *located, newData []byte, id string, to model.Action) error {
	oldRF, _, err := c.ParseFile(loc.layer, loc.path, loc.data)
	if err != nil || oldRF == nil {
		return fmt.Errorf("stats: internal: original no longer parses: %v", err)
	}
	rf, diags, err := c.ParseFile(loc.layer, loc.path, newData)
	if err != nil || rf == nil {
		return fmt.Errorf("stats: rewritten rule file does not decode: %v", err)
	}
	for _, d := range diags {
		if d.Severity == model.SevError {
			return fmt.Errorf("stats: rewritten rule file is invalid (%s: %s)", d.Code, d.Message)
		}
	}
	if len(rf.Rules) != len(oldRF.Rules) {
		return errors.New("stats: rewritten rule file changed the number of rules")
	}
	for i := range rf.Rules {
		got, want := rf.Rules[i].EffectiveAction(), oldRF.Rules[i].EffectiveAction()
		if rf.Rules[i].ID == id {
			want = to
		}
		if rf.Rules[i].ID != oldRF.Rules[i].ID || got != want {
			return fmt.Errorf("stats: rewritten rule file differs unexpectedly at rule %q", rf.Rules[i].ID)
		}
	}
	return nil
}
