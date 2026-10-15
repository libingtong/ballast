package dsl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func hit(id string, a model.Action, msg string) model.RuleHit {
	return model.RuleHit{RuleID: id, Layer: model.LayerRepo, Action: a, Target: a, Message: msg}
}

func TestArbitrate(t *testing.T) {
	pre := model.EventPreToolUse
	cases := []struct {
		name   string
		client model.ClientID
		event  model.EventName
		hits   []model.RuleHit
		action model.Action
		reason string
		eff    []model.Action
	}{
		{"no hits", model.ClientClaude, pre, nil, model.ActionNone, "", nil},
		{"strictest wins and only it explains", model.ClientClaude, pre,
			[]model.RuleHit{hit("aaa", model.ActionWarn, "w"), hit("bbb", model.ActionDeny, "d"), hit("ccc", model.ActionLog, "l")},
			model.ActionDeny, "[bbb] d", []model.Action{model.ActionWarn, model.ActionDeny, model.ActionLog}},
		{"same tier keeps rule order", model.ClientClaude, pre,
			[]model.RuleHit{hit("aaa", model.ActionWarn, "one"), hit("bbb", model.ActionWarn, "two")},
			model.ActionWarn, "[aaa] one\n[bbb] two", []model.Action{model.ActionWarn, model.ActionWarn}},
		{"ask outranks warn", model.ClientClaude, pre,
			[]model.RuleHit{hit("aaa", model.ActionAsk, "a"), hit("bbb", model.ActionWarn, "w")},
			model.ActionAsk, "[aaa] a", []model.Action{model.ActionAsk, model.ActionWarn}},
		{"only log hits give no reason", model.ClientClaude, pre,
			[]model.RuleHit{hit("aaa", model.ActionLog, "l")}, model.ActionLog, "", []model.Action{model.ActionLog}},
		{"unsupported ask degrades on codex", model.ClientCodex, pre,
			[]model.RuleHit{hit("aaa", model.ActionAsk, "a")}, model.ActionLog, "", []model.Action{model.ActionLog}},
		{"degraded hit does not raise the tier", model.ClientCodex, pre,
			[]model.RuleHit{hit("aaa", model.ActionAsk, "a"), hit("bbb", model.ActionDeny, "d")},
			model.ActionDeny, "[bbb] d", []model.Action{model.ActionLog, model.ActionDeny}},
		{"deny after the tool ran degrades", model.ClientClaude, model.EventPostToolUse,
			[]model.RuleHit{hit("aaa", model.ActionDeny, "d")}, model.ActionLog, "", []model.Action{model.ActionLog}},
		{"stop warn is supported on claude", model.ClientClaude, model.EventStop,
			[]model.RuleHit{hit("aaa", model.ActionWarn, "w")}, model.ActionWarn, "[aaa] w", []model.Action{model.ActionWarn}},
		{"event missing on the client degrades", model.ClientHermes, model.EventStop,
			[]model.RuleHit{hit("aaa", model.ActionDeny, "d")}, model.ActionLog, "", []model.Action{model.ActionLog}},
		{"unknown client degrades everything", "mystery", pre,
			[]model.RuleHit{hit("aaa", model.ActionDeny, "d")}, model.ActionLog, "", []model.Action{model.ActionLog}},
		{"empty message still names the rule", model.ClientClaude, pre,
			[]model.RuleHit{hit("aaa", model.ActionDeny, "")}, model.ActionDeny, "[aaa]", []model.Action{model.ActionDeny}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := append([]model.RuleHit(nil), c.hits...)
			v := Arbitrate(c.client, c.event, []model.RuleResult{{RuleID: "aaa", Outcome: T}}, c.hits)
			if v.Action != c.action || v.Reason != c.reason {
				t.Errorf("action=%q reason=%q, want %q %q", v.Action, v.Reason, c.action, c.reason)
			}
			for i, h := range v.Hits {
				if h.Effective != c.eff[i] {
					t.Errorf("hit %d effective = %q, want %q", i, h.Effective, c.eff[i])
				}
			}
			for i := range in {
				if c.hits[i] != in[i] {
					t.Errorf("Arbitrate modified the caller's hits")
				}
			}
			if len(v.Results) != 1 {
				t.Errorf("results not passed through: %+v", v.Results)
			}
		})
	}
}

func TestArbitrateReasonBudget(t *testing.T) {
	var hits []model.RuleHit
	for i := 0; i < 12; i++ {
		hits = append(hits, hit(fmt.Sprintf("rule-%02d", i), model.ActionDeny, strings.Repeat("字", 300)))
	}
	v := Arbitrate(model.ClientClaude, model.EventPreToolUse, nil, hits)
	if n := RuneLen(v.Reason); n > model.BudgetReason {
		t.Fatalf("reason has %d runes (budget %d)", n, model.BudgetReason)
	}
	lines := strings.Split(v.Reason, "\n")
	last := lines[len(lines)-1]
	var omitted int
	if _, err := fmt.Sscanf(last, "…(+%d)", &omitted); err != nil {
		t.Fatalf("last line %q is not a truncation marker", last)
	}
	if kept := len(lines) - 1; kept+omitted != 12 || omitted == 0 {
		t.Errorf("kept %d + omitted %d != 12", kept, omitted)
	}
	for i, l := range lines[:len(lines)-1] {
		if !strings.HasPrefix(l, fmt.Sprintf("[rule-%02d] ", i)) || RuneLen(l) != 300+len("[rule-00] ") {
			t.Errorf("line %d was cut or reordered: %.30q", i, l)
		}
	}

	// A single oversized message is cut so that the whole still fits.
	long := Arbitrate(model.ClientClaude, model.EventPreToolUse, nil, []model.RuleHit{hit("aaa", model.ActionDeny, strings.Repeat("x", 5000))})
	if RuneLen(long.Reason) > model.BudgetReason || !strings.HasSuffix(long.Reason, "…(+1)") {
		t.Errorf("oversized message: %d runes, suffix %q", RuneLen(long.Reason), long.Reason[len(long.Reason)-12:])
	}

	// Exactly at the budget nothing is cut.
	fit := Arbitrate(model.ClientClaude, model.EventPreToolUse, nil, []model.RuleHit{hit("aaa", model.ActionDeny, strings.Repeat("x", model.BudgetReason-len("[aaa] ")))})
	if RuneLen(fit.Reason) != model.BudgetReason || strings.Contains(fit.Reason, "…") {
		t.Errorf("exact fit was truncated: %d runes", RuneLen(fit.Reason))
	}
}

func TestEffectiveAction(t *testing.T) {
	if EffectiveAction(model.ClientClaude, model.EventPreToolUse, model.ActionNone) != model.ActionLog {
		t.Error("none ⇒ log")
	}
	if EffectiveAction(model.ClientCursor, model.EventStop, model.ActionDeny) != model.ActionDeny {
		t.Error("cursor stop deny is supported")
	}
	if EffectiveAction(model.ClientCursor, model.EventPreToolUse, model.ActionWarn) != model.ActionLog {
		t.Error("cursor PreToolUse warn is unsupported")
	}
}
