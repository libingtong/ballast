package replay

import (
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// Limits mirrored from the live state store (docs/architecture.md §3.1).
const (
	maxReadPaths   = 4096
	maxRing        = 8
	promptTailRune = 200
)

// memState is replay's own in-memory model.SessionState for one
// (session, agent). It is updated with the same rules as the hook
// (internal/hook/doc.go) but never touches internal/hook or internal/state.
type memState struct {
	started     time.Time
	hasStarted  bool
	read        map[string]time.Time
	readOrder   []string
	readHead    int
	toolCount   int
	sinceInject int
	promptCount int
	promptTail  string
	priorHits   map[string]int
	failureAt   time.Time
	seq         int64
	ring        []model.EventSummary
	generation  string
	lastText    string
}

var _ model.SessionState = (*memState)(nil)

func newMemState(generation string) *memState {
	return &memState{read: map[string]time.Time{}, priorHits: map[string]int{}, generation: generation}
}

func (s *memState) Seen(string) bool                    { return false }
func (s *memState) HasRead(norm string) bool            { _, ok := s.read[norm]; return ok }
func (s *memState) StartedAt() (time.Time, bool)        { return s.started, s.hasStarted }
func (s *memState) ToolCount() int                      { return s.toolCount }
func (s *memState) ToolCountSinceInject() int           { return s.sinceInject }
func (s *memState) PromptCount() int                    { return s.promptCount }
func (s *memState) PrevPromptTail() string              { return s.promptTail }
func (s *memState) PriorHits(ruleID string) int         { return s.priorHits[ruleID] }
func (s *memState) Generation() string                  { return s.generation }
func (s *memState) RuleDigest() map[string]model.Action { return nil }
func (s *memState) FullInjected() bool                  { return true }
func (s *memState) ReportedJob(string) bool             { return false }
func (s *memState) LastJobReportAt() time.Time          { return time.Time{} }
func (s *memState) LastAssistantText() string           { return s.lastText }
func (s *memState) Evals() map[string]model.EvalCount   { return map[string]model.EvalCount{} }
func (s *memState) EventSeq() int64                     { return s.seq }

func (s *memState) LastReadAt(norm string) (time.Time, bool) {
	t, ok := s.read[norm]
	return t, ok
}

func (s *memState) LastToolFailureAt() (time.Time, bool) {
	return s.failureAt, !s.failureAt.IsZero()
}

func (s *memState) Recent(n int) []model.EventSummary {
	if n <= 0 {
		return nil
	}
	if n > len(s.ring) {
		n = len(s.ring)
	}
	return append([]model.EventSummary(nil), s.ring[len(s.ring)-n:]...)
}

// touch records the first event time and appends a ring entry.
func (s *memState) touch(ts time.Time, ev model.EventName, tool string) {
	if !s.hasStarted {
		s.started, s.hasStarted = ts, true
	}
	s.seq++
	s.ring = append(s.ring, model.EventSummary{Seq: s.seq, TS: ts, Event: ev, Tool: tool})
	if len(s.ring) > maxRing {
		s.ring = s.ring[len(s.ring)-maxRing:]
	}
}

// addRead registers a normalized path as read (FIFO-bounded like the store).
func (s *memState) addRead(norm string, ts time.Time) {
	if norm == "" {
		return
	}
	if _, ok := s.read[norm]; !ok {
		s.readOrder = append(s.readOrder, norm)
	}
	s.read[norm] = ts
	for len(s.read) > maxReadPaths && s.readHead < len(s.readOrder) {
		delete(s.read, s.readOrder[s.readHead])
		s.readHead++
	}
	if s.readHead > 1024 && s.readHead*2 > len(s.readOrder) {
		s.readOrder = append([]string(nil), s.readOrder[s.readHead:]...)
		s.readHead = 0
	}
}

func (s *memState) onPrompt(prompt string, anchorEvery int) {
	s.promptCount++
	s.promptTail = tailRunes(prompt, promptTailRune)
	if anchorEvery > 0 && s.promptCount%anchorEvery == 0 {
		s.sinceInject = 0
	}
}

func (s *memState) onToolCall() {
	s.toolCount++
	s.sinceInject++
}

// onMidTurn mimics the mid-turn re-anchor.
func (s *memState) onMidTurn(batchAfter int) {
	if batchAfter > 0 && s.sinceInject >= batchAfter {
		s.sinceInject = 0
	}
}

func (s *memState) onHits(rules []string) {
	for _, id := range rules {
		s.priorHits[id]++
	}
}

func tailRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)
	return string(rs[len(rs)-n:])
}
