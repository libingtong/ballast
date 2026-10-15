package model

import "time"

// ScopeKey addresses one state file: ~/.ballast/state/<RepoInst>/<Session>/<Agent>.json
// (plan B6). Components are sanitized by the state store, not by callers.
type ScopeKey struct {
	RepoInst string `json:"repo_inst"` // RepoInfo.Inst, or "norepo-<hash(cwd)>" outside git
	Session  string `json:"session"`
	Agent    string `json:"agent"` // AgentID or "main"
}

// EventSummary is one entry of the per-agent ring buffer (capacity 8). Text
// is redacted and ≤ MaxEventSummaryRunes. Used as audit "context" (the 2
// most recent entries before a hit).
type EventSummary struct {
	Seq   int64     `json:"seq"`
	TS    time.Time `json:"ts"`
	Event EventName `json:"ev"`
	Tool  string    `json:"tool,omitempty"`
	Text  string    `json:"text,omitempty"` // e.g. "Bash: git status", "Edit: src/a.go"
	Hits  []string  `json:"hits,omitempty"`
}

// EvalCount accumulates per-rule evaluation statistics between flushes
// (plan B2 denominators).
type EvalCount struct {
	Evals   int `json:"evals"`
	Hits    int `json:"hits"`    // effective warn/ask/deny hits
	Would   int `json:"would"`   // log-tier hits (would_<target>)
	Unknown int `json:"unknown"` // outcome unknown
}

// Add accumulates o into c.
func (c *EvalCount) Add(o EvalCount) {
	c.Evals += o.Evals
	c.Hits += o.Hits
	c.Would += o.Would
	c.Unknown += o.Unknown
}

// SessionState is the read-only view of one (repo_inst, session, agent)
// state that predicates, the injector and the hook consult. Implemented by
// the state store's record type (U5); faked in tests.
type SessionState interface {
	// Seen reports whether the dedupe key (see StateDelta.DedupeKey) was
	// already applied (last 512 keys).
	Seen(dedupeKey string) bool
	// HasRead / LastReadAt use NORMALIZED absolute paths, agent-scoped.
	HasRead(norm string) bool
	LastReadAt(norm string) (time.Time, bool)
	// StartedAt is the time of the first event recorded for this scope.
	StartedAt() (time.Time, bool)
	ToolCount() int
	ToolCountSinceInject() int
	PromptCount() int
	// PrevPromptTail is the tail (≤200 runes) of the previous user prompt.
	PrevPromptTail() string
	PriorHits(ruleID string) int
	Generation() string
	RuleDigest() map[string]Action
	FullInjected() bool // full text injected in this session (Hermes first-turn logic)
	ReportedJob(jobID string) bool
	LastJobReportAt() time.Time
	LastAssistantText() string // Cursor afterAgentResponse fallback
	LastToolFailureAt() (time.Time, bool)
	Recent(n int) []EventSummary // up to n most recent ring entries, oldest first
	Evals() map[string]EvalCount
	EventSeq() int64
}

// StateDelta is the increment one hook invocation merges into state, under
// the lock, onto a FRESH re-read of the file (plan v0.4 §3: lock-free read
// for evaluation, then lock + re-read + merge). Every field is additive or
// last-writer-wins so concurrent deltas commute.
type StateDelta struct {
	// DedupeKey = "<EventName>:<tool_use_id>" when a tool_use_id exists
	// (PreToolUse and PostToolUse share ids, so the event is part of the key).
	// If already Seen, ONLY idempotent fields (Generation, RuleDigest,
	// ReportedJobs) are applied.
	DedupeKey string
	Now       time.Time

	Summary    *EventSummary // appended to the ring (seq assigned by the store)
	ToolCall   bool          // increments tool_count and tool_count_since_inject
	Prompt     bool          // increments prompt_count
	PromptTail string        // replaces prev prompt tail when non-empty

	// ReadPaths are normalized paths read (Read-family targets; non-dynamic
	// CommandNode.Reads of a Bash/PowerShell PostToolUse) or written by a
	// file tool (Edit/Write ⇒ also counts as read). See internal/hook/doc.go.
	ReadPaths []string
	HitRules  []string // effective or would hits → prior_hit counters
	Evals     map[string]EvalCount

	// FlushEvals: the hook flushes the counts it READ (FlushedEvals); the
	// store subtracts them (not zeroing, so concurrent increments survive).
	// The hook appends the eval_summary audit record ONLY when the commit
	// reports Applied && !Duplicate && !LockTimeout; otherwise the counts
	// stay in state for the next flush (no double counting).
	FlushEvals   bool
	FlushedEvals map[string]EvalCount

	AnchorEmitted bool // resets tool_count_since_inject to 0
	FullEmitted   bool
	ReportedJobs  []string
	JobReportAt   time.Time

	Generation string
	RuleDigest map[string]Action

	LastAssistantText string // Cursor afterAgentResponse (≤4000 runes)
	ToolFailure       bool   // PostToolUseFailure observed (weak evidence)
}

// StateLoadInfo describes a lock-free load.
type StateLoadInfo struct {
	Exists   bool
	Corrupt  bool // unparsable or wrong version → treated as empty, audit "diag:state_corrupt"
	Readable bool // false on I/O errors other than not-exist
	Bytes    int
}

// StateCommitInfo describes a commit transaction.
type StateCommitInfo struct {
	Applied     bool          // delta merged and file replaced
	Duplicate   bool          // DedupeKey already seen
	LockTimeout bool          // 20ms budget exceeded → delta dropped, audit diag
	LockWait    time.Duration //
	Corrupt     bool          // re-read under lock found a corrupt file (reset)
	EventSeq    int64         // seq assigned to Summary
}
