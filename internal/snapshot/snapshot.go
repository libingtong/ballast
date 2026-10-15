package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// RepoInst computes repo_inst = hex(sha256(toplevel + "\x00" + commonDir))[:16]
// from REALPATHs in OS form (plan B3). Implemented here because it is part
// of the frozen contract (state paths and evidence paths depend on it).
func RepoInst(toplevel, commonDir string) string {
	h := sha256.Sum256([]byte(toplevel + "\x00" + commonDir))
	return hex.EncodeToString(h[:8])
}

// Locator implements ports.RepoLocator.
type Locator struct {
	FS sys.FS
}

var _ ports.RepoLocator = (*Locator)(nil)

// Locate implements ports.RepoLocator. cwd must be absolute; it is
// resolved to its realpath first. Outside a work tree the error wraps
// model.ErrNotRepo.
func (l *Locator) Locate(cwd string) (*model.RepoInfo, error) {
	return l.locate(cwd)
}

// Branch implements ports.RepoLocator.
func (l *Locator) Branch(repo *model.RepoInfo) (string, model.Tri) {
	return Branch(l.fsys(), repo)
}

// Branch reads the current branch short name from HEAD ("main"); detached
// HEAD ⇒ ("", TriFalse); unreadable ⇒ ("", TriUnknown).
func Branch(fsys sys.FS, repo *model.RepoInfo) (string, model.Tri) {
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	return branchOf(fsys, repo)
}

// Options control a snapshot computation.
type Options struct {
	FS    sys.FS
	Clock sys.Clock
	// Deadline bounds the git fallback (zero ⇒ no deadline, runner only).
	// The native loops honour it too (checked every few hundred entries).
	Deadline time.Time
	// AllowFallback enables gitfallback.go; false ⇒ ErrFallback is returned.
	AllowFallback bool
	// Git runs git for the fallback (nil ⇒ ExecGit).
	Git GitRunner
	// Env supplies HOME / XDG_CONFIG_HOME / GIT_CONFIG_* for finding the
	// global and system git config and TMPDIR for the fallback's temporary
	// index, and is the base of the fallback git environment. nil ⇒
	// sys.OSEnv.
	Env sys.Env

	// inMemoryLimit overrides the size above which files are streamed
	// instead of read at once (tests only).
	inMemoryLimit int64
}

// TreeC computes C for a commit form (see package doc). For the paths form
// the pathspecs are taken relative to form.Dir when that is set, else
// relative to the repo root; magic pathspecs (":(top)...") are left to the
// git fallback.
func TreeC(opt Options, repo *model.RepoInfo, form model.CommitForm, f *Filter) (model.TreeID, error) {
	switch form.Kind {
	case model.CommitPlain:
		return compute(opt, repo, request{}, f)
	case model.CommitAll:
		return compute(opt, repo, request{refresh: true}, f)
	case model.CommitPaths:
		if repo == nil {
			return "", errors.New("snapshot: nil repo")
		}
		args, pats, native, err := resolveSpecs(opt.withDefaults().FS, repo, form.Dir, form.Paths)
		if err != nil {
			return "", err
		}
		req := request{refresh: true, limited: true, specs: pats, args: args}
		if !native {
			req.forceFallback = "magic pathspec"
		}
		return compute(opt, repo, req, f)
	}
	reason := form.Reason
	if reason == "" {
		reason = string(form.Kind)
	}
	return "", errors.New("snapshot: unparseable commit form: " + reason)
}

// TreeS computes S (work tree snapshot of tracked files).
func TreeS(opt Options, repo *model.RepoInfo, f *Filter) (model.TreeID, error) {
	return compute(opt, repo, request{refresh: true}, f)
}

// NeedsFallback reports whether native computation cannot guarantee git
// equivalence for this repo (attributes, autocrlf, sha256, ...), judging by
// the repository's own config and the attribute files at repo level (info/
// attributes and the root .gitattributes). It never looks at user or system
// config; NeedsFallbackEnv does.
//
// This is the strict, contract-level answer. TreeS/TreeC are more precise:
// text/eol/autocrlf settings only matter for dirty files that contain CRLF,
// and the plain C form never reads the work tree, so they use the git
// fallback only when a conversion would actually be applied.
func NeedsFallback(fsys sys.FS, repo *model.RepoInfo) (bool, string) {
	return NeedsFallbackEnv(fsys, nil, repo)
}

// NeedsFallbackEnv is NeedsFallback that also reads the user and system git
// config (and global attributes) found through env.
func NeedsFallbackEnv(fsys sys.FS, env sys.Env, repo *model.RepoInfo) (bool, string) {
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	if of := repo.ObjectFormat; of != "" && of != "sha1" {
		return true, "object format " + of
	}
	set := loadSettings(fsys, env, repo)
	if err := set.checkNative(); err != nil {
		return true, reasonOf(err)
	}
	switch {
	case set.convRisk:
		return true, "core.autocrlf or core.eol conversion"
	case set.safeCRLF:
		return true, "core.safecrlf"
	}
	fl, err := newAttrResolver(fsys, repo, set).flagsFor("")
	switch {
	case err != nil:
		return true, "attributes unreadable: " + err.Error()
	case fl.hard:
		return true, "filter, ident or working-tree-encoding attribute"
	case fl.soft:
		return true, "text, eol or crlf attribute"
	}
	return false, ""
}

func reasonOf(err error) string {
	return strings.TrimPrefix(err.Error(), model.ErrFallback.Error()+": ")
}
