package dsl

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// ---- CommandNode / CommandList builders ----

func nd(name string, args ...string) model.CommandNode {
	n := model.CommandNode{Name: name, Argv: []model.Word{{Lit: name}}}
	for _, a := range args {
		n.Argv = append(n.Argv, model.Word{Lit: a})
	}
	return n
}

func withDyn(n model.CommandNode, lits ...string) model.CommandNode {
	for _, l := range lits {
		n.Argv = append(n.Argv, model.Word{Lit: l, Dynamic: true})
	}
	return n
}

func withWrites(n model.CommandNode, paths ...string) model.CommandNode {
	for _, p := range paths {
		n.Writes = append(n.Writes, model.WriteTarget{Path: p, Via: "redirect"})
	}
	return n
}

func withDynWrite(n model.CommandNode) model.CommandNode {
	n.Writes = append(n.Writes, model.WriteTarget{Dynamic: true, Via: "redirect"})
	return n
}

func withLoop(n model.CommandNode, kind model.LoopKind, bounded model.Tri) model.CommandNode {
	n.Loop = &model.LoopInfo{Kind: kind, N: -1, Bounded: bounded, Depth: 1}
	return n
}

func withSleep(n model.CommandNode, secs float64, known bool) model.CommandNode {
	n.Sleep = &model.SleepInfo{Seconds: secs, Known: known}
	return n
}

func withGit(n model.CommandNode, sub string) model.CommandNode {
	n.Git = &model.GitInvocation{Sub: sub}
	return n
}

func bashList(nodes ...model.CommandNode) *model.CommandList {
	return &model.CommandList{Shell: model.ShellBash, Nodes: nodes}
}

func psList(nodes ...model.CommandNode) *model.CommandList {
	return &model.CommandList{Shell: model.ShellPowerShell, Nodes: nodes}
}

// ---- scenarios ----

// scenario describes one evaluation input in terms of a Claude Code payload.
type scenario struct {
	native  string // default PreToolUse
	payload string // JSON object (session_id/cwd are filled in)
	cl      *model.CommandList
	cmdErr  error
	fx      model.Fixture
	client  model.ClientID // default claude-code
}

func toolPayload(name, input string) string {
	return fmt.Sprintf(`{"tool_name":%q,"tool_input":%s}`, name, input)
}

func bashScn(command string, cl *model.CommandList) scenario {
	return scenario{payload: toolPayload("Bash", fmt.Sprintf(`{"command":%q}`, command)), cl: cl}
}

func psScn(command string, cl *model.CommandList) scenario {
	return scenario{payload: toolPayload("PowerShell", fmt.Sprintf(`{"command":%q}`, command)), cl: cl}
}

func editScn(file string) scenario {
	return scenario{payload: toolPayload("Edit", fmt.Sprintf(`{"file_path":%q,"old_string":"a","new_string":"b"}`, file))}
}

func (s scenario) with(fx model.Fixture) scenario { s.fx = fx; return s }

func (s scenario) at(native string) scenario { s.native = native; return s }

// input builds the EvalInput through the fake claude adapter.
func (s scenario) input(t testing.TB) *model.EvalInput {
	t.Helper()
	native := s.native
	if native == "" {
		native = "PreToolUse"
	}
	payload := s.payload
	if payload == "" {
		payload = "{}"
	}
	payload = strings.TrimSuffix(payload, "}")
	if strings.TrimSpace(payload) != "{" {
		payload += ","
	}
	payload += `"session_id":"s1","cwd":"/repo"}`
	ev, err := fakeClaude{}.ParseEvent(native, []byte(payload))
	if err != nil {
		t.Fatalf("parse event: %v\n%s", err, payload)
	}
	fx := s.fx
	pc := model.PathContext{Cwd: "/repo", ProjectRoot: "/repo", Home: "/home/user", GOOS: "linux"}
	sess, evid, world := FixtureEnv(&fx, pc)
	in := &model.EvalInput{
		Event: ev, Client: s.client, Paths: pc, Session: sess, SessionReadable: true,
		Evidence: evid, World: world, Now: fixtureNow,
	}
	if in.Client == "" {
		in.Client = model.ClientClaude
	}
	if s.cl != nil || s.cmdErr != nil {
		in.Commands = staticCommands{cl: s.cl, err: s.cmdErr}
	}
	return in
}

// ruleJSON assembles a rule; match and when may be empty.
func ruleJSON(event, match, when string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"id":"t-rule","action":"deny","event":%q`, event)
	if match != "" {
		b.WriteString(`,"match":` + match)
	}
	if when != "" {
		b.WriteString(`,"when":` + when)
	}
	b.WriteString(`,"message":"m","examples":[]}`)
	return b.String()
}

// evalRuleJSON compiles one rule and evaluates it against the scenario.
func evalRuleJSON(t testing.TB, e *Engine, rule string, s scenario) (model.RuleResult, *model.Verdict) {
	t.Helper()
	cr, err := e.Compile([]model.LoadedRule{{Rule: decodeRule(t, rule), Layer: model.LayerRepo}}, model.Settings{}, "g")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v, err := e.Evaluate(cr, s.input(t))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(v.Results) == 0 {
		return model.RuleResult{}, v
	}
	return v.Results[0], v
}

type row struct {
	name  string
	rule  string
	s     scenario
	want  model.Tri
	cause string // expected UnknownCause prefix when want is unknown
}

func runRows(t *testing.T, e *Engine, rows []row) {
	t.Helper()
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rr, v := evalRuleJSON(t, e, r.rule, r.s)
			if len(v.Results) != 1 {
				t.Fatalf("expected one result, got %+v", v.Results)
			}
			if rr.Outcome != r.want {
				t.Fatalf("outcome = %v (cause %q), want %v", rr.Outcome, rr.UnknownCause, r.want)
			}
			if r.want == model.TriUnknown && !strings.HasPrefix(rr.UnknownCause, r.cause) {
				t.Errorf("unknown cause = %q, want prefix %q", rr.UnknownCause, r.cause)
			}
			if r.want != model.TriUnknown && rr.UnknownCause != "" {
				t.Errorf("unexpected unknown cause %q on a %v outcome", rr.UnknownCause, rr.Outcome)
			}
		})
	}
}

const (
	T = model.TriTrue
	F = model.TriFalse
	U = model.TriUnknown
)

// evalWith is evalRuleJSON with a hook to adjust the EvalInput.
func evalWith(t testing.TB, e *Engine, rule string, s scenario, mutate func(*model.EvalInput)) (model.RuleResult, *model.Verdict) {
	t.Helper()
	cr, err := e.Compile([]model.LoadedRule{{Rule: decodeRule(t, rule), Layer: model.LayerRepo}}, model.Settings{}, "g")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	in := s.input(t)
	if mutate != nil {
		mutate(in)
	}
	v, err := e.Evaluate(cr, in)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(v.Results) == 0 {
		return model.RuleResult{}, v
	}
	return v.Results[0], v
}

// jsonUnmarshalLoose decodes into v without rejecting unknown fields.
func jsonUnmarshalLoose(s string, v any) error { return json.Unmarshal([]byte(s), v) }
