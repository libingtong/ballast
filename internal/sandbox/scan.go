package sandbox

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/sys"
)

// scanRoot walks one tree completely (Lstat semantics, never following
// symlinks) and appends every mandatory match to out.
func scanRoot(fsys sys.FS, root string, pats []pattern, isRepo bool, out *[]string) error {
	var walk func(dirAbs, dirRel string) error
	walk = func(dirAbs, dirRel string) error {
		ents, err := fsys.ReadDir(dirAbs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // vanished while scanning
			}
			return fmt.Errorf("%w: scan %s: %v", ErrInvalidSpec, dirAbs, err)
		}
		for _, e := range ents {
			name := e.Name()
			rel := name
			if dirRel != "" {
				rel = dirRel + "/" + name
			}
			abs := filepath.Join(dirAbs, name)
			matched, subtree, _ := matchInfo(pats, isRepo, rel)
			if matched {
				*out = append(*out, abs)
				if len(*out) > MaxMandatoryMatches {
					return fmt.Errorf("%w: more than %d protected paths below %s", ErrInvalidSpec, MaxMandatoryMatches, root)
				}
				if subtree {
					continue
				}
			}
			if e.IsDir() { // a DirEntry's type does not follow symlinks
				if err := walk(abs, rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	fi, err := fsys.Lstat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: scan %s: %v", ErrInvalidSpec, root, err)
	}
	if !fi.IsDir() {
		return nil
	}
	return walk(root, "")
}

// ScanMandatory walks every Spec.RepoRoots and Spec.GitDirs tree completely
// (Lstat, never following symlinks, including ignored directories such as
// node_modules: a nested .git there can be turned into a gitlink) and
// returns, absolute and sorted, every path that MatchMandatory matches (a
// matched subtree is reported once, at its top). Not hot path; its cost is
// one tree walk per scan. More than MaxMandatoryMatches matches ⇒ error
// wrapping ErrInvalidSpec (fail closed and visible: the job is infra
// "sandbox_spec_invalid" rather than running with unbound matches).
//
// A match by an exact pattern (".git") is reported and the walk continues
// below it, because other patterns apply there (".git/hooks/", ".git/config");
// a match by a subtree pattern is reported once and not entered.
func ScanMandatory(fsys sys.FS, spec Spec) ([]string, error) {
	repoPats := parsePatterns(RepoMandatoryDeny)
	gitPats := parsePatterns(GitDirMandatoryDeny)
	var found []string
	for _, r := range spec.RepoRoots {
		if err := scanRoot(fsys, filepath.Clean(r), repoPats, true, &found); err != nil {
			return nil, err
		}
	}
	for _, g := range spec.GitDirs {
		if err := scanRoot(fsys, filepath.Clean(g), gitPats, false, &found); err != nil {
			return nil, err
		}
	}
	sort.Strings(found)
	found = dedupe(found)
	if len(found) > MaxMandatoryMatches {
		return nil, fmt.Errorf("%w: more than %d protected paths", ErrInvalidSpec, MaxMandatoryMatches)
	}
	return found, nil
}

// PostCheck runs after the child exited (both platforms; on darwin it is
// defence in depth). It returns, absolute and sorted, every path that is
// (a) one of w.MissingProtected and now exists, or (b) a ScanMandatory match
// of spec that is not in w.Baseline — i.e. a protected path the child
// created, at any depth, including under a renamed ancestor. found non-empty
// ⇒ err wraps ErrProtectedCreated; the runner then calls Quarantine on
// found. A scan failure is returned as an error with found nil (the runner
// records infra).
func PostCheck(fsys sys.FS, spec Spec, w Wrapped) (found []string, err error) {
	seen := map[string]bool{}
	for _, p := range w.MissingProtected {
		if _, lerr := fsys.Lstat(p); lerr == nil {
			seen[filepath.Clean(p)] = true
		} else if !errors.Is(lerr, fs.ErrNotExist) {
			return nil, fmt.Errorf("sandbox: post-check %s: %w", p, lerr)
		}
	}
	matches, serr := ScanMandatory(fsys, spec)
	if serr != nil {
		return nil, serr
	}
	base := make(map[string]bool, len(w.Baseline))
	for _, b := range w.Baseline {
		base[filepath.Clean(b)] = true
	}
	for _, m := range matches {
		if !base[m] {
			seen[m] = true
		}
	}
	// A Baseline path that now names another object was replaced: the child
	// moved a non-pinned ancestor away and recreated the entry (its contents
	// are not scanned, so a path comparison alone would not see it).
	for p, was := range w.ids {
		if fi, lerr := fsys.Lstat(p); lerr == nil {
			if now, ok := keyOf(fi); ok && now != was {
				seen[filepath.Clean(p)] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}
	for p := range seen {
		found = append(found, p)
	}
	sort.Strings(found)
	return found, fmt.Errorf("%w: %d path(s) created inside the sandbox", ErrProtectedCreated, len(found))
}

// Quarantine moves each of paths (files, symlinks — not followed — or
// whole directories) out of the repo into dir (the runner passes
// <layout.QuarantineDir>/<job_id>), keeping the path relative to its root
// in roots (RepoRoots and GitDirs) so the layout stays readable. rename(2)
// first; on EXDEV a copy that never follows symlinks followed by RemoveAll;
// if that fails too, RemoveAll in place. A path that survives every step is
// reported with Err set and makes the runner additionally record error
// "protected_path_residue". The quarantine dir lives under ~/.ballast
// (protected, outside W) and is cleaned by `ballast gc` after 30 days.
//
// A path that lies in none of roots, or that contains dir, is refused (Err
// set, nothing touched): Quarantine only ever moves things out of the trees
// the sandbox guarded.
func Quarantine(fsys sys.FS, paths, roots []string, dir string) []QuarantineEntry {
	out := make([]QuarantineEntry, 0, len(paths))
	dir = filepath.Clean(dir)
	for _, p := range paths {
		p = filepath.Clean(p)
		ent := QuarantineEntry{Path: p}
		rel, ok := relToRoots(p, roots)
		switch {
		case !ok:
			ent.Err = "path is outside every guarded root"
		case within(dir, p):
			ent.Err = "quarantine directory lies inside the path"
		default:
			dest := uniqueDest(fsys, filepath.Join(dir, rel))
			ent.MovedTo, ent.Err = quarantineOne(fsys, p, dest)
		}
		out = append(out, ent)
	}
	return out
}

func relToRoots(p string, roots []string) (string, bool) {
	best := ""
	for _, r := range roots {
		r = filepath.Clean(r)
		if p != r && within(p, r) && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return "", false
	}
	rel, err := filepath.Rel(best, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

func uniqueDest(fsys sys.FS, dest string) string {
	if _, err := fsys.Lstat(dest); err != nil {
		return dest
	}
	for i := 1; ; i++ {
		d := fmt.Sprintf("%s.%d", dest, i)
		if _, err := fsys.Lstat(d); err != nil {
			return d
		}
	}
}

func quarantineOne(fsys sys.FS, p, dest string) (movedTo, errMsg string) {
	if err := fsys.MkdirAll(filepath.Dir(dest), 0o700); err == nil {
		if err := fsys.Rename(p, dest); err == nil {
			return dest, ""
		}
		// Rename failed (EXDEV or anything else): copy without following
		// symlinks, then remove the original.
		if cerr := copyNoFollow(fsys, p, dest); cerr == nil {
			if rerr := fsys.RemoveAll(p); rerr == nil {
				if _, lerr := fsys.Lstat(p); lerr != nil {
					return dest, ""
				}
			}
		} else {
			_ = fsys.RemoveAll(dest) // do not leave a partial copy behind
		}
	}
	// Last resort: remove in place.
	_ = fsys.RemoveAll(p)
	if _, err := fsys.Lstat(p); err == nil {
		return "", "could be neither moved nor removed"
	}
	return "", ""
}

// symlinker is implemented by file systems that can create symlinks
// (sys.OSFS does not expose it; the standard library does).
type symlinker interface {
	Symlink(oldname, newname string) error
}

func copyNoFollow(fsys sys.FS, src, dst string) error {
	fi, err := fsys.Lstat(src)
	if err != nil {
		return err
	}
	switch mode := fi.Mode(); {
	case mode&fs.ModeSymlink != 0:
		target, err := fsys.Readlink(src)
		if err != nil {
			return err
		}
		if sl, ok := fsys.(symlinker); ok {
			return sl.Symlink(target, dst)
		}
		return os.Symlink(target, dst)
	case mode.IsDir():
		if err := fsys.MkdirAll(dst, 0o700); err != nil {
			return err
		}
		ents, err := fsys.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyNoFollow(fsys, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	case mode.IsRegular():
		in, err := fsys.OpenFile(src, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := fsys.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm()&0o700|0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	default:
		return nil // sockets, devices, fifos are not copied; RemoveAll drops them
	}
}

// AncestorPins returns, sorted and deduplicated, every directory A such
// that A is inside some allow root (A == root or below it) and A is a
// proper ancestor of some explicit protected path of spec (DenyWrite, glob
// directories) or of a top-level mandatory path (RepoMandatoryDeny entries
// directly under a RepoRoots entry, the literal GitDirMandatoryDeny entries
// directly under a GitDirs entry) — i.e. the directories whose rename would
// let the child swap a protected path (pure; golden-tested). Deeper
// mandatory matches are covered by darwin's any-depth regex and by linux
// PostCheck instead of pins.
func AncestorPins(spec Spec) []string {
	var roots []string
	for _, a := range spec.AllowWrite {
		roots = append(roots, filepath.Clean(a))
	}
	var targets []string
	for _, d := range spec.DenyWrite {
		targets = append(targets, filepath.Clean(d))
	}
	for _, g := range spec.DenyWriteGlobs {
		targets = append(targets, filepath.Clean(g)) // its parent is the "glob directory"
	}
	for _, r := range spec.RepoRoots {
		for _, e := range RepoMandatoryDeny {
			if t := strings.Trim(e, "/"); t != "" && !strings.ContainsRune(t, '*') {
				targets = append(targets, filepath.Join(r, filepath.FromSlash(t)))
			}
		}
	}
	for _, g := range spec.GitDirs {
		for _, e := range GitDirMandatoryDeny {
			if t := strings.Trim(e, "/"); t != "" && !strings.ContainsRune(t, '*') {
				targets = append(targets, filepath.Join(g, filepath.FromSlash(t)))
			}
		}
	}
	set := map[string]bool{}
	for _, t := range targets {
		for _, root := range roots {
			if !within(t, root) || t == root {
				continue
			}
			for a := filepath.Dir(t); within(a, root); a = filepath.Dir(a) {
				set[a] = true
				if a == root || filepath.Dir(a) == a {
					break
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
