package dsl

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// nameGlobs is a compiled name-glob list (dsl-semantics §5.1): doublestar
// dialect, case-sensitive on every GOOS, list-level "!" negation. The list
// matches iff some name matches ≥1 positive pattern and no name matches a
// negative one.
type nameGlobs struct {
	pos, neg []string
}

func newNameGlobs(list []string) *nameGlobs {
	if len(list) == 0 {
		return nil
	}
	g := &nameGlobs{}
	for _, p := range list {
		if strings.HasPrefix(p, "!") {
			g.neg = append(g.neg, p[1:])
		} else {
			g.pos = append(g.pos, p)
		}
	}
	return g
}

func globMatch(pattern, name string) bool {
	ok, err := doublestar.Match(pattern, name)
	return err == nil && ok
}

// match reports whether the list matches the candidate names. Empty names
// are ignored.
func (g *nameGlobs) match(names ...string) bool {
	if g == nil {
		return false
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		for _, p := range g.neg {
			if globMatch(p, n) {
				return false
			}
		}
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		for _, p := range g.pos {
			if globMatch(p, n) {
				return true
			}
		}
	}
	return false
}

// checkGlobList validates a glob list (name or path dialect): every pattern
// must be a valid doublestar pattern and the list must have at least one
// positive pattern. It returns the diagnostic code and message of the first
// problem, or "" when the list is fine.
func checkGlobList(list []string) (code, msg string) {
	positives := 0
	for _, p := range list {
		body := strings.TrimPrefix(p, "!")
		if body == "" || !doublestar.ValidatePattern(body) {
			return CodeBadGlob, "invalid glob pattern " + quote(p)
		}
		if !strings.HasPrefix(p, "!") {
			positives++
		}
	}
	if len(list) > 0 && positives == 0 {
		return CodeGlobOnlyNegation, "glob list has only negated patterns and can never match; add a positive pattern"
	}
	return "", ""
}

func quote(s string) string { return "\"" + s + "\"" }
