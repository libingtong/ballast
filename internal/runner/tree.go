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
// explicitly, and -c core.fsmonitor=false):
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
//  2. Root/index.tree: GIT_INDEX_FILE=Root/index.tree `git read-tree <Full>`
//     then `git checkout-index -a -f --prefix=<Dir>/` (Dir created 0700).
//     Dir contains no .git.
//  3. spec.InputsUntracked non-empty: walk repo.Toplevel (Lstat, never
//     following symlinks, never entering the real .git), select every file or
//     symlink matching one of the globs that is NOT an entry of Full, copy it
//     to the same relative path under Dir (regular files: content and exec
//     bit, hashed while copying; symlinks: as symlinks, target bytes hashed);
//     copy-on-write clones (clonefile / FICLONE) are allowed, hard links are
//     NOT (a write through a hard link would reach the real file). Limits ⇒
//     ErrInputsTooLarge. InputsHash per model.VerifyRecord.InputsHash.
//
// On any error Root is removed before returning. Not hot path.
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
