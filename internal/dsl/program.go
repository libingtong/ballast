package dsl

import (
	"regexp"
	"sort"
	"sync"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// lazyRE is a regular expression compiled on first use. Compilation is
// shared by every rule of an Engine that uses the same source text.
type lazyRE struct {
	src  string
	once sync.Once
	re   *regexp.Regexp
	err  error
}

func (l *lazyRE) get() (*regexp.Regexp, error) {
	l.once.Do(func() { l.re, l.err = regexp.Compile(l.src) })
	return l.re, l.err
}

// maxCachedRegexes bounds the per-Engine regex cache; beyond it expressions
// are compiled per program instead of being shared.
const maxCachedRegexes = 4096

func (e *Engine) lazy(src string) *lazyRE {
	if v, ok := e.res.Load(src); ok {
		return v.(*lazyRE)
	}
	l := &lazyRE{src: src}
	if e.resN.Load() >= maxCachedRegexes {
		return l
	}
	v, loaded := e.res.LoadOrStore(src, l)
	if !loaded {
		e.resN.Add(1)
	}
	return v.(*lazyRE)
}

func (e *Engine) lazyList(srcs []string) []*lazyRE {
	if len(srcs) == 0 {
		return nil
	}
	out := make([]*lazyRE, len(srcs))
	for i, s := range srcs {
		out[i] = e.lazy(s)
	}
	return out
}

// pathProg is a path-glob list; its compiled form (ports.GlobMatcher)
// depends on the home directory and GOOS of the evaluation context and is
// cached per (Home, GOOS).
type pathProg struct {
	patterns []string
	mu       sync.Mutex
	cache    map[string]globEntry
}

type globEntry struct {
	m   ports.GlobMatcher
	err error
}

func newPathProg(list []string) *pathProg {
	if len(list) == 0 {
		return nil
	}
	return &pathProg{patterns: list}
}

func (p *pathProg) matcher(ps ports.PathService, ctx model.PathContext) (ports.GlobMatcher, error) {
	key := ctx.Home + "\x00" + ctx.GOOS
	p.mu.Lock()
	defer p.mu.Unlock()
	if ent, ok := p.cache[key]; ok {
		return ent.m, ent.err
	}
	m, err := ps.CompileGlobs(p.patterns, ctx)
	if p.cache == nil {
		p.cache = make(map[string]globEntry, 1)
	}
	p.cache[key] = globEntry{m, err}
	return m, err
}

type shellProg struct {
	kind     model.ShellKind
	m        *model.ShellMatch
	cmd      *nameGlobs
	args     []*lazyRE
	flags    []string
	gitSub   []string
	pipeFrom *nameGlobs
	pipeTo   *nameGlobs
	writes   *pathProg
}

type inputProg struct {
	key string
	res []*lazyRE
}

type textProg struct {
	source     model.TextSource
	preprocess []string
	head, tail int
	regex      []*lazyRE
	notRegex   []*lazyRE
	cjk        *model.Cmp
	count      *lazyRE
	countCmp   model.Cmp
}

type condKind uint8

const (
	condBad condKind = iota
	condAll
	condAny
	condNot
	condPred
)

type condProg struct {
	kind  condKind
	subs  []*condProg
	pred  string // predicate name (condPred)
	path  string // PathArg.Path
	names []string
	cmp   model.Cmp
	re    *lazyRE
	rule  string
	env   *model.EnvEqualsArg
	globs *nameGlobs
	bad   string
}

// program is the dsl-owned compiled form of one rule.
type program struct {
	id    string
	event model.EventName

	tool   *nameGlobs
	agent  *nameGlobs
	runBG  *bool
	inputs []inputProg
	text   *textProg
	bash   *shellProg
	ps     *shellProg
	path   *pathProg
	when   []*condProg
}

// RuleID implements model.RuleProgram.
func (p *program) RuleID() string { return p.id }

var _ model.RuleProgram = (*program)(nil)

// defaultTextSource returns the default text.source of a rule event.
func defaultTextSource(ev model.EventName) model.TextSource {
	switch ev {
	case model.EventUserPromptSubmit:
		return model.TextPrompt
	case model.EventPostToolUse:
		return model.TextToolResponse
	case model.EventPostToolUseFailure:
		return model.TextError
	}
	return model.TextLastAssistantMessage
}

func (e *Engine) buildProgram(r *model.Rule) *program {
	p := &program{id: r.ID, event: r.Event}
	if m := r.Match; m != nil {
		p.tool = newNameGlobs(m.Tool)
		p.agent = newNameGlobs(m.AgentType)
		p.runBG = m.RunInBackground
		p.path = newPathProg(m.Path)
		if len(m.InputRegex) > 0 {
			keys := sortedKeys(m.InputRegex)
			for _, k := range keys {
				p.inputs = append(p.inputs, inputProg{key: k, res: e.lazyList(m.InputRegex[k])})
			}
		}
		if m.Bash != nil {
			p.bash = e.buildShell(model.ShellBash, m.Bash)
		}
		if m.PS != nil {
			p.ps = e.buildShell(model.ShellPowerShell, m.PS)
		}
		if m.Text != nil {
			p.text = e.buildText(r.Event, m.Text)
		}
	}
	for i := range r.When {
		p.when = append(p.when, e.buildCond(&r.When[i]))
	}
	return p
}

func (e *Engine) buildShell(kind model.ShellKind, m *model.ShellMatch) *shellProg {
	s := &shellProg{kind: kind, m: m}
	s.cmd = newNameGlobs(m.Cmd)
	s.args = e.lazyList(m.ArgsRegex)
	s.flags = m.Flags
	s.gitSub = m.GitSub
	s.pipeFrom = newNameGlobs(m.PipeFrom)
	s.pipeTo = newNameGlobs(m.PipeTo)
	s.writes = newPathProg(m.Writes)
	return s
}

func (e *Engine) buildText(ev model.EventName, t *model.TextMatch) *textProg {
	tp := &textProg{source: t.Source, preprocess: t.Preprocess}
	if tp.source == "" {
		tp.source = defaultTextSource(ev)
	}
	if t.Window != nil {
		tp.head, tp.tail = t.Window.HeadChars, t.Window.TailChars
	}
	tp.regex = e.lazyList(t.Regex)
	tp.notRegex = e.lazyList(t.NotRegex)
	tp.cjk = t.CJKRatio
	if t.CountRegex != nil {
		tp.count = e.lazy(t.CountRegex.Regex)
		tp.countCmp = t.CountRegex.Cmp
	}
	return tp
}

func (e *Engine) buildCond(c *model.Cond) *condProg {
	n := 0
	var cp *condProg
	set := func(p *condProg) {
		n++
		cp = p
	}
	if c.All != nil {
		p := &condProg{kind: condAll}
		for i := range c.All {
			p.subs = append(p.subs, e.buildCond(&c.All[i]))
		}
		set(p)
	}
	if c.Any != nil {
		p := &condProg{kind: condAny}
		for i := range c.Any {
			p.subs = append(p.subs, e.buildCond(&c.Any[i]))
		}
		set(p)
	}
	if c.Not != nil {
		set(&condProg{kind: condNot, subs: []*condProg{e.buildCond(c.Not)}})
	}
	if c.ReadThisSession != nil {
		set(&condProg{kind: condPred, pred: model.PredReadThisSession, path: c.ReadThisSession.Path})
	}
	if c.PathExists != nil {
		set(&condProg{kind: condPred, pred: model.PredPathExists, path: c.PathExists.Path})
	}
	if c.VerifyFresh != nil {
		set(&condProg{kind: condPred, pred: model.PredVerifyFresh, names: c.VerifyFresh.Names})
	}
	if c.VerifyFailedSinceRead != nil {
		set(&condProg{kind: condPred, pred: model.PredVerifyFailedSinceRead, path: c.VerifyFailedSinceRead.Path})
	}
	if c.ToolCountSinceInject != nil {
		set(&condProg{kind: condPred, pred: model.PredToolCountSinceInject, cmp: *c.ToolCountSinceInject})
	}
	if c.PrevPromptRegex != nil {
		set(&condProg{kind: condPred, pred: model.PredPrevPromptRegex, re: e.lazy(c.PrevPromptRegex.Regex)})
	}
	if c.PriorHit != nil {
		set(&condProg{kind: condPred, pred: model.PredPriorHit, rule: c.PriorHit.Rule, cmp: c.PriorHit.Cmp})
	}
	if c.EnvEquals != nil {
		set(&condProg{kind: condPred, pred: model.PredEnvEquals, env: c.EnvEquals})
	}
	if c.BranchGlob != nil {
		set(&condProg{kind: condPred, pred: model.PredBranchGlob, globs: newNameGlobs(c.BranchGlob.Patterns)})
	}
	if c.IsSubagent != nil {
		set(&condProg{kind: condPred, pred: model.PredIsSubagent})
	}
	if n != 1 {
		return &condProg{kind: condBad, bad: "cond:arity"}
	}
	return cp
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
