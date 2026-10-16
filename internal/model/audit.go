package model

import (
	"encoding/json"
	"time"
)

// AuditType is the "type" field of an audit JSONL line (plan B8).
type AuditType string

const (
	AuditHit           AuditType = "hit"            // one per RuleHit
	AuditEvalSummary   AuditType = "eval_summary"   // flushed per-rule EvalCount (Stop/SubagentStop/SessionEnd)
	AuditRuleDropped   AuditType = "rule_dropped"   //
	AuditRuleShadowed  AuditType = "rule_shadowed"  //
	AuditConfigInvalid AuditType = "config_invalid" //
	AuditVerify        AuditType = "verify"         // verify job finished (written by the runner)
	AuditDiag          AuditType = "diag"           // lock_timeout, state_corrupt, capability_degraded, snapshot_fallback, unknown predicate causes
	AuditPromote       AuditType = "promote"        // rules promote (incl. --force)
	AuditInstall       AuditType = "install"        // install / uninstall actions
	AuditTamper        AuditType = "tamper"         // T0 detection (boundary-t0)
	AuditConfigChange  AuditType = "config_change"  // Claude ConfigChange event, audit only (P2, architecture v2.0)
)

// AuditVersion is the current record schema version ("v").
const AuditVersion = 1

// MaxAuditLineBytes is the hard cap for one JSONL line including '\n'.
const MaxAuditLineBytes = 8 * 1024

// AuditRecord is one audit line. Field names are frozen (plan B8); new
// type-specific data goes into Detail.
type AuditRecord struct {
	TS             time.Time      `json:"ts"` // UTC, RFC3339Nano
	V              int            `json:"v"`
	Type           AuditType      `json:"type"`
	EventSeq       int64          `json:"event_seq,omitempty"`
	Client         ClientID       `json:"client,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	AgentID        string         `json:"agent_id,omitempty"`
	AgentType      string         `json:"agent_type,omitempty"`
	ToolUseID      string         `json:"tool_use_id,omitempty"`
	TranscriptRef  string         `json:"transcript_ref,omitempty"` // "~/"-relative transcript path
	RepoInst       string         `json:"repo_inst,omitempty"`
	RepoRemoteHash string         `json:"repo_remote_hash,omitempty"` // sha256(origin url)[:16], "" if none
	Event          EventName      `json:"event,omitempty"`
	NativeEvent    string         `json:"native_event,omitempty"`
	Tool           string         `json:"tool,omitempty"`
	RuleID         string         `json:"rule_id,omitempty"`
	RuleLayer      Layer          `json:"rule_layer,omitempty"`
	Generation     string         `json:"generation,omitempty"`
	Action         Action         `json:"action,omitempty"`
	Target         Action         `json:"target,omitempty"`
	Decision       string         `json:"decision,omitempty"` // deny|ask|warn|log|would_<target>|none
	Matched        string         `json:"matched,omitempty"`  // ≤200 runes, redacted; audit.Writer redacts and clips on write (audit.ClipRunes: ≤n runes, the "…" counts), so callers need not (v1.3)
	Context        []EventSummary `json:"context,omitempty"`  // ≤2 preceding ring entries
	LatencyUS      int64          `json:"latency_us,omitempty"`
	Tier           Tier           `json:"tier,omitempty"`
	BallastVersion string         `json:"ballast_version,omitempty"`

	Evals  map[string]EvalCount `json:"evals,omitempty"`  // eval_summary
	Verify *VerifyRecord        `json:"verify,omitempty"` // verify
	// Detail holds type-specific fields, e.g. {"diag":"lock_timeout","wait_us":20000}.
	Detail json.RawMessage `json:"detail,omitempty"`
}
