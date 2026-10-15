package model

// ClientID identifies an AI coding client. The installer writes it into the
// hook command line (`ballast hook --client <id> --event <native>`); the hook
// never sniffs the client at runtime (plan v0.4 §1).
type ClientID string

// Supported clients.
const (
	ClientClaude ClientID = "claude-code"
	ClientCodex  ClientID = "codex"
	ClientCursor ClientID = "cursor"
	ClientHermes ClientID = "hermes"
)

// AllClients lists clients in support-tier order (tier 1 first).
var AllClients = []ClientID{ClientClaude, ClientCodex, ClientCursor, ClientHermes}

// Valid reports whether c is a known client.
func (c ClientID) Valid() bool {
	switch c {
	case ClientClaude, ClientCodex, ClientCursor, ClientHermes:
		return true
	}
	return false
}

// EventName is a NORMALIZED (Claude-Code-style, PascalCase) event name.
// Native client event names are mapped onto these by NativeEventTable.
type EventName string

// Normalized events. Only the first six (RuleEvents) may appear in a rule's
// "event" field; the rest drive injection, lifecycle and bookkeeping.
const (
	EventPreToolUse         EventName = "PreToolUse"
	EventUserPromptSubmit   EventName = "UserPromptSubmit"
	EventPostToolUse        EventName = "PostToolUse"
	EventPostToolUseFailure EventName = "PostToolUseFailure"
	EventStop               EventName = "Stop"
	EventSubagentStop       EventName = "SubagentStop"

	EventSessionStart  EventName = "SessionStart"
	EventPostToolBatch EventName = "PostToolBatch"
	EventSessionEnd    EventName = "SessionEnd"
	EventSubagentStart EventName = "SubagentStart"
	EventPreCompact    EventName = "PreCompact"
	EventPostCompact   EventName = "PostCompact"
	// EventAgentResponse is Cursor's afterAgentResponse: audit/bookkeeping only
	// (stores the assistant text so a later Cursor `stop` can evaluate text rules).
	EventAgentResponse EventName = "AgentResponse"
)

// RuleEvents are the only values accepted by Rule.Event (plan B1).
var RuleEvents = []EventName{
	EventPreToolUse, EventUserPromptSubmit, EventPostToolUse,
	EventPostToolUseFailure, EventStop, EventSubagentStop,
}

// IsRuleEvent reports whether e may be used in Rule.Event.
func (e EventName) IsRuleEvent() bool {
	for _, r := range RuleEvents {
		if r == e {
			return true
		}
	}
	return false
}

// IsStopFamily reports whether e is Stop or SubagentStop.
func (e EventName) IsStopFamily() bool { return e == EventStop || e == EventSubagentStop }

// IsToolEvent reports whether events of this kind carry a ToolCall.
func (e EventName) IsToolEvent() bool {
	return e == EventPreToolUse || e == EventPostToolUse || e == EventPostToolUseFailure
}

// Role classifies what Ballast does on a native event.
type Role string

const (
	RoleGate      Role = "gate"      // may block (deny/ask)
	RoleInject    Role = "inject"    // may add context
	RoleAudit     Role = "audit"     // observation / bookkeeping only
	RoleLifecycle Role = "lifecycle" // state cleanup, eval_summary flush
)

// NativeEvent maps one client-native hook event onto a normalized event.
type NativeEvent struct {
	Native string    // exact event key used in the client's hook config
	Event  EventName // normalized event
	Roles  []Role
	// ToolHint is the canonical tool name implied by a dedicated gate event
	// (Cursor beforeShellExecution → "Bash", beforeReadFile → "Read",
	// beforeMCPExecution → "mcp__*"); empty when the payload names the tool.
	ToolHint string
	// Install is true when `ballast install` registers this event.
	Install bool
	// Verified is false when the payload/behaviour has not been captured by a
	// probe (M0a-style). Unverified events still install but `doctor` lists them.
	Verified bool
}

// NativeEventTable is the frozen native→normalized mapping (architecture §5).
// Adapters (U6) and installers (U10) MUST derive from this table instead of
// keeping private copies.
var NativeEventTable = map[ClientID][]NativeEvent{
	ClientClaude: {
		{Native: "SessionStart", Event: EventSessionStart, Roles: []Role{RoleInject, RoleLifecycle}, Install: true, Verified: true},
		{Native: "UserPromptSubmit", Event: EventUserPromptSubmit, Roles: []Role{RoleGate, RoleInject}, Install: true, Verified: true},
		{Native: "PreToolUse", Event: EventPreToolUse, Roles: []Role{RoleGate}, Install: true, Verified: true},
		{Native: "PostToolUse", Event: EventPostToolUse, Roles: []Role{RoleInject, RoleAudit}, Install: true, Verified: true},
		{Native: "PostToolUseFailure", Event: EventPostToolUseFailure, Roles: []Role{RoleInject, RoleAudit}, Install: true, Verified: true},
		{Native: "PostToolBatch", Event: EventPostToolBatch, Roles: []Role{RoleInject}, Install: true, Verified: true},
		{Native: "Stop", Event: EventStop, Roles: []Role{RoleGate, RoleLifecycle}, Install: true, Verified: true},
		{Native: "SubagentStop", Event: EventSubagentStop, Roles: []Role{RoleGate, RoleLifecycle}, Install: true, Verified: true},
		{Native: "SessionEnd", Event: EventSessionEnd, Roles: []Role{RoleLifecycle}, Install: true, Verified: true},
	},
	ClientCodex: {
		{Native: "SessionStart", Event: EventSessionStart, Roles: []Role{RoleInject}, Install: true, Verified: false},
		{Native: "UserPromptSubmit", Event: EventUserPromptSubmit, Roles: []Role{RoleInject}, Install: true, Verified: false},
		{Native: "PreToolUse", Event: EventPreToolUse, Roles: []Role{RoleGate}, Install: true, Verified: false},
		{Native: "PermissionRequest", Event: EventPreToolUse, Roles: []Role{RoleGate}, Install: true, Verified: false},
		{Native: "PostToolUse", Event: EventPostToolUse, Roles: []Role{RoleInject, RoleAudit}, Install: true, Verified: false},
		{Native: "Stop", Event: EventStop, Roles: []Role{RoleGate, RoleLifecycle}, Install: true, Verified: false},
	},
	ClientCursor: {
		{Native: "sessionStart", Event: EventSessionStart, Roles: []Role{RoleInject}, Install: true, Verified: false},
		{Native: "beforeSubmitPrompt", Event: EventUserPromptSubmit, Roles: []Role{RoleGate, RoleInject}, Install: true, Verified: false},
		{Native: "preToolUse", Event: EventPreToolUse, Roles: []Role{RoleGate}, Install: true, Verified: false},
		{Native: "beforeShellExecution", Event: EventPreToolUse, Roles: []Role{RoleGate}, ToolHint: "Bash", Install: true, Verified: false},
		{Native: "beforeMCPExecution", Event: EventPreToolUse, Roles: []Role{RoleGate}, ToolHint: "mcp__*", Install: true, Verified: false},
		{Native: "beforeReadFile", Event: EventPreToolUse, Roles: []Role{RoleGate}, ToolHint: "Read", Install: true, Verified: false},
		{Native: "postToolUse", Event: EventPostToolUse, Roles: []Role{RoleInject, RoleAudit}, Install: true, Verified: false},
		{Native: "afterAgentResponse", Event: EventAgentResponse, Roles: []Role{RoleAudit}, Install: true, Verified: false},
		{Native: "stop", Event: EventStop, Roles: []Role{RoleGate, RoleLifecycle}, Install: true, Verified: false},
		{Native: "subagentStop", Event: EventSubagentStop, Roles: []Role{RoleGate, RoleLifecycle}, Install: true, Verified: false},
	},
	ClientHermes: {
		{Native: "on_session_start", Event: EventSessionStart, Roles: []Role{RoleLifecycle}, Install: true, Verified: false},
		{Native: "pre_llm_call", Event: EventUserPromptSubmit, Roles: []Role{RoleInject}, Install: true, Verified: false},
		{Native: "pre_tool_call", Event: EventPreToolUse, Roles: []Role{RoleGate}, Install: true, Verified: false},
		{Native: "post_tool_call", Event: EventPostToolUse, Roles: []Role{RoleAudit}, Install: true, Verified: false},
		{Native: "on_session_end", Event: EventSessionEnd, Roles: []Role{RoleLifecycle}, Install: true, Verified: false},
	},
}

// LookupNative returns the table entry for (client, native) or ok=false.
func LookupNative(c ClientID, native string) (NativeEvent, bool) {
	for _, ne := range NativeEventTable[c] {
		if ne.Native == native {
			return ne, true
		}
	}
	return NativeEvent{}, false
}
