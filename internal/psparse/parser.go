package psparse

import (
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// ctx is the inherited state of the region being tokenized.
type ctx struct {
	async   bool
	inSubst bool
	loop    *model.LoopInfo
	blocks  int // nesting depth of script blocks ({ } used as values)
	stmts   int // nesting depth of statement bodies (if/foreach/try/... blocks)
	groups  int // nesting depth of ( ) / $( ) groups
	// noOutput suppresses the implicit Write-Output node of expression
	// statements (the operand of return/throw/exit).
	noOutput bool
}

func loopDepth(l *model.LoopInfo) int {
	if l == nil {
		return 0
	}
	return l.Depth
}

func (p *parser) newLoop(c ctx, kind model.LoopKind) *model.LoopInfo {
	return &model.LoopInfo{Kind: kind, N: -1, Depth: loopDepth(c.loop) + 1}
}

// statements parses statements until the closer byte (')' or '}') is found
// at p.i, or until the end of input when closer is 0. The closer is not
// consumed.
func (p *parser) statements(c ctx, closer byte) error {
	for {
		if err := p.skipStatementSeps(); err != nil {
			return err
		}
		if p.eof() {
			if closer != 0 {
				return errUnbalanced
			}
			return nil
		}
		if ch := p.src[p.i]; ch == ')' || ch == '}' {
			if ch == closer {
				return nil
			}
			return p.errf("unexpected %q", ch)
		}
		if err := p.statement(c, closer); err != nil {
			return err
		}
	}
}

func (p *parser) skipStatementSeps() error {
	for {
		if err := p.skipSpace(); err != nil {
			return err
		}
		if p.i < len(p.src) && p.src[p.i] == ';' {
			p.i++
			continue
		}
		return nil
	}
}

func (p *parser) statement(c ctx, closer byte) error {
	handled, err := p.keyword(c, closer)
	if handled || err != nil {
		return err
	}
	return p.pipeline(c, closer)
}

// parenGroup parses a ( ... ) group whose '(' is at p.i and returns its raw
// source. Nodes inside are marked InSubst.
func (p *parser) parenGroup(c ctx) (string, error) {
	start := p.i
	c.inSubst = true
	c.groups++
	if c.groups > maxGroupDepth {
		p.unknown("nesting too deep")
		if err := p.skipGroup(); err != nil {
			return "", err
		}
		return p.src[start:p.i], nil
	}
	p.i++
	if err := p.statements(c, ')'); err != nil {
		return "", err
	}
	p.i++
	return p.src[start:p.i], nil
}

// blockGroup parses a { ... } block whose '{' is at p.i and returns its raw
// source. Script blocks (script=true) nested deeper than maxBlockDepth, and
// statement bodies deeper than maxBodyDepth, are skipped.
func (p *parser) blockGroup(c ctx, script bool) (string, error) {
	start := p.i
	if script {
		c.blocks++
	} else {
		c.stmts++
	}
	if c.blocks > maxBlockDepth || c.stmts > maxBodyDepth {
		p.unknown("block nesting too deep")
		if err := p.skipGroup(); err != nil {
			return "", err
		}
		return p.src[start:p.i], nil
	}
	p.i++
	if err := p.statements(c, '}'); err != nil {
		return "", err
	}
	p.i++
	return p.src[start:p.i], nil
}

// body parses the { } block of a keyword statement.
func (p *parser) body(c ctx) error {
	if err := p.skipSpace(); err != nil {
		return err
	}
	if p.eof() || p.src[p.i] != '{' {
		return p.errf("expected '{'")
	}
	_, err := p.blockGroup(c, false)
	return err
}

// header parses the ( ... ) part of a keyword statement and returns the raw
// text between the parentheses.
func (p *parser) header(c ctx) (string, error) {
	if err := p.skipSpace(); err != nil {
		return "", err
	}
	if p.eof() || p.src[p.i] != '(' {
		return "", p.errf("expected '('")
	}
	p.i++
	start := p.i
	if err := p.statements(c, ')'); err != nil {
		return "", err
	}
	text := p.src[start:p.i]
	p.i++
	return text, nil
}

func isWordCont(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '-' || c == '.' || c == ':' || c == '\\' || c == '/' || c == '$'
}

// keyword handles statement keywords. handled is false when the statement is
// an ordinary pipeline.
func (p *parser) keyword(c ctx, closer byte) (handled bool, err error) {
	j := p.i
	for j < len(p.src) && isLetter(p.src[j]) {
		j++
	}
	if j == p.i || j < len(p.src) && isWordCont(p.src[j]) {
		return false, nil
	}
	word := strings.ToLower(p.src[p.i:j])
	k := j
	for k < len(p.src) && isSpace(p.src[k]) {
		k++
	}
	var next byte
	if k < len(p.src) {
		next = p.src[k]
	}
	switch word {
	case "if", "foreach", "for", "while", "param":
		if next != '(' {
			return false, nil
		}
	case "switch":
		if next != '(' && next != '-' {
			return false, nil
		}
	case "do", "try", "begin", "process", "end", "dynamicparam", "clean":
		if next != '{' {
			return false, nil
		}
	case "trap":
		if next != '{' && next != '[' {
			return false, nil
		}
	case "function", "filter", "workflow":
		if next == 0 || next == '\n' || next == ';' || next == '{' || next == '|' {
			return false, nil
		}
	case "return", "throw", "exit", "break", "continue":
	default:
		return false, nil
	}
	p.i = k
	switch word {
	case "if":
		return true, p.kwIf(c)
	case "foreach":
		return true, p.kwForeach(c)
	case "for":
		return true, p.kwFor(c)
	case "while":
		return true, p.kwWhile(c)
	case "do":
		return true, p.kwDo(c)
	case "try":
		return true, p.kwTry(c)
	case "switch":
		return true, p.kwSwitch(c)
	case "function", "filter", "workflow":
		return true, p.kwFunction(c)
	case "param":
		return true, p.skipGroup()
	case "trap":
		if next == '[' {
			p.skipBracket()
			if err := p.skipSpace(); err != nil {
				return true, err
			}
		}
		return true, p.body(c)
	case "begin", "process", "end", "dynamicparam", "clean":
		return true, p.body(c)
	}
	// return / throw / exit / break / continue: the operand is an ordinary pipeline.
	if next == 0 || next == '\n' || next == ';' || next == '}' || next == ')' || next == '#' {
		return true, nil
	}
	c.noOutput = true
	return true, p.pipeline(c, closer)
}

func (p *parser) kwIf(c ctx) error {
	if _, err := p.header(c); err != nil {
		return err
	}
	if err := p.body(c); err != nil {
		return err
	}
	for {
		save := p.i
		if err := p.skipSpace(); err != nil {
			return err
		}
		switch {
		case p.hasWord("elseif"):
			p.i += len("elseif")
			if _, err := p.header(c); err != nil {
				return err
			}
			if err := p.body(c); err != nil {
				return err
			}
		case p.hasWord("else"):
			p.i += len("else")
			return p.body(c)
		default:
			p.i = save
			return nil
		}
	}
}

// hasWord reports whether the keyword w (case-insensitive) starts at p.i and
// is a complete word.
func (p *parser) hasWord(w string) bool {
	end := p.i + len(w)
	if end > len(p.src) || !strings.EqualFold(p.src[p.i:end], w) {
		return false
	}
	return end == len(p.src) || !isWordCont(p.src[end])
}

func (p *parser) kwForeach(c ctx) error {
	p.i++ // '('
	if err := p.skipSpace(); err != nil {
		return err
	}
	if p.eof() || p.src[p.i] != '$' {
		return p.errf("foreach: expected a loop variable")
	}
	p.i++
	if !p.eof() && p.src[p.i] == '{' {
		end := strings.IndexByte(p.src[p.i:], '}')
		if end < 0 {
			return p.errf("unterminated ${")
		}
		p.i += end + 1
	} else {
		for !p.eof() && (isLetter(p.src[p.i]) || isDigit(p.src[p.i]) || p.src[p.i] == '_' || p.src[p.i] == ':') {
			p.i++
		}
	}
	if err := p.skipSpace(); err != nil {
		return err
	}
	if !p.hasWord("in") {
		return p.errf("foreach: expected 'in'")
	}
	p.i += 2
	start := p.i
	if err := p.statements(c, ')'); err != nil {
		return err
	}
	text := p.src[start:p.i]
	p.i++
	li := p.newLoop(c, model.LoopForEach)
	if n, ok := literalCount(text); ok {
		li.N = n
		li.Bounded = model.TriTrue
	}
	c.loop = li
	return p.body(c)
}

func (p *parser) kwFor(c ctx) error {
	c.loop = p.newLoop(c, model.LoopForC)
	if _, err := p.header(c); err != nil {
		return err
	}
	return p.body(c)
}

func (p *parser) kwWhile(c ctx) error {
	li := p.newLoop(c, model.LoopWhile)
	c.loop = li
	text, err := p.header(c)
	if err != nil {
		return err
	}
	if isInfinite(text, false) {
		li.Bounded = model.TriFalse
	}
	return p.body(c)
}

func (p *parser) kwDo(c ctx) error {
	li := p.newLoop(c, model.LoopWhile)
	c.loop = li
	if err := p.body(c); err != nil {
		return err
	}
	if err := p.skipSpace(); err != nil {
		return err
	}
	until := false
	switch {
	case p.hasWord("while"):
		p.i += len("while")
	case p.hasWord("until"):
		p.i += len("until")
		until = true
		li.Kind = model.LoopUntil
	default:
		return p.errf("do: expected 'while' or 'until'")
	}
	text, err := p.header(c)
	if err != nil {
		return err
	}
	if isInfinite(text, until) {
		li.Bounded = model.TriFalse
	}
	return nil
}

func (p *parser) kwTry(c ctx) error {
	if err := p.body(c); err != nil {
		return err
	}
	for {
		save := p.i
		if err := p.skipSpace(); err != nil {
			return err
		}
		switch {
		case p.hasWord("catch"):
			p.i += len("catch")
			// optional [ExceptionType], [Other] list before the block
			for {
				if err := p.skipSpace(); err != nil {
					return err
				}
				if !p.eof() && p.src[p.i] == '[' && p.skipBracket() {
					if err := p.skipSpace(); err != nil {
						return err
					}
					if !p.eof() && p.src[p.i] == ',' {
						p.i++
					}
					continue
				}
				break
			}
			if err := p.body(c); err != nil {
				return err
			}
		case p.hasWord("finally"):
			p.i += len("finally")
			if err := p.body(c); err != nil {
				return err
			}
		default:
			p.i = save
			return nil
		}
	}
}

func (p *parser) kwSwitch(c ctx) error {
	for {
		if err := p.skipInline(); err != nil {
			return err
		}
		if p.eof() {
			return errUnbalanced
		}
		switch p.src[p.i] {
		case '-':
			t, err := p.readWord(c)
			if err != nil {
				return err
			}
			if strings.EqualFold(t.word.Lit, "-file") {
				if err := p.skipInline(); err != nil {
					return err
				}
				if _, err := p.readWord(c); err != nil {
					return err
				}
			}
			continue
		case '(':
			p.i++
			if err := p.statements(c, ')'); err != nil {
				return err
			}
			p.i++
			return p.body(c)
		case '{':
			return p.body(c)
		}
		return p.errf("switch: unexpected %q", p.src[p.i])
	}
}

func (p *parser) kwFunction(c ctx) error {
	if err := p.skipInline(); err != nil {
		return err
	}
	if _, err := p.readWord(c); err != nil {
		return err
	}
	if err := p.skipSpace(); err != nil {
		return err
	}
	if !p.eof() && p.src[p.i] == '(' {
		if err := p.skipGroup(); err != nil {
			return err
		}
	}
	return p.body(c)
}

// isInfinite reports whether a loop condition text is a constant that never
// ends the loop: while ($true) / while (1), do..until ($false) / until (0).
func isInfinite(text string, until bool) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if until {
		return t == "$false" || t == "0"
	}
	return t == "$true" || t == "1"
}

// literalCount returns the number of items of a foreach source expression
// made only of literals (numbers, plain quoted strings, integer ranges).
func literalCount(text string) (int, bool) {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "@(") && strings.HasSuffix(t, ")") {
		t = strings.TrimSpace(t[2 : len(t)-1])
	}
	if t == "" {
		return 0, false
	}
	if i := strings.Index(t, ".."); i > 0 {
		a, e1 := strconv.Atoi(strings.TrimSpace(t[:i]))
		b, e2 := strconv.Atoi(strings.TrimSpace(t[i+2:]))
		if e1 == nil && e2 == nil {
			d := b - a
			if d < 0 {
				d = -d
			}
			return d + 1, true
		}
		return 0, false
	}
	n := 0
	for _, item := range splitTopLevelCommas(t) {
		item = strings.TrimSpace(item)
		if !isLiteralItem(item) {
			return 0, false
		}
		n++
	}
	return n, n > 0
}

func splitTopLevelCommas(s string) []string {
	var out []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ',':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func isLiteralItem(s string) bool {
	if len(s) >= 2 {
		q := s[0]
		if (q == '\'' || q == '"') && s[len(s)-1] == q {
			inner := s[1 : len(s)-1]
			return !strings.ContainsAny(inner, string(q)) && (q == '\'' || !strings.ContainsAny(inner, "$`"))
		}
	}
	return isNumber(s)
}

// isNumber reports whether s is a PowerShell numeric literal.
func isNumber(s string) bool {
	s = strings.ToLower(s)
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "+")
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "0x") {
		_, err := strconv.ParseUint(strings.TrimRight(s[2:], "lu"), 16, 64)
		return err == nil
	}
	for _, suf := range []string{"kb", "mb", "gb", "tb", "pb", "ul", "ll", "l", "d", "u"} {
		if strings.HasSuffix(s, suf) && len(s) > len(suf) && isDigit(s[len(s)-len(suf)-1]) {
			s = s[:len(s)-len(suf)]
			break
		}
	}
	digits := 0
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && isDigit(s[i]) {
			i++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(s) && s[i] == 'e' {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		e := 0
		for i < len(s) && isDigit(s[i]) {
			i++
			e++
		}
		if e == 0 {
			return false
		}
	}
	return i == len(s)
}
