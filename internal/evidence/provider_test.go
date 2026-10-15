package evidence

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
)

// countFS counts every call.
type countFS struct {
	sys.FS
	calls atomic.Int64
}

func (c *countFS) ReadFile(n string) ([]byte, error)       { c.calls.Add(1); return c.FS.ReadFile(n) }
func (c *countFS) Stat(n string) (fs.FileInfo, error)      { c.calls.Add(1); return c.FS.Stat(n) }
func (c *countFS) Lstat(n string) (fs.FileInfo, error)     { c.calls.Add(1); return c.FS.Lstat(n) }
func (c *countFS) ReadDir(n string) ([]fs.DirEntry, error) { c.calls.Add(1); return c.FS.ReadDir(n) }
func (c *countFS) Readlink(n string) (string, error)       { c.calls.Add(1); return c.FS.Readlink(n) }
func (c *countFS) EvalSymlinks(n string) (string, error)   { c.calls.Add(1); return c.FS.EvalSymlinks(n) }
func (c *countFS) MkdirAll(n string, p fs.FileMode) error  { c.calls.Add(1); return c.FS.MkdirAll(n, p) }
func (c *countFS) Rename(o, n string) error                { c.calls.Add(1); return c.FS.Rename(o, n) }
func (c *countFS) Remove(n string) error                   { c.calls.Add(1); return c.FS.Remove(n) }
func (c *countFS) RemoveAll(n string) error                { c.calls.Add(1); return c.FS.RemoveAll(n) }
func (c *countFS) OpenFile(n string, f int, p fs.FileMode) (sys.File, error) {
	c.calls.Add(1)
	return c.FS.OpenFile(n, f, p)
}

// countProject counts loader calls.
type countProject struct {
	calls atomic.Int64
	cfg   *model.ProjectConfig
	err   error
}

func (c *countProject) LoadProject(string) (*model.ProjectConfig, error) {
	c.calls.Add(1)
	return c.cfg, c.err
}

var _ ports.ProjectLoader = (*countProject)(nil)

func TestProviderImplementsPort(t *testing.T) {
	var _ ports.EvidenceProvider = (*Provider)(nil)
}

func TestOpenDoesNoIO(t *testing.T) {
	sb := newSandbox(t)
	cfs := &countFS{FS: sys.OSFS{}}
	cp := &countProject{}
	opt := sb.options(t, sys.NewFakeClock(epoch), &sys.SeqID{})
	opt.FS = cfs
	opt.Project = cp
	opt.Locator = &snapshot.Locator{FS: cfs}
	p := NewProvider(opt)
	repo := &model.RepoInfo{Toplevel: "/nonexistent/repo", GitDir: "/nonexistent/repo/.git", CommonDir: "/nonexistent/repo/.git", IndexPath: "/nonexistent/repo/.git/index", Inst: "abcdabcdabcdabcd"}
	for _, r := range []*model.RepoInfo{repo, nil} {
		v, err := p.Open(r, time.Now().Add(time.Second))
		if err != nil || v == nil {
			t.Fatalf("Open = %v, %v", v, err)
		}
	}
	if n := cfs.calls.Load(); n != 0 {
		t.Errorf("Open made %d FS calls", n)
	}
	if n := cp.calls.Load(); n != 0 {
		t.Errorf("Open loaded project.json %d times", n)
	}
	// NewProvider itself is lazy too.
	if n := cfs.calls.Load(); n != 0 {
		t.Errorf("FS calls after NewProvider: %d", n)
	}
}

func TestNilRepoView(t *testing.T) {
	p := NewProvider(Options{Layout: newSandbox(t).Layout(t)})
	v, _ := p.Open(nil, time.Time{})
	for _, k := range []model.FreshTargetKind{model.FreshCommit, model.FreshWorktree} {
		r := v.VerifyFresh(model.FreshQuery{Kind: k, Git: &model.GitInvocation{Sub: "commit"}})
		if r.Value != model.TriUnknown || r.Cause != "not_a_repo" {
			t.Errorf("%s: %+v", k, r)
		}
	}
	if tri, ts := v.LastFailureAfter(epoch); tri != model.TriUnknown || !ts.IsZero() {
		t.Errorf("LastFailureAfter = %v, %v", tri, ts)
	}
	if recs := v.FinishedSince(epoch); recs != nil {
		t.Errorf("FinishedSince = %v", recs)
	}
}

func fakeRepo(root string) *model.RepoInfo {
	return &model.RepoInfo{Toplevel: root, GitDir: filepath.Join(root, ".git"), CommonDir: filepath.Join(root, ".git"),
		IndexPath: filepath.Join(root, ".git", "index"), Inst: snapshot.RepoInst(root, filepath.Join(root, ".git")), ObjectFormat: "sha1"}
}

func TestProjectCauses(t *testing.T) {
	sb := newSandbox(t)
	root := filepath.Join(sb.root, "r")
	repo := fakeRepo(root)
	good := &model.ProjectConfig{Verify: []model.VerifySpec{{Name: "test", Argv: []string{"x"}, Required: true}}}
	tests := []struct {
		name  string
		proj  ports.ProjectLoader
		names []string
		cause string
	}{
		{"no loader", nil, nil, "no_project"},
		{"missing project.json", fakeProject{}, nil, "no_project"},
		{"invalid project.json", fakeProject{err: map[string]error{root: errors.New("bad")}}, nil, "project_invalid"},
		{"bad exempt glob", fakeProject{cfg: map[string]*model.ProjectConfig{root: {Verify: good.Verify, Exempt: []string{""}}}}, nil, "project_invalid"},
		{"no required entries", fakeProject{cfg: map[string]*model.ProjectConfig{root: {Verify: []model.VerifySpec{{Name: "lint", Argv: []string{"x"}}}}}}, nil, "no_required"},
		{"empty explicit names", fakeProject{cfg: map[string]*model.ProjectConfig{root: good}}, []string{}, "no_required"},
		{"empty verify list", fakeProject{cfg: map[string]*model.ProjectConfig{root: {}}}, nil, "no_required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opt := sb.options(t, sys.NewFakeClock(epoch), &sys.SeqID{})
			opt.Project = tc.proj
			v, _ := NewProvider(opt).Open(repo, time.Time{})
			r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree, Names: tc.names})
			if r.Value != model.TriUnknown || r.Cause != tc.cause {
				t.Errorf("got %+v, want unknown/%s", r, tc.cause)
			}
		})
	}
}

func TestProjectLoadedLazilyAndMemoized(t *testing.T) {
	sb := newSandbox(t)
	repo := fakeRepo(filepath.Join(sb.root, "r"))
	cp := &countProject{} // returns nil config: no_project
	opt := sb.options(t, sys.NewFakeClock(epoch), &sys.SeqID{})
	opt.Project = cp
	v, _ := NewProvider(opt).Open(repo, time.Time{})

	// LastFailureAfter and FinishedSince never read project.json.
	v.LastFailureAfter(epoch)
	v.FinishedSince(epoch)
	if cp.calls.Load() != 0 {
		t.Fatalf("project loaded by %d calls of record views", cp.calls.Load())
	}
	for i := 0; i < 3; i++ {
		v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	}
	if cp.calls.Load() != 1 {
		t.Errorf("project loaded %d times for three queries, want 1 (memoized per view)", cp.calls.Load())
	}
	// A new view starts fresh.
	v2, _ := NewProvider(opt).Open(repo, time.Time{})
	v2.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if cp.calls.Load() != 2 {
		t.Errorf("second view: %d loads", cp.calls.Load())
	}
}

func TestUnparseableAndBadKind(t *testing.T) {
	sb := newSandbox(t)
	opt := sb.options(t, sys.NewFakeClock(epoch), &sys.SeqID{})
	cp := &countProject{}
	opt.Project = cp
	v, _ := NewProvider(opt).Open(fakeRepo(filepath.Join(sb.root, "r")), time.Time{})
	for name, q := range map[string]model.FreshQuery{
		"nil git":      {Kind: model.FreshCommit},
		"interactive":  {Kind: model.FreshCommit, Git: &model.GitInvocation{Sub: "commit", Args: lits("-p")}},
		"not a commit": {Kind: model.FreshCommit, Git: &model.GitInvocation{Sub: "push"}},
		"unknown kind": {Kind: "bogus"},
		"relative -C":  {Kind: model.FreshCommit, Git: &model.GitInvocation{Sub: "commit", Cwd: "../x"}},
	} {
		r := v.VerifyFresh(q)
		if r.Value != model.TriUnknown || r.Cause != "unparseable_commit" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if cp.calls.Load() != 0 {
		t.Error("project.json loaded before the commit form was parsed")
	}
}

func TestTargetNotARepo(t *testing.T) {
	sb := newSandbox(t)
	opt := sb.options(t, sys.NewFakeClock(epoch), &sys.SeqID{})
	v, _ := NewProvider(opt).Open(fakeRepo(filepath.Join(sb.root, "r")), time.Time{})
	outside := filepath.Join(sb.root, "plain-dir")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshCommit, Cwd: sb.root, Git: &model.GitInvocation{Sub: "commit", Cwd: "plain-dir"}})
	if r.Value != model.TriUnknown || r.Cause != "not_a_repo" {
		t.Errorf("%+v", r)
	}
}

func TestSnapshotErrorsMapToCauses(t *testing.T) {
	requireGit(t)
	h := newHarness(t)
	repo := h.sb.locate(t, h.root)

	// Deadline already passed: fallback_timeout (checked before any I/O).
	v, _ := h.prov.Open(repo, h.clock.Now().Add(-time.Second))
	r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if r.Value != model.TriUnknown || r.Cause != "fallback_timeout" {
		t.Errorf("deadline: %+v", r)
	}

	// An unusable index: snapshot_error. (A garbage index first falls back
	// to git, which fails on it too.)
	bad := *repo
	bad.IndexPath = filepath.Join(h.sb.root, "garbage-index")
	writeFile(t, bad.IndexPath, "garbage")
	badGit := filepath.Join(h.sb.root, "badgit")
	if err := os.MkdirAll(badGit, 0o755); err != nil {
		t.Fatal(err)
	}
	bad.GitDir, bad.CommonDir = badGit, badGit
	v, _ = h.prov.Open(&bad, time.Time{})
	r = v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if r.Value != model.TriUnknown || r.Cause != "snapshot_error" {
		t.Errorf("bad index: %+v", r)
	}
}

func TestRunningJobBlocksBeforeSnapshot(t *testing.T) {
	// job_running must be decided before any snapshot work: the repo here has
	// no usable git directory at all.
	sb := newSandbox(t)
	root := filepath.Join(sb.root, "r")
	repo := fakeRepo(root)
	clock := sys.NewFakeClock(epoch)
	opt := sb.options(t, clock, &sys.SeqID{})
	opt.Project = fakeProject{cfg: map[string]*model.ProjectConfig{root: {Verify: []model.VerifySpec{
		{Name: "test", Argv: []string{"x"}, Required: true}, {Name: "e2e", Argv: []string{"x"}, Required: true}}}}}
	p := NewProvider(opt)
	now := clock.Now()
	if err := p.Store.WriteJob(&model.JobStatus{JobID: "20261016T120000-aaaaaa", Name: "e2e", RepoInst: repo.Inst,
		State: model.JobRunning, PID: os.Getpid(), StartedAt: now, Deadline: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	v, _ := p.Open(repo, time.Time{})
	r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if r.Value != model.TriUnknown || r.Cause != "job_running" || len(r.Running) != 1 || r.Running[0] != "e2e" || len(r.Names) != 2 {
		t.Fatalf("%+v", r)
	}
	// A job of another repo_inst or another name does not matter, and an
	// expired one neither.
	clock.Advance(2 * time.Hour)
	r = v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if r.Cause == "job_running" {
		t.Errorf("expired job still blocks: %+v", r)
	}
}

func TestLastFailureAfter(t *testing.T) {
	sb := newSandbox(t)
	clock := sys.NewFakeClock(epoch)
	opt := sb.options(t, clock, &sys.SeqID{})
	p := NewProvider(opt)
	repo := fakeRepo(filepath.Join(sb.root, "r"))
	other := fakeRepo(filepath.Join(sb.root, "o"))
	add := func(inst, name, job string, off int, o model.Outcome) {
		r := rec(inst, name, job, epoch.Add(time.Duration(off)*time.Minute), o, "t")
		if err := p.Store.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	add(repo.Inst, "test", "a", 0, model.OutcomePass)  // ends +1m
	add(repo.Inst, "test", "b", 10, model.OutcomeFail) // ends +11m
	add(repo.Inst, "lint", "c", 20, model.OutcomeTimeout)
	add(repo.Inst, "lint", "d", 30, model.OutcomeInfra)      // not a failure of the code under test
	add(repo.Inst, "lint", "e", 40, model.OutcomeSuperseded) // neither
	add(repo.Inst, "test", "f", 50, model.OutcomePass)
	add(other.Inst, "test", "g", 60, model.OutcomeFail) // another repo
	v, _ := p.Open(repo, time.Time{})

	tri, ts := v.LastFailureAfter(epoch)
	if tri != model.TriTrue || !ts.Equal(epoch.Add(21*time.Minute)) {
		t.Errorf("after epoch: %v %v", tri, ts)
	}
	tri, ts = v.LastFailureAfter(epoch.Add(21 * time.Minute))
	if tri != model.TriFalse || !ts.IsZero() {
		t.Errorf("after the last failure: %v %v", tri, ts)
	}
	tri, _ = v.LastFailureAfter(epoch.Add(5 * time.Minute))
	if tri != model.TriTrue {
		t.Errorf("mid: %v", tri)
	}
	recs := v.FinishedSince(epoch.Add(30 * time.Minute))
	if len(recs) != 3 || recs[0].JobID != "d" || recs[2].JobID != "f" {
		t.Errorf("FinishedSince = %+v", recs)
	}
	// A repo with no evidence at all: nothing failed.
	v2, _ := p.Open(fakeRepo(filepath.Join(sb.root, "empty")), time.Time{})
	if tri, _ := v2.LastFailureAfter(epoch); tri != model.TriFalse {
		t.Errorf("empty repo: %v", tri)
	}
}

func TestCorruptLatestRecordIsNotAPass(t *testing.T) {
	requireGit(t)
	h := newHarness(t)
	h.step("verify test pass")
	repo := h.sb.locate(t, h.root)
	// A corrupt newer file shadows the valid pass.
	dir := filepath.Join(h.opt.Layout.EvidenceDir, repo.Inst, "test")
	writeFile(t, filepath.Join(dir, "99999999999999999999-zz.json"), "{truncated")
	v, _ := h.prov.Open(repo, time.Time{})
	r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if r.Value != model.TriFalse || r.Cause != "not_valid_pass" {
		t.Errorf("%+v", r)
	}
}

func TestViewConcurrentUse(t *testing.T) {
	requireGit(t)
	h := newHarness(t)
	h.step("verify test pass")
	repo := h.sb.locate(t, h.root)
	v, _ := h.prov.Open(repo, time.Time{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				if r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree}); r.Value != model.TriTrue {
					t.Errorf("%+v", r)
				}
				v.LastFailureAfter(epoch)
				v.FinishedSince(epoch)
			}
		}()
	}
	wg.Wait()
}

func TestResultFields(t *testing.T) {
	requireGit(t)
	h := newHarness(t)
	h.step("verify test pass")
	repo := h.sb.locate(t, h.root)
	v, _ := h.prov.Open(repo, time.Time{})
	r := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree, Names: []string{"test", "lint", "ghost"}})
	if r.Value != model.TriFalse || r.Cause != "no_record" {
		t.Fatalf("%+v", r)
	}
	if got := r.Names; len(got) != 3 {
		t.Errorf("Names = %v", got)
	}
	if got := r.Stale; len(got) != 2 || got[0] != "lint" || got[1] != "ghost" {
		t.Errorf("Stale = %v (all non-fresh names, in query order)", got)
	}
	if r.C == "" {
		t.Error("C missing")
	}
	// Names is a copy.
	r.Names[0] = "mutated"
	r2 := v.VerifyFresh(model.FreshQuery{Kind: model.FreshWorktree})
	if r2.Names[0] != "test" {
		t.Errorf("shared slice: %v", r2.Names)
	}
}
