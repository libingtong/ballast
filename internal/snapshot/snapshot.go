package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
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

// Locate implements ports.RepoLocator.
func (l *Locator) Locate(cwd string) (*model.RepoInfo, error) {
	panic("unimplemented: snapshot.Locator.Locate")
}

// Branch implements ports.RepoLocator.
func (l *Locator) Branch(repo *model.RepoInfo) (string, model.Tri) {
	return Branch(l.FS, repo)
}

// Branch reads the current branch short name from HEAD ("main"); detached
// HEAD ⇒ ("", TriFalse); unreadable ⇒ ("", TriUnknown).
func Branch(fsys sys.FS, repo *model.RepoInfo) (string, model.Tri) {
	panic("unimplemented: snapshot.Branch")
}

// IndexEntry is one parsed index entry (stage-0 only; others ⇒ fallback).
type IndexEntry struct {
	Path    string
	Mode    uint32
	SHA     [20]byte
	CtimeS  uint32
	CtimeNS uint32
	MtimeS  uint32
	MtimeNS uint32
	Dev     uint32
	Ino     uint32
	UID     uint32
	GID     uint32
	Size    uint32
	Flags   uint16
}

// Index is a parsed index file.
type Index struct {
	Version int
	Entries []IndexEntry
	// MtimeNS of the index file itself (racy-git reference).
	FileMtime time.Time
}

// ParseIndex parses index versions 2, 3 and 4 (v4 path prefix compression).
// Returns model.ErrFallback (wrapped with a reason) for unsupported content.
func ParseIndex(data []byte) (*Index, error) {
	panic("unimplemented: snapshot.ParseIndex")
}

// Filter is the single exempt filter shared by S and C.
type Filter struct {
	patterns []string
}

// CompileExempt compiles project.json "exempt" globs.
func CompileExempt(globs []string) (*Filter, error) {
	panic("unimplemented: snapshot.CompileExempt")
}

// Exempt reports whether a repo-relative path is exempt.
func (f *Filter) Exempt(rel string) bool {
	panic("unimplemented: snapshot.Filter.Exempt")
}

// Options control a snapshot computation.
type Options struct {
	FS    sys.FS
	Clock sys.Clock
	// Deadline bounds the git fallback (zero ⇒ no deadline, runner only).
	Deadline time.Time
	// AllowFallback enables gitfallback.go; false ⇒ ErrFallback is returned.
	AllowFallback bool
	// Git runs git for the fallback (nil ⇒ ExecGit).
	Git GitRunner
}

// TreeC computes C for a commit form (see package doc).
func TreeC(opt Options, repo *model.RepoInfo, form model.CommitForm, f *Filter) (model.TreeID, error) {
	panic("unimplemented: snapshot.TreeC")
}

// TreeS computes S (work tree snapshot of tracked files).
func TreeS(opt Options, repo *model.RepoInfo, f *Filter) (model.TreeID, error) {
	panic("unimplemented: snapshot.TreeS")
}

// NeedsFallback reports whether native computation cannot guarantee git
// equivalence for this repo (attributes, autocrlf, sha256, ...).
func NeedsFallback(fsys sys.FS, repo *model.RepoInfo) (bool, string) {
	panic("unimplemented: snapshot.NeedsFallback")
}
