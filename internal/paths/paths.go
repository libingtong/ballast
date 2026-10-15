package paths

import (
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// IsCaseInsensitive reports whether path comparison folds case on goos
// (windows and darwin: yes; others: no).
func IsCaseInsensitive(goos string) bool {
	return goos == "windows" || goos == "darwin"
}

// Normalize converts p to the internal form (see package doc). Relative p is
// joined to base (itself normalized). Empty p returns "".
func Normalize(p, base string, ctx model.PathContext) string {
	panic("unimplemented: paths.Normalize")
}

// ToSlashForm converts separators and Windows/UNC/Git-Bash prefixes without
// making the path absolute ("C:\\a\\b" → "c:/a/b", "\\\\srv\\sh\\x" → "//srv/sh/x",
// "/c/Users" → "c:/Users" when goos == "windows").
func ToSlashForm(p, goos string) string {
	panic("unimplemented: paths.ToSlashForm")
}

// ToOS converts an internal-form path back to an OS path for goos.
func ToOS(norm, goos string) string {
	panic("unimplemented: paths.ToOS")
}

// ExpandHome expands a leading "~" or "~/" using home (internal form).
func ExpandHome(p, home string) string {
	panic("unimplemented: paths.ExpandHome")
}

// Equal compares two internal-form paths, folding case when goos is case-insensitive.
func Equal(a, b, goos string) bool {
	panic("unimplemented: paths.Equal")
}

// Within reports whether p equals dir or is inside it (component-wise, case
// folding per goos). Both arguments are internal form.
func Within(p, dir, goos string) bool {
	panic("unimplemented: paths.Within")
}

// Rel returns p relative to root ("a/b.go") when p is within root.
func Rel(root, p, goos string) (string, bool) {
	panic("unimplemented: paths.Rel")
}

// RealPath resolves symlinks/junctions of an internal-form path. For a
// non-existent path it resolves the nearest existing ancestor and re-appends
// the remaining components. Errors other than not-exist are returned.
func RealPath(fsys sys.FS, norm, goos string) (string, error) {
	panic("unimplemented: paths.RealPath")
}

// Candidates implements ports.PathService.Candidates using fsys for EvalSymlinks.
func Candidates(fsys sys.FS, raw string, ctx model.PathContext) []string {
	panic("unimplemented: paths.Candidates")
}

// Service is the ports.PathService implementation.
type Service struct {
	FS sys.FS
}

var _ ports.PathService = (*Service)(nil)

// NewService returns a Service over fsys.
func NewService(fsys sys.FS) *Service { return &Service{FS: fsys} }

// Normalize implements ports.PathService.
func (s *Service) Normalize(p, base string, ctx model.PathContext) string {
	return Normalize(p, base, ctx)
}

// Candidates implements ports.PathService.
func (s *Service) Candidates(raw string, ctx model.PathContext) []string {
	return Candidates(s.FS, raw, ctx)
}

// CompileGlobs implements ports.PathService.
func (s *Service) CompileGlobs(patterns []string, ctx model.PathContext) (ports.GlobMatcher, error) {
	g, err := CompileGlobs(patterns, GlobOptions{Home: ctx.Home, CaseInsensitive: IsCaseInsensitive(ctx.GOOS)})
	if err != nil {
		return nil, err // never return a typed-nil *GlobSet inside the interface
	}
	return g, nil
}
