package inject

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// cutRunes shortens s to at most n runes, ending in "…" when it was cut.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if runeLen(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// headRunes returns the first n runes of s.
func headRunes(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// layout describes a block of items: prefix + items joined by sep, plus an
// optional final "…(+N <what> omitted)" line when items are dropped.
type layout struct {
	prefix string
	sep    string
	what   string
}

func (ly layout) omitLine(n int) string { return fmt.Sprintf("…(+%d %s omitted)", n, ly.what) }

// fit renders items within budget runes. Whole items are dropped from the
// end; a single over-long first item is cut at a rune boundary. It returns
// the text and how many items are fully or partly present (0 for no text).
func (ly layout) fit(items []string, budget int) (string, int) {
	s, n, _ := ly.fitCut(items, budget)
	return s, n
}

// fitCut is fit that also reports whether the first item had to be cut.
func (ly layout) fitCut(items []string, budget int) (string, int, bool) {
	if len(items) == 0 || budget <= 0 {
		return "", 0, false
	}
	build := func(k int) string {
		s := ly.prefix + strings.Join(items[:k], ly.sep)
		if k < len(items) {
			s += "\n" + ly.omitLine(len(items)-k)
		}
		return s
	}
	if s := build(len(items)); runeLen(s) <= budget {
		return s, len(items), false
	}
	for k := len(items) - 1; k >= 1; k-- {
		if s := build(k); runeLen(s) <= budget {
			return s, k, false
		}
	}
	// Not even the first item plus the omitted line fits. A line is cut only
	// when it alone exceeds the budget; otherwise keep it whole and leave the
	// rest to the caller (the omitted line is what no longer fits).
	if first := ly.prefix + items[0]; runeLen(first) <= budget {
		return first, 1, false
	}
	return cutRunes(ly.prefix+items[0], budget), 1, true
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func injectText(r *model.Rule) string {
	if r.Inject == nil {
		return ""
	}
	return strings.TrimSpace(r.Inject.Text)
}

// anchorOf is the one-line anchor of r: Inject.Anchor, or the first line of
// the text, in both cases at most MaxAnchorRunes.
func anchorOf(r *model.Rule) string {
	a := firstLine(r.Inject.Anchor)
	if a == "" {
		a = firstLine(r.Inject.Text)
	}
	return cutRunes(a, model.MaxAnchorRunes)
}

func fullText(rules []model.CompiledRule, generation string, budget int) string {
	var items []string
	for i := range rules {
		r := &rules[i].Rule
		if t := injectText(r); t != "" {
			items = append(items, "- ["+r.ID+"] "+t)
		}
	}
	ly := layout{prefix: "Ballast rules (generation " + generation + "):\n", sep: "\n", what: "rules"}
	s, _ := ly.fit(items, budget)
	return s
}

func anchorText(rules []model.CompiledRule, budget int) string {
	var items []string
	for i := range rules {
		r := &rules[i].Rule
		if injectText(r) == "" {
			continue
		}
		items = append(items, "["+r.ID+"] "+anchorOf(r))
	}
	ly := layout{prefix: "Ballast: ", sep: "; ", what: "rules"}
	s, _ := ly.fit(items, budget)
	return s
}

func genSummary(prev map[string]model.Action, rs *model.CompiledRuleSet, budget int) string {
	var items []string
	cur := make(map[string]bool, len(rs.Rules))
	for i := range rs.Rules {
		id, act := rs.Rules[i].Rule.ID, rs.Rules[i].Action
		cur[id] = true
		old, had := prev[id]
		switch {
		case !had:
			items = append(items, fmt.Sprintf("+ %s: added (%s)", id, act))
		case old != act:
			verb := "promoted"
			if act.Severity() < old.Severity() {
				verb = "demoted"
			}
			items = append(items, fmt.Sprintf("~ %s: %s → %s (%s)", id, old, act, verb))
		}
	}
	var gone []string
	for id := range prev {
		if !cur[id] {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		items = append(items, fmt.Sprintf("- %s: removed", id))
	}
	ly := layout{prefix: "Ballast rules changed (generation " + rs.Generation + "):\n", sep: "\n", what: "changes"}
	s, _ := ly.fit(items, budget)
	return s
}

// jobResults renders recs within budget and returns how many leading
// records are present in full. A first line cut for lack of room counts as
// not reported, so it is shown whole later; one that cannot fit even the
// full job budget counts as reported to avoid repeating it forever.
func jobResults(recs []model.VerifyRecord, budget int) (string, int) {
	items := make([]string, len(recs))
	for i := range recs {
		r := &recs[i]
		s := fmt.Sprintf("verify %s: %s (job=%s", r.Name, r.Outcome, r.JobID)
		if r.SEnd != "" {
			s += ", S=" + headRunes(string(r.SEnd), 12)
		}
		items[i] = s + ")"
	}
	ly := layout{sep: "\n", what: "results"}
	t, n, cut := ly.fitCut(items, budget)
	if cut && budget < BudgetJobResults {
		n = 0
	}
	return t, n
}

func noticesText(notices []model.ConfigNotice, budget int) string {
	var items []string
	for _, n := range notices {
		if n.Kind != model.NoticeConfigInvalid && n.Kind != model.NoticeRuleDropped {
			continue
		}
		items = append(items, fmt.Sprintf("- %s (%s): %s", n.Kind, n.Layer, firstLine(n.Message)))
	}
	ly := layout{prefix: "Ballast config notices:\n", sep: "\n", what: "notices"}
	s, _ := ly.fit(items, budget)
	return s
}

// emptyState is the SessionState of a request without one.
type emptyState struct{}

func (emptyState) Seen(string) bool                          { return false }
func (emptyState) HasRead(string) bool                       { return false }
func (emptyState) LastReadAt(string) (t time.Time, ok bool)  { return }
func (emptyState) StartedAt() (t time.Time, ok bool)         { return }
func (emptyState) ToolCount() int                            { return 0 }
func (emptyState) ToolCountSinceInject() int                 { return 0 }
func (emptyState) PromptCount() int                          { return 0 }
func (emptyState) PrevPromptTail() string                    { return "" }
func (emptyState) PriorHits(string) int                      { return 0 }
func (emptyState) Generation() string                        { return "" }
func (emptyState) RuleDigest() map[string]model.Action       { return nil }
func (emptyState) FullInjected() bool                        { return false }
func (emptyState) ReportedJob(string) bool                   { return false }
func (emptyState) LastJobReportAt() (t time.Time)            { return }
func (emptyState) LastAssistantText() string                 { return "" }
func (emptyState) LastToolFailureAt() (t time.Time, ok bool) { return }
func (emptyState) Recent(int) []model.EventSummary           { return nil }
func (emptyState) Evals() map[string]model.EvalCount         { return nil }
func (emptyState) EventSeq() int64                           { return 0 }
