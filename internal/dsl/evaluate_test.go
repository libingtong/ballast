package dsl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func compileRules(t testing.TB, e *Engine, rules ...string) *model.CompiledRuleSet {
	t.Helper()
	var lrs []model.LoadedRule
	for i, r := range rules {
		lrs = append(lrs, model.LoadedRule{Rule: decodeRule(t, r), Layer: model.LayerRepo, Index: i})
	}
	rs, err := e.Compile(lrs, model.Settings{}, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func rule(id, event, action, clients, match, message string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"id":%q,"event":%q,"action":%q`, id, event, action)
	if clients != "" {
		b.WriteString(`,"clients":` + clients)
	}
	if match != "" {
		b.WriteString(`,"match":` + match)
	}
	if message != "" {
		b.WriteString(`,"message":` + jsonStr(message))
	}
	b.WriteString(`,"examples":[]}`)
	return b.String()
}

func TestEvaluateGating(t *testing.T) {
	e := newEngine(t)
	rs := compileRules(t, e,
		rule("pre-rule", "PreToolUse", "deny", "", `{"tool":"Edit"}`, "no edits"),
		rule("post-rule", "PostToolUse", "warn", "", `{"tool":"Edit"}`, "post"),
		rule("codex-only", "PreToolUse", "deny", `["codex"]`, `{"tool":"Edit"}`, "codex"),
		rule("tool-miss", "PreToolUse", "deny", "", `{"tool":"Write"}`, "write"),
	)
	v, err := e.Evaluate(rs, editScn("/repo/a.go").input(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Results) != 2 {
		t.Fatalf("results = %+v (only same-event, applicable rules are evaluated)", v.Results)
	}
	if v.Results[0].RuleID != "pre-rule" || v.Results[0].Outcome != T || v.Results[1].RuleID != "tool-miss" || v.Results[1].Outcome != F {
		t.Errorf("results = %+v", v.Results)
	}
	if v.Action != model.ActionDeny || v.Reason != "[pre-rule] no edits" || len(v.Hits) != 1 {
		t.Errorf("verdict = %+v", v)
	}
	h := v.Hits[0]
	if h.Layer != model.LayerRepo || h.Action != model.ActionDeny || h.Effective != model.ActionDeny || h.Target != model.ActionDeny || h.Matched != "path: /repo/a.go" {
		t.Errorf("hit = %+v", h)
	}
}

func TestEvaluateClientAndDegradation(t *testing.T) {
	e := newEngine(t)
	rs := compileRules(t, e,
		rule("ask-rule", "PreToolUse", "ask", "", `{"tool":"Edit"}`, "confirm"),
		rule("deny-rule", "PreToolUse", "deny", "", `{"tool":"Write"}`, "no write"),
	)
	// On codex "ask" is unsupported and degrades to log.
	s := editScn("/repo/a.go")
	s.client = model.ClientCodex
	v, _ := e.Evaluate(rs, s.input(t))
	if v.Action != model.ActionLog || len(v.Hits) != 1 || v.Hits[0].Effective != model.ActionLog || v.Hits[0].Action != model.ActionAsk || v.Reason != "" {
		t.Errorf("codex verdict = %+v", v)
	}
	s.client = model.ClientClaude
	v, _ = e.Evaluate(rs, s.input(t))
	if v.Action != model.ActionAsk || v.Reason != "[ask-rule] confirm" {
		t.Errorf("claude verdict = %+v", v)
	}
}

func TestEvaluateDefaultsAndLabels(t *testing.T) {
	e := newEngine(t)
	// No action: log, target defaults to the strictest action of the event.
	rs := compileRules(t, e, `{"id":"shadow-rule","event":"PreToolUse","match":{"tool":"Edit"},"examples":[]}`,
		`{"id":"post-shadow","event":"PostToolUse","match":{"tool":"Edit"},"examples":[]}`)
	cr := rs.Rule("shadow-rule")
	if cr.Action != model.ActionLog || cr.Target != model.ActionDeny {
		t.Errorf("defaults = %s/%s", cr.Action, cr.Target)
	}
	if rs.Rule("post-shadow").Target != model.ActionWarn {
		t.Errorf("PostToolUse default target = %s", rs.Rule("post-shadow").Target)
	}
	v, _ := e.Evaluate(rs, editScn("/repo/a.go").input(t))
	if v.Action != model.ActionLog || v.Reason != "" || len(v.Hits) != 1 || v.Hits[0].Label() != "would_deny" {
		t.Errorf("verdict = %+v", v)
	}
}

func TestEvaluateSkipsInternalCompactAgent(t *testing.T) {
	e := newEngine(t)
	rs := compileRules(t, e, rule("sub-stop", "SubagentStop", "deny", "", `{"agent_type":"*"}`, "x"))
	compact := scenario{native: "SubagentStop", payload: `{"agent_id":"a","agent_type":"","last_assistant_message":"x"}`}
	v, _ := e.Evaluate(rs, compact.input(t))
	if len(v.Results) != 0 || v.Action != model.ActionNone {
		t.Errorf("compact agent verdict = %+v", v)
	}
	normal := scenario{native: "SubagentStop", payload: `{"agent_id":"a","agent_type":"general","last_assistant_message":"x"}`}
	v, _ = e.Evaluate(rs, normal.input(t))
	if len(v.Results) != 1 || v.Action != model.ActionDeny {
		t.Errorf("normal subagent verdict = %+v", v)
	}
}

func TestEvaluateNilAndForeignPrograms(t *testing.T) {
	e := newEngine(t)
	if v, err := e.Evaluate(nil, nil); err != nil || v == nil {
		t.Errorf("nil inputs: %v %v", v, err)
	}
	rs := compileRules(t, e, rule("some-rule", "PreToolUse", "deny", "", `{"tool":"Edit"}`, "m"))
	if v, err := e.Evaluate(rs, &model.EvalInput{}); err != nil || len(v.Results) != 0 {
		t.Errorf("no event: %v %v", v, err)
	}
	// A rule set without programs (e.g. decoded from a cache) still evaluates.
	rs.Rules[0].Program = nil
	v, err := e.Evaluate(rs, editScn("/repo/a").input(t))
	if err != nil || v.Action != model.ActionDeny {
		t.Errorf("program-less evaluation: %+v %v", v, err)
	}
	// Hand-built CompiledRules without Action/Target get the defaults.
	rs.Rules[0].Action, rs.Rules[0].Target = "", ""
	rs.Rules[0].Rule.Action = model.ActionWarn
	v, _ = e.Evaluate(rs, editScn("/repo/a").input(t))
	if v.Action != model.ActionWarn || v.Hits[0].Target != model.ActionDeny {
		t.Errorf("defaults: %+v", v.Hits)
	}
}

func TestCompile(t *testing.T) {
	e := newEngine(t)
	disabled := false
	r := decodeRule(t, rule("off-rule", "PreToolUse", "deny", "", `{"tool":"Edit"}`, "m"))
	r.Enabled = &disabled
	rs, err := e.Compile([]model.LoadedRule{{Rule: r}}, model.Settings{}, "g")
	if err != nil || len(rs.Rules) != 0 {
		t.Errorf("disabled rule: %v %+v", err, rs.Rules)
	}
	anchor := 3
	rs, err = e.Compile(nil, model.Settings{Inject: &model.InjectSettings{AnchorEvery: &anchor}}, "abc")
	if err != nil || rs.Generation != "abc" || *rs.Settings.Inject.AnchorEvery != 3 {
		t.Errorf("settings/generation not passed through: %+v %v", rs, err)
	}
	for _, bad := range []string{
		`{"id":"bad-event","event":"SessionStart","examples":[]}`,
		`{"id":"bad-act","event":"Stop","action":"nuke","examples":[]}`,
		`{"id":"bad-tgt","event":"Stop","target":"nuke","examples":[]}`,
	} {
		if _, err := e.Compile([]model.LoadedRule{{Rule: decodeRule(t, bad)}}, model.Settings{}, "g"); err == nil {
			t.Errorf("Compile accepted %s", bad)
		}
	}
	cr, err := e.Compile([]model.LoadedRule{{Rule: decodeRule(t, rule("prog-rule", "Stop", "log", "", "", "")), Layer: model.LayerOrg, Source: "x", Index: 4}}, model.Settings{}, "g")
	if err != nil || cr.Rules[0].Program == nil || cr.Rules[0].Program.RuleID() != "prog-rule" || cr.Rules[0].Layer != model.LayerOrg || cr.Rules[0].Index != 4 {
		t.Errorf("compiled rule: %+v %v", cr, err)
	}
}

func TestEvaluateRecoversFromPanics(t *testing.T) {
	e := newEngine(t)
	// A typed-nil session panics inside the predicate; the rule must become unknown.
	rs := compileRules(t, e, `{"id":"panic-rule","event":"PreToolUse","match":{"tool":"Edit"},"when":[{"read_this_session":{}}],"examples":[]}`)
	in := editScn("/repo/a").input(t)
	var nilSess *fixSession
	in.Session = nilSess
	v, err := e.Evaluate(rs, in)
	if err != nil || len(v.Results) != 1 || v.Results[0].Outcome != U || v.Results[0].UnknownCause != "internal:panic" {
		t.Errorf("verdict = %+v err=%v", v, err)
	}
}
