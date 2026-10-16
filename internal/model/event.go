package model

import (
	"encoding/json"
	"time"
)

// Event is the normalized hook input produced by an adapter's ParseEvent.
//
// All fields are optional unless stated: adapters MUST tolerate missing keys
// and type mismatches by leaving the field at its zero value (M0a conclusion
// 6; plan A1). ParseEvent fails only when the stdin is not a JSON object.
type Event struct {
	Client ClientID  `json:"client"`
	Name   EventName `json:"event"`  // normalized; required
	Native string    `json:"native"` // native event name as passed by --event; required

	SessionID      string `json:"session_id,omitempty"` // Cursor: conversation_id fallback
	TranscriptPath string `json:"transcript_path,omitempty"`
	Cwd            string `json:"cwd,omitempty"` // raw, as reported by the client
	PromptID       string `json:"prompt_id,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	// Effort is effort.level when effort is an object, the string itself when
	// it is a string, empty otherwise (M0a: effort is {"level": ...}).
	Effort    string `json:"effort,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`   // Cursor: subagent_id
	AgentType string `json:"agent_type,omitempty"` // "" for Claude's internal compact agent
	// HasAgentType distinguishes "agent_type absent" from "agent_type == \"\"".
	// SubagentStop with HasAgentType && AgentType=="" is the internal compact
	// agent and is ignored by default (plan v0.4 §3).
	HasAgentType bool `json:"-"`

	// SessionStart: startup|resume|clear|compact|fork. ConfigChange (P2,
	// v1.5 docs/p2-design.md §5.1) REUSES this field for the payload's
	// "source" (which settings layer changed; common.FillCommon already maps
	// the "source" key for every event) — there is no separate ConfigSource.
	Source string `json:"source,omitempty"`

	// ConfigFile (P2, frozen by the v1.5 design commit) is the ConfigChange
	// payload's changed-file path, verbatim (not normalized). Filled ONLY by
	// the claudecode adapter (unit P2-CC, once M0b case configchange-payload
	// has confirmed the key; until then it stays ""); read ONLY by the hook's
	// ConfigChange audit (unit P2-HOOK), which rewrites it into the "~/" /
	// "<repo>/" relative form for detail.file. Other clients never set it.
	ConfigFile string `json:"config_file,omitempty"`

	// UserPromptSubmit / Hermes pre_llm_call
	Prompt      string `json:"prompt,omitempty"`
	IsFirstTurn bool   `json:"is_first_turn,omitempty"` // Hermes extra.is_first_turn

	// Tool events
	Tool         *ToolCall       `json:"tool,omitempty"`
	ToolResponse json.RawMessage `json:"tool_response,omitempty"` // verbatim; shape varies (A8)
	// ToolResponseText is a best-effort flattening of ToolResponse used by the
	// text matcher (source "tool_response"): for an object, stdout+"\n"+stderr
	// when present, otherwise the compact JSON; for a string, the string.
	ToolResponseText string `json:"-"`
	Error            string `json:"error,omitempty"` // PostToolUseFailure
	// ExitCode is parsed from the first line "Exit code N" of Error; nil when
	// absent or unparsable (weak evidence only, M0a conclusion 4).
	ExitCode    *int   `json:"exit_code,omitempty"`
	IsInterrupt bool   `json:"is_interrupt,omitempty"`
	DurationMS  *int64 `json:"duration_ms,omitempty"`

	// PostToolBatch
	BatchCalls []ToolCall `json:"batch_calls,omitempty"`

	// Stop / SubagentStop
	StopHookActive       bool   `json:"stop_hook_active,omitempty"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
	// HasLastAssistantMessage is false when the client did not send the field
	// (Cursor stop): the hook then falls back to the text stored in state by
	// AgentResponse, otherwise text rules evaluate to unknown.
	HasLastAssistantMessage bool   `json:"-"`
	AgentTranscriptPath     string `json:"agent_transcript_path,omitempty"`

	// SessionEnd / PreCompact
	Reason  string `json:"reason,omitempty"`
	Trigger string `json:"trigger,omitempty"`

	// ReceivedAt is stamped by the hook from the injected Clock (never by the adapter).
	ReceivedAt time.Time `json:"received_at"`
}

// IsSubagent reports whether the event originates inside a sub-agent.
func (e *Event) IsSubagent() bool { return e.AgentID != "" }

// AgentKey returns the state-scope agent key: AgentID, or "main".
func (e *Event) AgentKey() string {
	if e.AgentID == "" {
		return "main"
	}
	return e.AgentID
}

// IsInternalCompactAgent reports the Claude compact helper agent case
// (SubagentStop with agent_type present and empty).
func (e *Event) IsInternalCompactAgent() bool {
	return e.Name == EventSubagentStop && e.HasAgentType && e.AgentType == ""
}

// ShellKind tells which shell grammar a command string uses.
type ShellKind string

const (
	ShellNone       ShellKind = ""
	ShellBash       ShellKind = "bash"
	ShellPowerShell ShellKind = "powershell"
)

// Canonical tool names after NormalizeTool.
const (
	ToolBash         = "Bash"
	ToolPowerShell   = "PowerShell"
	ToolEdit         = "Edit"
	ToolMultiEdit    = "MultiEdit"
	ToolWrite        = "Write"
	ToolNotebookEdit = "NotebookEdit"
	ToolRead         = "Read"
	ToolAgent        = "Agent"
)

// ToolCall is a normalized tool invocation (adapter NormalizeTool output).
type ToolCall struct {
	Name    string `json:"name"`               // canonical (Bash, Edit, mcp__x__y, ...)
	RawName string `json:"raw_name,omitempty"` // native name (apply_patch, exec_command, Shell, terminal, ...)
	UseID   string `json:"tool_use_id,omitempty"`

	// Input is the verbatim tool_input object (nil when absent).
	Input json.RawMessage `json:"input,omitempty"`
	// Fields maps a tool_input field path to every string value found there,
	// e.g. "command" → ["sleep 300"], "edits.new_string" → ["a","b"].
	// Nested arrays of objects use "parent.child" paths. This is what
	// match.input_regex reads; only string leaves are included.
	Fields map[string][]string `json:"fields,omitempty"`

	// Shell commands (Bash / PowerShell after normalization).
	Shell           ShellKind `json:"shell,omitempty"`
	Command         string    `json:"command,omitempty"`
	RunInBackground bool      `json:"run_in_background,omitempty"`

	// Targets are the raw file paths the tool reads or writes (file_path,
	// notebook_path, every apply_patch target). Not normalized; the PathService
	// normalizes them at evaluation time.
	Targets []string `json:"targets,omitempty"`
	// Edits are per-file edits for Edit/MultiEdit/Write/apply_patch (for
	// input_regex convenience and for replay); may be empty.
	Edits []FileEdit `json:"edits,omitempty"`
	// TargetDeletes lists apply_patch "*** Delete File" targets (subset of Targets).
	TargetDeletes []string `json:"target_deletes,omitempty"`
	// TargetAdds lists apply_patch "*** Add File" targets (subset of Targets);
	// read-before-edit exempts them like path_exists=false.
	TargetAdds []string `json:"target_adds,omitempty"`
}

// FileEdit is one file-level edit extracted from a tool call.
type FileEdit struct {
	Path      string `json:"path"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}
