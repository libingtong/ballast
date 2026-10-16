package sandbox

import (
	"bytes"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/sys"
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSpec, fmt.Sprintf(format, a...))
}

// maxLinkFile bounds the gitfile / commondir / gitdir link files read by
// ResolveGitDirs (all are agent-writable; a real one is a single short line).
const maxLinkFile = 64 << 10

// readLink reads a small, single-line link file through fsys.
func readLinkFile(fsys sys.FS, p string) (string, error) {
	fi, err := fsys.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	if fi.Size() > maxLinkFile {
		return "", fmt.Errorf("%s is too large", filepath.Base(p))
	}
	b, err := fsys.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(b) > maxLinkFile {
		return "", fmt.Errorf("%s is too large", filepath.Base(p))
	}
	b = bytes.TrimSpace(b)
	if bytes.ContainsAny(b, "\x00\r\n") {
		return "", fmt.Errorf("%s is not a single line", filepath.Base(p))
	}
	return string(b), nil
}

// resolveRel resolves target (absolute, or relative to base) through
// EvalSymlinks.
func resolveRel(fsys sys.FS, base, target string) (string, error) {
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	return fsys.EvalSymlinks(filepath.Clean(target))
}

// ResolveGitDirs derives the git dir and common dir of workTree and checks
// their provenance; both inputs are agent-writable files (the work tree's
// ".git" gitfile and the git dir's "commondir"), so they are trusted only in
// two shapes (frozen, docs/p2-design.md §3.4):
//
//  1. inside the work tree: <workTree>/.git is a directory (not a symlink);
//     gitDir == commonDir == <workTree>/.git, and if a "commondir" file is
//     present it must resolve to that same directory;
//  2. a standard linked worktree: <workTree>/.git is a regular file
//     "gitdir: X/.git/worktrees/<n>" (absolute, or relative to workTree),
//     where after EvalSymlinks gitDir == X/.git/worktrees/<n> with <n> a
//     single path element, gitDir/commondir resolves (relative to gitDir)
//     to X/.git == commonDir, X/.git is a directory, and
//     gitDir/gitdir (the back link) contains, after Clean and
//     EvalSymlinks, exactly <workTree>/.git.
//
// Anything else (gitfile pointing anywhere else, a submodule work tree
// whose git dir lives in a superproject's modules/, a commondir not equal to
// X/.git, a missing or mismatched back link, symlinked .git) ⇒ error
// wrapping ErrInvalidSpec; the runner records outcome infra, error
// "sandbox_spec_invalid", and does not run the test command. (Submodule
// work trees as verify targets are a possible later contract addition.)
func ResolveGitDirs(fsys sys.FS, workTree string) (gitDir, commonDir string, err error) {
	if !filepath.IsAbs(workTree) {
		return "", "", invalid("work tree %q is not absolute", workTree)
	}
	wt := filepath.Clean(workTree)
	dotGit := filepath.Join(wt, ".git")
	fi, err := fsys.Lstat(dotGit)
	if err != nil {
		return "", "", invalid("%s: %v", dotGit, err)
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", "", invalid("%s is a symlink", dotGit)
	case fi.IsDir():
		// Shape 1.
		if _, cerr := fsys.Lstat(filepath.Join(dotGit, "commondir")); cerr == nil {
			link, rerr := readLinkFile(fsys, filepath.Join(dotGit, "commondir"))
			if rerr != nil {
				return "", "", invalid("commondir: %v", rerr)
			}
			got, rerr := resolveRel(fsys, dotGit, link)
			if rerr != nil {
				return "", "", invalid("commondir: %v", rerr)
			}
			want, rerr := fsys.EvalSymlinks(dotGit)
			if rerr != nil {
				return "", "", invalid("%s: %v", dotGit, rerr)
			}
			if got != want {
				return "", "", invalid("commondir of %s points elsewhere", dotGit)
			}
		}
		return dotGit, dotGit, nil
	case !fi.Mode().IsRegular():
		return "", "", invalid("%s is neither a directory nor a regular file", dotGit)
	}
	// Shape 2: a gitfile.
	line, err := readLinkFile(fsys, dotGit)
	if err != nil {
		return "", "", invalid("gitfile: %v", err)
	}
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", "", invalid("gitfile %s has no gitdir line", dotGit)
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if target == "" {
		return "", "", invalid("gitfile %s is empty", dotGit)
	}
	gd, err := resolveRel(fsys, wt, target)
	if err != nil {
		return "", "", invalid("gitdir %q: %v", target, err)
	}
	// gd must be X/.git/worktrees/<n>.
	wtDir := filepath.Dir(gd)
	common := filepath.Dir(wtDir)
	if filepath.Base(wtDir) != "worktrees" || filepath.Base(common) != ".git" || filepath.Base(gd) == "" ||
		gd == wtDir || wtDir == common {
		return "", "", invalid("gitdir %q is not <X>/.git/worktrees/<n>", gd)
	}
	if cfi, cerr := fsys.Lstat(common); cerr != nil || !cfi.IsDir() {
		return "", "", invalid("%s is not a directory", common)
	}
	if gfi, gerr := fsys.Lstat(gd); gerr != nil || !gfi.IsDir() {
		return "", "", invalid("%s is not a directory", gd)
	}
	clink, err := readLinkFile(fsys, filepath.Join(gd, "commondir"))
	if err != nil {
		return "", "", invalid("commondir: %v", err)
	}
	cgot, err := resolveRel(fsys, gd, clink)
	if err != nil {
		return "", "", invalid("commondir: %v", err)
	}
	if cgot != common {
		return "", "", invalid("commondir of %s is %q, not %q", gd, cgot, common)
	}
	back, err := readLinkFile(fsys, filepath.Join(gd, "gitdir"))
	if err != nil {
		return "", "", invalid("back link: %v", err)
	}
	bgot, err := resolveRel(fsys, gd, back)
	if err != nil {
		return "", "", invalid("back link: %v", err)
	}
	realWT, err := fsys.EvalSymlinks(wt)
	if err != nil {
		return "", "", invalid("%s: %v", wt, err)
	}
	if bgot != filepath.Join(realWT, ".git") {
		return "", "", invalid("back link of %s does not name %s", gd, dotGit)
	}
	return gd, common, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// resolvePartial resolves symlinks in the longest existing prefix of p and
// appends the rest unchanged.
func resolvePartial(fsys sys.FS, p string) string {
	p = filepath.Clean(p)
	rest := ""
	cur := p
	for {
		if r, err := fsys.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// globMatchesAncestorOrSelf reports whether p or one of its ancestors matches
// the glob g (an absolute path whose last element contains '*').
func globCovers(g, p string) bool {
	for a := p; ; a = filepath.Dir(a) {
		if ok, _ := filepath.Match(g, a); ok {
			return true
		}
		if filepath.Dir(a) == a {
			return false
		}
	}
}

func sameSet(a, b []string) bool {
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	x = dedupe(x)
	y = dedupe(y)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func dedupe(s []string) []string {
	out := s[:0:0]
	seen := map[string]bool{}
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ValidateSpec rejects specs that would make the sandbox meaningless:
// an AllowWrite entry equal to "/", to Home or to an ancestor of Home, or
// equal to / inside a DenyWrite directory (sole exception, v1.5: Spec.Tree
// under the rules documented on Spec.Tree); a Cwd outside AllowWrite; a
// relative path anywhere; and a GitDirs set that differs from what
// ResolveGitDirs(fsys, r) returns for the RepoRoots entries r (re-checked
// through fsys at wrap time, so W can never be widened to an arbitrary
// directory such as another repository's .git through an agent-written
// gitfile or commondir). Errors wrap ErrInvalidSpec.
func ValidateSpec(fsys sys.FS, spec Spec) error {
	// Paths: absolute, no control characters (the darwin profile is text).
	check := func(what string, ps ...string) error {
		for _, p := range ps {
			if p == "" {
				return invalid("%s: empty path", what)
			}
			if !filepath.IsAbs(p) {
				return invalid("%s: %q is not absolute", what, p)
			}
			if hasControl(p) {
				return invalid("%s: path contains a control character", what)
			}
		}
		return nil
	}
	for _, c := range []struct {
		what string
		ps   []string
	}{
		{"AllowWrite", spec.AllowWrite}, {"RepoRoots", spec.RepoRoots}, {"GitDirs", spec.GitDirs},
		{"DenyWrite", spec.DenyWrite}, {"DenyWriteGlobs", spec.DenyWriteGlobs},
	} {
		if err := check(c.what, c.ps...); err != nil {
			return err
		}
	}
	if err := check("Home", spec.Home); err != nil {
		return err
	}
	if err := check("Cwd", spec.Cwd); err != nil {
		return err
	}
	for _, p := range []string{spec.TmpDir, spec.Tree, spec.WorkRoot} {
		if p != "" {
			if err := check("path", p); err != nil {
				return err
			}
		}
	}
	for _, kv := range spec.LoaderEnv {
		k := envKey(kv)
		if !strings.HasPrefix(k, "LD_") || !strings.Contains(kv, "=") {
			return invalid("LoaderEnv entry %q is not LD_*=value", k)
		}
	}
	if len(spec.AllowWrite) == 0 {
		return invalid("AllowWrite is empty")
	}

	home := filepath.Clean(spec.Home)
	homes := []string{home}
	if r := resolvePartial(fsys, home); r != home {
		homes = append(homes, r)
	}
	denyDirs := make([]string, 0, len(spec.DenyWrite))
	for _, d := range spec.DenyWrite {
		denyDirs = append(denyDirs, filepath.Clean(d))
	}
	tree := ""
	if spec.Tree != "" {
		tree = filepath.Clean(spec.Tree)
	}

	roots := make([]string, 0, len(spec.AllowWrite))
	for _, a := range spec.AllowWrite {
		roots = append(roots, filepath.Clean(a))
	}
	if spec.TmpDir != "" {
		roots = append(roots, filepath.Clean(spec.TmpDir))
	}
	for _, w := range roots {
		forms := []string{w}
		if r := resolvePartial(fsys, w); r != w {
			forms = append(forms, r)
		}
		for _, f := range forms {
			if f == string(filepath.Separator) {
				return invalid("writable root %q is the file system root", w)
			}
			for _, h := range homes {
				if within(h, f) { // f == h or f is an ancestor of h
					return invalid("writable root %q is the home directory or an ancestor of it", w)
				}
			}
			if f == tree || (tree != "" && within(f, tree)) {
				continue // checked below
			}
			for _, d := range denyDirs {
				if within(f, d) {
					return invalid("writable root %q lies inside the protected path %q", w, d)
				}
			}
			for _, g := range spec.DenyWriteGlobs {
				if globCovers(filepath.Clean(g), f) {
					return invalid("writable root %q lies inside the protected glob %q", w, g)
				}
			}
		}
	}

	// Cwd inside AllowWrite.
	cwd := filepath.Clean(spec.Cwd)
	inW := false
	for _, a := range spec.AllowWrite {
		if within(cwd, filepath.Clean(a)) {
			inW = true
			break
		}
	}
	if !inW {
		return invalid("Cwd %q is outside AllowWrite", spec.Cwd)
	}

	// RepoRoots are AllowWrite entries.
	allow := map[string]bool{}
	for _, a := range spec.AllowWrite {
		allow[filepath.Clean(a)] = true
	}
	for _, r := range spec.RepoRoots {
		if !allow[filepath.Clean(r)] {
			return invalid("repo root %q is not in AllowWrite", r)
		}
	}

	if tree != "" {
		return validateTree(fsys, spec, tree, cwd)
	}
	// GitDirs == union of ResolveGitDirs over RepoRoots.
	var want []string
	for _, r := range spec.RepoRoots {
		gd, cd, err := ResolveGitDirs(fsys, r)
		if err != nil {
			return err
		}
		want = append(want, gd, cd)
	}
	var have []string
	for _, g := range spec.GitDirs {
		have = append(have, filepath.Clean(g))
	}
	if !sameSet(want, have) {
		return invalid("GitDirs %v differ from the resolved git dirs %v", have, dedupe(want))
	}
	return nil
}

func validateTree(fsys sys.FS, spec Spec, tree, cwd string) error {
	if spec.WorkRoot == "" {
		return invalid("Tree is set but WorkRoot is empty")
	}
	wr := filepath.Clean(spec.WorkRoot)
	if filepath.Base(tree) != "tree" {
		return invalid("Tree %q is not <WorkRoot>/<job>/tree", tree)
	}
	job := filepath.Dir(tree)
	if filepath.Dir(job) != wr || filepath.Base(job) == "" || filepath.Base(job) == "." || filepath.Base(job) == string(filepath.Separator) {
		return invalid("Tree %q is not <WorkRoot>/<one element>/tree", tree)
	}
	if real, err := fsys.EvalSymlinks(tree); err != nil {
		return invalid("Tree %q: %v", tree, err)
	} else if real != tree {
		return invalid("Tree %q has a symlink on its path (resolves to %q)", tree, real)
	}
	if len(spec.RepoRoots) != 1 || filepath.Clean(spec.RepoRoots[0]) != tree {
		return invalid("with Tree set RepoRoots must be exactly [Tree]")
	}
	if len(spec.GitDirs) != 0 {
		return invalid("with Tree set GitDirs must be empty")
	}
	if !within(cwd, tree) {
		return invalid("with Tree set Cwd must lie inside Tree")
	}
	found := false
	for _, a := range spec.AllowWrite {
		a = filepath.Clean(a)
		if a == tree {
			found = true
		}
		// Other roots must not be (or contain) the private tree area.
		if a != tree && (within(a, wr) || within(wr, a)) {
			return invalid("writable root %q overlaps the job tree area %q", a, wr)
		}
	}
	if !found {
		return invalid("Tree %q is not in AllowWrite", tree)
	}
	return nil
}
