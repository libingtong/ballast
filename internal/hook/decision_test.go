package hook

import (
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func hit(id string, action, eff model.Action, msg string) model.RuleHit {
	return model.RuleHit{RuleID: id, Layer: model.LayerUser, Action: action, Target: action, Effective: eff, Message: msg}
}

func TestApplyCapabilities(t *testing.T) {
	codexPre := &model.Event{Client: model.ClientCodex, Name: model.EventPreToolUse}
	codexStop := &model.Event{Client: model.ClientCodex, Name: model.EventStop}
	claudePre := &model.Event{Client: model.ClientClaude, Name: model.EventPreToolUse}
	cursorPost := &model.Event{Client: model.ClientCursor, Name: model.EventPostToolUse}
	hermesPost := &model.Event{Client: model.ClientHermes, Name: model.EventPostToolUse}

	cases := []struct {
		name       string
		ev         *model.Event
		v          *model.Verdict
		wantAction model.Action
		wantReason string
		wantEff    []model.Action
		wantDiags  []string // rule ids ("" for the decision-level diag)
	}{
		{
			name:       "supported hits pass through untouched",
			ev:         claudePre,
			v:          &model.Verdict{Action: model.ActionDeny, Reason: "[a] x", Hits: []model.RuleHit{hit("a", model.ActionDeny, model.ActionDeny, "x")}},
			wantAction: model.ActionDeny, wantReason: "[a] x", wantEff: []model.Action{model.ActionDeny},
		},
		{
			name:       "evaluator already degraded the hit: still reported",
			ev:         codexPre,
			v:          &model.Verdict{Action: model.ActionLog, Hits: []model.RuleHit{hit("a", model.ActionAsk, model.ActionLog, "x")}},
			wantAction: model.ActionLog, wantEff: []model.Action{model.ActionLog}, wantDiags: []string{"a"},
		},
		{
			name:       "ask on codex is degraded",
			ev:         codexPre,
			v:          &model.Verdict{Action: model.ActionAsk, Reason: "[a] x", Hits: []model.RuleHit{hit("a", model.ActionAsk, model.ActionAsk, "x")}},
			wantAction: model.ActionLog, wantReason: "", wantEff: []model.Action{model.ActionLog}, wantDiags: []string{"a"},
		},
		{
			name: "next supported tier takes over after a degradation",
			ev:   codexPre,
			v: &model.Verdict{Action: model.ActionAsk, Reason: "[a] ask msg", Hits: []model.RuleHit{
				hit("a", model.ActionAsk, model.ActionAsk, "ask msg"),
				hit("b", model.ActionDeny, model.ActionDeny, "deny msg"),
			}},
			// deny is supported on codex PreToolUse; ask is not. The evaluator
			// (wrongly) put ask first, the hook recomputes.
			wantAction: model.ActionDeny, wantReason: "[b] deny msg", wantEff: []model.Action{model.ActionLog, model.ActionDeny}, wantDiags: []string{"a"},
		},
		{
			name: "warn survives when the stricter hit is degraded",
			ev:   cursorPost,
			v: &model.Verdict{Action: model.ActionDeny, Reason: "[a] no", Hits: []model.RuleHit{
				hit("a", model.ActionDeny, model.ActionDeny, "no"),
				hit("w", model.ActionWarn, model.ActionWarn, "careful"),
			}},
			wantAction: model.ActionWarn, wantReason: "[w] careful", wantEff: []model.Action{model.ActionLog, model.ActionWarn}, wantDiags: []string{"a"},
		},
		{
			name:       "warn on codex PreToolUse is degraded",
			ev:         codexPre,
			v:          &model.Verdict{Action: model.ActionWarn, Reason: "[w] x", Hits: []model.RuleHit{hit("w", model.ActionWarn, model.ActionWarn, "x")}},
			wantAction: model.ActionLog, wantEff: []model.Action{model.ActionLog}, wantDiags: []string{"w"},
		},
		{
			name:       "warn on hermes PostToolUse is degraded (log only)",
			ev:         hermesPost,
			v:          &model.Verdict{Action: model.ActionWarn, Reason: "[w] x", Hits: []model.RuleHit{hit("w", model.ActionWarn, model.ActionWarn, "x")}},
			wantAction: model.ActionLog, wantEff: []model.Action{model.ActionLog}, wantDiags: []string{"w"},
		},
		{
			name:       "deny on codex Stop is supported",
			ev:         codexStop,
			v:          &model.Verdict{Action: model.ActionDeny, Reason: "[s] x", Hits: []model.RuleHit{hit("s", model.ActionDeny, model.ActionDeny, "x")}},
			wantAction: model.ActionDeny, wantReason: "[s] x", wantEff: []model.Action{model.ActionDeny},
		},
		{
			name:       "shadow hit (log with a deny target) is not a degradation",
			ev:         codexPre,
			v:          &model.Verdict{Action: model.ActionLog, Hits: []model.RuleHit{{RuleID: "sh", Action: model.ActionLog, Target: model.ActionDeny, Effective: model.ActionLog}}},
			wantAction: model.ActionLog, wantEff: []model.Action{model.ActionLog},
		},
		{
			name:       "missing Effective defaults to Action",
			ev:         claudePre,
			v:          &model.Verdict{Action: model.ActionAsk, Hits: []model.RuleHit{{RuleID: "a", Action: model.ActionAsk}}},
			wantAction: model.ActionAsk, wantEff: []model.Action{model.ActionAsk},
		},
		{
			name:       "decision-level action without hits is checked too",
			ev:         codexPre,
			v:          &model.Verdict{Action: model.ActionAsk, Reason: "r"},
			wantAction: model.ActionLog, wantDiags: []string{""},
		},
		{
			name:       "empty verdict",
			ev:         claudePre,
			v:          &model.Verdict{},
			wantAction: model.ActionNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caps := model.Capabilities(c.ev.Client)
			orig := *c.v
			origHits := append([]model.RuleHit(nil), c.v.Hits...)
			got, diags := applyCapabilities(c.ev, caps, c.v)
			if got.Action != c.wantAction || got.Reason != c.wantReason {
				t.Errorf("action=%q reason=%q, want %q / %q", got.Action, got.Reason, c.wantAction, c.wantReason)
			}
			var eff []model.Action
			for _, h := range got.Hits {
				eff = append(eff, h.Effective)
			}
			if !reflect.DeepEqual(eff, c.wantEff) {
				t.Errorf("effective = %v, want %v", eff, c.wantEff)
			}
			var ids []string
			for _, d := range diags {
				if d.name != "capability_degraded" {
					t.Errorf("diag %q", d.name)
				}
				id, _ := d.fields["rule_id"].(string)
				ids = append(ids, id)
			}
			if !reflect.DeepEqual(ids, c.wantDiags) {
				t.Errorf("degraded rule ids = %v, want %v", ids, c.wantDiags)
			}
			if !reflect.DeepEqual(*c.v, orig) || !reflect.DeepEqual(c.v.Hits, origHits) {
				t.Error("the evaluator's verdict must not be mutated")
			}
		})
	}
}

func TestBuildReason(t *testing.T) {
	h := []model.RuleHit{
		hit("a", model.ActionDeny, model.ActionDeny, "first"),
		hit("b", model.ActionWarn, model.ActionWarn, "ignored: weaker"),
		hit("c", model.ActionDeny, model.ActionDeny, "second"),
	}
	if got := buildReason(h, model.ActionDeny); got != "[a] first\n[c] second" {
		t.Errorf("reason = %q", got)
	}
	if got := buildReason(h, model.ActionLog); got != "" {
		t.Errorf("log tier has no reason: %q", got)
	}
	if got := buildReason(nil, model.ActionDeny); got != "" {
		t.Errorf("reason = %q", got)
	}
	// Budget: whole entries only, then …(+N).
	long := strings.Repeat("x", 290)
	var many []model.RuleHit
	for i := 0; i < 12; i++ {
		many = append(many, hit(string(rune('a'+i)), model.ActionDeny, model.ActionDeny, long))
	}
	got := buildReason(many, model.ActionDeny)
	if n := len([]rune(got)); n > model.BudgetReason+10 {
		t.Errorf("reason has %d runes", n)
	}
	if !strings.HasSuffix(got, "…(+5)") && !strings.Contains(got, "…(+") {
		t.Errorf("reason lacks the truncation marker: …%s", got[len(got)-20:])
	}
}

// TestDegradationEndToEnd checks the audit/state/stdout effects of a degraded hit.
func TestDegradationEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = &model.Verdict{
		Action: model.ActionAsk, Reason: "[r] ask me",
		Hits:    []model.RuleHit{hit("r", model.ActionAsk, model.ActionAsk, "ask me")},
		Results: []model.RuleResult{{RuleID: "r", Outcome: model.TriTrue}},
	}
	mustExit0(t, h, h.main(model.ClientCodex, "PreToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Bash","tool_use_id":"t","tool_input":{"command":"ls"}}`))
	if h.stdout.Len() != 0 {
		t.Errorf("a degraded ask must not render: %q", h.stdout.String())
	}
	recs := h.audit.all()
	if len(recs) != 2 || recs[0].Type != model.AuditHit || recs[0].Decision != "would_ask" || recs[0].Action != model.ActionAsk || recs[1].Type != model.AuditDiag {
		t.Fatalf("audit = %+v", recs)
	}
	if !strings.Contains(string(recs[1].Detail), `"diag":"capability_degraded"`) || !strings.Contains(string(recs[1].Detail), `"rule_id":"r"`) {
		t.Errorf("detail = %s", recs[1].Detail)
	}
	d := h.state.lastCommit(t).delta
	if d.Evals["r"] != (model.EvalCount{Evals: 1, Would: 1}) || !reflect.DeepEqual(d.HitRules, []string{"r"}) {
		t.Errorf("a degraded hit counts as would: %+v", d.Evals)
	}
}
