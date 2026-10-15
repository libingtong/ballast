package dsl

import (
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// effectiveAction applies the runtime capability check (dsl-semantics
// §10.1): the configured action when client c supports (event, action),
// otherwise ActionLog.
func effectiveAction(c model.ClientID, ev model.EventName, a model.Action) model.Action {
	if a == model.ActionNone || a == model.ActionLog {
		return model.ActionLog
	}
	if _, ok := model.Capabilities(c).Lookup(ev, a); ok {
		return a
	}
	return model.ActionLog
}

// Arbitrate combines per-rule hits into a Verdict for client c (exported
// for replay, which re-arbitrates recorded hits). Effective is recomputed
// from each hit's Action against c's capability matrix; the caller's slices
// are not modified.
func Arbitrate(c model.ClientID, ev model.EventName, results []model.RuleResult, hits []model.RuleHit) *model.Verdict {
	v := &model.Verdict{Results: results}
	if len(hits) == 0 {
		return v
	}
	v.Hits = make([]model.RuleHit, len(hits))
	copy(v.Hits, hits)
	for i := range v.Hits {
		h := &v.Hits[i]
		h.Effective = effectiveAction(c, ev, h.Action)
		v.Action = model.Stricter(v.Action, h.Effective)
	}
	if v.Action.Severity() >= model.ActionWarn.Severity() {
		var lines []string
		for i := range v.Hits {
			h := &v.Hits[i]
			if h.Effective == v.Action {
				lines = append(lines, reasonLine(h))
			}
		}
		v.Reason = joinReason(lines, model.BudgetReason)
	}
	return v
}

func reasonLine(h *model.RuleHit) string {
	if h.Message == "" {
		return "[" + h.RuleID + "]"
	}
	return "[" + h.RuleID + "] " + h.Message
}

// joinReason joins lines with "\n" within budget runes; when they do not
// fit, trailing lines are replaced by a "…(+N)" marker where N is the number
// of omitted lines.
func joinReason(lines []string, budget int) string {
	full := strings.Join(lines, "\n")
	if RuneLen(full) <= budget {
		return full
	}
	for keep := len(lines) - 1; keep >= 0; keep-- {
		marker := fmt.Sprintf("…(+%d)", len(lines)-keep)
		head := strings.Join(lines[:keep], "\n")
		if keep > 0 {
			head += "\n"
		}
		if RuneLen(head)+RuneLen(marker) <= budget {
			if keep == 0 {
				// Not even one whole line fits: keep a prefix of the first.
				room := budget - RuneLen(marker)
				if room > 0 {
					return truncateRunes(lines[0], room) + marker
				}
			}
			return head + marker
		}
	}
	return truncateRunes(full, budget)
}
