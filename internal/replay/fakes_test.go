package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// ---- adapter ----------------------------------------------------------

type fakeAdapter struct{ id model.ClientID }

func (a fakeAdapter) ID() model.ClientID                { return a.id }
func (a fakeAdapter) Capabilities() model.CapabilitySet { return model.Capabilities(a.id) }
func (a fakeAdapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[a.id] }
func (a fakeAdapter) Render(*model.Event, *model.Decision) ([]byte, error) {
	return nil, nil
}

type fakePayload struct {
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"`
	Cwd            string          `json:"cwd"`
	AgentID        string          `json:"agent_id"`
	SubagentID     string          `json:"subagent_id"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	Error          string          `json:"error"`
	LastAssistant  *string         `json:"last_assistant_message"`
}

func (a fakeAdapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	ne, ok := model.LookupNative(a.id, native)
	if !ok {
		return nil, model.ErrUnknownEvent
	}
	var p fakePayload
	if err := json.Unmarshal(stdin, &p); err != nil {
		return nil, model.ErrBadInput
	}
	ev := &model.Event{Client: a.id, Name: ne.Event, Native: native, SessionID: p.SessionID, Cwd: p.Cwd, AgentID: p.AgentID, Prompt: p.Prompt, Error: p.Error}
	if ev.SessionID == "" {
		ev.SessionID = p.ConversationID
	}
	if ev.AgentID == "" {
		ev.AgentID = p.SubagentID
	}
	if p.LastAssistant != nil {
		ev.LastAssistantMessage, ev.HasLastAssistantMessage = *p.LastAssistant, true
	}
	if len(p.ToolResponse) > 0 {
		ev.ToolResponse = p.ToolResponse
		_ = json.Unmarshal(p.ToolResponse, &ev.ToolResponseText)
	}
	if p.ToolName != "" {
		tc, _ := a.NormalizeTool(p.ToolName, p.ToolInput)
		tc.UseID = p.ToolUseID
		ev.Tool = &tc
	}
	return ev, nil
}

func (a fakeAdapter) NormalizeTool(raw string, input json.RawMessage) (model.ToolCall, error) {
	tc := model.ToolCall{Name: raw, RawName: raw, Input: input}
	var in struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
		Cmd      string `json:"cmd"`
		Input    string `json:"input"`
	}
	_ = json.Unmarshal(input, &in)
	switch raw {
	case "Bash", "exec_command", "Shell", "shell":
		tc.Name, tc.Shell = model.ToolBash, model.ShellBash
		tc.Command = in.Command
		if in.Cmd != "" {
			tc.Command = in.Cmd
		}
	case "PowerShell":
		tc.Shell, tc.Command = model.ShellPowerShell, in.Command
	case "Read", "Edit", "Write", "MultiEdit":
		if in.FilePath != "" {
			tc.Targets = []string{in.FilePath}
		}
	case "apply_patch":
		tc.Name = model.ToolEdit
		for _, line := range strings.Split(firstNonEmpty(in.Command, in.Input), "\n") {
			for _, pre := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
				if rest, ok := strings.CutPrefix(line, pre); ok {
					tc.Targets = append(tc.Targets, rest)
					if pre == "*** Delete File: " {
						tc.TargetDeletes = append(tc.TargetDeletes, rest)
					}
				}
			}
		}
	case "Task":
		tc.Name = model.ToolAgent
	}
	return tc, nil
}

type fakeRegistry struct{}

func (fakeRegistry) Get(id model.ClientID) (ports.Adapter, bool) { return fakeAdapter{id}, true }
func (fakeRegistry) All() []ports.Adapter                        { return nil }

// ---- paths ------------------------------------------------------------

type fakePaths struct{}

func (fakePaths) Normalize(p, base string, ctx model.PathContext) string {
	p = strings.ReplaceAll(p, `\`, "/")
	switch {
	case p == "~":
		p = ctx.Home
	case strings.HasPrefix(p, "~/"):
		p = ctx.Home + p[1:]
	}
	if !path.IsAbs(p) && !regexp.MustCompile(`^[A-Za-z]:/`).MatchString(p) {
		p = path.Join(strings.ReplaceAll(base, `\`, "/"), p)
	}
	return path.Clean(p)
}
func (fakePaths) Candidates(raw string, ctx model.PathContext) []string { return []string{raw} }
func (fakePaths) CompileGlobs([]string, model.PathContext) (ports.GlobMatcher, error) {
	return nil, fmt.Errorf("not used")
}

// ---- shell ------------------------------------------------------------

// fakeBash parses `cat F`, `sed -n R F` and `cd D && ...` into nodes.
type fakeBash struct{}

func (fakeBash) ParseBash(src string) (*model.CommandList, error) {
	if strings.Contains(src, "$(") || strings.Contains(src, "BROKEN(") {
		return nil, fmt.Errorf("fake parse failure")
	}
	cl := &model.CommandList{Shell: model.ShellBash}
	for _, part := range strings.Split(src, "&&") {
		f := strings.Fields(part)
		if len(f) == 0 {
			continue
		}
		n := model.CommandNode{Name: f[0]}
		for _, w := range f {
			n.Argv = append(n.Argv, model.Word{Lit: w})
		}
		switch f[0] {
		case "cat":
			for _, a := range f[1:] {
				n.Reads = append(n.Reads, model.WriteTarget{Path: a, Via: "cat"})
			}
		case "sed":
			if len(f) >= 4 && f[1] == "-n" {
				n.Reads = append(n.Reads, model.WriteTarget{Path: f[3], Via: "sed"})
			}
		}
		cl.Nodes = append(cl.Nodes, n)
	}
	return cl, nil
}

// fakeShellReadPaths mirrors the frozen model.ShellReadPaths rule for bash
// (U2 implements the real one; the stub in this worktree panics).
func fakeShellReadPaths(cl *model.CommandList) []string {
	if cl == nil {
		return nil
	}
	hasCD := false
	var out []string
	seen := map[string]bool{}
	for _, n := range cl.Nodes {
		if len(n.Argv) > 0 && !n.Argv[0].Dynamic {
			switch n.Argv[0].Lit {
			case "cd", "pushd", "popd":
				hasCD = true
			}
		}
		for _, r := range n.Reads {
			if !r.Dynamic && r.Path != "" && !seen[r.Path] {
				seen[r.Path] = true
				out = append(out, r.Path)
			}
		}
	}
	if hasCD {
		var abs []string
		for _, p := range out {
			if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~/") {
				abs = append(abs, p)
			}
		}
		return abs
	}
	return out
}

// sharedShellReads records whether Run is wired to model.ShellReadPaths
// (checked before TestMain swaps in the fake).
var sharedShellReads bool

// TestMain runs the package with fakeShellReadPaths in place of the U2
// function (a panicking stub in this worktree).
func TestMain(m *testing.M) {
	sharedShellReads = reflect.ValueOf(shellReadPaths).Pointer() == reflect.ValueOf(model.ShellReadPaths).Pointer()
	shellReadPaths = fakeShellReadPaths
	os.Exit(m.Run())
}

func TestShellReadsUseTheSharedFunction(t *testing.T) {
	if !sharedShellReads {
		t.Error("replay does not call model.ShellReadPaths")
	}
}

func useFakeShellReads(testing.TB) {}

// ---- rules loader -----------------------------------------------------

type fakeLoader struct {
	rs   *model.CompiledRuleSet
	reqs []model.LoadRequest
	err  error
}

func (l *fakeLoader) Load(req model.LoadRequest) (*model.CompiledRuleSet, error) {
	l.reqs = append(l.reqs, req)
	return l.rs, l.err
}

// ---- compiler (for --rules) ---------------------------------------------

type fakeCompiler struct{}

func (fakeCompiler) ParseFile(layer model.Layer, p string, data []byte) (*model.RuleFile, []model.Diagnostic, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rf model.RuleFile
	if err := dec.Decode(&rf); err != nil {
		return nil, nil, err
	}
	return &rf, nil, nil
}

func (fakeCompiler) Compile(rules []model.LoadedRule, s model.Settings, gen string) (*model.CompiledRuleSet, error) {
	rs := &model.CompiledRuleSet{Generation: gen, Settings: s}
	for _, lr := range rules {
		rs.Rules = append(rs.Rules, model.CompiledRule{LoadedRule: lr, Action: lr.Rule.EffectiveAction(), Target: lr.Rule.Target})
	}
	return rs, nil
}

// ---- mini engine --------------------------------------------------------

// miniEngine evaluates the predicates the replay tests need, with the same
// Kleene semantics and per-target bindings as the real engine.
type miniEngine struct {
	calls int
	err   error
}

func (e *miniEngine) Evaluate(rs *model.CompiledRuleSet, in *model.EvalInput) (*model.Verdict, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	v := &model.Verdict{}
	for i := range rs.Rules {
		cr := &rs.Rules[i]
		if cr.Rule.Event != in.Event.Name || !cr.Rule.AppliesTo(in.Client) {
			continue
		}
		out, matched := e.rule(cr, in)
		v.Results = append(v.Results, model.RuleResult{RuleID: cr.Rule.ID, Outcome: out})
		if out == model.TriTrue {
			v.Hits = append(v.Hits, model.RuleHit{RuleID: cr.Rule.ID, Layer: cr.Layer, Action: cr.Action, Target: cr.Target, Effective: cr.Action, Matched: matched})
		}
	}
	return v, nil
}

func (e *miniEngine) rule(cr *model.CompiledRule, in *model.EvalInput) (model.Tri, string) {
	r := &cr.Rule
	targets := []string{""}
	if in.Event.Tool != nil && len(in.Event.Tool.Targets) > 0 {
		targets = in.Event.Tool.Targets
	}
	res, matched := model.TriFalse, ""
	for _, tg := range targets {
		t := e.match(r.Match, in)
		for i := range r.When {
			t = t.And(e.cond(&r.When[i], in, tg))
		}
		if t == model.TriTrue && res != model.TriTrue {
			matched = "tool=" + toolName(in) + " target=" + tg
		}
		res = res.Or(t)
	}
	return res, matched
}

func toolName(in *model.EvalInput) string {
	if in.Event.Tool != nil {
		return in.Event.Tool.Name
	}
	return ""
}

func (e *miniEngine) match(m *model.Match, in *model.EvalInput) model.Tri {
	if m == nil {
		return model.TriTrue
	}
	t := model.TriTrue
	if len(m.Tool) > 0 {
		ok := false
		if in.Event.Tool != nil {
			for _, g := range m.Tool {
				a, _ := path.Match(g, in.Event.Tool.Name)
				b, _ := path.Match(g, in.Event.Tool.RawName)
				ok = ok || a || b
			}
		}
		t = t.And(model.TriOf(ok))
	}
	if m.Bash != nil && len(m.Bash.Cmd) > 0 {
		if in.Commands == nil {
			return model.TriFalse
		}
		cl, err := in.Commands.Get()
		if err != nil {
			return model.TriUnknown
		}
		ok := false
		for _, n := range cl.Nodes {
			for _, g := range m.Bash.Cmd {
				if hit, _ := path.Match(g, n.Name); hit {
					ok = true
				}
			}
		}
		t = t.And(model.TriOf(ok))
	}
	if m.Text != nil && len(m.Text.Regex) > 0 {
		text := in.Event.LastAssistantMessage
		if in.Event.Name == model.EventUserPromptSubmit {
			text = in.Event.Prompt
		}
		ok := false
		for _, rx := range m.Text.Regex {
			ok = ok || regexp.MustCompile(rx).MatchString(text)
		}
		t = t.And(model.TriOf(ok))
	}
	return t
}

func (e *miniEngine) cond(c *model.Cond, in *model.EvalInput, target string) model.Tri {
	switch {
	case len(c.All) > 0:
		t := model.TriTrue
		for i := range c.All {
			t = t.And(e.cond(&c.All[i], in, target))
		}
		return t
	case len(c.Any) > 0:
		t := model.TriFalse
		for i := range c.Any {
			t = t.Or(e.cond(&c.Any[i], in, target))
		}
		return t
	case c.Not != nil:
		return e.cond(c.Not, in, target).Not()
	case c.ReadThisSession != nil:
		p := c.ReadThisSession.Path
		if p == model.TargetBinding {
			p = target
		}
		if p == "" {
			return model.TriFalse
		}
		n := (fakePaths{}).Normalize(p, in.Event.Cwd, in.Paths)
		return model.TriOf(in.Session.HasRead(n))
	case c.ToolCountSinceInject != nil:
		return model.TriOf(c.ToolCountSinceInject.Eval(float64(in.Session.ToolCountSinceInject())))
	case c.PrevPromptRegex != nil:
		return model.TriOf(regexp.MustCompile(c.PrevPromptRegex.Regex).MatchString(in.Session.PrevPromptTail()))
	case c.PriorHit != nil:
		return model.TriOf(c.PriorHit.Cmp.Eval(float64(in.Session.PriorHits(c.PriorHit.Rule))))
	case c.IsSubagent != nil:
		return model.TriOf(in.Event.IsSubagent())
	case c.EnvEquals != nil:
		v, ok := in.World.Getenv(c.EnvEquals.Name)
		return model.TriOf(ok && v == c.EnvEquals.Value)
	case c.BranchGlob != nil:
		b, known := in.World.Branch()
		if known != model.TriTrue {
			return model.TriUnknown
		}
		for _, g := range c.BranchGlob.Patterns {
			if hit, _ := path.Match(g, b); hit {
				return model.TriTrue
			}
		}
		return model.TriFalse
	case c.PathExists != nil:
		p := c.PathExists.Path
		if p == model.TargetBinding {
			p = target
		}
		if p == "" {
			return model.TriFalse
		}
		return in.World.PathExists((fakePaths{}).Normalize(p, in.Event.Cwd, in.Paths))
	case c.VerifyFresh != nil:
		if in.Evidence == nil {
			return model.TriUnknown
		}
		return in.Evidence.VerifyFresh(model.FreshQuery{Names: c.VerifyFresh.Names, Kind: model.FreshWorktree}).Value
	case c.VerifyFailedSinceRead != nil:
		if in.Evidence == nil {
			return model.TriUnknown
		}
		t, _ := in.Evidence.LastFailureAfter(time.Time{})
		return t
	}
	return model.TriUnknown
}

// ---- rule builders --------------------------------------------------------

func cr(id string, ev model.EventName, mut func(*model.Rule)) model.CompiledRule {
	r := model.Rule{ID: id, Event: ev, Action: model.ActionLog, Target: model.ActionDeny}
	if mut != nil {
		mut(&r)
	}
	return model.CompiledRule{LoadedRule: model.LoadedRule{Rule: r, Layer: model.LayerUser}, Action: r.Action, Target: r.Target}
}

func rset(gen string, rules ...model.CompiledRule) *model.CompiledRuleSet {
	return &model.CompiledRuleSet{Generation: gen, Rules: rules}
}

// readBeforeEdit: Edit/Write hit unless the target was read this session.
func readBeforeEdit() model.CompiledRule {
	return cr("read-before-edit", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Edit", "Write", "MultiEdit"}}
		r.When = []model.Cond{{Not: &model.Cond{ReadThisSession: &model.PathArg{Path: model.TargetBinding}}}}
	})
}

// ---- environment ------------------------------------------------------------

type env struct {
	sb      *testenv.Sandbox
	dir     string // transcript directory
	loader  *fakeLoader
	engine  *miniEngine
	deps    Deps
	labels  string
	evDir   string
	home    string
	workdir string
}

func newEnv(t testing.TB, rs *model.CompiledRuleSet) *env {
	t.Helper()
	sb := testenv.New(t)
	e := &env{sb: sb, dir: sb.Root + "/transcripts", loader: &fakeLoader{rs: rs}, engine: &miniEngine{}}
	e.labels = sb.Root + "/labels"
	e.evDir = sb.Root + "/evidence"
	e.home = sb.Home
	e.workdir = sb.Root + "/work/proj"
	e.deps = Deps{
		FS: sys.OSFS{}, Clock: sys.NewFakeClock(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)),
		Adapters: fakeRegistry{}, Compiler: fakeCompiler{}, Engine: e.engine, Rules: e.loader,
		Bash: fakeBash{}, Paths: fakePaths{}, EvidenceDir: e.evDir, LabelsDir: e.labels, Home: sb.Home,
	}
	return e
}
