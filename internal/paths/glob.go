package paths

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
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
	body := strings.TrimPrefix(p, "!")
	if body == "" {
		return fmt.Errorf("%w: %q is empty", ErrBadPattern, p)
	}
	if strings.HasPrefix(body, "!") {
		return fmt.Errorf("%w: %q: double negation", ErrBadPattern, p)
	}
	if !doublestar.ValidatePattern(body) {
		return fmt.Errorf("%w: %q", ErrBadPattern, p)
	}
	return nil
}

// CompileGlobs compiles a pattern list. See package doc for semantics.
func CompileGlobs(patterns []string, opt GlobOptions) (*GlobSet, error) {
	if len(patterns) == 0 {
		return nil, ErrEmptyPatternList
	}
	g := &GlobSet{patterns: append([]string(nil), patterns...), opt: opt}
	hasPositive := false
	for _, p := range patterns {
		if err := ValidatePattern(p); err != nil {
			return nil, err
		}
		neg := strings.HasPrefix(p, "!")
		if !neg {
			hasPositive = true
		}
		body := strings.TrimPrefix(p, "!")
		compiled, ok := g.expand(body)
		if !ok {
			continue // "~/" pattern without a home: never matches
		}
		if !doublestar.ValidatePattern(compiled) {
			return nil, fmt.Errorf("%w: %q", ErrBadPattern, p)
		}
		if neg {
			g.negative = append(g.negative, compiled)
		} else {
			g.positive = append(g.positive, compiled)
		}
	}
	if !hasPositive {
		return nil, fmt.Errorf("%w: %q", ErrOnlyNegation, patterns)
	}
	return g, nil
}

// expand turns one validated pattern body into the final doublestar pattern:
// "~" expansion, "./" anchoring, basename patterns ("*.md" → "**/*.md") and
// case folding.
func (g *GlobSet) expand(body string) (string, bool) {
	anchored := false
	switch {
	case body == "~" || strings.HasPrefix(body, "~/"):
		if g.opt.Home == "" {
			return "", false
		}
		body = escapeGlob(strings.TrimSuffix(g.opt.Home, "/")) + body[1:]
		anchored = true
	case strings.HasPrefix(body, "./"):
		body = strings.TrimLeft(body[2:], "/")
		anchored = true
		if body == "" {
			return "", false
		}
	}
	if !anchored && !strings.Contains(body, "/") {
		body = "**/" + body
	}
	if g.opt.CaseInsensitive {
		body = strings.ToLower(body)
	}
	return body, true
}

// escapeGlob escapes doublestar metacharacters in a literal path.
func escapeGlob(s string) string {
	if !strings.ContainsAny(s, `\*?[]{}`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '*', '?', '[', ']', '{', '}':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Match reports whether some candidate matches a positive pattern and no
// candidate matches a negative pattern.
func (g *GlobSet) Match(candidates ...string) bool {
	if g == nil || len(candidates) == 0 {
		return false
	}
	matched := false
	for _, c := range candidates {
		if g.opt.CaseInsensitive {
			c = strings.ToLower(c)
		}
		for _, p := range g.negative {
			if doublestar.MatchUnvalidated(p, c) {
				return false
			}
		}
		if !matched {
			for _, p := range g.positive {
				if doublestar.MatchUnvalidated(p, c) {
					matched = true
					break
				}
			}
		}
	}
	return matched
}

// Patterns returns the source patterns.
func (g *GlobSet) Patterns() []string { return g.patterns }
