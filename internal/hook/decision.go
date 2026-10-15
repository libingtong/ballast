package hook

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// applyCapabilities re-checks every hit against the client's capability
// matrix (docs/dsl-semantics.md §10.1): an unsupported (event, action) is
// degraded to log and reported as diag capability_degraded. When nothing was
// degraded the evaluator's verdict (Action, Reason) is used unchanged;
// otherwise both are recomputed from the adjusted hits.
func applyCapabilities(ev *model.Event, caps model.CapabilitySet, v *model.Verdict) (*model.Verdict, []diag) {
	out := *v
	var diags []diag
	supported := func(a model.Action) bool {
		if a.Severity() < model.ActionWarn.Severity() {
			return true
		}
		_, ok := caps.Lookup(ev.Name, a)
		return ok
	}

	changed := false
	if len(v.Hits) > 0 {
		out.Hits = make([]model.RuleHit, len(v.Hits))
		for i, h := range v.Hits {
			if h.Effective == "" {
				h.Effective = h.Action
			}
			if !supported(h.Action) || !supported(h.Effective) {
				if h.Effective != model.ActionLog {
					changed = true
				}
				h.Effective = model.ActionLog
				diags = append(diags, diag{name: "capability_degraded", fields: map[string]any{
					"rule_id": h.RuleID, "action": string(h.Action), "event": string(ev.Name), "client": string(ev.Client),
				}})
			}
			out.Hits[i] = h
		}
	}
	if changed {
		out.Action = model.ActionNone
		for _, h := range out.Hits {
			out.Action = model.Stricter(out.Action, h.Effective)
		}
		out.Reason = buildReason(out.Hits, out.Action)
	}
	if !supported(out.Action) {
		diags = append(diags, diag{name: "capability_degraded", fields: map[string]any{
			"action": string(out.Action), "event": string(ev.Name), "client": string(ev.Client),
		}})
		out.Action, out.Reason = model.ActionLog, ""
	}
	return &out, diags
}

// buildReason concatenates "[id] message" of the hits whose effective action
// equals the decision action (warn/ask/deny only) within BudgetReason runes.
func buildReason(hits []model.RuleHit, action model.Action) string {
	if action.Severity() < model.ActionWarn.Severity() {
		return ""
	}
	var parts []string
	for _, h := range hits {
		if h.Effective == action {
			parts = append(parts, "["+h.RuleID+"] "+h.Message)
		}
	}
	var b strings.Builder
	used := 0
	for i, p := range parts {
		n := utf8.RuneCountInString(p)
		if i > 0 {
			n++
		}
		if used+n > model.BudgetReason {
			fmt.Fprintf(&b, "…(+%d)", len(parts)-i)
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p)
		used += n
	}
	return b.String()
}

// causeVerifyRunning is the unknown cause of a verify_fresh predicate whose
// verify job is still running (plan B3).
const causeVerifyRunning = "verify_fresh:job_running"

// verifyRunningHint returns the warn-level notice for rules that evaluated to
// unknown only because a verify job is still running, or "" when there is
// none. Only rules configured at warn or stricter speak up (a log-tier rule
// stays silent, shadow mode never talks to the agent), and nothing is said
// when the client cannot carry a warn on this event.
func verifyRunningHint(rs *model.CompiledRuleSet, caps model.CapabilitySet, ev *model.Event, v *model.Verdict) string {
	if rs == nil || v == nil {
		return ""
	}
	if _, ok := caps.Lookup(ev.Name, model.ActionWarn); !ok {
		return ""
	}
	var parts []string
	for _, r := range v.Results {
		if r.Outcome != model.TriUnknown || r.UnknownCause != causeVerifyRunning {
			continue
		}
		if cr := rs.Rule(r.RuleID); cr == nil || cr.Action.Severity() < model.ActionWarn.Severity() {
			continue
		}
		parts = append(parts, "["+r.RuleID+"] verify 进行中,本次按 unknown 放行(未命中);结果会在下一次注入中回报。")
	}
	return strings.Join(parts, "\n")
}
