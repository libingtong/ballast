package state

import (
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Frozen limits (plan B6).
const (
	LockBudget        = 20 * time.Millisecond
	LockRetryInterval = 200 * time.Microsecond
	SeenCap           = 512
	RingCap           = 8
	ReadCap           = 4096
	ReportedJobsCap   = 64
	MaxAssistantRunes = 4000
	MaxPromptTailRune = 200
	TTL               = 7 * 24 * time.Hour
	RecordVersion     = 1
)

// Options configures a Store.
type Options struct {
	Layout layout.Layout
	FS     sys.FS
	Clock  sys.Clock
	IDs    sys.IDGen
}

// Store implements ports.StateStore.
type Store struct {
	opt Options
}

var _ ports.StateStore = (*Store)(nil)

// New returns a Store.
func New(opt Options) *Store { return &Store{opt: opt} }

// Load implements ports.StateStore.
func (s *Store) Load(key model.ScopeKey) (model.SessionState, model.StateLoadInfo, error) {
	panic("unimplemented: state.Store.Load")
}

// Commit implements ports.StateStore.
func (s *Store) Commit(key model.ScopeKey, d *model.StateDelta) (model.StateCommitInfo, error) {
	panic("unimplemented: state.Store.Commit")
}

// DropAgent implements ports.StateStore.
func (s *Store) DropAgent(key model.ScopeKey) error {
	panic("unimplemented: state.Store.DropAgent")
}

// DropSession implements ports.StateStore.
func (s *Store) DropSession(key model.ScopeKey) error {
	panic("unimplemented: state.Store.DropSession")
}

// GCStats reports a GC run.
type GCStats struct {
	ScannedSessions int
	RemovedSessions int
	Errors          int
}

// GC removes state older than ttl (relative to Clock.Now).
func (s *Store) GC(ttl time.Duration) (GCStats, error) {
	panic("unimplemented: state.Store.GC")
}

// Path returns the state file path for key (components sanitized).
func (s *Store) Path(key model.ScopeKey) string {
	return s.opt.Layout.StatePath(SanitizeComponent(key.RepoInst), SanitizeComponent(key.Session), SanitizeComponent(key.Agent))
}

// SanitizeComponent returns c when it matches ^[A-Za-z0-9_-]{1,64}$,
// otherwise "h" + hex(sha256(c))[:16]. Empty ⇒ "h" + hash of "".
func SanitizeComponent(c string) string {
	panic("unimplemented: state.SanitizeComponent")
}

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
func (r *Record) Merge(d *model.StateDelta) (applied, duplicate bool) {
	panic("unimplemented: state.Record.Merge")
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
