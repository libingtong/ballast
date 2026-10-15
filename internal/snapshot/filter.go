package snapshot

import (
	"fmt"
	"strings"
)

const (
	pkAll     = iota // matches every path ("**" or the empty/"." pathspec)
	pkGeneral        // wildmatch
	pkDirStar        // "<lit>/**": every path below the literal directory
	pkSuffix         // "**/*<lit>" (any depth) or "*<lit>" (root only in glob mode)
)

// pathPattern is one pathspec element with git's matching rules: a literal
// comparison (exact name or leading directory), then wildmatch for patterns
// that contain wildcard characters. glob selects ':(glob)' semantics ('*'
// does not cross '/'); plain pathspecs let '*' match '/'.
type pathPattern struct {
	pat      string
	glob     bool
	wild     bool // contains one of * ? [ \
	kind     int
	lit      string
	anyDepth bool // pkSuffix: written as "**/*<lit>" (or plain mode)
}

func hasGlobSpecial(s string) bool {
	return strings.ContainsAny(s, `*?[\`)
}

func newPathPattern(pat string, glob bool) pathPattern {
	p := pathPattern{pat: pat, glob: glob, wild: hasGlobSpecial(pat), kind: pkGeneral}
	switch {
	case pat == "":
		p.kind = pkAll
	case pat == "**":
		p.kind = pkAll
	case strings.HasSuffix(pat, "/**") && !hasGlobSpecial(pat[:len(pat)-2]):
		p.kind, p.lit = pkDirStar, pat[:len(pat)-2]
	default:
		rest, deep := strings.CutPrefix(pat, "**/")
		// Without ':(glob)' the "**/" prefix is no special form (it needs
		// a slash in the name), so only "*<lit>" has a fast path there.
		if l, ok := strings.CutPrefix(rest, "*"); ok && l != "" && !hasGlobSpecial(l) && !strings.Contains(l, "/") && (glob || !deep) {
			p.kind, p.lit, p.anyDepth = pkSuffix, l, deep || !glob
		}
	}
	return p
}

// match reports whether name (repo-relative, '/'-separated) is selected.
func (p *pathPattern) match(name string) bool {
	if p.kind == pkAll || p.pat == "" {
		return true
	}
	// Literal rule: exact name, or a leading directory of name.
	if len(name) >= len(p.pat) && name[:len(p.pat)] == p.pat &&
		(len(name) == len(p.pat) || p.pat[len(p.pat)-1] == '/' || name[len(p.pat)] == '/') {
		return true
	}
	if !p.wild {
		return false
	}
	switch p.kind {
	case pkDirStar:
		return strings.HasPrefix(name, p.lit)
	case pkSuffix:
		return strings.HasSuffix(name, p.lit) && (p.anyDepth || !strings.Contains(name, "/"))
	}
	// git matches the literal head of the pattern separately and runs
	// wildmatch on the rest, so "x**" and "x**/z" see "**" at the start of
	// their pattern.
	n := strings.IndexAny(p.pat, `*?[\\`)
	if n > 0 {
		if len(name) < n || name[:n] != p.pat[:n] {
			return false
		}
		return wildmatch(p.pat[n:], name[n:], p.glob)
	}
	return wildmatch(p.pat, name, p.glob)
}

// normalizeSpec cleans a pathspec the way git's prefix handling does:
// "." and empty components vanish, ".." pops one component, a trailing
// slash is preserved. An error means the spec leaves the repository.
func normalizeSpec(s string) (string, error) {
	parts := strings.Split(s, "/")
	out := make([]string, 0, len(parts))
	for _, c := range parts {
		switch c {
		case "", ".":
		case "..":
			if len(out) == 0 {
				return "", fmt.Errorf("pathspec %q is outside the repository", s)
			}
			out = out[:len(out)-1]
		default:
			out = append(out, c)
		}
	}
	r := strings.Join(out, "/")
	if r != "" && (strings.HasSuffix(s, "/") || strings.HasSuffix(s, "/.") || strings.HasSuffix(s, "/..")) {
		r += "/"
	}
	return r, nil
}

// Filter is the single exempt filter shared by S and C.
type Filter struct {
	patterns []string
	pats     []pathPattern
}

// CompileExempt compiles project.json "exempt" globs. Each glob has the
// meaning of the git pathspec ':(glob)<glob>' (the fallback passes exactly
// that to `git rm --cached`), evaluated by a port of git's wildmatch, so the
// native and the fallback result cannot drift.
func CompileExempt(globs []string) (*Filter, error) {
	f := &Filter{}
	for _, g := range globs {
		if g == "" {
			return nil, fmt.Errorf("snapshot: empty exempt pattern")
		}
		if strings.ContainsRune(g, 0) {
			return nil, fmt.Errorf("snapshot: exempt pattern %q contains NUL", g)
		}
		if strings.HasPrefix(g, "/") {
			return nil, fmt.Errorf("snapshot: exempt pattern %q must be repo-relative", g)
		}
		n, err := normalizeSpec(g)
		if err != nil {
			return nil, fmt.Errorf("snapshot: exempt pattern: %w", err)
		}
		f.patterns = append(f.patterns, g)
		f.pats = append(f.pats, newPathPattern(n, true))
	}
	return f, nil
}

// Exempt reports whether a repo-relative path is exempt.
func (f *Filter) Exempt(rel string) bool {
	if f == nil {
		return false
	}
	for i := range f.pats {
		if f.pats[i].match(rel) {
			return true
		}
	}
	return false
}

// empty reports whether the filter removes nothing.
func (f *Filter) empty() bool { return f == nil || len(f.pats) == 0 }

// removeExempt drops exempt entries in place and returns the shortened slice.
func (f *Filter) removeExempt(ents []IndexEntry) []IndexEntry {
	if f.empty() {
		return ents
	}
	out := ents[:0]
	for i := range ents {
		if f.Exempt(ents[i].Path) {
			continue
		}
		out = append(out, ents[i])
	}
	return out
}

// pathspecArgs are the ':(glob)'-prefixed pathspecs for the git fallback.
func (f *Filter) pathspecArgs() []string {
	out := make([]string, len(f.patterns))
	for i, g := range f.patterns {
		out[i] = ":(glob)" + g
	}
	return out
}
