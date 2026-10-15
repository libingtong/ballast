package psparse

import (
	"errors"
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// Limits of the recursive tokenization (doc.go: script blocks are tokenized
// recursively, depth <= 2; deeper content is skipped and marks the list
// unknown).
const (
	maxBlockDepth = 2 // script blocks: ForEach-Object { }, Start-Job { }, & { }
	maxBodyDepth  = 8 // statement bodies: if/foreach/while/try/function blocks
	maxGroupDepth = 4 // ( ) and $( ) groups
)

var errUnbalanced = errors.New("psparse: unbalanced quote, bracket or here-string")

type parser struct {
	src string
	i   int
	cl  *model.CommandList
}

func (p *parser) eof() bool { return p.i >= len(p.src) }

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("psparse: offset %d: %s", p.i, fmt.Sprintf(format, a...))
}

func (p *parser) unknown(reason string) {
	if !p.cl.Unknown {
		p.cl.Unknown = true
		p.cl.UnknownReason = reason
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v' }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// quoteAt reports whether a quote character starts at src[i]: 's' for single
// quotes (also the typographic ones PowerShell accepts), 'd' for double
// quotes, 0 otherwise; sz is the byte length of the quote character.
func (p *parser) quoteAt(i int) (kind byte, sz int) {
	if i >= len(p.src) {
		return 0, 0
	}
	switch p.src[i] {
	case '\'':
		return 's', 1
	case '"':
		return 'd', 1
	case 0xE2:
		if i+2 < len(p.src) && p.src[i+1] == 0x80 {
			switch b := p.src[i+2]; {
			case b >= 0x98 && b <= 0x9B:
				return 's', 3
			case b >= 0x9C && b <= 0x9E:
				return 'd', 3
			}
		}
	}
	return 0, 0
}

// skipInline skips blanks, backtick line continuations and comments, but not
// the newline that ends a statement.
func (p *parser) skipInline() error {
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case isSpace(c):
			p.i++
		case c == '`' && p.i+1 < len(p.src) && (p.src[p.i+1] == '\n' || p.src[p.i+1] == '\r'):
			p.i += 2
			if p.src[p.i-1] == '\r' && p.i < len(p.src) && p.src[p.i] == '\n' {
				p.i++
			}
		case c == '#':
			p.skipLineComment()
		case c == '<' && p.i+1 < len(p.src) && p.src[p.i+1] == '#':
			if err := p.skipBlockComment(); err != nil {
				return err
			}
		default:
			return nil
		}
	}
	return nil
}

// skipSpace is skipInline that also consumes newlines.
func (p *parser) skipSpace() error {
	for {
		if err := p.skipInline(); err != nil {
			return err
		}
		if p.i < len(p.src) && p.src[p.i] == '\n' {
			p.i++
			continue
		}
		return nil
	}
}

func (p *parser) skipLineComment() {
	for p.i < len(p.src) && p.src[p.i] != '\n' {
		p.i++
	}
}

func (p *parser) skipBlockComment() error {
	end := strings.Index(p.src[p.i+2:], "#>")
	if end < 0 {
		return p.errf("unterminated block comment")
	}
	p.i += 2 + end + 2
	return nil
}

// readSQ reads a single-quoted string starting at the opening quote.
func (p *parser) readSQ() (string, error) {
	_, sz := p.quoteAt(p.i)
	p.i += sz
	var sb strings.Builder
	for p.i < len(p.src) {
		if k, n := p.quoteAt(p.i); k == 's' {
			if k2, n2 := p.quoteAt(p.i + n); k2 == 's' {
				sb.WriteByte('\'')
				p.i += n + n2
				continue
			}
			p.i += n
			return sb.String(), nil
		}
		sb.WriteByte(p.src[p.i])
		p.i++
	}
	return "", p.errf("unterminated single-quoted string")
}

type dqMode uint8

const (
	dqQuote dqMode = iota // reads up to the closing double quote
	dqEOF                 // here-string body: reads to the end of src
)

// readDQ reads a double-quoted string (p.i at the opening quote in dqQuote
// mode). It reports whether the text contains expansions; $( ... ) are parsed
// as sub-expressions.
func (p *parser) readDQ(c ctx, mode dqMode) (string, bool, error) {
	if mode == dqQuote {
		_, sz := p.quoteAt(p.i)
		p.i += sz
	}
	var sb strings.Builder
	dyn := false
	for p.i < len(p.src) {
		ch := p.src[p.i]
		if k, n := p.quoteAt(p.i); k == 'd' && mode == dqQuote {
			if k2, n2 := p.quoteAt(p.i + n); k2 == 'd' {
				sb.WriteByte('"')
				p.i += n + n2
				continue
			}
			p.i += n
			return sb.String(), dyn, nil
		}
		switch ch {
		case '`':
			if p.i+1 >= len(p.src) {
				p.i++
				continue
			}
			p.i++
			e := p.src[p.i]
			p.i++
			switch e {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case '0':
				sb.WriteByte(0)
			case 'a':
				sb.WriteByte(7)
			case 'b':
				sb.WriteByte(8)
			case 'f':
				sb.WriteByte(12)
			case 'v':
				sb.WriteByte(11)
			case 'e':
				sb.WriteByte(27)
			case '\n':
			default:
				sb.WriteByte(e)
			}
		case '$':
			var next byte
			if p.i+1 < len(p.src) {
				next = p.src[p.i+1]
			}
			switch {
			case next == '(':
				start := p.i
				p.i++
				if _, err := p.parenGroup(c); err != nil {
					return "", false, err
				}
				sb.WriteString(p.src[start:p.i])
				dyn = true
			case next == '{':
				end := strings.IndexByte(p.src[p.i:], '}')
				if end < 0 {
					return "", false, p.errf("unterminated ${")
				}
				sb.WriteString(p.src[p.i : p.i+end+1])
				p.i += end + 1
				dyn = true
			case isLetter(next) || isDigit(next) || next == '_' || next == '?' || next == '$' || next == '^':
				dyn = true
				sb.WriteByte('$')
				p.i++
			default:
				sb.WriteByte('$')
				p.i++
			}
		default:
			sb.WriteByte(ch)
			p.i++
		}
	}
	if mode == dqEOF {
		return sb.String(), dyn, nil
	}
	return "", false, p.errf("unterminated double-quoted string")
}

// herestring reads @'...'@ / @"..."@ starting at the '@'. ok is false when the
// text at p.i is not a here-string opener.
func (p *parser) herestring(c ctx, parse bool) (lit string, dyn bool, ok bool, err error) {
	if p.i+1 >= len(p.src) || p.src[p.i] != '@' {
		return "", false, false, nil
	}
	q := p.src[p.i+1]
	if q != '\'' && q != '"' {
		return "", false, false, nil
	}
	j := p.i + 2
	for j < len(p.src) && isSpace(p.src[j]) {
		j++
	}
	if j >= len(p.src) || p.src[j] != '\n' {
		return "", false, false, nil
	}
	bodyStart := j + 1
	term := string(q) + "@"
	pos := bodyStart
	for {
		if strings.HasPrefix(p.src[pos:], term) {
			break
		}
		nl := strings.IndexByte(p.src[pos:], '\n')
		if nl < 0 {
			return "", false, true, p.errf("here-string has no terminator")
		}
		pos += nl + 1
	}
	body := p.src[bodyStart:pos]
	body = strings.TrimSuffix(body, "\n")
	body = strings.TrimSuffix(body, "\r")
	p.i = pos + 2
	if q == '\'' || !parse {
		return body, false, true, nil
	}
	sub := &parser{src: body, cl: p.cl}
	lit, dyn, err = sub.readDQ(c, dqEOF)
	return lit, dyn, true, err
}

// skipGroup skips a balanced (...) or {...} group whose opener is at p.i,
// honouring strings, here-strings and comments.
func (p *parser) skipGroup() error {
	var stack []byte
	for p.i < len(p.src) {
		ch := p.src[p.i]
		switch {
		case ch == '`':
			p.i += 2
			continue
		case ch == '@' && p.i+1 < len(p.src) && (p.src[p.i+1] == '\'' || p.src[p.i+1] == '"'):
			if _, _, ok, err := p.herestring(ctx{}, false); err != nil {
				return err
			} else if ok {
				continue
			}
			p.i++
			continue
		case ch == '#' && (p.i == 0 || isSpace(p.src[p.i-1]) || strings.IndexByte("\n({;|&", p.src[p.i-1]) >= 0):
			p.skipLineComment()
			continue
		case ch == '<' && p.i+1 < len(p.src) && p.src[p.i+1] == '#':
			if err := p.skipBlockComment(); err != nil {
				return err
			}
			continue
		}
		if k, _ := p.quoteAt(p.i); k == 's' {
			if _, err := p.readSQ(); err != nil {
				return err
			}
			continue
		} else if k == 'd' {
			if err := p.skipDQ(); err != nil {
				return err
			}
			continue
		}
		switch ch {
		case '(':
			stack = append(stack, ')')
		case '{':
			stack = append(stack, '}')
		case ')', '}':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return p.errf("mismatched %q", ch)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				p.i++
				return nil
			}
		}
		p.i++
	}
	return p.errf("unterminated group")
}

// skipDQ skips a double-quoted string without parsing its sub-expressions.
func (p *parser) skipDQ() error {
	_, sz := p.quoteAt(p.i)
	p.i += sz
	for p.i < len(p.src) {
		ch := p.src[p.i]
		if k, n := p.quoteAt(p.i); k == 'd' {
			if k2, n2 := p.quoteAt(p.i + n); k2 == 'd' {
				p.i += n + n2
				continue
			}
			p.i += n
			return nil
		}
		switch {
		case ch == '`':
			p.i += 2
		case ch == '$' && p.i+1 < len(p.src) && p.src[p.i+1] == '(':
			p.i++
			if err := p.skipGroup(); err != nil {
				return err
			}
		default:
			p.i++
		}
	}
	return p.errf("unterminated double-quoted string")
}

// maxBracketSpan bounds the lookahead for the closing ']' of an index or type
// literal, so a long run of unmatched '[' stays linear.
const maxBracketSpan = 512

// skipBracket skips a [...] run starting at p.i (index or type literal). It
// reports false (leaving p.i unchanged) when no matching ']' exists nearby.
func (p *parser) skipBracket() bool {
	start := p.i
	depth := 0
	for p.i < len(p.src) && p.i-start < maxBracketSpan {
		ch := p.src[p.i]
		if k, _ := p.quoteAt(p.i); k == 's' {
			if _, err := p.readSQ(); err != nil {
				p.i = start
				return false
			}
			continue
		} else if k == 'd' {
			if err := p.skipDQ(); err != nil {
				p.i = start
				return false
			}
			continue
		}
		switch ch {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				p.i++
				return true
			}
		case '\n':
			p.i = start
			return false
		}
		p.i++
	}
	p.i = start
	return false
}
