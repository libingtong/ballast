package evidence

import (
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Retention (plan B8).
const (
	RecordRetention = 30 * 24 * time.Hour
	JobRetention    = 7 * 24 * time.Hour
)

// Options configures the store and provider.
type Options struct {
	Layout  layout.Layout
	FS      sys.FS
	Clock   sys.Clock
	IDs     sys.IDGen
	Locator ports.RepoLocator // resolves git -C targets
	// Project loads <toplevel>/.ballast/project.json. Called lazily (first
	// view method that needs it), memoized per toplevel for the lifetime of
	// one view; for `git -C <dir>` targeting another repo the TARGET repo's
	// project.json (required names and exempt) is used.
	Project ports.ProjectLoader
}

// Store reads and writes verify records and job status files.
type Store struct {
	opt Options
}

// NewStore returns a Store.
func NewStore(opt Options) *Store { return &Store{opt: opt} }

// RecordPath returns the evidence file path for rec.
func (s *Store) RecordPath(rec *model.VerifyRecord) string {
	panic("unimplemented: evidence.Store.RecordPath")
}

// WriteRecord writes rec atomically WITH fsync.
func (s *Store) WriteRecord(rec *model.VerifyRecord) error {
	panic("unimplemented: evidence.Store.WriteRecord")
}

// Latest returns the latest record (by t_start) for name, or nil.
func (s *Store) Latest(repoInst, name string) (*model.VerifyRecord, error) {
	panic("unimplemented: evidence.Store.Latest")
}

// FinishedSince lists records of repoInst with t_end > since (all names).
func (s *Store) FinishedSince(repoInst string, since time.Time) ([]model.VerifyRecord, error) {
	panic("unimplemented: evidence.Store.FinishedSince")
}

// FindJob locates a job by id (any repo/name) for `verify --status <job>`.
func (s *Store) FindJob(jobID string) (*model.JobStatus, *model.VerifyRecord, error) {
	panic("unimplemented: evidence.Store.FindJob")
}

// WriteJob writes a JobStatus atomically.
func (s *Store) WriteJob(js *model.JobStatus) error {
	panic("unimplemented: evidence.Store.WriteJob")
}

// RunningJobs returns running jobs for (repoInst, name); dead PIDs or
// passed deadlines are reported as not running (and marked done/infra by GC).
func (s *Store) RunningJobs(repoInst, name string) ([]model.JobStatus, error) {
	panic("unimplemented: evidence.Store.RunningJobs")
}

// GC applies retention (package doc).
func (s *Store) GC() error {
	panic("unimplemented: evidence.Store.GC")
}

// ProcessAlive reports whether pid is alive (unix: kill(pid,0); windows:
// OpenProcess + GetExitCodeProcess == STILL_ACTIVE).
func ProcessAlive(pid int) bool {
	return processAlive(pid)
}

// ParseCommitForm turns a git commit invocation into a CommitForm (package doc).
func ParseCommitForm(git *model.GitInvocation, cwd string) model.CommitForm {
	panic("unimplemented: evidence.ParseCommitForm")
}

// Provider implements ports.EvidenceProvider.
type Provider struct {
	Store *Store
	opt   Options
}

var _ ports.EvidenceProvider = (*Provider)(nil)

// NewProvider returns a Provider.
func NewProvider(opt Options) *Provider { return &Provider{Store: NewStore(opt), opt: opt} }

// Open implements ports.EvidenceProvider. It does no I/O (no project.json
// read, no snapshot) until a method of the returned view is called.
func (p *Provider) Open(repo *model.RepoInfo, deadline time.Time) (model.EvidenceView, error) {
	panic("unimplemented: evidence.Provider.Open")
}

// CheckMode selects what `verify --check` compares against.
type CheckMode string

const (
	CheckCommit   CheckMode = "commit"   // C = F(index) (plain-commit form), default
	CheckWorktree CheckMode = "worktree" // C = S of the work tree
)

// CheckOptions configures Check.
type CheckOptions struct {
	Mode CheckMode // "" ⇒ CheckCommit
	// IndexFile, when non-empty and Mode == CheckCommit, replaces
	// RepoInfo.IndexPath: C = TreeC(repo with IndexPath = IndexFile,
	// CommitForm{Kind: plain}). The CLI sets it from GIT_INDEX_FILE (a
	// relative value is resolved against the process cwd, which git sets
	// to the work tree top level for hooks). Under a pre-commit hook git
	// points GIT_INDEX_FILE at the index it is about to commit
	// (.git/index.lock for `commit -a`, a temporary next-index for
	// `commit <paths>`/--only), so F(IndexFile) IS the to-be-committed tree
	// for every commit form. Observed with git 2.50 (architect, 2026-10-16):
	// plain commit ⇒ ".git/index" (relative) in the main worktree, absolute
	// <GitDir>/index in a linked worktree; `commit -a` ⇒ <GitDir>/index.lock;
	// `commit <paths>` ⇒ <GitDir>/next-index-<pid>.lock; the hook's cwd is
	// the committed repo's top level, also for `git -C <dir> commit`.
	// Ignored in CheckWorktree mode (S always uses the real <GitDir>/index
	// as its stat cache and tracked-file list).
	IndexFile string
}

// Check backs `ballast verify --check`; the CLI maps FreshResult to exit
// codes 0/10/11/12/13 (architecture §7). cwd selects the repo (git runs
// pre-commit hooks at the top level of the repo being committed, so for
// `git -C ../other commit` the check runs in ../other with ../other's
// project.json and evidence). Required names come from that repo's
// project.json (opt.Project); verify_fresh steps 2–5 of the package doc
// apply with C as described in CheckOptions.
func Check(opt Options, cwd string, co CheckOptions) (model.FreshResult, error) {
	panic("unimplemented: evidence.Check")
}
