package bashparse

import (
	"fmt"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// ws builds words from literals; a word containing '$' is dynamic.
func ws(lits ...string) []model.Word {
	out := make([]model.Word, len(lits))
	for i, l := range lits {
		out[i] = model.Word{Lit: l, Dynamic: strings.Contains(l, "$")}
	}
	return out
}

func lits(w []model.Word) []string {
	out := make([]string, len(w))
	for i := range w {
		out[i] = w[i].Lit
	}
	return out
}

// tgt renders targets as "via:path" (or "via:*" when dynamic).
func tgt(ts []model.WriteTarget) []string {
	out := []string{}
	for _, t := range ts {
		p := t.Path
		if t.Dynamic {
			p = "*"
		}
		out = append(out, t.Via+":"+p)
	}
	return out
}

func mustParse(t testing.TB, src string) *model.CommandList {
	t.Helper()
	cl, err := Parse(src, Options{})
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return cl
}

// nodeOf parses src and returns its only node.
func nodeOf(t testing.TB, src string) model.CommandNode {
	t.Helper()
	cl := mustParse(t, src)
	if len(cl.Nodes) != 1 {
		t.Fatalf("Parse(%q): %d nodes, want 1: %+v", src, len(cl.Nodes), cl.Nodes)
	}
	return cl.Nodes[0]
}

func names(cl *model.CommandList) string {
	var s []string
	for _, n := range cl.Nodes {
		s = append(s, fmt.Sprintf("%s/%d", n.Name, n.Depth))
	}
	return strings.Join(s, " ")
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinLits(w []model.Word) string { return strings.Join(lits(w), " ") }

func joinStr(s []string) string { return strings.Join(s, " ") }

// shellFields splits a command line into words with minimal quoting support
// (single and double quotes, no escapes): enough for the table tests, which
// avoid the parser under test.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
