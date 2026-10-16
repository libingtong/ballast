package tier

import (
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/sys"
)

// dirOfLiteral splits p on '/' (and '\' when goos is windows) without
// resolving anything; it mirrors the dirOf of `org render`.
func dirOfLiteral(p, goos string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || (goos == "windows" && p[i] == '\\') {
			if i == 0 {
				return p[:1]
			}
			return p[:i]
		}
	}
	return "."
}

func managedForms(lay layout.Layout, self string, extra []string) (denyWrite, permDeny []string) {
	orgDir := dirOfLiteral(lay.OrgRules, lay.GOOS)
	binDir := dirOfLiteral(self, lay.GOOS)

	// Each tuple is one protected location: the denyWrite string plus how it
	// appears inside Edit(...). Only Edit(...) is emitted: in Claude Code
	// 2.1.300 an Edit rule covers the Edit and Write tools alike, and
	// Write(...) / NotebookEdit(...) path entries are warned about as invalid
	// (docs/facts/M0b-CONCLUSIONS.md "org render 的路径写法与 ManagedForms").
	type form struct {
		dw   []string // strings for sandbox.filesystem.denyWrite
		perm []string // strings inside Tool(...)
	}
	var forms []form
	dir := func(p string) form {
		return form{dw: []string{p}, perm: []string{strings.TrimSuffix(permAbs(p), "/") + "/**"}}
	}
	forms = append(forms,
		dir(orgDir),
		dir("~/.ballast"),
		dir("./.ballast"),
		dir("**/.ballast"),
		dir(binDir),
	)
	for _, e := range extra {
		forms = append(forms, form{dw: []string{e}, perm: []string{permAbs(e), permAbs(e) + "/**"}})
	}
	for _, p := range []string{"~/.claude/settings*.json", "./.claude/settings*.json", "**/.claude/settings*.json"} {
		forms = append(forms, form{dw: []string{p}, perm: []string{p}})
	}

	seen := map[string]bool{}
	for _, f := range forms {
		for _, s := range f.dw {
			if !seen[s] {
				seen[s] = true
				denyWrite = append(denyWrite, s)
			}
		}
	}
	for _, tool := range []string{"Edit"} {
		seenP := map[string]bool{}
		for _, f := range forms {
			for _, s := range f.perm {
				v := tool + "(" + s + ")"
				if !seenP[v] {
					seenP[v] = true
					permDeny = append(permDeny, v)
				}
			}
		}
	}
	return denyWrite, permDeny
}

// permAbs rewrites a POSIX absolute path into the "//" form that Claude's
// permission rules read as a filesystem-absolute path: a single leading "/"
// is project-relative in Edit(...) rules, so Edit(/srv/x/**) does not stop a
// write to /srv/x/y while Edit(//srv/x/**) does (M0b, 2.1.300). Anything
// else ("~/", "./", "**/", a Windows drive path) is returned unchanged, and
// so is a path that already starts with "//".
func permAbs(p string) string {
	if strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") {
		return "/" + p
	}
	return p
}

// protectedPaths implements ProtectedPaths.
func protectedPaths(fsys sys.FS, lay layout.Layout, self, repo string, extra []string) []ProtectedPath {
	home := lay.InstallRoot
	var out []ProtectedPath
	add := func(kind ProtectedKind, p string, dir, glob bool) {
		if p == "" {
			return
		}
		pp := ProtectedPath{Kind: kind, Path: p, Dir: dir, Glob: glob}
		pp.Resolved = resolvedForm(fsys, p)
		out = append(out, pp)
	}
	add(ProtectedOrgDir, filepath.Dir(lay.OrgRules), true, false)
	add(ProtectedBallastHome, lay.Home, true, false)
	if repo != "" {
		add(ProtectedRepoBallast, filepath.Join(repo, ".ballast"), true, false)
	}
	if self != "" {
		add(ProtectedBinaryDir, filepath.Dir(self), true, false)
	}
	claudeDir := lay.ClaudeDir
	if claudeDir == "" && home != "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	if claudeDir != "" {
		add(ProtectedClaudeUser, filepath.Join(claudeDir, "settings*.json"), false, true)
	}
	if repo != "" {
		add(ProtectedClaudeRepo, filepath.Join(repo, ".claude", "settings*.json"), false, true)
	}
	for _, e := range extra {
		if strings.HasPrefix(e, "~/") && home != "" {
			e = filepath.Join(home, filepath.FromSlash(e[2:]))
		}
		add(ProtectedExtraPath, e, true, false)
	}
	return out
}

// resolvedForm returns the symlink-free form of p when it differs from p, ""
// otherwise. The deepest existing ancestor is resolved when p itself does not
// exist (macOS /var → /private/var also applies to a not-yet-created
// <repo>/.ballast), and a glob last element is carried over verbatim.
func resolvedForm(fsys sys.FS, p string) string {
	base := ""
	cur := p
	if strings.ContainsAny(filepath.Base(p), "*?[") {
		base = filepath.Base(p)
		cur = filepath.Dir(p)
	}
	tail := ""
	for {
		r, err := fsys.EvalSymlinks(cur)
		if err == nil {
			if tail != "" {
				r = filepath.Join(r, tail)
			}
			if base != "" {
				r = filepath.Join(r, base)
			}
			if r == p {
				return ""
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		if tail == "" {
			tail = filepath.Base(cur)
		} else {
			tail = filepath.Join(filepath.Base(cur), tail)
		}
		cur = parent
	}
}
