package dsl

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// res is a three-valued result that remembers why it is unknown.
type res struct {
	t     model.Tri
	cause string
}

var (
	resTrue  = res{t: model.TriTrue}
	resFalse = res{t: model.TriFalse}
)

func unk(cause string) res { return res{t: model.TriUnknown, cause: cause} }

func fromBool(b bool) res {
	if b {
		return resTrue
	}
	return resFalse
}

func fromTri(t model.Tri, cause string) res {
	if t == model.TriUnknown {
		return unk(cause)
	}
	return res{t: t}
}

func (a res) and(b res) res {
	t := a.t.And(b.t)
	switch {
	case t != model.TriUnknown:
		return res{t: t}
	case a.t == model.TriUnknown:
		return a
	}
	return b
}

func (a res) or(b res) res {
	t := a.t.Or(b.t)
	switch {
	case t != model.TriUnknown:
		return res{t: t}
	case a.t == model.TriUnknown:
		return a
	}
	return b
}

func (a res) not() res {
	if a.t == model.TriUnknown {
		return a
	}
	return res{t: a.t.Not()}
}

// target is the path a binding is attached to (a tool target or a shell
// write target).
type target struct {
	raw string
	dyn bool
	ok  bool
}

// binding is one (node, target) pair `when` is evaluated on; node is -1
// when the binding is not tied to a shell node.
type binding struct {
	node int
	tgt  target
}

type evalCtx struct {
	e  *Engine
	in *model.EvalInput
	ev *model.Event
	pc model.PathContext
	ps ports.PathService

	cmdsDone  bool
	cmds      *model.CommandList
	cmdsCause string

	cands map[string][]string
}

// Evaluate implements ports.RuleEvaluator.
func (e *Engine) Evaluate(rs *model.CompiledRuleSet, in *model.EvalInput) (v *model.Verdict, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = &model.Verdict{}, fmt.Errorf("dsl: internal error: %v", r)
		}
	}()
	if rs == nil || in == nil || in.Event == nil {
		return &model.Verdict{}, nil
	}
	ev := in.Event
	client := in.Client
	if client == "" {
		client = ev.Client
	}
	if ev.IsInternalCompactAgent() {
		return &model.Verdict{}, nil
	}
	pc := in.Paths
	if pc.GOOS == "" {
		pc.GOOS = e.opt.GOOS
		if pc.GOOS == "" {
			pc.GOOS = runtime.GOOS
		}
	}
	c := &evalCtx{e: e, in: in, ev: ev, pc: pc, ps: e.opt.Paths}

	results := []model.RuleResult{}
	var hits []model.RuleHit
	for i := range rs.Rules {
		cr := &rs.Rules[i]
		r := &cr.Rule
		if r.Event != ev.Name || !r.AppliesTo(client) {
			continue
		}
		out, matched := c.safeEvalRule(cr)
		rr := model.RuleResult{RuleID: r.ID, Outcome: out.t}
		if out.t == model.TriUnknown {
			rr.UnknownCause = out.cause
		}
		results = append(results, rr)
		if out.t == model.TriTrue {
			action, tgt := cr.Action, cr.Target
			if action == model.ActionNone {
				action = r.EffectiveAction()
			}
			if tgt == model.ActionNone {
				tgt = r.Target
				if tgt == model.ActionNone {
					tgt = model.DefaultTarget(r.Event)
				}
			}
			hits = append(hits, model.RuleHit{
				RuleID: r.ID, Layer: cr.Layer, Action: action, Target: tgt,
				Message: r.Message, Matched: matched,
			})
		}
	}
	return Arbitrate(client, ev.Name, results, hits), nil
}

func (c *evalCtx) safeEvalRule(cr *model.CompiledRule) (out res, matched string) {
	defer func() {
		if r := recover(); r != nil {
			out, matched = unk("internal:panic"), ""
		}
	}()
	p, ok := cr.Program.(*program)
	if !ok || p == nil {
		p = c.e.buildProgram(&cr.Rule)
	}
	return c.evalRule(p)
}

// evalRule implements dsl-semantics §9.
func (c *evalCtx) evalRule(p *program) (res, string) {
	m := c.evalFlat(p)
	if m.t == model.TriFalse {
		return m, ""
	}
	bs, uncertain, ucause := c.bindings(p)
	w := resFalse
	matched := ""
	for _, b := range bs {
		wb := resTrue
		for _, cp := range p.when {
			wb = wb.and(c.evalCond(cp, b))
			if wb.t == model.TriFalse {
				break
			}
		}
		w = w.or(wb)
		if w.t == model.TriTrue {
			matched = c.describe(p, b)
			break
		}
	}
	if uncertain {
		w = w.or(unk(ucause))
	}
	return m.and(w), matched
}

func (c *evalCtx) shellKind() model.ShellKind {
	t := c.ev.Tool
	if t == nil {
		return model.ShellNone
	}
	if t.Shell != model.ShellNone {
		return t.Shell
	}
	switch t.Name {
	case model.ToolBash:
		return model.ShellBash
	case model.ToolPowerShell:
		return model.ShellPowerShell
	}
	return model.ShellNone
}

func (c *evalCtx) agentType() string {
	if c.ev.AgentType != "" {
		return c.ev.AgentType
	}
	if c.ev.AgentID == "" {
		return "main"
	}
	return ""
}

// evalFlat evaluates the non-binding match fields (AND).
func (c *evalCtx) evalFlat(p *program) res {
	t := c.ev.Tool
	if p.tool != nil {
		if t == nil || !p.tool.match(t.Name, t.RawName) {
			return resFalse
		}
	}
	if p.bash != nil || p.ps != nil {
		switch c.shellKind() {
		case model.ShellBash:
			if p.bash == nil {
				return resFalse
			}
		case model.ShellPowerShell:
			if p.ps == nil {
				return resFalse
			}
		default:
			return resFalse
		}
	}
	if p.agent != nil && !p.agent.match(c.agentType()) {
		return resFalse
	}
	if p.runBG != nil && (t == nil || t.RunInBackground != *p.runBG) {
		return resFalse
	}
	out := resTrue
	for i := range p.inputs {
		out = out.and(c.evalInput(&p.inputs[i]))
		if out.t == model.TriFalse {
			return out
		}
	}
	if p.text != nil {
		out = out.and(c.evalText(p.text))
	}
	return out
}

func (c *evalCtx) fieldValues(key string) []string {
	t := c.ev.Tool
	if t == nil {
		return nil
	}
	switch key {
	case VirtualNewText:
		out := make([]string, 0, len(t.Edits))
		for _, e := range t.Edits {
			out = append(out, e.NewString)
		}
		return out
	case VirtualOldText:
		out := make([]string, 0, len(t.Edits))
		for _, e := range t.Edits {
			out = append(out, e.OldString)
		}
		return out
	}
	return t.Fields[key]
}

func (c *evalCtx) evalInput(ip *inputProg) res {
	vals := c.fieldValues(ip.key)
	if len(vals) == 0 {
		return resFalse
	}
	out := resFalse
	for _, l := range ip.res {
		re, err := l.get()
		if err != nil {
			out = out.or(unk("input_regex:" + ip.key + ":bad_regex"))
			continue
		}
		for _, v := range vals {
			if re.MatchString(v) {
				return resTrue
			}
		}
	}
	return out
}

// textSource resolves the text a text matcher reads. ok=false means the
// returned res is the final answer.
func (c *evalCtx) textSource(src model.TextSource) (string, res, bool) {
	ev := c.ev
	switch src {
	case model.TextPrompt:
		return ev.Prompt, resTrue, true
	case model.TextError:
		return ev.Error, resTrue, true
	case model.TextToolResponse:
		if ev.ToolResponseText != "" || len(ev.ToolResponse) == 0 {
			return ev.ToolResponseText, resTrue, true
		}
		return flattenToolResponse(ev.ToolResponse), resTrue, true
	default:
		if ev.HasLastAssistantMessage || ev.LastAssistantMessage != "" {
			return ev.LastAssistantMessage, resTrue, true
		}
		if c.stateOK() {
			if s := c.in.Session.LastAssistantText(); s != "" {
				return s, resTrue, true
			}
		}
		return "", unk("text:missing_source"), false
	}
}

func (c *evalCtx) evalText(tp *textProg) res {
	text, r, ok := c.textSource(tp.source)
	if !ok {
		return r
	}
	if text == "" {
		return resFalse
	}
	if len(tp.preprocess) > 0 {
		text = Preprocess(text, tp.preprocess)
	}
	if tp.head > 0 || tp.tail > 0 {
		text = WindowText(text, tp.head, tp.tail)
	}
	out := resTrue
	if len(tp.regex) > 0 {
		anyRe := resFalse
		for _, l := range tp.regex {
			re, err := l.get()
			if err != nil {
				anyRe = anyRe.or(unk("text:bad_regex"))
				continue
			}
			if re.MatchString(text) {
				anyRe = resTrue
				break
			}
		}
		if anyRe.t == model.TriFalse {
			return anyRe
		}
		out = out.and(anyRe)
	}
	if tp.cjk != nil {
		ratio, known := CJKRatio(text)
		if !known {
			return resFalse
		}
		if !tp.cjk.Eval(ratio) {
			return resFalse
		}
	}
	if tp.count != nil {
		re, err := tp.count.get()
		if err != nil {
			out = out.and(unk("text:bad_regex"))
		} else {
			if n := len(re.FindAllStringIndex(text, -1)); !tp.countCmp.Eval(float64(n)) {
				return resFalse
			}
		}
	}
	for _, l := range tp.notRegex {
		re, err := l.get()
		if err != nil {
			out = out.and(unk("text:bad_regex"))
			continue
		}
		if re.MatchString(text) {
			return resFalse
		}
	}
	return out
}

func (c *evalCtx) stateOK() bool {
	return c.in.Session != nil && c.in.SessionReadable
}

// candidates memoizes PathService.Candidates for one evaluation.
func (c *evalCtx) candidates(raw string) []string {
	if cs, ok := c.cands[raw]; ok {
		return cs
	}
	cs := c.ps.Candidates(raw, c.pc)
	if c.cands == nil {
		c.cands = make(map[string][]string)
	}
	c.cands[raw] = cs
	return cs
}

// pathMatch matches one raw path against a path-glob list.
func (c *evalCtx) pathMatch(pp *pathProg, raw string) res {
	if c.ps == nil {
		return unk("paths:unavailable")
	}
	if raw == "" {
		return resFalse
	}
	m, err := pp.matcher(c.ps, c.pc)
	if err != nil || m == nil {
		return unk("paths:bad_glob")
	}
	return fromBool(m.Match(c.candidates(raw)...))
}

// writesMatch matches a node's write targets against pp.
func (c *evalCtx) writesMatch(pp *pathProg, ws []model.WriteTarget, kind string) (res, []target) {
	var matched []target
	out := resFalse
	for _, w := range ws {
		if w.Dynamic {
			out = out.or(unk(kind + ":dynamic_write_target"))
			continue
		}
		r := c.pathMatch(pp, w.Path)
		switch r.t {
		case model.TriTrue:
			matched = append(matched, target{raw: w.Path, ok: true})
			out = resTrue
		case model.TriUnknown:
			out = out.or(r)
		}
	}
	return out, matched
}

func kindName(k model.ShellKind) string {
	if k == model.ShellPowerShell {
		return "ps"
	}
	return "bash"
}

// commandList parses (once) the event's shell command. A nil list comes with
// the unknown cause.
func (c *evalCtx) commandList() (*model.CommandList, string) {
	if c.cmdsDone {
		return c.cmds, c.cmdsCause
	}
	c.cmdsDone = true
	kind := kindName(c.shellKind())
	switch {
	case c.in.Commands == nil:
		if t := c.ev.Tool; t == nil || t.Command == "" {
			c.cmds = &model.CommandList{Shell: c.shellKind()}
		} else {
			c.cmdsCause = kind + ":no_commands"
		}
	default:
		cl, err := c.in.Commands.Get()
		switch {
		case err != nil:
			c.cmdsCause = kind + ":parse_error"
		case cl == nil:
			c.cmds = &model.CommandList{Shell: c.shellKind()}
		default:
			c.cmds = cl
		}
	}
	return c.cmds, c.cmdsCause
}

// bindings computes the binding set of a rule (dsl-semantics §9). uncertain
// reports that some candidate was unknown (U in the spec).
func (c *evalCtx) bindings(p *program) (bs []binding, uncertain bool, cause string) {
	note := func(why string) {
		if !uncertain {
			uncertain, cause = true, why
		}
	}
	t := c.ev.Tool
	sk := c.shellKind()
	if p.bash != nil || p.ps != nil {
		sp := p.bash
		if sk == model.ShellPowerShell {
			sp = p.ps
		}
		if sp == nil {
			return nil, false, ""
		}
		kind := kindName(sk)
		cl, why := c.commandList()
		if cl == nil {
			note(why)
			return nil, uncertain, cause
		}
		for i := range cl.Nodes {
			r := c.matchNode(sp, cl, i, kind)
			switch r.t {
			case model.TriUnknown:
				note(r.cause)
			case model.TriTrue:
				bs = c.nodeBindings(bs, p, sp, cl, i, kind, note)
			}
		}
		if cl.Unknown {
			reason := cl.UnknownReason
			if reason == "" {
				reason = "unknown_construct"
			}
			note(kind + ":" + reason)
		}
		return bs, uncertain, cause
	}
	if p.path == nil {
		if t != nil && sk == model.ShellNone {
			for _, raw := range t.Targets {
				if raw != "" {
					bs = append(bs, binding{node: -1, tgt: target{raw: raw, ok: true}})
				}
			}
			if len(bs) > 0 {
				return bs, false, ""
			}
		}
		return []binding{{node: -1}}, false, ""
	}
	if t == nil {
		return nil, false, ""
	}
	if sk != model.ShellNone {
		kind := kindName(sk)
		cl, why := c.commandList()
		if cl == nil {
			note(why)
			return nil, uncertain, cause
		}
		for i := range cl.Nodes {
			for _, w := range cl.Nodes[i].Writes {
				if w.Dynamic {
					note(kind + ":dynamic_write_target")
					continue
				}
				switch r := c.pathMatch(p.path, w.Path); r.t {
				case model.TriTrue:
					bs = append(bs, binding{node: i, tgt: target{raw: w.Path, ok: true}})
				case model.TriUnknown:
					note(r.cause)
				}
			}
		}
		if cl.Unknown {
			reason := cl.UnknownReason
			if reason == "" {
				reason = "unknown_construct"
			}
			note(kind + ":" + reason)
		}
		return bs, uncertain, cause
	}
	for _, raw := range t.Targets {
		switch r := c.pathMatch(p.path, raw); r.t {
		case model.TriTrue:
			bs = append(bs, binding{node: -1, tgt: target{raw: raw, ok: true}})
		case model.TriUnknown:
			note(r.cause)
		}
	}
	return bs, uncertain, cause
}

// nodeBindings expands one positively matching shell node into bindings:
// with match.path, one per write target satisfying it; else with
// <shell>.writes, one per matching write target; else the bare node.
func (c *evalCtx) nodeBindings(bs []binding, p *program, sp *shellProg, cl *model.CommandList, i int, kind string, note func(string)) []binding {
	n := &cl.Nodes[i]
	switch {
	case p.path != nil:
		for _, w := range n.Writes {
			if w.Dynamic {
				note(kind + ":dynamic_write_target")
				continue
			}
			switch r := c.pathMatch(p.path, w.Path); r.t {
			case model.TriTrue:
				bs = append(bs, binding{node: i, tgt: target{raw: w.Path, ok: true}})
			case model.TriUnknown:
				note(r.cause)
			}
		}
	case sp.writes != nil:
		_, matched := c.writesMatch(sp.writes, n.Writes, kind)
		if len(matched) == 0 {
			return append(bs, binding{node: i})
		}
		for _, t := range matched {
			bs = append(bs, binding{node: i, tgt: t})
		}
	default:
		bs = append(bs, binding{node: i})
	}
	return bs
}

func (c *evalCtx) describe(p *program, b binding) string {
	var s string
	switch {
	case b.node >= 0 && c.cmds != nil && b.node < len(c.cmds.Nodes):
		n := &c.cmds.Nodes[b.node]
		parts := make([]string, 0, len(n.Argv))
		for _, w := range n.Argv {
			parts = append(parts, w.Lit)
		}
		s = fmt.Sprintf("%s node %d: %s", kindName(c.shellKind()), b.node, strings.Join(parts, " "))
	case b.tgt.ok:
		s = "path: " + b.tgt.raw
	case p.text != nil:
		s = "text: " + string(p.text.source)
	case c.ev.Tool != nil:
		s = "tool: " + c.ev.Tool.Name
	default:
		s = "event: " + string(c.ev.Name)
	}
	return truncateRunes(s, model.MaxMatchedRunes)
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}
