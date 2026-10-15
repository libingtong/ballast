package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// Persisted-text limits (internal/state caps; the hook clips first).
const (
	maxPromptTailRunes    = 200
	maxAssistantTextRunes = 4000
)

func scopeFor(ev *model.Event, repo *model.RepoInfo, normCwd string) model.ScopeKey {
	inst := ""
	if repo != nil {
		inst = repo.Inst
	}
	if inst == "" {
		sum := sha256.Sum256([]byte(normCwd))
		inst = "norepo-" + hex.EncodeToString(sum[:])[:16]
	}
	return model.ScopeKey{RepoInst: inst, Session: ev.SessionID, Agent: ev.AgentKey()}
}

func buildDelta(ev *model.Event, rs *model.CompiledRuleSet, v *model.Verdict, inj *model.Injection, readPaths []string, now time.Time) *model.StateDelta {
	d := &model.StateDelta{Now: now}
	if ev == nil {
		return d
	}
	if ev.Tool != nil && ev.Tool.UseID != "" {
		d.DedupeKey = string(ev.Name) + ":" + ev.Tool.UseID
	}
	if ev.Name != model.EventAgentResponse && ev.Name != model.EventSessionEnd {
		d.Summary = &model.EventSummary{TS: now, Event: ev.Name, Text: summaryText(ev.Tool)}
		if ev.Tool != nil {
			d.Summary.Tool = ev.Tool.Name
		}
	}
	d.ToolCall = ev.Name == model.EventPreToolUse
	if ev.Name == model.EventUserPromptSubmit {
		d.Prompt = true
		d.PromptTail = tailRunes(strings.TrimSpace(ev.Prompt), maxPromptTailRunes)
	}
	d.ToolFailure = ev.Name == model.EventPostToolUseFailure
	if len(readPaths) > 0 {
		d.ReadPaths = append([]string(nil), readPaths...)
	}
	if ev.Name == model.EventAgentResponse {
		// Keep the tail: Stop-family text rules usually window the end of
		// the message (tail_chars), and the store keeps the tail too.
		d.LastAssistantText = tailRunes(ev.LastAssistantMessage, maxAssistantTextRunes)
	}
	if ev.Name.IsStopFamily() || ev.Name == model.EventSessionEnd {
		d.FlushEvals = true
	}

	if v != nil {
		d.HitRules, d.Evals = verdictCounts(v)
		if d.Summary != nil && len(d.HitRules) > 0 {
			d.Summary.Hits = append([]string(nil), d.HitRules...)
		}
	}
	if inj != nil {
		d.AnchorEmitted = inj.AnchorEmitted
		d.FullEmitted = inj.FullEmitted
		if len(inj.ReportedJobs) > 0 {
			d.ReportedJobs = append([]string(nil), inj.ReportedJobs...)
			d.JobReportAt = now
		}
		// The generation is recorded together with the digest only: an event
		// that could not carry a generation summary must not consume it.
		if inj.RuleDigest != nil && rs != nil {
			d.Generation = rs.Generation
			d.RuleDigest = make(map[string]model.Action, len(inj.RuleDigest))
			for k, a := range inj.RuleDigest {
				d.RuleDigest[k] = a
			}
		}
	}
	return d
}

// verdictCounts derives the prior-hit rule ids and per-rule eval counters.
func verdictCounts(v *model.Verdict) ([]string, map[string]model.EvalCount) {
	var hitRules []string
	effective := make(map[string]model.Action, len(v.Hits))
	for _, h := range v.Hits {
		if _, dup := effective[h.RuleID]; !dup {
			hitRules = append(hitRules, h.RuleID)
		}
		effective[h.RuleID] = model.Stricter(effective[h.RuleID], h.Effective)
	}
	if len(v.Results) == 0 {
		return hitRules, nil
	}
	evals := make(map[string]model.EvalCount, len(v.Results))
	for _, r := range v.Results {
		c := evals[r.RuleID]
		c.Evals++
		if r.Outcome == model.TriUnknown {
			c.Unknown++
		}
		if a, ok := effective[r.RuleID]; ok && r.Outcome == model.TriTrue {
			if a.Severity() >= model.ActionWarn.Severity() {
				c.Hits++
			} else {
				c.Would++
			}
		}
		evals[r.RuleID] = c
	}
	return hitRules, evals
}

func cloneEvals(m map[string]model.EvalCount) map[string]model.EvalCount {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]model.EvalCount, len(m))
	for k, c := range m {
		out[k] = c
	}
	return out
}

// summaryText is the redacted one-line ring entry of a tool call, e.g.
// "Bash: git status" or "Edit: src/a.go".
func summaryText(t *model.ToolCall) string {
	if t == nil {
		return ""
	}
	var s string
	switch {
	case t.Command != "":
		first, _, _ := strings.Cut(strings.TrimSpace(t.Command), "\n")
		s = t.Name + ": " + first
	case len(t.Targets) > 0:
		s = t.Name + ": " + t.Targets[0]
	default:
		s = t.Name
	}
	return clipRunes(redact(s), model.MaxEventSummaryRunes)
}

// clipRunes truncates s to n runes, appending "…" when cut.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// tailRunes keeps the last n runes of s.
func tailRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[len(r)-n:])
}

// emptyState stands in when a StateStore violates "never returns nil".
type emptyState struct{}

func (emptyState) Seen(string) bool                     { return false }
func (emptyState) HasRead(string) bool                  { return false }
func (emptyState) LastReadAt(string) (time.Time, bool)  { return time.Time{}, false }
func (emptyState) StartedAt() (time.Time, bool)         { return time.Time{}, false }
func (emptyState) ToolCount() int                       { return 0 }
func (emptyState) ToolCountSinceInject() int            { return 0 }
func (emptyState) PromptCount() int                     { return 0 }
func (emptyState) PrevPromptTail() string               { return "" }
func (emptyState) PriorHits(string) int                 { return 0 }
func (emptyState) Generation() string                   { return "" }
func (emptyState) RuleDigest() map[string]model.Action  { return nil }
func (emptyState) FullInjected() bool                   { return false }
func (emptyState) ReportedJob(string) bool              { return false }
func (emptyState) LastJobReportAt() time.Time           { return time.Time{} }
func (emptyState) LastAssistantText() string            { return "" }
func (emptyState) LastToolFailureAt() (time.Time, bool) { return time.Time{}, false }
func (emptyState) Recent(int) []model.EventSummary      { return nil }
func (emptyState) Evals() map[string]model.EvalCount    { return nil }
func (emptyState) EventSeq() int64                      { return 0 }
