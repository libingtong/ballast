package psparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

type tokKind uint8

const (
	tkWord  tokKind = iota // bare, quoted or variable word (numbers included)
	tkParam                // -name or -name:value
	tkOp                   // ',', assignment operators, the call operator
	tkGroup                // (...) or {...}; contents were already tokenized
)

type token struct {
	kind   tokKind
	word   model.Word
	raw    string // original text of a parameter token
	quoted bool   // contains a quoted segment
	bare   bool   // plain bare word: no quotes, variables, groups or brackets
	isVar  bool   // starts with '$' or '@'
	block  bool   // tkGroup: script block
	call   bool   // tkOp: the call operator '&'
	param  string // tkParam: lower-cased "-name"
	pval   *model.Word
}

// redir is one redirection of a command.
type redir struct {
	op, fd string
	target model.Word
	merge  bool // 2>&1 style stream merge: no file involved
}

const (
	sInit = iota
	sPending
	sRHS
	sCall
	sDone
)

// elemState tracks how a pipeline element is classified while its tokens are
// read: a command (its node is reserved as soon as the head is known so that
// nested nodes follow it), or an expression.
type elemState struct {
	state   int
	cmdTok  int
	cmdIdx  int
	canon   string
	loop    *model.LoopInfo
	headDot bool
}

func isAssignOp(s string) bool {
	switch s {
	case "=", "+=", "-=", "*=", "/=", "%=", "??=":
		return true
	}
	return false
}

// looksNumeric reports whether a bare word is a number, an integer range such
// as 1..3 or digit-and-operator arithmetic such as 60*5 (an expression, never
// a command name).
func looksNumeric(s string) bool {
	digit := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case isDigit(c):
			digit = true
		case c == '.' || c == '+' || c == '-' || c == '*' || c == '/' || c == '%':
		default:
			return false
		}
	}
	return digit
}

func isCommandLike(t token) bool {
	if t.kind != tkWord || !t.bare || t.word.Lit == "" {
		return false
	}
	s := t.word.Lit
	switch s[0] {
	case '-', '+', '=', '!':
		return false
	}
	return !looksNumeric(s) && !isNumber(s)
}

// element reads one pipeline element (a command or an expression). The
// terminator that ended it is returned and left unconsumed: '\n', ';', ')',
// '}', '|' (pipe), '&' (background), 'c' (&& or ||) or 0 at the end of input.
// head is the index of the element's node, or -1.
func (p *parser) element(c ctx, closer byte) (head int, term byte, ntok int, err error) {
	nodes0 := len(p.cl.Nodes)
	var toks []token
	var redirs []redir
	st := elemState{cmdTok: -1, cmdIdx: -1}

	start := func(i int) {
		st.cmdTok = i
		st.canon = canonicalHead(toks[i].word.Lit)
		st.headDot = toks[i].word.Lit == "."
		st.cmdIdx = len(p.cl.Nodes)
		p.cl.Nodes = append(p.cl.Nodes, model.CommandNode{})
		if st.canon == "invoke-expression" {
			p.unknown("invoke-expression")
		}
	}
	step := func() {
		n := len(toks)
		t := toks[n-1]
		switch st.state {
		case sInit:
			switch {
			case t.kind == tkOp && t.call:
				st.state = sCall
			case isCommandLike(t):
				start(n - 1)
				st.state = sDone
			case t.kind == tkWord && (t.isVar || strings.HasPrefix(t.word.Lit, "[")):
				st.state = sPending
			default:
				st.state = sDone
			}
		case sCall:
			switch {
			case t.kind == tkWord && !t.word.Dynamic && !t.isVar:
				start(n - 1)
			case t.kind == tkGroup && t.block:
			default:
				p.unknown("dynamic invocation")
			}
			st.state = sDone
		case sPending:
			switch {
			case t.kind == tkOp && isAssignOp(t.word.Lit):
				st.state = sRHS
			case t.kind == tkOp && t.word.Lit == ",":
			case t.kind == tkWord && (t.isVar || strings.HasPrefix(t.word.Lit, "[")):
			default:
				st.state = sDone
			}
		case sRHS:
			if isCommandLike(t) {
				start(n - 1)
			}
			st.state = sDone
		}
	}

loop:
	for {
		if err = p.skipInline(); err != nil {
			return -1, 0, 0, err
		}
		if p.eof() {
			break
		}
		ch := p.src[p.i]
		var next byte
		if p.i+1 < len(p.src) {
			next = p.src[p.i+1]
		}
		switch ch {
		case '\n', ';', ')', '}':
			term = ch
			break loop
		case '|':
			term = '|'
			if next == '|' {
				term = 'c'
			}
			break loop
		case '&':
			if next == '&' {
				term = 'c'
				break loop
			}
			if len(toks) == 0 && len(redirs) == 0 {
				p.i++
				toks = append(toks, token{kind: tkOp, call: true, word: model.Word{Lit: "&"}})
				step()
				continue
			}
			term = '&'
			break loop
		case ',':
			p.i++
			toks = append(toks, token{kind: tkOp, word: model.Word{Lit: ","}})
			step()
			if err = p.skipSpace(); err != nil {
				return -1, 0, 0, err
			}
			continue
		}
		if ch == '>' || isRedirFd(p.src, p.i) {
			var r redir
			if r, err = p.redirect(c); err != nil {
				return -1, 0, 0, err
			}
			redirs = append(redirs, r)
			continue
		}
		var t token
		switch ch {
		case '(':
			var raw string
			if raw, err = p.parenGroup(c); err != nil {
				return -1, 0, 0, err
			}
			t = token{kind: tkGroup, word: model.Word{Lit: raw, Dynamic: true}}
		case '{':
			bc := c
			switch st.canon {
			case "foreach-object":
				if st.loop == nil {
					st.loop = p.newLoop(c, model.LoopForEach)
				}
				bc.loop = st.loop
			case "start-job", "start-threadjob":
				bc.async = true
			}
			var raw string
			if raw, err = p.blockGroup(bc, true); err != nil {
				return -1, 0, 0, err
			}
			t = token{kind: tkGroup, block: true, word: model.Word{Lit: raw, Dynamic: true}}
		default:
			if t, err = p.readWord(c); err != nil {
				return -1, 0, 0, err
			}
		}
		toks = append(toks, t)
		step()
		if t.kind == tkOp {
			if err = p.skipSpace(); err != nil {
				return -1, 0, 0, err
			}
		}
	}

	ntok = len(toks) + len(redirs)
	switch {
	case st.cmdIdx >= 0:
		p.finishCommand(c, &st, toks, redirs)
		return st.cmdIdx, term, ntok, nil
	case closer != ')' && !c.noOutput && valueOnly(toks):
		return p.implicitOutput(c, toks, redirs), term, ntok, nil
	case len(p.cl.Nodes) > nodes0:
		// An expression made of groups, e.g. (Get-A) | Get-B: the pipe starts
		// at the last command inside.
		return len(p.cl.Nodes) - 1, term, ntok, nil
	}
	return -1, term, ntok, nil
}

// valueOnly reports whether the tokens are just literal values and variables
// (an expression statement whose value is written to the output stream).
func valueOnly(toks []token) bool {
	words := 0
	for _, t := range toks {
		switch {
		case t.kind == tkWord && (t.quoted || t.isVar || isNumber(t.word.Lit) || looksNumeric(t.word.Lit)):
			words++
		case t.kind == tkOp && t.word.Lit == ",":
		default:
			return false
		}
	}
	return words > 0
}

// implicitOutput emits the Write-Output node PowerShell implies for an
// expression statement such as 'x' | Out-File f.
func (p *parser) implicitOutput(c ctx, toks []token, redirs []redir) int {
	argv := []model.Word{{Lit: "Write-Output"}}
	for _, t := range toks {
		if t.kind == tkWord {
			argv = append(argv, t.word)
		}
	}
	n := model.CommandNode{Argv: argv, Name: "write-output", Loop: c.loop, Async: c.async, InSubst: c.inSubst}
	applyRedirs(&n, redirs)
	p.cl.Nodes = append(p.cl.Nodes, n)
	return len(p.cl.Nodes) - 1
}

// pipeline parses `element | element ...` plus an optional trailing '&'.
func (p *parser) pipeline(c ctx, closer byte) error {
	startNode := len(p.cl.Nodes)
	prev := -1
	for {
		head, term, ntok, err := p.element(c, closer)
		if err != nil {
			return err
		}
		if ntok == 0 && (term == '|' || term == 'c') {
			return p.errf("empty pipeline element")
		}
		if prev >= 0 && head >= 0 {
			p.cl.Pipes = append(p.cl.Pipes, model.PipeEdge{From: prev, To: head})
		}
		switch term {
		case '|':
			p.i++
			prev = head
			if err := p.skipSpace(); err != nil {
				return err
			}
			if p.eof() || strings.IndexByte(")};|", p.src[p.i]) >= 0 {
				return p.errf("empty pipe element")
			}
			continue
		case '&':
			p.i++
			for i := startNode; i < len(p.cl.Nodes); i++ {
				p.cl.Nodes[i].Async = true
			}
		case 'c':
			p.i += 2
			if err := p.skipSpace(); err != nil {
				return err
			}
			if p.eof() || p.src[p.i] == ')' || p.src[p.i] == '}' {
				return p.errf("missing statement after chain operator")
			}
		}
		return nil
	}
}

// isRedirFd reports whether a numbered/starred redirection (2>, *>) starts at i.
func isRedirFd(src string, i int) bool {
	if src[i] == '*' {
		return i+1 < len(src) && src[i+1] == '>'
	}
	j := i
	for j < len(src) && isDigit(src[j]) {
		j++
	}
	return j > i && j < len(src) && src[j] == '>'
}

func (p *parser) redirect(c ctx) (redir, error) {
	var r redir
	if p.src[p.i] == '*' {
		r.fd = "*"
		p.i++
	} else {
		j := p.i
		for j < len(p.src) && isDigit(p.src[j]) {
			j++
		}
		r.fd = p.src[p.i:j]
		p.i = j
	}
	p.i++ // '>'
	r.op = ">"
	if p.i < len(p.src) && p.src[p.i] == '>' {
		r.op = ">>"
		p.i++
	}
	if p.i+1 < len(p.src) && p.src[p.i] == '&' && isDigit(p.src[p.i+1]) {
		j := p.i + 1
		for j < len(p.src) && isDigit(p.src[j]) {
			j++
		}
		r.op = ">&"
		r.merge = true
		r.target = model.Word{Lit: p.src[p.i+1 : j]}
		p.i = j
		return r, nil
	}
	if err := p.skipInline(); err != nil {
		return r, err
	}
	if p.eof() || strings.IndexByte("\n;|)}&>", p.src[p.i]) >= 0 {
		return r, p.errf("redirection without a target")
	}
	t, err := p.readWord(c)
	if err != nil {
		return r, err
	}
	r.target = t.word
	return r, nil
}

// readWord reads one word (bare, quoted, variable or a mix) at p.i.
func (p *parser) readWord(c ctx) (token, error) {
	var sb strings.Builder
	t := token{kind: tkWord, bare: true}
	dyn := false
	startPos := p.i
	first := p.src[p.i]
	firstQuoted := false
	if k, _ := p.quoteAt(p.i); k != 0 {
		firstQuoted = true
	}
loop:
	for p.i < len(p.src) {
		ch := p.src[p.i]
		if k, _ := p.quoteAt(p.i); k != 0 {
			t.quoted, t.bare = true, false
			if k == 's' {
				s, err := p.readSQ()
				if err != nil {
					return t, err
				}
				sb.WriteString(s)
			} else {
				s, d, err := p.readDQ(c, dqQuote)
				if err != nil {
					return t, err
				}
				dyn = dyn || d
				sb.WriteString(s)
			}
			continue
		}
		switch ch {
		case ' ', '\t', '\r', '\n', '\f', '\v', ';', '|', '&', ')', '}', '(', '{', ',', '>':
			break loop
		case '`':
			if p.i+1 >= len(p.src) {
				p.i++
				break loop
			}
			if nx := p.src[p.i+1]; nx == '\n' || nx == '\r' {
				break loop
			}
			sb.WriteByte(p.src[p.i+1])
			p.i += 2
		case '$':
			d, err := p.readDollar(c, &sb)
			if err != nil {
				return t, err
			}
			if d {
				dyn = true
				t.bare = false
			}
		case '@':
			if p.i != startPos {
				sb.WriteByte(ch)
				p.i++
				continue
			}
			lit, d, ok, err := p.herestring(c, true)
			if err != nil {
				return t, err
			}
			if ok {
				t.quoted, t.bare = true, false
				dyn = dyn || d
				sb.WriteString(lit)
				continue
			}
			t.bare = false
			dyn = true
			start := p.i
			switch {
			case p.i+1 < len(p.src) && p.src[p.i+1] == '(':
				p.i++
				if _, err := p.parenGroup(c); err != nil {
					return t, err
				}
				sb.WriteString(p.src[start:p.i])
			case p.i+1 < len(p.src) && p.src[p.i+1] == '{':
				p.i++
				if err := p.skipGroup(); err != nil {
					return t, err
				}
				sb.WriteString(p.src[start:p.i])
			default: // @splat
				sb.WriteByte('@')
				p.i++
			}
		case '[':
			start := p.i
			if p.skipBracket() {
				t.bare = false
				sb.WriteString(p.src[start:p.i])
				continue
			}
			sb.WriteByte(ch)
			p.i++
		case '*', '?':
			dyn = true
			sb.WriteByte(ch)
			p.i++
		default:
			sb.WriteByte(ch)
			p.i++
		}
	}
	if p.i == startPos {
		return t, p.errf("unexpected %q", first)
	}
	lit := sb.String()
	if lit == "?" && !t.quoted {
		dyn = false
	}
	t.word = model.Word{Lit: lit, Dynamic: dyn}
	t.isVar = !firstQuoted && (first == '$' || first == '@')
	if t.isVar || strings.HasPrefix(lit, "[") {
		low := strings.ToLower(lit)
		if strings.Contains(low, "scriptblock]::create") {
			p.unknown("scriptblock create")
		}
	}
	switch {
	case first == '-' && !firstQuoted && len(lit) >= 2 && (isLetter(lit[1]) || lit[1] == '_' || lit[1] == '?'):
		t.kind = tkParam
		t.raw = lit
		name, val, attached := strings.Cut(lit, ":")
		t.param = strings.ToLower(name)
		if attached && val != "" {
			t.pval = &model.Word{Lit: val, Dynamic: dyn}
		}
	case t.bare && isAssignOp(lit):
		t.kind = tkOp
	}
	return t, nil
}

// readDollar consumes a variable or sub-expression at '$', appending its
// source text to sb. It reports whether the text is dynamic (a lone '$' is not).
func (p *parser) readDollar(c ctx, sb *strings.Builder) (bool, error) {
	start := p.i
	var next byte
	if p.i+1 < len(p.src) {
		next = p.src[p.i+1]
	}
	switch {
	case next == '(':
		p.i++
		if _, err := p.parenGroup(c); err != nil {
			return false, err
		}
		sb.WriteString(p.src[start:p.i])
	case next == '{':
		end := strings.IndexByte(p.src[p.i:], '}')
		if end < 0 {
			return false, p.errf("unterminated ${")
		}
		sb.WriteString(p.src[p.i : p.i+end+1])
		p.i += end + 1
	case next == '$' || next == '?' || next == '^':
		sb.WriteString(p.src[p.i : p.i+2])
		p.i += 2
	case isLetter(next) || isDigit(next) || next == '_':
		p.i++
		for p.i < len(p.src) {
			ch := p.src[p.i]
			if isLetter(ch) || isDigit(ch) || ch == '_' {
				p.i++
				continue
			}
			if ch == ':' && p.i+1 < len(p.src) && (isLetter(p.src[p.i+1]) || p.src[p.i+1] == '_') {
				p.i++
				continue
			}
			break
		}
		sb.WriteString(p.src[start:p.i])
	default:
		sb.WriteByte('$')
		p.i++
		return false, nil
	}
	return true, nil
}

func applyRedirs(n *model.CommandNode, redirs []redir) {
	for _, r := range redirs {
		n.Redirects = append(n.Redirects, model.Redirect{Op: r.op, Fd: r.fd, Target: r.target})
		if r.merge || r.op == ">&" {
			continue
		}
		if !r.target.Dynamic {
			switch strings.ToLower(r.target.Lit) {
			case "$null", "/dev/null", "nul", "":
				continue
			}
		}
		if r.target.Dynamic && strings.EqualFold(r.target.Lit, "$null") {
			continue
		}
		n.Writes = append(n.Writes, model.WriteTarget{Path: pathOf(r.target), Dynamic: r.target.Dynamic, Via: "redirect"})
	}
}

// pathOf returns the path of a target word ("" when dynamic).
func pathOf(w model.Word) string {
	if w.Dynamic {
		return ""
	}
	return w.Lit
}
