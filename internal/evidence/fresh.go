package evidence

import (
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/snapshot"
)

// projEntry is one project.json load, memoized per toplevel.
type projEntry struct {
	cfg    *model.ProjectConfig
	filter *snapshot.Filter
	cause  string // non-empty when the project cannot be used
}

// engine evaluates verify_fresh steps 3-6 against one target repo.
type engine struct {
	opt   Options
	store *Store

	mu       sync.Mutex
	projects map[string]*projEntry
}

func newEngine(opt Options, store *Store) *engine {
	return &engine{opt: opt, store: store, projects: map[string]*projEntry{}}
}

func unknown(cause string) model.FreshResult {
	return model.FreshResult{Value: model.TriUnknown, Cause: cause}
}

func (e *engine) locator() ports.RepoLocator {
	if e.opt.Locator != nil {
		return e.opt.Locator
	}
	return &snapshot.Locator{FS: e.opt.FS}
}

func (e *engine) snapOpts(deadline time.Time) snapshot.Options {
	return snapshot.Options{FS: e.opt.FS, Clock: e.opt.Clock, Deadline: deadline, AllowFallback: true, Env: e.opt.Env}
}

// project loads (once) the project.json of toplevel.
func (e *engine) project(toplevel string) *projEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pe, ok := e.projects[toplevel]; ok {
		return pe
	}
	pe := &projEntry{}
	switch {
	case e.opt.Project == nil:
		pe.cause = "no_project"
	default:
		cfg, err := e.opt.Project.LoadProject(toplevel)
		switch {
		case err != nil:
			pe.cause = "project_invalid"
		case cfg == nil:
			pe.cause = "no_project"
		default:
			pe.cfg = cfg
			f, ferr := snapshot.CompileExempt(cfg.Exempt)
			if ferr != nil {
				pe.cfg, pe.cause = nil, "project_invalid"
			} else {
				pe.filter = f
			}
		}
	}
	e.projects[toplevel] = pe
	return pe
}

// fresh runs steps 3-6 of the package doc for target. namesIn nil means
// every required entry. computeC receives the target's exempt filter.
func (e *engine) fresh(target *model.RepoInfo, namesIn []string, deadline time.Time, computeC func(f *snapshot.Filter) (model.TreeID, error)) model.FreshResult {
	pe := e.project(target.Toplevel)
	if pe.cause != "" {
		return unknown(pe.cause)
	}
	names := namesIn
	if names == nil {
		names = pe.cfg.Required()
	}
	if len(names) == 0 {
		return unknown("no_required")
	}
	res := model.FreshResult{Names: append([]string(nil), names...)}

	for _, n := range names {
		jobs, err := e.store.RunningJobs(target.Inst, n)
		if err == nil && len(jobs) > 0 {
			res.Running = append(res.Running, n)
		}
	}
	if len(res.Running) > 0 {
		res.Value, res.Cause = model.TriUnknown, "job_running"
		return res
	}

	c, err := computeC(pe.filter)
	if err != nil {
		res.Value = model.TriUnknown
		if errors.Is(err, model.ErrDeadline) {
			res.Cause = "fallback_timeout"
		} else {
			res.Cause = "snapshot_error"
		}
		return res
	}
	res.C = c

	for _, n := range names {
		cause := ""
		rec, err := e.store.Latest(target.Inst, n)
		switch {
		case err != nil:
			// Unreadable or corrupt evidence cannot vouch for anything.
			cause = "not_valid_pass"
		case rec == nil:
			cause = "no_record"
		case !rec.ValidPass():
			cause = "not_valid_pass"
		case rec.SEnd != c:
			cause = "stale"
		}
		if cause == "" {
			continue
		}
		res.Stale = append(res.Stale, n)
		if res.Cause == "" {
			res.Cause = cause
		}
	}
	if len(res.Stale) > 0 {
		res.Value = model.TriFalse
		return res
	}
	res.Value, res.Cause = model.TriTrue, "fresh"
	return res
}

// view implements model.EvidenceView for one event repo.
type view struct {
	opt      Options
	store    *Store
	eng      *engine
	repo     *model.RepoInfo
	deadline time.Time
}

var _ model.EvidenceView = (*view)(nil)

// VerifyFresh implements model.EvidenceView (package doc steps 1-6).
func (v *view) VerifyFresh(q model.FreshQuery) model.FreshResult {
	if v.repo == nil {
		return unknown("not_a_repo")
	}
	target := v.repo
	var form model.CommitForm
	switch q.Kind {
	case model.FreshCommit:
		form = ParseCommitForm(q.Git, q.Cwd)
		if form.Kind == model.CommitUnparseable {
			return unknown("unparseable_commit")
		}
		if form.Dir != "" {
			t, err := v.eng.locator().Locate(filepath.FromSlash(form.Dir))
			switch {
			case err == nil:
				if t.Inst != v.repo.Inst {
					target = t
				}
			case errors.Is(err, model.ErrNotRepo):
				return unknown("not_a_repo")
			default:
				return unknown("snapshot_error")
			}
		}
	case model.FreshWorktree, "":
	default:
		return unknown("unparseable_commit")
	}
	return v.eng.fresh(target, q.Names, v.deadline, func(f *snapshot.Filter) (model.TreeID, error) {
		if q.Kind == model.FreshCommit {
			return snapshot.TreeC(v.eng.snapOpts(v.deadline), target, form, f)
		}
		return snapshot.TreeS(v.eng.snapOpts(v.deadline), target, f)
	})
}

// LastFailureAfter implements model.EvidenceView.
func (v *view) LastFailureAfter(after time.Time) (model.Tri, time.Time) {
	if v.repo == nil {
		return model.TriUnknown, time.Time{}
	}
	recs, err := v.store.FinishedSince(v.repo.Inst, after)
	if err != nil {
		return model.TriUnknown, time.Time{}
	}
	var last time.Time
	for i := range recs {
		switch recs[i].Outcome {
		case model.OutcomeFail, model.OutcomeTimeout, model.OutcomeSignal:
			if recs[i].TEnd.After(last) {
				last = recs[i].TEnd
			}
		}
	}
	if last.IsZero() {
		return model.TriFalse, time.Time{}
	}
	return model.TriTrue, last
}

// FinishedSince implements model.EvidenceView.
func (v *view) FinishedSince(since time.Time) []model.VerifyRecord {
	if v.repo == nil {
		return nil
	}
	recs, err := v.store.FinishedSince(v.repo.Inst, since)
	if err != nil {
		return nil
	}
	return recs
}

func (p *Provider) open(repo *model.RepoInfo, deadline time.Time) *view {
	return &view{opt: p.opt, store: p.Store, eng: newEngine(p.opt, p.Store), repo: repo, deadline: deadline}
}

func check(opt Options, cwd string, co CheckOptions) (model.FreshResult, error) {
	opt = opt.withDefaults()
	mode := co.Mode
	if mode == "" {
		mode = CheckCommit
	}
	if mode != CheckCommit && mode != CheckWorktree {
		return model.FreshResult{}, errors.New("evidence: unknown check mode " + string(mode))
	}
	store := NewStore(opt)
	eng := newEngine(opt, store)
	if cwd == "" {
		return unknown("not_a_repo"), nil
	}
	repo, err := eng.locator().Locate(cwd)
	if err != nil {
		if errors.Is(err, model.ErrNotRepo) {
			return unknown("not_a_repo"), nil
		}
		return unknown("snapshot_error"), nil
	}
	return eng.fresh(repo, nil, time.Time{}, func(f *snapshot.Filter) (model.TreeID, error) {
		if mode == CheckWorktree {
			return snapshot.TreeS(eng.snapOpts(time.Time{}), repo, f)
		}
		r := *repo
		if co.IndexFile != "" {
			idx := co.IndexFile
			if !filepath.IsAbs(idx) {
				idx = filepath.Join(cwd, idx)
			}
			r.IndexPath = idx
		}
		return snapshot.TreeC(eng.snapOpts(time.Time{}), &r, model.CommitForm{Kind: model.CommitPlain}, f)
	}), nil
}
