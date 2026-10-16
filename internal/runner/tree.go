package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/sandbox"
	"github.com/libingtong/ballast/internal/snapshot"
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
	// ErrGitConfigUnsafe (audit r1): the repository's own git config (local
	// or per-worktree scope, includes resolved by git itself) defines a
	// command-running key — filter.<name>.clean / smudge / process — that
	// the out-of-sandbox git children of the runner would execute while
	// they hash or check out agent-shaped content. Test not run; only the
	// key name is reported, never the command.
	ErrGitConfigUnsafe = errors.New("runner: git_config_unsafe")
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

// The limits above are copied into variables so tests can lower them; the
// constants stay the frozen values.
var (
	maxIndexBytes       = MaxIndexBytes
	maxProjectJSONBytes = MaxProjectJSONBytes
	maxInputFiles       = MaxUntrackedInputFiles
	maxInputBytes       = MaxUntrackedInputBytes
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
	opt = opt.withDefaults()
	var t Tree
	bad := func(sentinel error, format string, a ...any) (Tree, error) {
		err := fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, a...))
		if t.Root != "" {
			_ = removeTreeRoot(opt, t.Root)
		}
		return Tree{}, err
	}
	if jobID == "" || jobID == "." || jobID == ".." || strings.ContainsAny(jobID, `/\`+"\x00") {
		return bad(ErrMaterialize, "bad job id %q", jobID)
	}
	if opt.Layout.WorkDir == "" {
		return bad(ErrMaterialize, "layout has no work directory")
	}
	filter, err := snapshot.CompileExempt(pin.Exempt)
	if err != nil {
		return bad(ErrMaterialize, "exempt: %v", err)
	}
	_ = filter

	// Step 0: provenance of the git dirs, before any git child runs.
	gd, cd, gerr := opt.sbx().ResolveGitDirs(opt.FS, repo.Toplevel)
	if gerr != nil {
		return bad(ErrMaterialize, "%v", gerr)
	}
	if !sameDir(opt, gd, repo.GitDir) || !sameDir(opt, cd, repo.CommonDir) {
		return bad(ErrMaterialize, "sandbox_spec_invalid: git dirs of %s differ from the located repository", repo.Toplevel)
	}

	// The work area is resolved once: Dir must have no symlink on its path
	// (sandbox.ValidateSpec), and everything below is addressed through it.
	if err := opt.FS.MkdirAll(opt.Layout.WorkDir, 0o700); err != nil {
		return bad(ErrMaterialize, "work dir: %v", err)
	}
	workDir, err := opt.FS.EvalSymlinks(opt.Layout.WorkDir)
	if err != nil {
		return bad(ErrMaterialize, "work dir: %v", err)
	}
	root := filepath.Join(workDir, jobID)
	if _, err := opt.FS.Lstat(root); err == nil {
		return bad(ErrMaterialize, "%s already exists", root)
	}
	if err := opt.FS.MkdirAll(root, 0o700); err != nil {
		return bad(ErrMaterialize, "create %s: %v", root, err)
	}
	t = Tree{Root: root, Dir: filepath.Join(root, "tree")}
	idx, idxTree := filepath.Join(root, "index"), filepath.Join(root, "index.tree")

	// Step 1: the real index, copied through one fd.
	if err := copyRealIndex(opt, repo.IndexPath, idx); err != nil {
		return bad(ErrMaterialize, "%v", err)
	}
	git := func(index, workTree, dir string, args ...string) ([]byte, error) {
		return runGit(opt, repo.GitDir, workTree, index, dir, args...)
	}
	if err := checkLocalGitConfig(git, idx, repo.Toplevel); err != nil {
		return bad(ErrGitConfigUnsafe, "%v", err)
	}
	if _, err := git(idx, repo.Toplevel, repo.Toplevel, "add", "-u"); err != nil {
		return bad(ErrMaterialize, "git add -u: %v", err)
	}
	full, err := gitWriteTree(git, idx, repo.Toplevel)
	if err != nil {
		return bad(ErrMaterialize, "%v", err)
	}
	t.Full = full
	out, err := git(idx, repo.Toplevel, repo.Toplevel, "ls-files", "-s", "-z")
	if err != nil {
		return bad(ErrMaterialize, "git ls-files: %v", err)
	}
	entries, err := parseIndexEntries(out)
	if err != nil {
		return bad(ErrMaterialize, "%v", err)
	}
	for _, e := range entries {
		if e.mode == "160000" {
			return bad(ErrTreeSubmodule, "gitlink %q", e.path)
		}
	}
	// 1a: path layout pre-check, before any checkout.
	if err := checkTreeLayout(entries); err != nil {
		return bad(ErrTreeUnsafePath, "%v", err)
	}
	if len(pin.Exempt) > 0 {
		if _, err := git(idx, repo.Toplevel, repo.Toplevel, rmExemptArgs(pin.Exempt)...); err != nil {
			return bad(ErrMaterialize, "git rm --cached: %v", err)
		}
	}
	sStart, err := gitWriteTree(git, idx, repo.Toplevel)
	if err != nil {
		return bad(ErrMaterialize, "%v", err)
	}
	t.SStart = sStart

	// 1b: the command comes from the same read as S_start.
	if err := checkPinned(opt, git, idx, repo.Toplevel, sStart, pin); err != nil {
		if errors.Is(err, ErrProjectJSONChanged) || errors.Is(err, ErrProjectJSONUntracked) {
			_ = removeTreeRoot(opt, t.Root)
			return Tree{}, err
		}
		return bad(ErrMaterialize, "%v", err)
	}

	// Step 2: checkout of T_full into Dir (no .git).
	// Checkout runs smudge drivers: look at the config once more.
	if err := checkLocalGitConfig(git, idxTree, repo.Toplevel); err != nil {
		return bad(ErrGitConfigUnsafe, "%v", err)
	}
	if _, err := git(idxTree, repo.Toplevel, repo.Toplevel, "read-tree", string(full)); err != nil {
		return bad(ErrMaterialize, "git read-tree: %v", err)
	}
	if err := opt.FS.MkdirAll(t.Dir, 0o700); err != nil {
		return bad(ErrMaterialize, "create %s: %v", t.Dir, err)
	}
	if _, err := git(idxTree, repo.Toplevel, repo.Toplevel, "-c", "core.protectHFS=true", "-c", "core.protectNTFS=true",
		"checkout-index", "-a", "-f", "--prefix="+t.Dir+string(filepath.Separator)); err != nil {
		return bad(ErrMaterialize, "git checkout-index: %v", err)
	}

	// Step 3: whitelisted untracked inputs.
	if len(pin.Spec.InputsUntracked) > 0 {
		tracked := make(map[string]string, len(entries))
		for _, e := range entries {
			tracked[e.path] = e.mode
		}
		hash, n, err := copyInputs(opt, repo.Toplevel, t.Dir, tracked, pin.Spec.InputsUntracked)
		if err != nil {
			_ = removeTreeRoot(opt, t.Root)
			return Tree{}, err
		}
		t.InputsHash, t.Inputs = hash, n
	}
	return t, nil
}

// gitFn runs git with explicit GIT_INDEX_FILE / GIT_WORK_TREE in dir.
type gitFn func(index, workTree, dir string, args ...string) ([]byte, error)

// runGit runs one git child outside the sandbox: environment filtered by
// sandbox.HostEnv, GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE explicit, and
// -c core.fsmonitor=false (docs/p2-design.md §3.4).
func runGit(opt Options, gitDir, workTree, index, dir string, args ...string) ([]byte, error) {
	env := append(sandbox.HostEnv(opt.Env.Environ()),
		"GIT_DIR="+gitDir, "GIT_WORK_TREE="+workTree, "GIT_INDEX_FILE="+index, "GIT_OPTIONAL_LOCKS=0")
	full := append([]string{"-c", "core.fsmonitor=false"}, args...)
	return opt.gitRunner().Run(gitCtx(), dir, env, full...)
}

// checkLocalGitConfig refuses to run the out-of-sandbox git children when the
// repository's own config (scope local or worktree; git resolves includes
// itself) defines a filter driver command. The runner pins only
// core.fsmonitor with -c; a filter driver selected through .gitattributes
// (which the agent shapes) would otherwise run outside every sandbox. User
// and system config are the user's own and stay trusted (trust-model residual
// 5). The check and the git children that follow are not atomic: it narrows
// the window, it does not remove the assumption that the agent cannot write
// <repo>/.git/config (docs/p2-design.md section 11).
func checkLocalGitConfig(git gitFn, index, top string) error {
	out, err := git(index, top, top, "config", "--list", "--show-scope", "-z")
	scoped := true
	if err != nil {
		// git before 2.26 has no --show-scope: list the repository's own
		// files (includes are not expanded by --local / --worktree, so an
		// include is treated as unsafe, see findUnsafeConfigKey).
		scoped = false
		var werr error
		if out, err = git(index, top, top, "config", "--local", "--list", "-z"); err != nil {
			return fmt.Errorf("git config --list: %w", err)
		}
		var wout []byte
		if wout, werr = git(index, top, top, "config", "--worktree", "--list", "-z"); werr == nil {
			out = append(append(out, 0), wout...)
		}
	}
	if scope, key := findUnsafeConfigKey(string(out), scoped); key != "" {
		return fmt.Errorf("repository config (%s scope) sets %s", scope, key)
	}
	return nil
}

// findUnsafeConfigKey scans the NUL-separated output of `git config --list -z`.
// scoped: "--show-scope" output, "<scope> NUL <key> [NL <value>] NUL" per entry,
// only the local and worktree scopes are looked at (git expanded the
// includes). Unscoped: "<key> [NL <value>] NUL" per entry of the repository's
// own files; there git did not expand includes, so any include key is unsafe
// (the included file could define a driver and is agent-writable if the
// config is).
func findUnsafeConfigKey(out string, scoped bool) (scope, key string) {
	fields := strings.Split(out, "\x00")
	if scoped {
		for i := 0; i+1 < len(fields); i += 2 {
			k, _, _ := strings.Cut(fields[i+1], "\n")
			if (fields[i] == "local" || fields[i] == "worktree") && isFilterCommandKey(strings.ToLower(k)) {
				return fields[i], k
			}
		}
		return "", ""
	}
	for _, f := range fields {
		k, _, _ := strings.Cut(f, "\n")
		lk := strings.ToLower(k)
		if isFilterCommandKey(lk) || lk == "include.path" || strings.HasPrefix(lk, "includeif.") && strings.HasSuffix(lk, ".path") {
			return "local", k
		}
	}
	return "", ""
}

// isFilterCommandKey matches filter.<name>.{clean,smudge,process} (lowercase
// key; the <name> part may itself contain dots).
func isFilterCommandKey(key string) bool {
	if !strings.HasPrefix(key, "filter.") {
		return false
	}
	for _, suf := range []string{".clean", ".smudge", ".process"} {
		if strings.HasSuffix(key, suf) && len(key) > len("filter.")+len(suf) {
			return true
		}
	}
	return false
}

func rmExemptArgs(exempt []string) []string {
	args := []string{"rm", "--cached", "-r", "-q", "-f", "--ignore-unmatch", "--"}
	for _, g := range exempt {
		args = append(args, ":(glob)"+g)
	}
	return args
}

func gitWriteTree(git gitFn, index, top string) (model.TreeID, error) {
	out, err := git(index, top, top, "write-tree")
	if err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if !isHexID(id) {
		return "", fmt.Errorf("git write-tree printed %q", id)
	}
	return model.TreeID(id), nil
}

func isHexID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// sameDir compares two directory paths the way the located repository and
// ResolveGitDirs may differ in form (symlinks resolved or not).
func sameDir(opt Options, a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := opt.FS.EvalSymlinks(a)
	rb, eb := opt.FS.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

// copyRealIndex copies the agent-writable real index to dst through ONE fd:
// O_RDONLY|O_NOFOLLOW|O_NONBLOCK, fstat must be a regular file of at most
// maxIndexBytes, at most maxIndexBytes+1 bytes are read from that fd. A
// missing index is an empty one (the snapshot fallback's semantics); the
// copy keeps the original mtime (git's racy-timestamp logic needs it).
func copyRealIndex(opt Options, src, dst string) error {
	f, err := opt.FS.OpenFile(src, os.O_RDONLY|openFlagsSource, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open index: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat index: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("index is not a regular file (%s)", fi.Mode().Type())
	}
	if fi.Size() > maxIndexBytes {
		return fmt.Errorf("index is %d bytes, more than %d", fi.Size(), maxIndexBytes)
	}
	out, err := opt.FS.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create index copy: %w", err)
	}
	n, cerr := io.Copy(out, io.LimitReader(f, maxIndexBytes+1))
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return fmt.Errorf("copy index: %w", cerr)
	}
	if n > maxIndexBytes {
		return fmt.Errorf("index grew beyond %d bytes while it was read", maxIndexBytes)
	}
	_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	return nil
}

// indexEntry is one `git ls-files -s -z` record.
type indexEntry struct {
	mode string
	path string
}

func parseIndexEntries(out []byte) ([]indexEntry, error) {
	var entries []indexEntry
	for len(out) > 0 {
		rec := out
		if i := bytes.IndexByte(out, 0); i >= 0 {
			rec, out = out[:i], out[i+1:]
		} else {
			out = nil
		}
		if len(rec) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected ls-files record %q", rec)
		}
		entries = append(entries, indexEntry{mode: fields[0], path: string(p)})
	}
	return entries, nil
}

// foldKey maps a path to a representative that is equal for exactly the
// strings strings.EqualFold treats as equal (smallest rune of each simple
// folding orbit).
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < m {
				m = f
			}
		}
		b.WriteRune(m)
	}
	return b.String()
}

// checkTreeLayout is the pure step 1a pre-check (see Materialize): (i) a
// symlink entry that is, under EqualFold, a proper prefix of another entry's
// path; (ii) a path component that is empty, ".", ".." or equals ".git"
// under EqualFold.
func checkTreeLayout(entries []indexEntry) error {
	dirs := map[string]bool{} // fold keys of every proper directory prefix
	for _, e := range entries {
		parts := strings.Split(e.path, "/")
		for _, c := range parts {
			if c == "" || c == "." || c == ".." || strings.EqualFold(c, ".git") {
				return fmt.Errorf("path %q has the component %q", e.path, c)
			}
		}
		for i := 1; i < len(parts); i++ {
			dirs[foldKey(strings.Join(parts[:i], "/"))] = true
		}
	}
	for _, e := range entries {
		if e.mode == "120000" && dirs[foldKey(e.path)] {
			return fmt.Errorf("symlink %q is a path prefix of another entry", e.path)
		}
	}
	return nil
}

// checkPinned is step 1b: the project.json blob of S_start must be the file
// StartRequest pinned.
func checkPinned(opt Options, git gitFn, index, top string, sStart model.TreeID, pin PinnedEntry) error {
	rel := filepath.ToSlash(filepath.Join(".ballast", "project.json"))
	out, err := git(index, top, top, "ls-tree", "-z", string(sStart), "--", rel)
	if err != nil {
		return fmt.Errorf("git ls-tree: %w", err)
	}
	rec := strings.TrimRight(string(out), "\x00")
	meta, p, ok := strings.Cut(rec, "\t")
	fields := strings.Fields(meta)
	if !ok || p != rel || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return fmt.Errorf("%w: %s is not a regular file of the start tree", ErrProjectJSONUntracked, rel)
	}
	blob := fields[2]
	sz, err := git(index, top, top, "cat-file", "-s", blob)
	if err != nil {
		return fmt.Errorf("git cat-file -s: %w", err)
	}
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(sz)), "%d", &n); err != nil {
		return fmt.Errorf("git cat-file -s printed %q", sz)
	}
	if n > maxProjectJSONBytes {
		return fmt.Errorf("%w: %s is %d bytes", ErrProjectJSONChanged, rel, n)
	}
	data, err := git(index, top, top, "cat-file", "blob", blob)
	if err != nil {
		return fmt.Errorf("git cat-file: %w", err)
	}
	if got := project.Hash(data); got != pin.ProjectJSONHash {
		return fmt.Errorf("%w: sha256 of %s in the start tree is %s, the job pinned %s", ErrProjectJSONChanged, rel, short(got), short(pin.ProjectJSONHash))
	}
	cfg, err := project.Decode(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProjectJSONChanged, err)
	}
	var entry *model.VerifySpec
	for i := range cfg.Verify {
		if cfg.Verify[i].Name == pin.Spec.Name {
			entry = &cfg.Verify[i]
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("%w: no entry %q in the start tree's %s", ErrProjectJSONChanged, pin.Spec.Name, rel)
	}
	if !jsonEqual(*entry, pin.Spec) {
		return fmt.Errorf("%w: entry %q differs from the pinned one", ErrProjectJSONChanged, pin.Spec.Name)
	}
	if !jsonEqual(nonNil(cfg.Exempt), nonNil(pin.Exempt)) {
		return fmt.Errorf("%w: exempt list differs from the pinned one", ErrProjectJSONChanged)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func jsonEqual(a, b any) bool {
	ja, ea := json.Marshal(a)
	jb, eb := json.Marshal(b)
	return ea == nil && eb == nil && bytes.Equal(ja, jb)
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// hashLines is the digest of VerifyRecord.InputsHash: sha256 over the lines
// "<mode> <sha256> <path>\n" sorted by path.
func hashLines(lines []inputLine) string {
	sortInputLines(lines)
	h := sha256.New()
	for _, l := range lines {
		fmt.Fprintf(h, "%s %s %s\n", l.mode, l.sum, l.path)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// removeTreeRoot removes root without following symlinks; directories the
// test made unreadable or read-only are opened up first when the plain
// removal fails.
func removeTreeRoot(opt Options, root string) error {
	if root == "" || root == "." || root == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove %q", root)
	}
	if err := opt.FS.RemoveAll(root); err == nil {
		return nil
	}
	// chmod through an os.Root on the tree: a path component that a surviving
	// sandbox descendant swaps for a symlink after the walk listed it cannot
	// redirect the chmod to a directory outside the tree (os.Root rejects
	// escaping links); Lstat first so a link itself is never chmod'ed.
	if fi, e := os.Lstat(root); e == nil && fi.IsDir() {
		_ = os.Chmod(root, 0o700) // the root itself may be 0500 / 0000
	}
	rt, rerr := os.OpenRoot(root)
	if rerr == nil {
		defer rt.Close()
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if d == nil || !d.IsDir() || d.Type()&fs.ModeSymlink != 0 || rerr != nil {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return nil
		}
		if fi, e := rt.Lstat(rel); e == nil && fi.Mode()&fs.ModeSymlink == 0 && fi.IsDir() {
			_ = rt.Chmod(rel, 0o700)
		}
		return nil
	})
	return opt.FS.RemoveAll(root)
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
	opt = opt.withDefaults()
	if t.Dir == "" || t.Root == "" {
		return "", fmt.Errorf("%w: tree is not materialized", ErrMaterialize)
	}
	idx := filepath.Join(t.Root, "index.tree")
	git := func(index, workTree, dir string, args ...string) ([]byte, error) {
		return runGit(opt, repo.GitDir, workTree, index, dir, args...)
	}
	if err := checkLocalGitConfig(git, idx, t.Dir); err != nil {
		return "", fmt.Errorf("%w: %v", ErrGitConfigUnsafe, err)
	}
	if _, err := git(idx, t.Dir, t.Dir, "add", "-u"); err != nil {
		return "", fmt.Errorf("git add -u (end): %w", err)
	}
	if len(exempt) > 0 {
		if _, err := git(idx, t.Dir, t.Dir, rmExemptArgs(exempt)...); err != nil {
			return "", fmt.Errorf("git rm --cached (end): %w", err)
		}
	}
	return gitWriteTree(git, idx, t.Dir)
}

// CleanupTree removes t.Root (RemoveAll, never following symlinks). A
// failure is noted in the job log only; `ballast gc` removes leftover
// <WorkDir>/<job_id> directories whose job is no longer running and that are
// older than 24 hours (runner.GCRequests, §8 E2).
func CleanupTree(opt Options, t Tree) error {
	if t.Root == "" {
		return nil
	}
	opt = opt.withDefaults()
	return removeTreeRoot(opt, t.Root)
}
