package model

import "time"

// LoadRequest asks the RulesLoader for the rule set in effect for an event.
type LoadRequest struct {
	Client ClientID
	Cwd    string    // event cwd (raw)
	Repo   *RepoInfo // nil when not inside a git work tree (repo layer = <cwd>/.ballast)
	Now    time.Time
}

// ShellCommands lazily parses the event's shell command (Bash or PowerShell).
// Implementations memoize; Get is cheap after the first call. A non-nil
// error means "unparsable" and makes shell matchers evaluate to TriUnknown.
type ShellCommands interface {
	Get() (*CommandList, error)
}

// WorldView answers environment predicates (path_exists, env_equals, branch_glob).
type WorldView interface {
	// PathExists reports existence of a normalized absolute path via Lstat:
	// exists → true, ENOENT/ENOTDIR → false, any other error → unknown.
	PathExists(norm string) Tri
	// Getenv reads the hook process environment (injected Env, never os directly).
	Getenv(name string) (string, bool)
	// Branch returns the current branch short name; TriUnknown when not in a
	// repo or unreadable; ("", TriFalse) for a detached HEAD.
	Branch() (string, Tri)
}

// PathContext carries what the PathService needs to derive match candidates.
type PathContext struct {
	Cwd         string // normalized event cwd
	ProjectRoot string // normalized git toplevel, or Cwd when not in a repo
	Home        string // normalized home directory
	GOOS        string // "windows" ⇒ case-insensitive; "darwin" ⇒ case-insensitive; others sensitive
}

// EvalInput is everything the evaluator may look at for one event. The hook
// builds it; dsl (U3) never performs I/O except through these interfaces.
type EvalInput struct {
	Event    *Event
	Client   ClientID
	Paths    PathContext
	Commands ShellCommands // nil when the event has no shell command
	Session  SessionState  // never nil (empty state when absent/corrupt)
	// SessionReadable is false when the state file could not be read (I/O
	// error other than not-exist): state-backed predicates then return unknown.
	SessionReadable bool
	Evidence        EvidenceView // nil when not in a repo → evidence predicates unknown
	World           WorldView
	Now             time.Time
	// Deadline bounds expensive predicate work (snapshot fallback to git).
	Deadline time.Time
}

// RuleResult is the per-rule outcome of one evaluation (feeds eval counts).
type RuleResult struct {
	RuleID  string `json:"rule_id"`
	Outcome Tri    `json:"outcome"`
	// UnknownCause names the first predicate/matcher that produced unknown
	// (e.g. "verify_fresh:job_running", "bash:parse_error"); "" otherwise.
	UnknownCause string `json:"unknown_cause,omitempty"`
}

// RuleHit is a rule whose outcome was TriTrue.
type RuleHit struct {
	RuleID string `json:"rule_id"`
	Layer  Layer  `json:"layer"`
	Action Action `json:"action"` // configured action (after defaults)
	Target Action `json:"target"`
	// Effective is the action actually applied on this client after the
	// capability check: equal to Action, or ActionLog when the combination is
	// unsupported at runtime (recorded as audit detail "capability_degraded").
	Effective Action `json:"effective"`
	Message   string `json:"message,omitempty"`
	// Matched is a short, redacted description of what matched (≤200 runes),
	// e.g. `bash node 0: sleep 300` or `path: src/a.go`.
	Matched string `json:"matched,omitempty"`
}

// Decision label for audit: the action, or would_<target> for log hits.
func (h RuleHit) Label() string {
	if h.Effective == ActionLog {
		return WouldLabel(h.Target)
	}
	return string(h.Effective)
}

// Verdict is the evaluator output (multi-rule arbitration applied, plan B1).
type Verdict struct {
	Results []RuleResult `json:"results"` // one per rule evaluated for this event, in rule order
	Hits    []RuleHit    `json:"hits,omitempty"`
	// Action is the strictest Effective action among hits (ActionNone if none).
	Action Action `json:"action"`
	// Reason concatenates "[id] message" of hits whose Effective == Action
	// (warn/ask/deny only), in rule order, within BudgetReason runes.
	Reason string `json:"reason,omitempty"`
}

// InjectKind enumerates why text is injected (plan B5).
type InjectKind string

const (
	InjectFull       InjectKind = "full"        // SessionStart: every rule's inject text
	InjectAnchor     InjectKind = "anchor"      // UserPromptSubmit / mid-turn re-anchor
	InjectGenSummary InjectKind = "gen_summary" // rules changed since the session's last generation
	InjectJobResults InjectKind = "job_results" // newly finished verify jobs
	InjectNotices    InjectKind = "notices"     // config_invalid etc.
)

// InjectRequest is the input of the Injector.
type InjectRequest struct {
	Event   *Event
	Client  ClientID
	Rules   *CompiledRuleSet
	Session SessionState
	Verdict *Verdict       // may be nil (SessionStart, PostToolBatch)
	NewJobs []VerifyRecord // finished verify jobs not yet reported to this session
	Now     time.Time
}

// Injection is the injector's output. Text is empty when nothing is injected.
type Injection struct {
	Text  string       `json:"text,omitempty"`
	Kinds []InjectKind `json:"kinds,omitempty"`
	// State bookkeeping the hook must persist (via StateDelta).
	AnchorEmitted bool     `json:"anchor_emitted,omitempty"` // resets tool_count_since_inject
	FullEmitted   bool     `json:"full_emitted,omitempty"`
	ReportedJobs  []string `json:"reported_jobs,omitempty"`
	// RuleDigest is the id→action digest of Rules to store in state so the
	// next generation change can be summarized.
	RuleDigest map[string]Action `json:"rule_digest,omitempty"`
}

// Decision is the single in-memory decision of one hook invocation (plan
// B7). The adapter renders it; it is never partially written.
type Decision struct {
	Event  EventName `json:"event"`
	Native string    `json:"native"`
	// Action is the effective verdict action on this client (ActionNone/Log ⇒
	// nothing blocks). Ask is only present when the client supports it.
	Action Action `json:"action"`
	// Reason is shown to the agent (deny/ask/block) or appended to context (warn).
	Reason string `json:"reason,omitempty"`
	// Context is injection text (Injection.Text); rendered as additionalContext
	// or the client's equivalent where the (client,event) supports it.
	Context string    `json:"context,omitempty"`
	Hits    []RuleHit `json:"hits,omitempty"`
}

// Empty reports whether rendering must produce an empty stdout.
func (d *Decision) Empty() bool {
	return (d.Action == ActionNone || d.Action == ActionLog) && d.Context == ""
}
