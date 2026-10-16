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
	// ErrProjectJSONChanged (v1.5.2): the .ballast/project.json blob of S_start
	// is not the file StartRequest read and pinned in the job sidecar
	// (sha256 differs), or the entry / exempt list decoded from that blob is
	// not byte-for-byte the pinned one — see step 1b. Test not run: the
	// command to execute must come from the same read as S_start and the
	// copy, otherwise an agent could swap project.json between the
	// --hook-start read and `git add -u` (run `true`, restore, get a fresh
	// pass whose S_start shows the honest file).
	ErrProjectJSONChanged = errors.New("runner: project_json_changed")
	// ErrProjectJSONUntracked (v1.5.2): .ballast/project.json is not a
	// regular-file blob (mode 100644 / 100755) of S_start: untracked,
	// ignored, removed from the index, a symlink, or matched by an exempt
	// glob. Test not run (see step 1b for why a T0 record would not help).
	ErrProjectJSONUntracked = errors.New("runner: project_json_untracked")
	// ErrMaterialize: any other materialization failure (git child failed,
	// copy failed, ResolveGitDirs rejected the repo's git dirs, the real
	// index is not a regular file or exceeds MaxIndexBytes).
	ErrMaterialize = errors.New("runner: materialize_failed")
)

// Limits of the inputs_untracked copy (fail closed and visible).
const (
	MaxUntrackedInputFiles = 500000
	MaxUntrackedInputBytes = int64(8) << 30
	// MaxIndexBytes (v1.5.2) caps the copy of the real index in step 1
	// (ErrMaterialize beyond it); MaxProjectJSONBytes caps the project.json
	// blob read in step 1b (ErrProjectJSONChanged beyond it).
	MaxIndexBytes       = int64(1) << 30
	MaxProjectJSONBytes = int64(1) << 20
)

// PinnedEntry (v1.5.2) is what StartRequest resolved from the work tree's
// project.json and pinned in the job sidecar (job.go jobSpec): the entry to
// run, the exempt globs and sha256 of the file bytes it read. RunJob passes
// it to Materialize, which checks it against the project.json blob of
// S_start (step 1b) before anything runs.
type PinnedEntry struct {
	Spec            model.VerifySpec
	Exempt          []string
	ProjectJSONHash string
}

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
//  1. Root/index = copy of repo.IndexPath (same semantics as the snapshot
//     fallback's copyIndex: absent ⇒ empty index, mtime preserved). Source
//     side (v1.5.2): the real index is agent-writable, so it is opened with
//     O_RDONLY|O_NOFOLLOW|O_NONBLOCK and fstat'ed — not a regular file
//     (symlink ⇒ ELOOP, FIFO, device such as /dev/zero) or larger than
//     MaxIndexBytes ⇒ ErrMaterialize; at most MaxIndexBytes+1 bytes are read
//     from that fd (growth while reading is caught too); never a path-based
//     ReadFile. With GIT_INDEX_FILE=Root/index:
//     `git add -u`; Full = `git write-tree`; any index entry with mode 160000
//     ⇒ ErrTreeSubmodule; `git rm --cached -r -q --ignore-unmatch --
//     ':(glob)<exempt>'...` (pin.Exempt); SStart = `git write-tree`. The work tree is read
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
//     1b. (v1.5.2) The command comes from the same read as S_start:
//     `git ls-tree <SStart> -- .ballast/project.json` (the file at the root
//     of repo.Toplevel, layout.ProjectJSON) must name a blob with mode
//     100644 or 100755 ⇒ else ErrProjectJSONUntracked (untracked, ignored,
//     not in the index, a symlink, or removed by exempt — an exempt
//     project.json would leave the executed command out of S and C, so the
//     agent could commit a different one and the pass stays fresh). Then
//     `git cat-file blob <id>` (size from `cat-file -s` first; more than
//     MaxProjectJSONBytes ⇒ ErrProjectJSONChanged): sha256 of the blob
//     bytes must equal pin.ProjectJSONHash, AND project.Decode(blob) must
//     yield an entry named pin.Spec.Name whose json.Marshal equals that of
//     pin.Spec, and an exempt list whose json.Marshal equals that of
//     pin.Exempt (a nil and an empty list compare equal) ⇒
//     else ErrProjectJSONChanged. Only then is anything checked out. The
//     blob is what `git add -u` read in step 1 (or, for a skip-worktree /
//     assume-unchanged entry, what the index holds); either way it is in
//     S_start, so S_end == C later means the commit carries exactly the
//     project.json whose command ran. A repository whose clean filter or
//     eol conversion makes the blob differ from the work-tree bytes gets
//     ErrProjectJSONChanged on every hook-started job (visible; the fix is
//     `-text` for that file or committing it normalized). Why not record T0
//     instead: freshness does not filter by tier (docs/p2-design.md §3.3),
//     so a T0 pass from a swapped command would still count.
//  2. Root/index.tree: GIT_INDEX_FILE=Root/index.tree `git read-tree <Full>`
//     then `git -c core.protectHFS=true -c core.protectNTFS=true
//     checkout-index -a -f --prefix=<Dir>/` (Dir created 0700). Dir
//     contains no .git. This git child runs outside the sandbox on
//     agent-influenced index content; staying inside Dir relies on git's
//     leading-path (symlink) protection, narrowed by step 1a and
//     registered as residual risk in docs/p2-design.md §11 (running it in a
//     W=[Tree] sandbox is a possible later contract addition).
//  3. pin.Spec.InputsUntracked non-empty: walk repo.Toplevel (Lstat, never
//     following symlinks, never entering the real .git), select every file or
//     symlink matching one of the globs that is NOT an entry of Full, copy it
//     to the same relative path under Dir (regular files: content and exec
//     bit, hashed while copying; symlinks: as symlinks, target bytes hashed);
//     hard links are NOT allowed (a write through a hard link would reach
//     the real file). Source side (v1.5.2; the real work tree is
//     agent-writable while this runs, so the Lstat of the walk and the open
//     are not atomic): a regular file is opened with
//     O_RDONLY|O_NOFOLLOW|O_NONBLOCK (relative to the walked directory fd
//     where available) and fstat'ed; it must still be a regular file with the
//     same dev and ino as the walk's Lstat ⇒ else ErrInputsConflict (a FIFO,
//     symlink, device or swapped file appearing between Lstat and open never
//     blocks the job or feeds /dev/zero); bytes are read only from that fd
//     and count against MaxUntrackedInputBytes as they are read, so a file
//     growing during the copy stops at the limit with ErrInputsTooLarge. A
//     symlink is read with Readlink on the walked entry (no open). Placement rules (v1.5.1, frozen; Dir may already hold
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
func Materialize(opt Options, repo model.RepoInfo, jobID string, pin PinnedEntry) (Tree, error) {
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
