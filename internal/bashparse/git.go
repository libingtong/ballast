package bashparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// ParseGit parses git global options of a node whose command is git.
// argv[0] must be the git word. Returns nil when argv[0] is not git.
func ParseGit(argv []model.Word) *model.GitInvocation {
	if len(argv) == 0 || argv[0].Dynamic || !isGitName(argv[0].Lit) {
		return nil
	}
	g := &model.GitInvocation{}
	i := 1
	for i < len(argv) {
		a := argv[i]
		s := a.Lit
		if a.Dynamic || len(s) == 0 || s[0] != '-' {
			if a.Dynamic {
				g.SubDynamic = true
			} else {
				g.Sub = s
			}
			if i+1 < len(argv) {
				g.Args = append([]model.Word(nil), argv[i+1:]...)
			}
			return g
		}
		// value returns the option value: attached via "=" or the next word.
		value := func(attached bool, v string) (model.Word, bool) {
			if attached {
				return model.Word{Lit: v, Dynamic: a.Dynamic}, true
			}
			if i+1 < len(argv) {
				i++
				return argv[i], true
			}
			return model.Word{}, false
		}
		name, v, attached := strings.Cut(s, "=")
		switch name {
		case "-C":
			if w, ok := value(false, ""); ok {
				addGitCwd(g, w)
			}
		case "-c":
			if w, ok := value(false, ""); ok {
				g.Config = append(g.Config, w.Lit)
			}
		case "--git-dir":
			if w, ok := value(attached, v); ok {
				g.GitDir = w.Lit
			}
		case "--work-tree":
			if w, ok := value(attached, v); ok {
				g.WorkTree = w.Lit
			}
		case "--namespace", "--super-prefix", "--config-env", "--attr-source":
			value(attached, v)
		case "--no-pager", "-P":
			g.NoPager = true
		case "-p", "--paginate":
			g.NoPager = false
		}
		i++
	}
	return g
}

func isGitName(s string) bool {
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 4 && strings.EqualFold(s[len(s)-4:], ".exe") {
		s = s[:len(s)-4]
	}
	return strings.EqualFold(s, "git")
}

// addGitCwd accumulates -C like git: each relative directory is resolved
// against the previous one, an absolute one replaces it, "" is a no-op.
func addGitCwd(g *model.GitInvocation, w model.Word) {
	if w.Dynamic {
		g.CwdDynamic = true
		return
	}
	d := w.Lit
	if d == "" {
		return
	}
	if g.Cwd == "" || model.IsAbsShellPath(d) {
		g.Cwd = d
		return
	}
	if strings.HasSuffix(g.Cwd, "/") || strings.HasSuffix(g.Cwd, `\`) {
		g.Cwd += d
		return
	}
	sep := "/"
	if strings.Contains(g.Cwd, `\`) && !strings.Contains(g.Cwd, "/") {
		sep = `\`
	}
	g.Cwd += sep + d
}
