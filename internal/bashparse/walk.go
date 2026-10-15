package bashparse

import (
	"strconv"

	"mvdan.cc/sh/v3/syntax"

	"github.com/libingtong/ballast/internal/model"
)

// builder accumulates the CommandList across `sh -c` recursion levels.
type builder struct {
	cl    *model.CommandList
	max   int
	words []model.Word // arena for node argv slices
	chunk int          // size of the next arena chunk
}

// argv returns a zeroed slice of n words carved out of a shared arena (few
// allocations instead of one per command).
func (b *builder) argv(n int) []model.Word {
	if n > 64 {
		return make([]model.Word, n)
	}
	if len(b.words) < n {
		if b.chunk < 16 {
			b.chunk = 16
		} else if b.chunk < 512 {
			b.chunk *= 2
		}
		if b.chunk < n {
			b.chunk = n
		}
		b.words = make([]model.Word, b.chunk)
	}
	s := b.words[:n:n]
	b.words = b.words[n:]
	return s
}

func (b *builder) unknown(reason string) {
	if !b.cl.Unknown {
		b.cl.Unknown = true
		b.cl.UnknownReason = reason
	}
}

// walker walks one parsed source text (src is needed for the best-effort
// rendering of dynamic words).
type walker struct {
	b   *builder
	src string
}

// ctx is the inherited state of the subtree being walked.
type ctx struct {
	async   bool
	inSubst bool
	loop    *model.LoopInfo
	depth   int
	// redirs are the redirections of enclosing compound commands; they apply
	// to every command inside (but not to command substitutions).
	redirs []redirInfo
	// timeWrap marks that the statement is the operand of the `time` keyword.
	timeWrap bool
}

// redirInfo is a converted redirection with the file targets it implies.
type redirInfo struct {
	r      model.Redirect
	writes []model.WriteTarget
	reads  []model.WriteTarget
}

// span is the first and last node index a statement's own command chain
// produced (-1 when it produced none); used for pipe edges.
type span struct{ first, last int }

var noSpan = span{-1, -1}

func (w *walker) stmts(list []*syntax.Stmt, c ctx) span {
	sp := noSpan
	for _, s := range list {
		x := w.stmt(s, c)
		if x.first < 0 {
			continue
		}
		if sp.first < 0 {
			sp.first = x.first
		}
		sp.last = x.last
	}
	return sp
}

func (w *walker) stmt(s *syntax.Stmt, c ctx) span {
	if s == nil {
		return noSpan
	}
	tw := c.timeWrap
	c.timeWrap = false
	if s.Background || s.Coprocess {
		c.async = true
	}
	switch x := s.Cmd.(type) {
	case nil:
		w.walkRedirWords(s.Redirs, c)
		return noSpan
	case *syntax.CallExpr:
		idx := w.call(s, x, c, tw)
		return span{idx, idx}
	}
	// Compound commands: their redirections apply to every command inside.
	if len(s.Redirs) > 0 {
		c.redirs = append(w.convertRedirs(s.Redirs), c.redirs...)
	}
	start := len(w.b.cl.Nodes)
	sp := noSpan
	switch x := s.Cmd.(type) {
	case *syntax.BinaryCmd:
		l := w.stmt(x.X, c)
		r := w.stmt(x.Y, c)
		if (x.Op == syntax.Pipe || x.Op == syntax.PipeAll) && l.last >= 0 && r.first >= 0 {
			w.b.cl.Pipes = append(w.b.cl.Pipes, model.PipeEdge{From: l.last, To: r.first})
		}
		sp = span{l.first, r.last}
		if sp.first < 0 {
			sp.first = r.first
		}
		if sp.last < 0 {
			sp.last = l.last
		}
	case *syntax.Block:
		sp = w.stmts(x.Stmts, c)
	case *syntax.Subshell:
		sp = w.stmts(x.Stmts, c)
	case *syntax.IfClause:
		w.ifClause(x, c)
	case *syntax.WhileClause:
		lc := c
		lc.loop = whileLoop(x, c.loop)
		w.stmts(x.Cond, lc)
		w.stmts(x.Do, lc)
	case *syntax.ForClause:
		w.forClause(x, c)
	case *syntax.CaseClause:
		w.walkNode(x.Word, c)
		for _, it := range x.Items {
			for _, p := range it.Patterns {
				w.walkNode(p, c)
			}
			w.stmts(it.Stmts, c)
		}
	case *syntax.FuncDecl:
		w.stmt(x.Body, c)
	case *syntax.TimeClause:
		if x.Stmt != nil {
			if _, ok := x.Stmt.Cmd.(*syntax.CallExpr); ok {
				c.timeWrap = true
			}
			w.stmt(x.Stmt, c)
		}
	case *syntax.CoprocClause:
		c.async = true
		w.stmt(x.Stmt, c)
	default:
		w.walkNode(s.Cmd, c)
	}
	w.walkRedirWords(s.Redirs, c)
	if sp.first < 0 {
		if n := len(w.b.cl.Nodes); n > start {
			sp = span{start, n - 1}
		}
	}
	return sp
}

func (w *walker) ifClause(x *syntax.IfClause, c ctx) {
	w.stmts(x.Cond, c)
	w.stmts(x.Then, c)
	if x.Else != nil {
		w.ifClause(x.Else, c)
	}
}

func loopDepth(l *model.LoopInfo) int {
	if l == nil {
		return 0
	}
	return l.Depth
}

// whileLoop builds the LoopInfo of a while/until clause: `while true`,
// `while :` and `until false` are unbounded, everything else is unknown.
func whileLoop(x *syntax.WhileClause, outer *model.LoopInfo) *model.LoopInfo {
	li := &model.LoopInfo{Kind: model.LoopWhile, N: -1, Depth: loopDepth(outer) + 1}
	want := "true"
	if x.Until {
		li.Kind = model.LoopUntil
		want = "false"
	}
	if len(x.Cond) == 1 {
		s := x.Cond[0]
		if ce, ok := s.Cmd.(*syntax.CallExpr); ok && len(s.Redirs) == 0 && !s.Negated &&
			len(ce.Assigns) == 0 && len(ce.Args) == 1 {
			if lit := ce.Args[0].Lit(); len(ce.Args[0].Parts) == 1 && (lit == want || !x.Until && lit == ":") {
				li.Bounded = model.TriFalse
			}
		}
	}
	return li
}

func (w *walker) forClause(x *syntax.ForClause, c ctx) {
	li := &model.LoopInfo{Kind: model.LoopForList, N: -1, Depth: loopDepth(c.loop) + 1}
	lc := c
	lc.loop = li
	switch lp := x.Loop.(type) {
	case *syntax.WordIter:
		for _, it := range lp.Items {
			w.walkNode(it, c) // evaluated once, outside the loop
		}
		if !x.Select && lp.InPos.IsValid() {
			if n := w.itemsCount(lp.Items); n >= 0 {
				li.N = n
				li.Bounded = model.TriTrue
			}
		}
	case *syntax.CStyleLoop:
		li.Kind = model.LoopForC
		w.walkNode(lp, lc)
	}
	w.stmts(x.Do, lc)
}

// itemsCount returns the number of items a `for x in ...` list expands to
// when every item is literal (brace expansions with literal operands
// included), else -1.
func (w *walker) itemsCount(items []*syntax.Word) int {
	var total int64
	for _, it := range items {
		n := w.itemCount(it)
		if n < 0 {
			return -1
		}
		total += n
		if total > maxItems {
			total = maxItems
		}
	}
	return int(total)
}

const maxItems = 1 << 30

func (w *walker) itemCount(wd *syntax.Word) int64 {
	if mw := w.word(wd); !mw.Dynamic {
		return 1
	}
	cp := &syntax.Word{Parts: append([]syntax.WordPart(nil), wd.Parts...)}
	if !syntax.SplitBraces(cp) {
		return -1
	}
	return braceWordCount(cp)
}

func braceWordCount(wd *syntax.Word) int64 {
	total := int64(1)
	for _, p := range wd.Parts {
		switch x := p.(type) {
		case *syntax.BraceExp:
			n := braceExpCount(x)
			if n < 0 {
				return -1
			}
			total *= n
			if total > maxItems {
				total = maxItems
			}
		case *syntax.Lit:
			if _, dyn := unquotedLit(x.Value); dyn {
				return -1
			}
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					return -1
				}
			}
		default:
			return -1
		}
	}
	return total
}

func braceExpCount(b *syntax.BraceExp) int64 {
	if !b.Sequence {
		var sum int64
		for _, e := range b.Elems {
			n := braceWordCount(e)
			if n < 0 {
				return -1
			}
			sum += n
		}
		return sum
	}
	if len(b.Elems) < 2 || len(b.Elems) > 3 {
		return -1
	}
	var v [3]string
	for i, e := range b.Elems {
		if len(e.Parts) != 1 {
			return -1
		}
		l, ok := e.Parts[0].(*syntax.Lit)
		if !ok {
			return -1
		}
		v[i] = l.Value
	}
	step := int64(1)
	if len(b.Elems) == 3 {
		s, err := strconv.ParseInt(v[2], 10, 64)
		if err != nil {
			return -1
		}
		if s < 0 {
			s = -s
		}
		if s > 0 {
			step = s
		}
	}
	from, e1 := strconv.ParseInt(v[0], 10, 64)
	to, e2 := strconv.ParseInt(v[1], 10, 64)
	if e1 != nil || e2 != nil {
		if len(v[0]) != 1 || len(v[1]) != 1 {
			return -1
		}
		from, to = int64(v[0][0]), int64(v[1][0])
	}
	d := to - from
	if d < 0 {
		d = -d
	}
	return d/step + 1
}

// walkNode visits the substitutions inside an arbitrary AST subtree.
func (w *walker) walkNode(n syntax.Node, c ctx) {
	if n == nil {
		return
	}
	syntax.Walk(n, func(m syntax.Node) bool {
		switch x := m.(type) {
		case *syntax.CmdSubst:
			w.subst(x.Stmts, c)
			return false
		case *syntax.ProcSubst:
			w.subst(x.Stmts, c)
			return false
		}
		return true
	})
}

// walkWord is walkNode for a word, skipping the common case of a word made
// only of literals and quoted literals (nothing to find inside).
func (w *walker) walkWord(wd *syntax.Word, c ctx) {
	for _, p := range wd.Parts {
		switch x := p.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
			continue
		case *syntax.DblQuoted:
			plain := true
			for _, q := range x.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					plain = false
					break
				}
			}
			if plain {
				continue
			}
		}
		w.walkNode(wd, c)
		return
	}
}

func (w *walker) subst(list []*syntax.Stmt, c ctx) {
	c.inSubst = true
	c.redirs = nil
	c.timeWrap = false
	w.stmts(list, c)
}

func (w *walker) walkRedirWords(rs []*syntax.Redirect, c ctx) {
	for _, r := range rs {
		if r.Word != nil && r.Op != syntax.Hdoc && r.Op != syntax.DashHdoc {
			w.walkWord(r.Word, c)
		}
		if r.Hdoc != nil {
			w.walkWord(r.Hdoc, c)
		}
	}
}

func (w *walker) convertRedirs(rs []*syntax.Redirect) []redirInfo {
	out := make([]redirInfo, 0, len(rs))
	for _, r := range rs {
		ri := redirInfo{r: model.Redirect{Op: r.Op.String()}}
		if r.N != nil {
			ri.r.Fd = r.N.Value
			if isNumberWord(r.N.Value) {
				ri.r.Op = r.N.Value + ri.r.Op // "2>", "3>>", "2>&"
			}
		}
		var tw model.Word
		if r.Word != nil {
			tw = w.word(r.Word)
			ri.r.Target = tw
		}
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
			if t, ok := target(tw, "redirect"); ok {
				ri.writes = append(ri.writes, t)
			}
		case syntax.RdrInOut:
			if t, ok := target(tw, "redirect"); ok {
				ri.writes = append(ri.writes, t)
				ri.reads = append(ri.reads, t)
			}
		case syntax.DplOut:
			// `>&file` (not an fd number or "-") is bash's `&>file`.
			if !tw.Dynamic && tw.Lit != "-" && !isNumberWord(tw.Lit) {
				if t, ok := target(tw, "redirect"); ok {
					ri.writes = append(ri.writes, t)
				}
			}
		case syntax.RdrIn:
			if t, ok := target(tw, "redirect"); ok {
				ri.reads = append(ri.reads, t)
			}
		}
		out = append(out, ri)
	}
	return out
}

// call converts one simple command into a CommandNode (when it has a command
// word), walks its substitutions and recurses into `sh -c` scripts. It returns
// the node index or -1.
func (w *walker) call(s *syntax.Stmt, x *syntax.CallExpr, c ctx, timeWrap bool) int {
	for _, a := range x.Assigns {
		w.walkNode(a, c)
	}
	if len(x.Args) == 0 {
		w.walkRedirWords(s.Redirs, c)
		return -1
	}
	argv := w.b.argv(len(x.Args))
	for i, a := range x.Args {
		argv[i] = w.word(a)
	}
	rest, wrappers, sudo := StripWrappers(argv)
	if timeWrap {
		wrappers = append([]string{"time"}, wrappers...)
	}
	n := model.CommandNode{
		Argv: rest, Wrappers: wrappers, Sudo: sudo,
		Loop: c.loop, Async: c.async, InSubst: c.inSubst, Depth: c.depth,
	}
	if !rest[0].Dynamic {
		n.Name = baseName(rest[0].Lit)
		n.Writes = WriteTargets(n.Name, rest)
		n.Reads = ReadTargets(n.Name, rest)
		switch n.Name {
		case "git":
			n.Git = ParseGit(rest)
		case "sleep":
			si := ParseSleep(rest[1:])
			n.Sleep = &si
		}
		w.markUnknown(n.Name, rest[1:])
	}
	var own []redirInfo
	if len(s.Redirs) > 0 {
		own = w.convertRedirs(s.Redirs)
	}
	for _, list := range [2][]redirInfo{own, c.redirs} {
		for _, ri := range list {
			n.Redirects = append(n.Redirects, ri.r)
			n.Writes = append(n.Writes, ri.writes...)
			n.Reads = append(n.Reads, ri.reads...)
		}
	}
	w.b.cl.Nodes = append(w.b.cl.Nodes, n)
	idx := len(w.b.cl.Nodes) - 1

	for _, a := range x.Args {
		w.walkWord(a, c)
	}
	w.walkRedirWords(s.Redirs, c)

	if n.Name != "" && isShell(n.Name) && c.depth < w.b.max {
		if script, ok := shellScript(rest); ok {
			ic := c
			ic.depth++
			ic.timeWrap = false
			ic.redirs = append(append([]redirInfo(nil), own...), c.redirs...)
			w.b.inner(script, ic)
		}
	}
	return idx
}

func isShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash":
		return true
	}
	return false
}

// shellScript extracts the literal script of `sh -c '<script>' ...`.
func shellScript(rest []model.Word) (string, bool) {
	cFlag := false
	for i := 1; i < len(rest); i++ {
		a := rest[i]
		if a.Dynamic {
			if cFlag {
				return "", false
			}
			continue
		}
		s := a.Lit
		if s == "--" {
			if cFlag && i+1 < len(rest) && !rest[i+1].Dynamic {
				return rest[i+1].Lit, true
			}
			return "", false
		}
		if len(s) >= 2 && (s[0] == '-' || s[0] == '+') {
			switch s {
			case "-o", "+o", "-O", "+O", "--rcfile", "--init-file":
				i++
				continue
			}
			if s[0] == '-' && s[1] != '-' {
				for j := 1; j < len(s); j++ {
					if s[j] == 'c' {
						cFlag = true
					}
				}
			}
			continue
		}
		if cFlag {
			return s, true
		}
		return "", false
	}
	return "", false
}

// inner parses a `sh -c` script and appends its nodes at the given context.
// A syntax error discards the inner nodes only.
func (b *builder) inner(script string, c ctx) {
	f, err := parseFile(script)
	if err != nil {
		return
	}
	(&walker{b: b, src: script}).stmts(f.Stmts, c)
}

// markUnknown flags commands whose effect cannot be seen statically: eval,
// source of a dynamic file and `env -S`. `builtin` forwards to the builtin it
// names.
func (w *walker) markUnknown(name string, args []model.Word) {
	if name == "builtin" && len(args) > 0 && !args[0].Dynamic {
		name, args = baseName(args[0].Lit), args[1:]
	}
	switch name {
	case "eval":
		w.b.unknown("eval")
	case "source", ".":
		if len(args) > 0 && args[0].Dynamic {
			w.b.unknown("source")
		}
	case "env":
		if envSplits(args) {
			w.b.unknown("env -S")
		}
	}
}
