package psparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// fileWriteMethods are the static System.IO.File methods whose first argument
// is a file the call writes (docs/p2-design.md E1).
var fileWriteMethods = map[string]bool{
	"writealltext": true, "appendalltext": true, "writeallbytes": true,
	"writealllines": true, "appendalllines": true,
	"writealltextasync": true, "appendalltextasync": true, "writeallbytesasync": true,
	"writealllinesasync": true, "appendalllinesasync": true,
}

// fileWriteMethod recognizes a type-literal member word such as
// "[IO.File]::WriteAllText", "[System.IO.File]::AppendAllText" or
// "[void][io.file]::writealltext" and returns the method name as written.
func fileWriteMethod(lit string) (method string, ok bool) {
	i := strings.Index(lit, "::")
	if i < 2 || lit[i-1] != ']' {
		return "", false
	}
	open := strings.LastIndexByte(lit[:i-1], '[')
	if open < 0 {
		return "", false
	}
	typ := strings.ToLower(strings.TrimSpace(lit[open+1 : i-1]))
	typ = strings.TrimPrefix(typ, "system.")
	if typ != "io.file" {
		return "", false
	}
	method = lit[i+2:]
	if !fileWriteMethods[strings.ToLower(method)] {
		return "", false
	}
	return method, true
}

// splitCallArgs splits the inside of a ( ... ) argument list on top-level
// commas, honouring quotes and nested ()[]{} groups.
func splitCallArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '`' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// literalString returns the value of s when it is exactly one quoted string
// literal without expansion: '...' (a doubled quote escapes one) or "..."
// without '$' or a backtick ("" escapes one). Anything else is dynamic.
func literalString(s string) (string, bool) {
	if len(s) < 2 {
		return "", false
	}
	q := s[0]
	if (q != '\'' && q != '"') || s[len(s)-1] != q {
		return "", false
	}
	var sb strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		switch {
		case c == q:
			if i+1 < len(s)-1 && s[i+1] == q {
				sb.WriteByte(q)
				i++
				continue
			}
			return "", false // a quote ends the literal early
		case q == '"' && (c == '$' || c == '`'):
			return "", false
		}
		sb.WriteByte(c)
	}
	return sb.String(), true
}

// staticCalls emits one node per `[IO.File]::WriteAllText('<path>', ...)`
// style call found among the tokens of an expression element (a call after a
// command word is only an argument string and never runs). The node is named
// like the member, e.g. "[io.file]::writealltext", and carries the first
// argument as its write target (Dynamic unless it is a plain quoted
// literal). It returns the index of the last emitted node, or -1.
func (p *parser) staticCalls(c ctx, toks []token, redirs []redir) int {
	idx := -1
	for i := 0; i+1 < len(toks); i++ {
		t, g := toks[i], toks[i+1]
		if t.kind != tkWord || g.kind != tkGroup || g.block {
			continue
		}
		method, ok := fileWriteMethod(t.word.Lit)
		if !ok {
			continue
		}
		head := model.Word{Lit: "[IO.File]::" + method}
		argv := []model.Word{head}
		var writes []model.WriteTarget
		inner := g.word.Lit
		if len(inner) >= 2 && inner[0] == '(' && inner[len(inner)-1] == ')' {
			inner = inner[1 : len(inner)-1]
		}
		for k, a := range splitCallArgs(inner) {
			a = strings.TrimSpace(a)
			if a == "" && k == 0 {
				break
			}
			lit, isLit := literalString(a)
			argv = append(argv, model.Word{Lit: a, Dynamic: !isLit})
			if k == 0 {
				if isLit {
					argv[1].Lit = lit
				}
				writes = psTargets([]model.Word{{Lit: argv[1].Lit, Dynamic: !isLit}}, strings.ToLower(head.Lit))
			}
		}
		n := model.CommandNode{Argv: argv, Name: CanonicalName(head.Lit), Writes: writes,
			Loop: c.loop, Async: c.async, InSubst: c.inSubst}
		p.cl.Nodes = append(p.cl.Nodes, n)
		idx = len(p.cl.Nodes) - 1
	}
	if idx >= 0 {
		applyRedirs(&p.cl.Nodes[idx], redirs)
	}
	return idx
}

// hasStaticCall is a cheap pre-check for staticCalls.
func (p *parser) hasStaticCall(toks []token) bool {
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].kind == tkWord && toks[i+1].kind == tkGroup && !toks[i+1].block &&
			strings.Contains(toks[i].word.Lit, "::") {
			if _, ok := fileWriteMethod(toks[i].word.Lit); ok {
				return true
			}
		}
	}
	return false
}
