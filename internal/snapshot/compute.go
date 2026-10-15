package snapshot

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// defaultInMemory bounds the files hashed from a single read; larger ones are
// streamed (and cannot be scanned for CRLF).
const defaultInMemory = 64 << 20

var crlf = []byte("\r\n")

// request describes one tree computation.
type request struct {
	refresh bool          // add -u: refresh tracked entries from the work tree
	specs   []pathPattern // with refresh: only entries matching one of these
	limited bool          // specs restrict the refresh
	args    []string      // the same pathspecs as strings (git fallback)
	dir     string        // directory for the fallback's add -u ("" ⇒ repo root)
	// forceFallback is a reason that makes the native path give up at once.
	forceFallback string
}

func (o Options) withDefaults() Options {
	if o.FS == nil {
		o.FS = sys.OSFS{}
	}
	if o.Clock == nil {
		o.Clock = sys.RealClock{}
	}
	if o.Env == nil {
		o.Env = sys.OSEnv{}
	}
	if o.Git == nil {
		o.Git = ExecGit{}
	}
	return o
}

func (o Options) memLimit() int64 {
	if o.inMemoryLimit > 0 {
		return o.inMemoryLimit
	}
	return defaultInMemory
}

func (o Options) deadlineExceeded() bool {
	return !o.Deadline.IsZero() && !o.Clock.Now().Before(o.Deadline)
}

func errDeadline() error { return fmt.Errorf("%w: snapshot", model.ErrDeadline) }

// compute runs the native algorithm and, when allowed, the git fallback.
func compute(opt Options, repo *model.RepoInfo, req request, f *Filter) (model.TreeID, error) {
	opt = opt.withDefaults()
	if repo == nil {
		return "", errors.New("snapshot: nil repo")
	}
	if opt.deadlineExceeded() {
		return "", errDeadline()
	}
	id, err := nativeTree(opt, repo, req, f)
	if err == nil || !errors.Is(err, model.ErrFallback) || !opt.AllowFallback {
		return id, err
	}
	return fallbackTree(opt, repo, req.refresh, req.dir, req.args, f)
}

// nativeTree computes F(...) in process.
func nativeTree(opt Options, repo *model.RepoInfo, req request, f *Filter) (model.TreeID, error) {
	if req.forceFallback != "" {
		return "", fallbackf("%s", req.forceFallback)
	}
	if of := repo.ObjectFormat; of != "" && of != "sha1" {
		return "", fallbackf("object format %s", of)
	}
	var set *repoSettings
	if req.refresh {
		set = loadSettings(opt.FS, opt.Env, repo)
		if err := set.checkNative(); err != nil {
			return "", err
		}
	}
	idx, err := readIndex(opt.FS, repo.IndexPath)
	if err != nil {
		return "", err
	}
	var attrDirs map[string]bool
	if req.refresh {
		attrDirs = trackedAttributeDirs(idx.Entries) // before the filter compacts them
	}
	ents := f.removeExempt(idx.Entries)
	if req.refresh {
		r := newRefresher(opt, repo, set, idx, attrDirs)
		if ents, err = r.run(ents, req); err != nil {
			return "", err
		}
	}
	return treeID(ents), nil
}

// checkNative rejects settings the native refresh cannot honour.
func (s *repoSettings) checkNative() error {
	switch {
	case s.objectFormat != "sha1":
		return fallbackf("object format %s", s.objectFormat)
	case s.uncertain != "":
		return fallbackf("%s", s.uncertain)
	}
	return nil
}

// readIndex reads and parses the index at path. A missing index is an empty
// one (git behaves the same), and its stat time is taken BEFORE the read so
// a concurrent rewrite can only make entries look more racy, never less.
func readIndex(fsys sys.FS, indexPath string) (*Index, error) {
	fi, err := fsys.Stat(indexPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Index{Version: 2}, nil
		}
		return nil, err
	}
	data, err := fsys.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	idx, err := ParseIndex(data)
	if err != nil {
		return nil, err
	}
	idx.FileMtime = fi.ModTime()
	return idx, nil
}

// refresher re-reads tracked entries from the work tree like `git add -u`.
type refresher struct {
	opt     Options
	fs      sys.FS
	root    string
	set     *repoSettings
	attrs   *attrResolver
	hasTime bool  // index file mtime known
	idxTime tspec // index file mtime
	nsec    bool  // the index carries sub-second timestamps
	dirs    map[string]bool
	lastDir string
	lastOK  bool
}

func newRefresher(opt Options, repo *model.RepoInfo, set *repoSettings, idx *Index, attrDirs map[string]bool) *refresher {
	r := &refresher{
		opt:     opt,
		fs:      opt.FS,
		root:    repo.Toplevel,
		set:     set,
		attrs:   newAttrResolver(opt.FS, repo, set, attrDirs),
		dirs:    map[string]bool{},
		lastDir: "\x00",
	}
	if t := idx.FileMtime; !t.IsZero() {
		r.hasTime = true
		r.idxTime = tspec{uint32(t.Unix()), uint32(t.Nanosecond())}
	}
	// An index written by a git without nanosecond support (or on a
	// whole-second file system) has zero nanosecond fields throughout.
	for i := range idx.Entries {
		if e := &idx.Entries[i]; e.MtimeNS != 0 || e.CtimeNS != 0 {
			r.nsec = true
			break
		}
	}
	return r
}

func (r *refresher) run(ents []IndexEntry, req request) ([]IndexEntry, error) {
	out := ents[:0]
	for i := range ents {
		e := ents[i]
		if i&0xff == 0 && r.opt.deadlineExceeded() {
			return nil, errDeadline()
		}
		if req.limited && !matchAny(req.specs, e.Path) {
			out = append(out, e)
			continue
		}
		keep, err := r.refreshOne(&e)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, e)
		}
	}
	return out, nil
}

func matchAny(pats []pathPattern, name string) bool {
	for i := range pats {
		if pats[i].match(name) {
			return true
		}
	}
	return false
}

// dirPlain reports whether dir (slash form, "" = root) exists as a real
// directory with no symlink on the way: git treats a path below a symlinked
// or missing directory as removed.
func (r *refresher) dirPlain(dir string) (bool, error) {
	if dir == "" {
		return true, nil
	}
	if dir == r.lastDir {
		return r.lastOK, nil
	}
	ok, err := r.dirPlainUncached(dir)
	if err == nil {
		r.lastDir, r.lastOK = dir, ok
	}
	return ok, err
}

func (r *refresher) dirPlainUncached(dir string) (bool, error) {
	if v, ok := r.dirs[dir]; ok {
		return v, nil
	}
	parent := path.Dir(dir)
	if parent == "." {
		parent = ""
	}
	ok, err := r.dirPlain(parent)
	if err != nil {
		return false, err
	}
	if ok {
		fi, err := r.fs.Lstat(filepath.Join(r.root, filepath.FromSlash(dir)))
		switch {
		case err == nil:
			ok = fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0
		case isMissing(err):
			ok = false
		default:
			return false, fallbackf("lstat %s: %v", dir, err)
		}
	}
	r.dirs[dir] = ok
	return ok, nil
}

// refreshOne updates e in place; keep is false when git would drop it.
func (r *refresher) refreshOne(e *IndexEntry) (keep bool, err error) {
	if e.Flags&flagAssumeValid != 0 {
		return true, nil // git add -u leaves assume-unchanged entries alone
	}
	if e.Mode == modeSymlink && !r.set.symlinks {
		return false, fallbackf("core.symlinks=false with symlink entry %s", e.Path)
	}
	if !safeWorktreePath(e.Path) {
		return false, fallbackf("unusual path %q", e.Path)
	}
	dir := path.Dir(e.Path)
	if dir == "." {
		dir = ""
	}
	if ok, err := r.dirPlain(dir); err != nil || !ok {
		return false, err
	}
	abs := filepath.Join(r.root, filepath.FromSlash(e.Path))
	fi, err := r.fs.Lstat(abs)
	if err != nil {
		if isMissing(err) {
			return false, nil
		}
		return false, fallbackf("lstat %s: %v", e.Path, err)
	}
	st := statOf(fi)
	if e.Mode == modeGitlink && st.kind != fs.ModeDir {
		return false, fallbackf("submodule %s replaced by a non-directory", e.Path)
	}
	if st.kind == fs.ModeSymlink && !r.set.symlinks {
		return false, fallbackf("core.symlinks=false with symlink %s", e.Path)
	}
	switch st.kind {
	case 0, fs.ModeSymlink:
	case fs.ModeDir:
		if e.Mode == modeGitlink {
			return r.refreshGitlink(e, abs)
		}
		// A directory where a file was tracked is a removal, unless it is
		// a repository: git then records it as a gitlink of its HEAD.
		if has, err := r.hasDotGit(abs); err != nil {
			return false, fallbackf("lstat %s/.git: %v", e.Path, err)
		} else if has {
			return false, fallbackf("tracked path %s replaced by a nested repository", e.Path)
		}
		return false, nil
	default:
		return false, fallbackf("special file %s", e.Path)
	}
	if r.statClean(e, &st) {
		return true, nil
	}
	if st.kind == fs.ModeSymlink {
		target, err := r.fs.Readlink(abs)
		if err != nil {
			if isMissing(err) {
				return false, nil
			}
			return false, fallbackf("readlink %s: %v", e.Path, err)
		}
		e.Mode, e.SHA = modeSymlink, hashBlob([]byte(target))
		return true, nil
	}
	sum, err := r.hashRegular(abs, dir, e.Path, st.size)
	if err != nil {
		return false, err
	}
	e.SHA = sum
	switch {
	case r.set.fileMode && st.exec:
		e.Mode = modeExec
	case r.set.fileMode:
		e.Mode = modeRegular
	case e.Mode != modeRegular && e.Mode != modeExec:
		e.Mode = modeRegular
	}
	return true, nil
}

// refreshGitlink handles a gitlink entry whose path is a directory. Git
// records the HEAD of a checked-out submodule and keeps the recorded commit
// of an uninitialised one. A HEAD that cannot be resolved with certainty
// (reftable, sha256, worktree-style layouts, odd refs) is left to git.
func (r *refresher) refreshGitlink(e *IndexEntry, abs string) (bool, error) {
	has, err := r.hasDotGit(abs)
	if err != nil {
		return false, fallbackf("lstat %s/.git: %v", e.Path, err)
	}
	if !has {
		return true, nil
	}
	head, ok := r.submoduleHead(abs)
	if !ok {
		return false, fallbackf("checked-out submodule %s: HEAD not natively resolvable", e.Path)
	}
	e.SHA = head
	return true, nil
}

// submoduleHead resolves HEAD of the repository checked out in dir: a
// detached id, or a symbolic ref found as a loose ref or in packed-refs.
func (r *refresher) submoduleHead(dir string) ([20]byte, bool) {
	var zero [20]byte
	gitDir, found, err := candidateGitDir(r.fs, dir)
	if err != nil || !found {
		return zero, false
	}
	if _, err := r.fs.Lstat(filepath.Join(gitDir, "commondir")); !isMissing(err) {
		return zero, false
	}
	// Like git's is_git_directory: a HEAD alone is not a repository. git
	// add fails on such a submodule, so the native path must not invent a
	// gitlink id for it (contract v1.3: fall back, git reports the error).
	for _, sub := range [...]string{"objects", "refs"} {
		fi, err := r.fs.Lstat(filepath.Join(gitDir, sub))
		if err != nil || !fi.IsDir() {
			return zero, false
		}
	}
	data, err := r.fs.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return zero, false
	}
	line := strings.TrimRight(string(data), "\r\n")
	for depth := 0; depth < 2; depth++ {
		ref, symbolic := strings.CutPrefix(line, "ref: ")
		if !symbolic {
			return parseHexID(line)
		}
		ref = strings.TrimSpace(ref)
		if !strings.HasPrefix(ref, "refs/") || !safeRefName(ref) {
			return zero, false
		}
		if data, err = r.fs.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
			line = strings.TrimRight(string(data), "\r\n")
			if _, sym := strings.CutPrefix(line, "ref: "); sym {
				continue
			}
			return parseHexID(line)
		} else if !isMissing(err) {
			return zero, false
		}
		packed, err := r.fs.ReadFile(filepath.Join(gitDir, "packed-refs"))
		if err != nil {
			return zero, false
		}
		for _, l := range strings.Split(string(packed), "\n") {
			if id, name, ok := strings.Cut(strings.TrimRight(l, "\r"), " "); ok && name == ref {
				return parseHexID(id)
			}
		}
		return zero, false
	}
	return zero, false
}

func parseHexID(s string) ([20]byte, bool) {
	var out [20]byte
	if len(s) != 40 {
		return out, false
	}
	if _, err := hex.Decode(out[:], []byte(s)); err != nil {
		return out, false
	}
	return out, true
}

// safeRefName accepts only plain ref names (no dot components, no empty
// components, no backslashes or control characters).
func safeRefName(ref string) bool {
	if strings.ContainsAny(ref, "\\:*?[~^ \x00\x7f") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return false
	}
	for _, c := range strings.Split(ref, "/") {
		if c == "" || c[0] == '.' || strings.HasSuffix(c, ".lock") {
			return false
		}
	}
	return !strings.HasSuffix(ref, ".")
}

// hasDotGit reports whether dir contains a .git entry (file or directory).
func (r *refresher) hasDotGit(dir string) (bool, error) {
	_, err := r.fs.Lstat(filepath.Join(dir, ".git"))
	switch {
	case err == nil:
		return true, nil
	case isMissing(err):
		return false, nil
	}
	return false, err
}

// safeWorktreePath rejects index paths that could leave the work tree or
// name something else than an ordinary tracked file (git refuses them too).
func safeWorktreePath(p string) bool {
	if p == "" || p[0] == '/' || p[len(p)-1] == '/' {
		return false
	}
	start := 0
	for i := 0; i <= len(p); i++ {
		if i < len(p) && p[i] != '/' {
			if p[i] == 0 || (filepath.Separator != '/' && (p[i] == filepath.Separator || p[i] == ':')) {
				return false
			}
			continue
		}
		switch comp := p[start:i]; comp {
		case "", ".", "..":
			return false
		}
		start = i + 1
	}
	return true
}

// statClean reports whether the index entry can be trusted without reading
// the file: the stat data match and the entry is not racily clean.
func (r *refresher) statClean(e *IndexEntry, st *fileStat) bool {
	isLink := st.kind == fs.ModeSymlink
	if (e.Mode == modeSymlink) != isLink {
		return false
	}
	if !isLink && r.set.fileMode && (e.Mode == modeExec) != st.exec {
		return false
	}
	if uint32(st.size) != e.Size || st.mtime.sec != e.MtimeS {
		return false
	}
	// git smudges racily clean entries to size 0 when it writes the index;
	// ce_match_stat_basic treats a size-0 entry that is not the empty blob
	// as changed, and after a rewrite that is the only protection left.
	if e.Size == 0 && e.SHA != emptyBlobID {
		return false
	}
	if r.nsec && st.mtime.nsec != e.MtimeNS {
		return false
	}
	if st.full {
		if st.ctime.sec != e.CtimeS || (r.nsec && st.ctime.nsec != e.CtimeNS) || st.ino != e.Ino || st.dev != e.Dev {
			return false
		}
	}
	if !r.hasTime {
		return false
	}
	// Racy git: an entry modified in the same second as the index was
	// written (or later) cannot be trusted even if its stat data match.
	if e.MtimeS >= r.idxTime.sec {
		return false
	}
	return true
}

// hashRegular returns the blob id of a regular file's content as `git add`
// would store it. Conversions that could rewrite the content make it fall
// back; text/eol conversions only act on CRLF, so content without CRLF is
// hashed as is.
func (r *refresher) hashRegular(abs, dir, rel string, size int64) ([20]byte, error) {
	flags, err := r.attrs.flagsFor(dir)
	if err != nil {
		return [20]byte{}, fallbackf("attributes near %s: %v", rel, err)
	}
	if flags.hard {
		return [20]byte{}, fallbackf("filter/ident/encoding attribute applies near %s", rel)
	}
	soft := flags.soft || r.set.convRisk
	if soft && r.set.safeCRLF {
		return [20]byte{}, fallbackf("core.safecrlf with text conversion for %s", rel)
	}
	if size > r.opt.memLimit() {
		if soft {
			return [20]byte{}, fallbackf("large file %s under text conversion", rel)
		}
		return r.streamBlob(abs, rel)
	}
	data, err := r.fs.ReadFile(abs)
	if err != nil {
		if isMissing(err) {
			return [20]byte{}, fallbackf("%s vanished while reading", rel)
		}
		return [20]byte{}, fallbackf("read %s: %v", rel, err)
	}
	if soft && bytes.Contains(data, crlf) {
		return [20]byte{}, fallbackf("CRLF content in %s under text conversion", rel)
	}
	return hashBlob(data), nil
}

func (r *refresher) streamBlob(abs, rel string) ([20]byte, error) {
	f, err := r.fs.OpenFile(abs, 0, 0)
	if err != nil {
		return [20]byte{}, fallbackf("open %s: %v", rel, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return [20]byte{}, fallbackf("stat %s: %v", rel, err)
	}
	h := sha1.New()
	writeObjectHeader(h, "blob", fi.Size())
	n, err := io.Copy(h, f)
	if err != nil || n != fi.Size() {
		return [20]byte{}, fallbackf("%s changed while hashing", rel)
	}
	var out [20]byte
	h.Sum(out[:0])
	return out, nil
}

// resolved is the result of resolveSpecs.
type resolved struct {
	args []string      // pathspecs for the git fallback, valid from cwd
	pats []pathPattern // the native form: repo-root relative
	// native is false when some spec is magic (":(glob)...") and only git
	// can interpret it.
	native bool
	// cwd is where the fallback runs `git add -u`: the directory the
	// pathspecs were written in, so magic specs mean what they mean to
	// `git commit` run there. Empty ⇒ the repo root.
	cwd string
}

// resolveSpecs turns the pathspecs of a `git commit <paths>` into repo-root
// relative specs. dir is the directory they were written in (empty ⇒ the
// repo root, the only base the caller knows without -C).
func resolveSpecs(fsys sys.FS, repo *model.RepoInfo, dir string, paths []string) (*resolved, error) {
	if len(paths) == 0 {
		return nil, errors.New("snapshot: paths form without paths")
	}
	roots := []string{repo.Toplevel}
	if real, err := fsys.EvalSymlinks(repo.Toplevel); err == nil && real != repo.Toplevel {
		roots = append(roots, real)
	}
	inside := func(target string) (string, bool) {
		for _, root := range roots {
			if rel, ok := relInside(root, target); ok {
				return rel, true
			}
		}
		return "", false
	}
	out := &resolved{native: true}
	prefix := ""
	if dir != "" {
		real := realPath(fsys, dir)
		rel, ok := inside(real)
		if !ok {
			return nil, fmt.Errorf("snapshot: commit directory %s is outside %s", dir, repo.Toplevel)
		}
		if rel != "." {
			prefix = rel + "/"
			out.cwd = real
		}
	}
	for _, p := range paths {
		if p == "" {
			return nil, errors.New("snapshot: empty pathspec")
		}
		pre := prefix
		if filepath.IsAbs(p) {
			rel, ok := inside(realPath(fsys, p))
			if !ok {
				return nil, fmt.Errorf("snapshot: pathspec %s is outside %s", p, repo.Toplevel)
			}
			p, pre = rel, ""
		} else if filepath.Separator != '/' {
			p = filepath.ToSlash(p)
		}
		if strings.HasPrefix(p, ":") {
			out.native = false
			out.args = append(out.args, p)
			continue
		}
		n, err := normalizeSpec(pre + p)
		if err != nil {
			return nil, err
		}
		out.pats = append(out.pats, newPathPattern(n, false))
		// n is repo-root relative; ":(top)" keeps it so when git runs in
		// a subdirectory, and "." stands for the root (git rejects "").
		switch {
		case prefix != "":
			n = ":(top)" + n
		case n == "":
			n = "."
		}
		out.args = append(out.args, n)
	}
	return out, nil
}

// realPath resolves symlinks in p; when p itself does not exist (a deleted
// file named by an absolute pathspec) its parent is resolved instead.
func realPath(fsys sys.FS, p string) string {
	if r, err := fsys.EvalSymlinks(p); err == nil {
		return r
	}
	if r, err := fsys.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(r, filepath.Base(p))
	}
	return p
}

// relInside returns target relative to root in slash form ("." for root
// itself); ok is false when target is not inside root.
func relInside(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
