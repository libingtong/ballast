package model

// Capability is one cell of the client × rule-event × action matrix
// (plan v0.4 §1; docs/architecture.md §4). `log` is supported for every
// event that exists on a client and is not listed explicitly.
type Capability struct {
	Event     EventName
	Action    Action
	Supported bool
	// Verified is false when the behaviour has not been captured by a
	// Ballast probe (M0a-style). Lint reports a warning for unverified cells.
	Verified bool
	// Render documents the stdout shape (informative; the adapter is authoritative).
	Render string
	Note   string
}

// CapabilitySet is everything lint, the hook and the injector need to know
// about one client.
type CapabilitySet struct {
	Client ClientID
	// RuleEvents available on this client (a rule for a missing event never fires;
	// lint warns "event-unavailable").
	RuleEvents []EventName
	Cells      []Capability
	// InjectEvents are normalized events whose output can carry Decision.Context.
	InjectEvents []EventName
	// FullInjectEvent carries the full rule text (SessionStart, or
	// UserPromptSubmit on the first turn for Hermes).
	FullInjectEvent EventName
	// MidTurnEvent carries the mid-turn re-anchor (PostToolBatch on Claude,
	// PostToolUse on Codex/Cursor, none on Hermes).
	MidTurnEvent EventName
	// StaticRulesFile is true when the installer also writes a static rules
	// file (Cursor ~/.cursor/rules/ballast.mdc, alwaysApply).
	StaticRulesFile bool
	// Tier is the best trust tier achievable (T1 only for Claude with managed settings).
	MaxTier Tier
}

// HasRuleEvent reports whether e exists on the client.
func (cs CapabilitySet) HasRuleEvent(e EventName) bool {
	for _, x := range cs.RuleEvents {
		if x == e {
			return true
		}
	}
	return false
}

// Lookup returns the cell for (event, action). For ActionLog it synthesizes a
// supported, verified cell when the event exists. ok=false means unsupported.
func (cs CapabilitySet) Lookup(e EventName, a Action) (Capability, bool) {
	if !cs.HasRuleEvent(e) {
		return Capability{Event: e, Action: a, Note: "event unavailable on client"}, false
	}
	if a == ActionLog {
		return Capability{Event: e, Action: a, Supported: true, Verified: true, Render: "empty stdout"}, true
	}
	for _, c := range cs.Cells {
		if c.Event == e && c.Action == a {
			return c, c.Supported
		}
	}
	return Capability{Event: e, Action: a}, false
}

// CanInject reports whether Decision.Context can be rendered on event e.
func (cs CapabilitySet) CanInject(e EventName) bool {
	for _, x := range cs.InjectEvents {
		if x == e {
			return true
		}
	}
	return false
}

// StrictestSupported returns the strictest supported action for e (log if none).
func (cs CapabilitySet) StrictestSupported(e EventName) Action {
	best := ActionLog
	for _, a := range []Action{ActionWarn, ActionAsk, ActionDeny} {
		if _, ok := cs.Lookup(e, a); ok {
			best = Stricter(best, a)
		}
	}
	return best
}

// Capabilities returns the frozen capability set of client c (zero value for
// unknown clients). The table below is the contract; adapters' Capabilities()
// MUST return exactly this value.
func Capabilities(c ClientID) CapabilitySet {
	switch c {
	case ClientClaude:
		return claudeCaps
	case ClientCodex:
		return codexCaps
	case ClientCursor:
		return cursorCaps
	case ClientHermes:
		return hermesCaps
	}
	return CapabilitySet{Client: c}
}

// DefaultTarget is the default `target` of a rule for event e: the strictest
// action e allows on the reference client (claude-code), plan B1.
func DefaultTarget(e EventName) Action {
	cs := Capabilities(ClientClaude)
	return cs.StrictestSupported(e)
}

var claudeCaps = CapabilitySet{
	Client:     ClientClaude,
	RuleEvents: RuleEvents,
	Cells: []Capability{
		{EventPreToolUse, ActionDeny, true, true, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":R}}`, ""},
		{EventPreToolUse, ActionAsk, true, true, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":R}}`, "-p 非交互模式下可能被直接拒绝"},
		{EventPreToolUse, ActionWarn, true, true, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":R+C}}`, "不输出 permissionDecision(M0a warn-additionalcontext)"},
		{EventUserPromptSubmit, ActionDeny, true, true, `{"decision":"block","reason":R}`, ""},
		{EventUserPromptSubmit, ActionWarn, true, true, `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":R+C}}`, ""},
		{EventPostToolUse, ActionWarn, true, true, `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":R+C}}`, "工具已执行,无 deny"},
		{EventPostToolUseFailure, ActionWarn, true, false, `{"hookSpecificOutput":{"hookEventName":"PostToolUseFailure","additionalContext":R+C}}`, "文档列出,未单独取证"},
		{EventStop, ActionDeny, true, true, `{"decision":"block","reason":R}`, "会话继续;连续 8 次上限(A10)"},
		{EventStop, ActionWarn, true, true, `{"systemMessage":R}`, "只给用户看,放行停止(M0a stop-systemmessage);不用 additionalContext"},
		{EventSubagentStop, ActionDeny, true, true, `{"decision":"block","reason":R}`, ""},
		{EventSubagentStop, ActionWarn, true, false, `{"systemMessage":R}`, "按 Stop 推断,未单独取证"},
	},
	InjectEvents:    []EventName{EventSessionStart, EventUserPromptSubmit, EventPostToolBatch, EventPostToolUse, EventPostToolUseFailure, EventPreToolUse},
	FullInjectEvent: EventSessionStart,
	MidTurnEvent:    EventPostToolBatch,
	MaxTier:         TierT1,
}

var codexCaps = CapabilitySet{
	Client:     ClientCodex,
	RuleEvents: []EventName{EventPreToolUse, EventUserPromptSubmit, EventPostToolUse, EventStop},
	Cells: []Capability{
		{EventPreToolUse, ActionDeny, true, false, `PreToolUse: {"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":R}}; PermissionRequest: {"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":R}}}`, "pinrule 实测可拦截;Ballast 未取证"},
		{EventPreToolUse, ActionAsk, false, false, "", "Codex 解析 ask 但不执行(fail open),lint 拒绝"},
		{EventPreToolUse, ActionWarn, false, false, "", "PreToolUse additionalContext 未见文档"},
		{EventUserPromptSubmit, ActionWarn, true, false, `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":R+C}}`, ""},
		{EventPostToolUse, ActionWarn, true, false, `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":R+C}}`, ""},
		{EventStop, ActionDeny, true, false, `{"decision":"block","reason":R}`, "【未核验】pinrule 亦标待验证"},
	},
	InjectEvents:    []EventName{EventSessionStart, EventUserPromptSubmit, EventPostToolUse},
	FullInjectEvent: EventSessionStart,
	MidTurnEvent:    EventPostToolUse,
	MaxTier:         TierT0,
}

var cursorCaps = CapabilitySet{
	Client:     ClientCursor,
	RuleEvents: []EventName{EventPreToolUse, EventUserPromptSubmit, EventPostToolUse, EventStop, EventSubagentStop},
	Cells: []Capability{
		{EventPreToolUse, ActionDeny, true, false, `{"permission":"deny","user_message":R,"agent_message":R}`, "preToolUse/beforeShellExecution/beforeMCPExecution/beforeReadFile"},
		{EventPreToolUse, ActionAsk, true, false, `{"permission":"ask","user_message":R,"agent_message":R}`, "【未核验】"},
		{EventUserPromptSubmit, ActionWarn, true, false, `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":R+C}}`, "沿用 pinrule 的嵌套格式【未核验】"},
		{EventPostToolUse, ActionWarn, true, false, `{"additional_context":R+C}`, ""},
		{EventStop, ActionDeny, true, false, `{"followup_message":R}`, "语义为续跑(followup),不是阻止停止;安装条目带 loop_limit"},
		{EventSubagentStop, ActionDeny, true, false, `{"followup_message":R}`, "同 stop"},
	},
	InjectEvents:    []EventName{EventSessionStart, EventUserPromptSubmit, EventPostToolUse},
	FullInjectEvent: EventSessionStart,
	MidTurnEvent:    EventPostToolUse,
	StaticRulesFile: true,
	MaxTier:         TierT0,
}

var hermesCaps = CapabilitySet{
	Client:     ClientHermes,
	RuleEvents: []EventName{EventPreToolUse, EventUserPromptSubmit, EventPostToolUse, EventStop},
	Cells: []Capability{
		{EventPreToolUse, ActionDeny, true, true, `{"decision":"block","reason":R}`, "离线核验(hooks test),触发时机未核验"},
		{EventUserPromptSubmit, ActionWarn, true, true, `{"context":R+C}`, "仅 pre_llm_call;离线核验,触发时机未核验"},
		{EventStop, ActionDeny, true, true, `{"decision":"block","reason":R}`, "仅 pre_verify;语义为续跑(agent 继续),不是阻止停止;受 agent.max_verify_nudges 限制;离线核验,触发时机未核验"},
	},
	InjectEvents:    []EventName{EventUserPromptSubmit},
	FullInjectEvent: EventUserPromptSubmit,
	MidTurnEvent:    "",
	MaxTier:         TierT0,
}
