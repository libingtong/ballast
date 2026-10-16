package sandbox

import (
	"path"
	"strings"
)

// pattern is a parsed RepoMandatoryDeny / GitDirMandatoryDeny entry.
type pattern struct {
	elems   []patElem
	subtree bool // trailing "/": the entry and everything below it
}

type patElem struct {
	lit  string
	kind int // 0 literal, 1 "*" (exactly one element), 2 "**" (one or more)
}

const (
	elemLit = iota
	elemStar
	elemDStar
)

func parsePattern(s string) pattern {
	p := pattern{subtree: strings.HasSuffix(s, "/")}
	s = strings.Trim(s, "/")
	if s == "" {
		return p
	}
	for _, e := range strings.Split(s, "/") {
		switch e {
		case "*":
			p.elems = append(p.elems, patElem{kind: elemStar})
		case "**":
			p.elems = append(p.elems, patElem{kind: elemDStar})
		default:
			p.elems = append(p.elems, patElem{lit: e})
		}
	}
	return p
}

func parsePatterns(ps []string) []pattern {
	out := make([]pattern, 0, len(ps))
	for _, s := range ps {
		if q := parsePattern(s); len(q.elems) > 0 {
			out = append(out, q)
		}
	}
	return out
}

// matchAnchored matches p.elems[pi:] against rel[ri:], starting at ri. A
// subtree pattern accepts any remainder once its elements are consumed; a
// plain pattern must consume rel completely.
func (p pattern) matchAnchored(pi, ri int, rel []string) bool {
	if pi == len(p.elems) {
		return p.subtree || ri == len(rel)
	}
	if ri >= len(rel) {
		return false
	}
	e := p.elems[pi]
	switch e.kind {
	case elemLit:
		return rel[ri] == e.lit && p.matchAnchored(pi+1, ri+1, rel)
	case elemStar:
		return p.matchAnchored(pi+1, ri+1, rel)
	default: // "**": one or more elements
		for k := ri + 1; k <= len(rel); k++ {
			if p.matchAnchored(pi+1, k, rel) {
				return true
			}
		}
		return false
	}
}

// match reports whether rel matches p. isRepo patterns are unanchored at the
// start (any number of leading elements), git dir patterns are anchored.
func (p pattern) match(isRepo bool, rel []string) bool {
	if len(p.elems) == 0 {
		return false
	}
	if !isRepo {
		return p.matchAnchored(0, 0, rel)
	}
	for s := 0; s < len(rel); s++ {
		if p.matchAnchored(0, s, rel) {
			return true
		}
	}
	return false
}

// container reports whether p is an exact (non-subtree) pattern that is the
// element-wise prefix of another pattern in all: ".git" is the container of
// ".git/hooks/" and ".git/config". Such an entry is protected as an entry
// (it cannot be replaced, renamed or removed) but its remaining contents stay
// writable.
func (p pattern) container(all []pattern) bool {
	if p.subtree {
		return false
	}
	for _, q := range all {
		if len(q.elems) <= len(p.elems) {
			continue
		}
		ok := true
		for i, e := range p.elems {
			if q.elems[i] != e {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func splitRel(rel string) ([]string, bool) {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") {
		return nil, false
	}
	if path.Clean(rel) != rel {
		return nil, false
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, false
	}
	return strings.Split(rel, "/"), true
}

// matchInfo is MatchMandatory with detail: subtree is true when at least one
// matching pattern is a subtree pattern (a scan need not descend), container
// when the match is a container entry (see pattern.container).
func matchInfo(all []pattern, isRepo bool, rel string) (matched, subtree, container bool) {
	elems, ok := splitRel(rel)
	if !ok {
		return false, false, false
	}
	for _, p := range all {
		if !p.match(isRepo, elems) {
			continue
		}
		matched = true
		if p.subtree {
			subtree = true
		} else if p.container(all) {
			container = true
		}
	}
	return matched, subtree, container
}

// MatchMandatory reports whether rel (slash-separated, relative to root,
// Clean, no leading "/") matches one of patterns. isRepo selects the
// RepoMandatoryDeny semantics (each pattern also matches under any number
// of leading path elements); otherwise GitDirMandatoryDeny semantics
// (anchored at root, "*" / "**" as documented there). A subtree pattern
// "x/" matches "x" and every path below it. Pure; table-tested
// (testdata/mandatory.json).
func MatchMandatory(patterns []string, isRepo bool, rel string) bool {
	m, _, _ := matchInfo(parsePatterns(patterns), isRepo, rel)
	return m
}
