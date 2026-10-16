package runner

import (
	"errors"

	"github.com/libingtong/ballast/internal/model"
)

// P2 contract v1.5 (docs/p2-design.md §3.4 "job 私有树"): an origin=hook job
// never runs in the real work tree. The runner materializes the S_start tree
// into a private directory under ~/.ballast (agent-unwritable outside the
// sandbox: ~/.ballast is in the managed denyWrite and permissions.deny), the
// sandboxed test runs in that copy, and S_end is recomputed on the copy.
// Untracked or ignored files of the real work tree reach the copy only
// through project.json verify[].inputs_untracked (default empty), and
// concurrent edits of the real work tree cannot influence the run (no
// untracked TestMain / conftest.py, no A-B-A swap between S_start and S_end).
// origin=cli runs (T0, or a human's --foreground) keep running in the work
// tree. Bodies owned by unit P2-RUNNER.

// Errors recorded as outcome infra (VerifyRecord.Error is the text after
// "runner: ").
var (
	// ErrTreeSubmodule: T_full contains a gitlink (mode 160000).
	// checkout-index does not populate submodule contents, so the copy would
	// not be the tree the tests expect; frozen handling: infra, test not run
	// (docs/p2-design.md §11; materializing submodules is a possible later
	// contract addition).
	ErrTreeSubmodule = errors.New("runner: tree_has_submodule")
	// ErrInputsTooLarge: inputs_untracked matched more than
	// MaxUntrackedInputFiles files or MaxUntrackedInputBytes bytes.
	ErrInputsTooLarge = errors.New("runner: inputs_too_large")
	// ErrTreeUnsafePath (v1.5.1): the refreshed index (which the agent can
	// shape: skip-worktree / assume-unchanged entries are not refreshed by
	// `git add -u`) contains a path layout whose checkout would depend on
	// git's leading-path protection to stay inside Dir — see step 1a.
	// Checked BEFORE checkout-index runs; test not run.
	ErrTreeUnsafePath = errors.New("runner: tree_unsafe_path")
	// ErrInputsConflict (v1.5.1): an inputs_untracked file could not be
	// placed at its relative path under Dir without passing through
	// something other than a real directory created by the checkout or by
	// the copy itself — see step 3. The whole job fails closed (visible),
	// it is never "skipped" silently, because a skipped input changes what
	// the test sees.
	ErrInputsConflict = errors.New("runner: inputs_path_conflict")
	// ErrMaterialize: any other materialization failure (git child failed,
	// copy failed, ResolveGitDirs rejected the repo's git dirs).
	ErrMaterialize = errors.New("runner: materialize_failed")
)

// Limits of the inputs_untracked copy (fail closed and visible).
const (
	MaxUntrackedInputFiles = 500000
	MaxUntrackedInputBytes = int64(8) << 30
)

// Tree is one materialized job tree.
type Tree struct {
	// Dir is <layout.WorkDir>/<job_id>/tree (absolute; sandbox.Spec.Tree).
	Dir string
	// Root is <layout.WorkDir>/<job_id> (holds the two temporary index files;
	// removed by CleanupTree).
	Root string
	// Full is T_full: the whole refreshed tree, exempt paths included (this
	// is what Dir contains).
	Full model.TreeID
	// SStart is T_full with the project.json exempt globs removed — by
	// construction the value snapshot.TreeS would have returned for the real
	// work tree at the moment it was read (same algorithm as the snapshot
	// fallback), so freshness (S_end == C) keeps its meaning.
	SStart model.TreeID
	// InputsHash is VerifyRecord.InputsHash ("" without inputs_untracked).
	InputsHash string
	// Inputs is the number of untracked input files copied.
	Inputs int
}

// Materialize builds the private tree of job jobID for repo, frozen algorithm
// (docs/p2-design.md §3.4 "job 私有树"; every git child gets a sys.Env
// filtered by sandbox.HostEnv, GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE set
// explicitly, and -c core.fsmonitor=false). Materialize runs OUTSIDE every
// sandbox and Dir lies inside ~/.ballast, which the runner can write: every
// write it makes must therefore be confined to Dir by construction, not by
// trusting the (agent-shaped) index or work tree (v1.5.1):
//
//  0. sandbox.ResolveGitDirs(repo.Toplevel) must return repo.GitDir and
//     repo.CommonDir (provenance, as for the sandbox Spec) ⇒ else
//     ErrMaterialize; Root = <WorkDir>/<jobID> is created 0700 (WorkDir
//     0700).
//  1. Root/index = copy of repo.IndexPath; with GIT_INDEX_FILE=Root/index:
//     `git add -u`; Full = `git write-tree`; any index entry with mode 160000
//     ⇒ ErrTreeSubmodule; `git rm --cached -r -q --ignore-unmatch --
//     ':(glob)<exempt>'...`; SStart = `git write-tree`. The work tree is read
//     ONCE (by add -u); every blob is what was read, so the copy and SStart
//     describe one consistent set of bytes whatever the agent does
//     concurrently.
//     1a. (v1.5.1) Path-layout pre-check over the entries of Full (the same
//     `ls-files -s` / index scan as the gitlink check; pure function, no
//     filesystem access) ⇒ ErrTreeUnsafePath, before any checkout:
//     (i) a symlink entry (mode 120000) whose path is, compared component
//     by component with strings.EqualFold, a proper prefix of another
//     entry's path (on a case-insensitive file system a checkout would
//     otherwise have to write "through" the link; checked on every
//     platform because the check is about the index, not the host);
//     (ii) any path component that is empty, "." or "..", or equal under
//     EqualFold to ".git" (git refuses these too; checked again so the
//     guarantee does not rest on one git version). Plain case collisions
//     between non-symlink entries are NOT rejected (on a case-insensitive
//     file system one overwrites the other INSIDE Dir; S_end then differs
//     from S_start — visible, not an escape). Unicode-normalization
//     prefixes (NFC vs NFD on APFS/HFS+) are NOT covered by this check and
//     stay with git's own protection — registered in docs/p2-design.md §11.
//  2. Root/index.tree: GIT_INDEX_FILE=Root/index.tree `git read-tree <Full>`
//     then `git -c core.protectHFS=true -c core.protectNTFS=true
//     checkout-index -a -f --prefix=<Dir>/` (Dir created 0700). Dir
//     contains no .git. This git child runs outside the sandbox on
//     agent-influenced index content; staying inside Dir relies on git's
//     leading-path (symlink) protection, narrowed by step 1a and
//     registered as residual risk in docs/p2-design.md §11 (running it in a
//     W=[Tree] sandbox is a possible later contract addition).
//  3. spec.InputsUntracked non-empty: walk repo.Toplevel (Lstat, never
//     following symlinks, never entering the real .git), select every file or
//     symlink matching one of the globs that is NOT an entry of Full, copy it
//     to the same relative path under Dir (regular files: content and exec
//     bit, hashed while copying; symlinks: as symlinks, target bytes hashed);
//     hard links are NOT allowed (a write through a hard link would reach
//     the real file). Placement rules (v1.5.1, frozen; Dir may already hold
//     symlinks checked out from Full, including entries that `git add -u`
//     did not refresh):
//     (a) every write goes through one os.Root opened on Dir
//     (os.OpenRoot; Mkdir / OpenFile / Symlink / Lstat on the Root), so no
//     path — via "..", an absolute symlink or a relative one — can resolve
//     outside Dir even if a rule below were implemented wrongly;
//     (b) if any proper prefix of the input path is an entry of Full that is
//     not a tree (blob, symlink or gitlink) ⇒ ErrInputsConflict;
//     (c) each parent level, from Dir downwards, is checked with Root.Lstat
//     (never following): absent ⇒ Root.Mkdir (0755); a real directory (not
//     a symlink) ⇒ continue; anything else (symlink, regular file, other)
//     ⇒ ErrInputsConflict. Nothing else writes Dir between step 2 and the
//     test start (~/.ballast is agent-unwritable, the test has not
//     started), so Lstat-then-act is not racy here; an implementation may
//     equally walk with openat(O_NOFOLLOW|O_DIRECTORY) per level — the
//     accepted / rejected set must be identical;
//     (d) the leaf is created exclusively and without following: regular
//     files with O_CREATE|O_EXCL|O_WRONLY (|O_NOFOLLOW where available),
//     symlinks with Root.Symlink; "already exists" (e.g. an untracked
//     "Foo" next to a tracked "foo" on a case-insensitive file system) ⇒
//     ErrInputsConflict. A copy-on-write clone is allowed only relative to
//     the verified parent directory fd and with no-follow semantics
//     (clonefileat(..., CLONE_NOFOLLOW) / FICLONE on a file just created
//     per (d)); otherwise copy bytes.
//     Limits ⇒ ErrInputsTooLarge. InputsHash per model.VerifyRecord.InputsHash.
//
// On any error Root is removed before returning (CleanupTree semantics: never
// following symlinks). Not hot path.
func Materialize(opt Options, repo model.RepoInfo, jobID string, spec model.VerifySpec, exempt []string) (Tree, error) {
	panic("unimplemented: runner.Materialize (unit P2-RUNNER)")
}

// TreeSEnd recomputes S on the copy after the run: with GIT_INDEX_FILE =
// t.Root/index.tree, GIT_WORK_TREE = t.Dir and GIT_DIR = repo.GitDir:
// `git add -u`, `git rm --cached -r -q --ignore-unmatch -- ':(glob)<exempt>'...`,
// `git write-tree`. A test that rewrote tracked files of the copy therefore
// yields S_end != S_start (not a valid pass); files the test created are
// untracked and ignored, as in the work tree. Frozen order around it
// (docs/p2-design.md §3.4 "收尾顺序"): child exited (darwin: the runner then
// SIGKILLs the child's session/process group; linux: bwrap's PID namespace is
// already gone) → sandbox.PostCheck → sandbox.Quarantine (when found) →
// TreeSEnd → record written with fsync → audit → CleanupTree.
func TreeSEnd(opt Options, repo model.RepoInfo, t Tree, exempt []string) (model.TreeID, error) {
	panic("unimplemented: runner.TreeSEnd (unit P2-RUNNER)")
}

// CleanupTree removes t.Root (RemoveAll, never following symlinks). A
// failure is noted in the job log only; `ballast gc` removes leftover
// <WorkDir>/<job_id> directories whose job is no longer running and that are
// older than 24 hours (runner.GCRequests, §8 E2).
func CleanupTree(opt Options, t Tree) error {
	panic("unimplemented: runner.CleanupTree (unit P2-RUNNER)")
}
