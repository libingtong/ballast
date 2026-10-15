package bashparse

import (
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"

	"github.com/libingtong/ballast/internal/model"
)

// word converts a syntax word into a model.Word. Words made only of literals
// and quoted literals are static; anything with an expansion, an unquoted
// glob or an unquoted brace expansion is Dynamic and Lit holds a best-effort
// rendering (literal parts unquoted, expansions as their source text).
func (w *walker) word(wd *syntax.Word) model.Word {
	if wd == nil {
		return model.Word{}
	}
	if len(wd.Parts) == 1 {
		switch x := wd.Parts[0].(type) {
		case *syntax.Lit:
			if !strings.ContainsAny(x.Value, `\*?[{`) {
				return model.Word{Lit: x.Value}
			}
		case *syntax.SglQuoted:
			if !x.Dollar {
				return model.Word{Lit: x.Value}
			}
		}
	}
	var sb strings.Builder
	dyn := false
	for _, p := range wd.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			s, d := unquotedLit(x.Value)
			sb.WriteString(s)
			if d {
				dyn = true
			}
		case *syntax.SglQuoted:
			if x.Dollar {
				sb.WriteString(ansiC(x.Value))
			} else {
				sb.WriteString(x.Value)
			}
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				if l, ok := q.(*syntax.Lit); ok {
					sb.WriteString(dblUnescape(l.Value))
					continue
				}
				dyn = true
				sb.WriteString(w.srcText(q))
			}
		default:
			dyn = true
			sb.WriteString(w.srcText(p))
		}
	}
	return model.Word{Lit: sb.String(), Dynamic: dyn}
}

// srcText returns the source text of n, or "" when its offsets are unusable.
func (w *walker) srcText(n syntax.Node) string {
	s, e := int(n.Pos().Offset()), int(n.End().Offset())
	if s < 0 || e > len(w.src) || s >= e {
		return ""
	}
	return w.src[s:e]
}

// unquotedLit removes backslash escapes from an unquoted literal and reports
// whether it contains an unescaped glob or brace-expansion construct. It makes
// a single linear pass: '{' pairs are matched with a stack that records, per
// open brace, whether a top-level ',' or ".." has been seen.
func unquotedLit(v string) (string, bool) {
	var sb strings.Builder
	sb.Grow(len(v))
	dyn := false
	lastClose := strings.LastIndexByte(v, ']')
	var braces []bool
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\\':
			if i+1 < len(v) {
				i++
				if v[i] != '\n' {
					sb.WriteByte(v[i])
				}
				continue
			}
			sb.WriteByte(c)
		case '*', '?':
			dyn = true
			sb.WriteByte(c)
		case '[':
			if i < lastClose {
				dyn = true
			}
			sb.WriteByte(c)
		case '{':
			braces = append(braces, false)
			sb.WriteByte(c)
		case '}':
			if n := len(braces); n > 0 {
				if braces[n-1] {
					dyn = true
				}
				braces = braces[:n-1]
			}
			sb.WriteByte(c)
		case ',':
			if n := len(braces); n > 0 {
				braces[n-1] = true
			}
			sb.WriteByte(c)
		case '.':
			if n := len(braces); n > 0 && i+1 < len(v) && v[i+1] == '.' {
				braces[n-1] = true
			}
			sb.WriteByte(c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String(), dyn
}

// dblUnescape applies double-quote backslash rules: only \$ \` \" \\ and
// backslash-newline are escapes.
func dblUnescape(v string) string {
	if strings.IndexByte(v, '\\') < 0 {
		return v
	}
	var sb strings.Builder
	sb.Grow(len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\\' && i+1 < len(v) {
			switch v[i+1] {
			case '$', '`', '"', '\\':
				i++
				sb.WriteByte(v[i])
				continue
			case '\n':
				i++
				continue
			}
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

// ansiC decodes the body of a $'...' string.
func ansiC(v string) string {
	if strings.IndexByte(v, '\\') < 0 {
		return v
	}
	var sb strings.Builder
	sb.Grow(len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' || i+1 >= len(v) {
			sb.WriteByte(c)
			continue
		}
		i++
		switch e := v[i]; e {
		case 'a':
			sb.WriteByte(7)
		case 'b':
			sb.WriteByte(8)
		case 'e', 'E':
			sb.WriteByte(27)
		case 'f':
			sb.WriteByte(12)
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'v':
			sb.WriteByte(11)
		case '\\', '\'', '"', '?':
			sb.WriteByte(e)
		case 'x':
			n, used := hexRun(v[i+1:], 2)
			if used == 0 {
				sb.WriteString(`\x`)
				continue
			}
			sb.WriteByte(byte(n))
			i += used
		case 'u', 'U':
			max := 4
			if e == 'U' {
				max = 8
			}
			n, used := hexRun(v[i+1:], max)
			if used == 0 {
				sb.WriteByte('\\')
				sb.WriteByte(e)
				continue
			}
			var buf [utf8.UTFMax]byte
			l := utf8.EncodeRune(buf[:], rune(n))
			sb.Write(buf[:l])
			i += used
		case 'c':
			if i+1 < len(v) {
				i++
				sb.WriteByte(v[i] & 0x1f)
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			n := int(e - '0')
			for k := 0; k < 2 && i+1 < len(v) && v[i+1] >= '0' && v[i+1] <= '7'; k++ {
				i++
				n = n*8 + int(v[i]-'0')
			}
			sb.WriteByte(byte(n))
		default:
			sb.WriteByte('\\')
			sb.WriteByte(e)
		}
	}
	return sb.String()
}

func hexRun(s string, max int) (n uint32, used int) {
	for used < len(s) && used < max {
		c := s[used]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return n, used
		}
		n = n<<4 | uint32(d)
		used++
	}
	return n, used
}

// baseName returns the part of s after the last '/'.
func baseName(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}
