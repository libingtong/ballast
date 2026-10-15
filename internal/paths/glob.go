package paths

import (
	"errors"

	"github.com/libingtong/ballast/internal/ports"
)

// Glob errors (wrapped with the offending pattern).
var (
	ErrBadPattern       = errors.New("paths: invalid glob pattern")
	ErrOnlyNegation     = errors.New("paths: glob list has only negated patterns")
	ErrEmptyPatternList = errors.New("paths: empty glob list")
)

// GlobOptions parameterize compilation.
type GlobOptions struct {
	Home            string // internal-form home for "~/" patterns ("" ⇒ "~/" patterns never match)
	CaseInsensitive bool
}

// GlobSet is a compiled glob list (implements ports.GlobMatcher).
type GlobSet struct {
	patterns []string
	positive []string // expanded (and lower-cased when CaseInsensitive)
	negative []string
	opt      GlobOptions
}

var _ ports.GlobMatcher = (*GlobSet)(nil)

// ValidatePattern checks one pattern (doublestar syntax, optional leading "!").
func ValidatePattern(p string) error {
	panic("unimplemented: paths.ValidatePattern")
}

// CompileGlobs compiles a pattern list. See package doc for semantics.
func CompileGlobs(patterns []string, opt GlobOptions) (*GlobSet, error) {
	panic("unimplemented: paths.CompileGlobs")
}

// Match reports whether some candidate matches a positive pattern and no
// candidate matches a negative pattern.
func (g *GlobSet) Match(candidates ...string) bool {
	panic("unimplemented: paths.GlobSet.Match")
}

// Patterns returns the source patterns.
func (g *GlobSet) Patterns() []string { return g.patterns }
