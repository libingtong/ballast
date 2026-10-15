package evidence

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// Option tables of `git commit` (git 2.50). Anything not listed makes the
// invocation unparseable: an unknown option may or may not take a value and
// would shift the path arguments.
var (
	// longFlags take no value.
	longFlags = map[string]bool{
		"allow-empty": true, "allow-empty-message": true, "branch": true, "dry-run": true,
		"edit": true, "long": true, "no-edit": true, "no-post-rewrite": true, "no-verify": true,
		"null": true, "porcelain": true, "quiet": true, "reset-author": true, "short": true,
		"signoff": true, "status": true, "verbose": true, "verify": true, "pathspec-file-nul": true,
		"no-status": true, "no-signoff": true, "no-gpg-sign": true, "no-verbose": true,
		"no-quiet": true, "no-short": true, "no-long": true, "no-branch": true,
		"no-reset-author": true, "no-allow-empty": true, "no-allow-empty-message": true,
		"no-dry-run": true, "no-null": true, "no-porcelain": true,
	}
	// longValue take a value, attached with '=' or as the next word.
	longValue = map[string]bool{
		"author": true, "cleanup": true, "date": true, "file": true, "fixup": true,
		"message": true, "reedit-message": true, "reuse-message": true, "squash": true,
		"template": true, "trailer": true,
	}
	// longOptional take an optional value, attached with '=' only.
	longOptional = map[string]bool{"gpg-sign": true, "untracked-files": true}
)

const (
	shortValue    = "mFCct" // value: rest of the cluster or the next word
	shortOptional = "uS"    // optional value: rest of the cluster only
	shortFlag     = "nsevqz"
)

// ParseCommitForm turns a git commit invocation into a CommitForm (package
// doc). Reason is left empty: the form is compared as a whole by the
// acceptance table.
func ParseCommitForm(git *model.GitInvocation, cwd string) model.CommitForm {
	unparseable := model.CommitForm{Kind: model.CommitUnparseable}
	if git == nil || git.Sub != "commit" || git.SubDynamic || git.CwdDynamic || git.GitDir != "" || git.WorkTree != "" {
		return unparseable
	}
	var (
		all, include, only, amend bool
		paths                     []string
		dashdash                  bool
	)
	args := git.Args
	for i := 0; i < len(args); i++ {
		w := args[i]
		if dashdash || !isOption(w.Lit) {
			if w.Dynamic {
				return unparseable
			}
			paths = append(paths, w.Lit)
			continue
		}
		if w.Dynamic {
			return unparseable
		}
		lit := w.Lit
		if lit == "--" {
			dashdash = true
			continue
		}
		// takeValue consumes the next word as an option value. A dynamic
		// value (a message built by command substitution, say) cannot change
		// which tree is committed, so it is accepted.
		takeValue := func() bool {
			if i+1 >= len(args) {
				return false
			}
			i++
			return true
		}
		if strings.HasPrefix(lit, "--") {
			name, _, hasValue := strings.Cut(lit[2:], "=")
			switch {
			case name == "all":
				if hasValue {
					return unparseable
				}
				all = true
			case name == "amend":
				amend = true
			case name == "include":
				include = true
			case name == "only":
				only = true
			case name == "interactive" || name == "patch" || name == "pathspec-from-file":
				return unparseable
			case longFlags[name]:
				if hasValue {
					return unparseable
				}
			case longOptional[name]:
			case longValue[name]:
				if !hasValue && !takeValue() {
					return unparseable
				}
			default:
				return unparseable
			}
			continue
		}
		// Short option cluster.
		cluster := lit[1:]
		for j := 0; j < len(cluster); j++ {
			c := cluster[j]
			switch {
			case c == 'a':
				all = true
			case c == 'i':
				include = true
			case c == 'o':
				only = true
			case c == 'p':
				return unparseable
			case strings.IndexByte(shortFlag, c) >= 0:
			case strings.IndexByte(shortOptional, c) >= 0:
				j = len(cluster)
			case strings.IndexByte(shortValue, c) >= 0:
				if j == len(cluster)-1 && !takeValue() {
					return unparseable
				}
				j = len(cluster)
			default:
				return unparseable
			}
		}
	}

	form := model.CommitForm{Kind: model.CommitPlain, Amend: amend}
	switch {
	case all && len(paths) > 0:
		return unparseable // git itself refuses
	case (include || only) && len(paths) == 0:
		return unparseable // git itself refuses
	case all:
		form.Kind = model.CommitAll
	case len(paths) > 0:
		form.Kind = model.CommitPaths
		form.Paths = paths
		form.Include = include
	}
	if git.Cwd != "" {
		dir, ok := resolveDir(cwd, git.Cwd)
		if !ok {
			return unparseable
		}
		form.Dir = dir
	}
	return form
}

func isOption(s string) bool { return len(s) > 1 && s[0] == '-' }

// resolveDir computes clean(join(cwd, c)) in forward-slash form; an absolute
// c stands alone. A relative c needs an absolute cwd.
func resolveDir(cwd, c string) (string, bool) {
	c = filepath.ToSlash(c)
	if isAbsSlash(c) {
		return path.Clean(c), true
	}
	cwd = filepath.ToSlash(cwd)
	if !isAbsSlash(cwd) {
		return "", false
	}
	return path.Clean(path.Join(cwd, c)), true
}

// isAbsSlash reports an absolute path in forward-slash form: "/x" or "c:/x".
func isAbsSlash(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '/' &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}
