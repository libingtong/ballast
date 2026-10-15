package state

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// Record is the on-disk state (JSON, v=1) and implements model.SessionState.
type Record struct {
	V                int                        `json:"v"`
	StartedAtT       time.Time                  `json:"started_at"`
	Seq              int64                      `json:"event_seq"`
	SeenKeys         []string                   `json:"seen"`
	ReadAt           map[string]time.Time       `json:"read"`
	Tools            int                        `json:"tool_count"`
	ToolsSinceInject int                        `json:"tool_count_since_inject"`
	Prompts          int                        `json:"prompt_count"`
	PromptTail       string                     `json:"prev_prompt_tail,omitempty"`
	Hits             map[string]int             `json:"prior_hits,omitempty"`
	Gen              string                     `json:"generation,omitempty"`
	Digest           map[string]model.Action    `json:"rule_digest,omitempty"`
	FullDone         bool                       `json:"full_injected,omitempty"`
	Reported         []string                   `json:"reported_jobs,omitempty"`
	JobReportAt      time.Time                  `json:"last_job_report_at"`
	AssistantText    string                     `json:"last_assistant_text,omitempty"`
	ToolFailureAt    time.Time                  `json:"last_tool_failure_at"`
	Ring             []model.EventSummary       `json:"ring,omitempty"`
	EvalCounts       map[string]model.EvalCount `json:"evals,omitempty"`
}

var _ model.SessionState = (*Record)(nil)

// NewRecord returns an empty v1 record.
func NewRecord() *Record { return &Record{V: RecordVersion} }

// Merge applies d to r (pure; used under the lock and by tests).
//
// A delta whose DedupeKey was already seen only applies the idempotent
// fields (Generation, RuleDigest, ReportedJobs) and reports duplicate=true;
// applied then says whether those changed r. A fresh delta is always applied
// and consumes one event_seq. d.Now should be set by the caller.
func (r *Record) Merge(d *model.StateDelta) (applied, duplicate bool) {
	if d == nil {
		return false, false
	}
	r.V = RecordVersion
	duplicate = d.DedupeKey != "" && r.Seen(d.DedupeKey)
	changed := r.mergeIdempotent(d)
	if duplicate {
		return changed, true
	}

	r.Seq++
	if r.StartedAtT.IsZero() {
		r.StartedAtT = d.Now
	}
	if d.DedupeKey != "" {
		r.SeenKeys = append(r.SeenKeys, d.DedupeKey)
		if len(r.SeenKeys) > SeenCap {
			r.SeenKeys = slices.Clone(r.SeenKeys[len(r.SeenKeys)-SeenCap:])
		}
	}
	if d.ToolCall {
		r.Tools++
		r.ToolsSinceInject++
	}
	if d.AnchorEmitted {
		r.ToolsSinceInject = 0
	}
	if d.FullEmitted {
		r.FullDone = true
	}
	if d.Prompt {
		r.Prompts++
	}
	if d.PromptTail != "" {
		r.PromptTail = tailRunes(d.PromptTail, MaxPromptTailRune)
	}
	if len(d.ReadPaths) > 0 {
		if r.ReadAt == nil {
			r.ReadAt = make(map[string]time.Time, len(d.ReadPaths))
		}
		for _, p := range d.ReadPaths {
			if p == "" {
				continue
			}
			if t, ok := r.ReadAt[p]; !ok || d.Now.After(t) {
				r.ReadAt[p] = d.Now
			}
		}
		r.evictReads()
	}
	if len(d.HitRules) > 0 {
		if r.Hits == nil {
			r.Hits = make(map[string]int, len(d.HitRules))
		}
		for _, id := range d.HitRules {
			r.Hits[id]++
		}
	}
	for id, c := range d.Evals {
		if r.EvalCounts == nil {
			r.EvalCounts = make(map[string]model.EvalCount, len(d.Evals))
		}
		cur := r.EvalCounts[id]
		cur.Add(c)
		r.EvalCounts[id] = cur
	}
	if d.FlushEvals {
		r.subtractEvals(d.FlushedEvals)
	}
	if !d.JobReportAt.IsZero() {
		r.JobReportAt = d.JobReportAt
	}
	if d.LastAssistantText != "" {
		// The tail is kept: the closing summary is what stop rules inspect.
		r.AssistantText = tailRunes(d.LastAssistantText, MaxAssistantRunes)
	}
	if d.ToolFailure {
		r.ToolFailureAt = d.Now
	}
	if d.Summary != nil {
		sum := *d.Summary
		sum.Seq = r.Seq
		if sum.TS.IsZero() {
			sum.TS = d.Now
		}
		sum.Text = headRunes(sum.Text, model.MaxEventSummaryRunes)
		sum.Hits = slices.Clone(sum.Hits)
		r.Ring = append(r.Ring, sum)
		if len(r.Ring) > RingCap {
			r.Ring = slices.Clone(r.Ring[len(r.Ring)-RingCap:])
		}
	}
	return true, false
}

// mergeIdempotent applies Generation, RuleDigest and ReportedJobs and
// reports whether r changed.
func (r *Record) mergeIdempotent(d *model.StateDelta) (changed bool) {
	if d.Generation != "" && r.Gen != d.Generation {
		r.Gen = d.Generation
		changed = true
	}
	if d.RuleDigest != nil && !maps.Equal(r.Digest, d.RuleDigest) {
		r.Digest = maps.Clone(d.RuleDigest)
		changed = true
	}
	for _, id := range d.ReportedJobs {
		if id == "" || r.ReportedJob(id) {
			continue
		}
		r.Reported = append(r.Reported, id)
		changed = true
	}
	if len(r.Reported) > ReportedJobsCap {
		r.Reported = slices.Clone(r.Reported[len(r.Reported)-ReportedJobsCap:])
	}
	return changed
}

// subtractEvals removes flushed counts, flooring at zero and dropping
// entries that reach all-zero.
func (r *Record) subtractEvals(flushed map[string]model.EvalCount) {
	for id, f := range flushed {
		cur, ok := r.EvalCounts[id]
		if !ok {
			continue
		}
		cur.Evals = max(0, cur.Evals-f.Evals)
		cur.Hits = max(0, cur.Hits-f.Hits)
		cur.Would = max(0, cur.Would-f.Would)
		cur.Unknown = max(0, cur.Unknown-f.Unknown)
		if cur == (model.EvalCount{}) {
			delete(r.EvalCounts, id)
		} else {
			r.EvalCounts[id] = cur
		}
	}
}

// evictReads drops the oldest read entries (ties by path) beyond ReadCap.
func (r *Record) evictReads() {
	over := len(r.ReadAt) - ReadCap
	if over <= 0 {
		return
	}
	keys := make([]string, 0, len(r.ReadAt))
	for k := range r.ReadAt {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ti, tj := r.ReadAt[keys[i]], r.ReadAt[keys[j]]
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:over] {
		delete(r.ReadAt, k)
	}
}

// enforceCaps trims a decoded record that exceeds the frozen caps (a
// hand-edited or foreign file must not grow the hot-path read unboundedly).
func (r *Record) enforceCaps() {
	if len(r.SeenKeys) > SeenCap {
		r.SeenKeys = slices.Clone(r.SeenKeys[len(r.SeenKeys)-SeenCap:])
	}
	if len(r.Ring) > RingCap {
		r.Ring = slices.Clone(r.Ring[len(r.Ring)-RingCap:])
	}
	if len(r.Reported) > ReportedJobsCap {
		r.Reported = slices.Clone(r.Reported[len(r.Reported)-ReportedJobsCap:])
	}
	r.evictReads()
	r.PromptTail = tailRunes(r.PromptTail, MaxPromptTailRune)
	r.AssistantText = tailRunes(r.AssistantText, MaxAssistantRunes)
}

// encode marshals r with non-nil seen/read so the file shape is stable.
func (r *Record) encode() ([]byte, error) {
	c := *r
	if c.SeenKeys == nil {
		c.SeenKeys = []string{}
	}
	if c.ReadAt == nil {
		c.ReadAt = map[string]time.Time{}
	}
	return json.Marshal(&c)
}

// tailRunes returns the last n runes of s.
func tailRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	extra := utf8.RuneCountInString(s) - n
	if extra <= 0 {
		return s
	}
	for i := range s {
		if extra == 0 {
			return s[i:]
		}
		extra--
	}
	return ""
}

// headRunes returns the first n runes of s.
func headRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// Seen implements model.SessionState.
func (r *Record) Seen(k string) bool {
	if k == "" {
		return false
	}
	for _, x := range r.SeenKeys {
		if x == k {
			return true
		}
	}
	return false
}

// HasRead implements model.SessionState.
func (r *Record) HasRead(p string) bool { _, ok := r.ReadAt[p]; return ok }

// LastReadAt implements model.SessionState.
func (r *Record) LastReadAt(p string) (time.Time, bool) { t, ok := r.ReadAt[p]; return t, ok }

// StartedAt implements model.SessionState.
func (r *Record) StartedAt() (time.Time, bool) { return r.StartedAtT, !r.StartedAtT.IsZero() }

// ToolCount implements model.SessionState.
func (r *Record) ToolCount() int { return r.Tools }

// ToolCountSinceInject implements model.SessionState.
func (r *Record) ToolCountSinceInject() int { return r.ToolsSinceInject }

// PromptCount implements model.SessionState.
func (r *Record) PromptCount() int { return r.Prompts }

// PrevPromptTail implements model.SessionState.
func (r *Record) PrevPromptTail() string { return r.PromptTail }

// PriorHits implements model.SessionState.
func (r *Record) PriorHits(id string) int { return r.Hits[id] }

// Generation implements model.SessionState.
func (r *Record) Generation() string { return r.Gen }

// RuleDigest implements model.SessionState.
func (r *Record) RuleDigest() map[string]model.Action { return r.Digest }

// FullInjected implements model.SessionState.
func (r *Record) FullInjected() bool { return r.FullDone }

// ReportedJob implements model.SessionState.
func (r *Record) ReportedJob(id string) bool {
	for _, x := range r.Reported {
		if x == id {
			return true
		}
	}
	return false
}

// LastJobReportAt implements model.SessionState.
func (r *Record) LastJobReportAt() time.Time { return r.JobReportAt }

// LastAssistantText implements model.SessionState.
func (r *Record) LastAssistantText() string { return r.AssistantText }

// LastToolFailureAt implements model.SessionState.
func (r *Record) LastToolFailureAt() (time.Time, bool) {
	return r.ToolFailureAt, !r.ToolFailureAt.IsZero()
}

// Recent implements model.SessionState.
func (r *Record) Recent(n int) []model.EventSummary {
	if n <= 0 || len(r.Ring) == 0 {
		return nil
	}
	if n > len(r.Ring) {
		n = len(r.Ring)
	}
	return r.Ring[len(r.Ring)-n:]
}

// Evals implements model.SessionState.
func (r *Record) Evals() map[string]model.EvalCount { return r.EvalCounts }

// EventSeq implements model.SessionState.
func (r *Record) EventSeq() int64 { return r.Seq }
