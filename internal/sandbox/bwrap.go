package sandbox

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// BwrapArgs renders the bwrap arguments before "--" for spec, given the
// subset of paths that exist (pure; golden-tested).
//
// existing is the set of paths that exist at wrap time. Wrap records every
// directory under both p and p+"/" (the second key is the directory marker);
// a path with only the plain key is treated as a non-directory. Mandatory
// matches (the Wrapped.Baseline) are read from the keys of existing itself:
// every key that lies below a Spec.RepoRoots / Spec.GitDirs entry and
// matches MatchMandatory is ro-bound (matches below a matched subtree top are
// covered by the top).
//
// Implementation notes beyond the frozen order (package doc):
//   - A directory matched by a CONTAINER entry (".git": the exact entry that
//     ".git/hooks/" and ".git/config" extend) is bound read-write, not
//     read-only: a bind mount makes it a mount point, which rename(2) and
//     rmdir(2) refuse with EBUSY, so it cannot be replaced, while the index
//     and objects inside stay writable and ".git/hooks", ".git/config" are
//     ro-bound on top. A file matched by it (a gitfile) is ro-bound.
//   - Residual (hardening, registered rather than fixed to keep the frozen
//     mount order): "directory or not" is decided at Wrap time with Lstat and
//     bwrap follows symlinks when it binds, so a writer OUTSIDE the sandbox
//     that swaps a pinned ancestor (e.g. <repo>/.git) for a symlink between
//     Wrap and exec could make the rw bind land on the link target, after the
//     ro-binds of the D entries outside W. Linux origin=cli only; the later
//     baseline ro-binds (hooks, config) must also exist in the target for
//     bwrap to start. A fix would be --bind-fd on O_PATH|O_NOFOLLOW handles,
//     or a second ro-bind of those D entries after the pins.
//   - AncestorPins entries that are not directories are skipped (the
//     top-level ".git" of a linked worktree is a gitfile).
func BwrapArgs(spec Spec, existing map[string]bool) []string {
	ex := func(p string) bool { return existing[p] }
	isDir := func(p string) bool { return existing[p] && existing[p+"/"] }
	args := []string{"--die-with-parent", "--new-session", "--unshare-pid"}
	if spec.Network != model.NetworkAllow {
		args = append(args, "--unshare-net")
	}
	args = append(args,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--tmpfs", "/var/run",
	)
	bind := func(flag, p string) { args = append(args, flag, p, p) }

	tree := ""
	if spec.Tree != "" {
		tree = filepath.Clean(spec.Tree)
	}
	var w []string
	for _, a := range spec.AllowWrite {
		if a != "" {
			w = append(w, filepath.Clean(a))
		}
	}
	w = dedupe(w)
	var deny []string
	for _, d := range spec.DenyWrite {
		if d != "" {
			deny = append(deny, filepath.Clean(d))
		}
	}
	deny = dedupe(deny)

	// W (except Tree).
	for _, a := range w {
		if a != tree && ex(a) {
			bind("--bind", a)
		}
	}
	// D not inside Tree.
	for _, d := range deny {
		if (tree == "" || !within(d, tree)) && ex(d) {
			bind("--ro-bind", d)
		}
	}
	// The one carve-out inside the read-only ~/.ballast.
	if tree != "" && ex(tree) {
		bind("--bind", tree)
	}
	// Ancestor pins.
	pins := AncestorPins(spec)
	pinned := map[string]bool{}
	for _, a := range pins {
		if isDir(a) {
			bind("--bind", a)
			pinned[a] = true
		}
	}
	// D inside a W root; a glob inside W ro-binds its whole directory.
	done := map[string]bool{}
	for _, d := range deny {
		if withinAny(d, w) && ex(d) && !done[d] {
			done[d] = true
			bind("--ro-bind", d)
		}
	}
	for _, g := range spec.DenyWriteGlobs {
		dir := filepath.Dir(filepath.Clean(g))
		if withinAny(dir, w) && ex(dir) && !done[dir] {
			done[dir] = true
			bind("--ro-bind", dir)
		}
	}

	// Baseline: the mandatory matches that exist.
	repoPats := parsePatterns(RepoMandatoryDeny)
	gitPats := parsePatterns(GitDirMandatoryDeny)
	var keys []string
	for k := range existing {
		if existing[k] && !strings.HasSuffix(k, "/") {
			keys = append(keys, filepath.Clean(k))
		}
	}
	sort.Strings(keys)
	var tops []string
	for _, k := range keys {
		matched, subtree, container := false, false, false
		for _, r := range spec.RepoRoots {
			r = filepath.Clean(r)
			if k != r && within(k, r) {
				rel, _ := filepath.Rel(r, k)
				if m, s, c := matchInfo(repoPats, true, filepath.ToSlash(rel)); m {
					matched, subtree, container = true, subtree || s, container || c
				}
			}
		}
		for _, g := range spec.GitDirs {
			g = filepath.Clean(g)
			if k != g && within(k, g) {
				rel, _ := filepath.Rel(g, k)
				if m, s, c := matchInfo(gitPats, false, filepath.ToSlash(rel)); m {
					matched, subtree, container = true, subtree || s, container || c
				}
			}
		}
		if !matched || withinAny(k, tops) {
			continue
		}
		if subtree {
			tops = append(tops, k)
		}
		switch {
		case container && isDir(k):
			if !pinned[k] {
				bind("--bind", k)
			}
		default:
			bind("--ro-bind", k)
		}
	}

	args = append(args, "--chdir", filepath.Clean(spec.Cwd), "--setenv", "TMPDIR", "/tmp")
	for _, kv := range spec.LoaderEnv {
		k, v, _ := strings.Cut(kv, "=")
		args = append(args, "--setenv", k, v)
	}
	return args
}
