package cursor

import (
	"encoding/json"
	"strings"

	"github.com/libingtong/ballast/internal/adapter/common"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Adapter implements ports.Adapter for model.ClientCursor.
type Adapter struct{}

var _ ports.Adapter = (*Adapter)(nil)

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// ID implements ports.Adapter.
func (*Adapter) ID() model.ClientID { return model.ClientCursor }

// Capabilities implements ports.Adapter.
func (*Adapter) Capabilities() model.CapabilitySet { return model.Capabilities(model.ClientCursor) }

// NativeEvents implements ports.Adapter.
func (*Adapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[model.ClientCursor] }

// ParseEvent implements ports.Adapter.
func (a *Adapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	ne, err := common.Lookup(model.ClientCursor, native)
	if err != nil {
		return nil, err
	}
	f, err := common.DecodeObject(stdin)
	if err != nil {
		return nil, err
	}
	ev := &model.Event{Client: model.ClientCursor, Name: ne.Event, Native: native}
	common.FillCommon(ev, f)

	if ev.Cwd == "" {
		if roots := rawStrings(f["workspace_roots"]); len(roots) > 0 {
			ev.Cwd = roots[0]
		}
	}
	// Cursor has no empty-agent_type compact agent: never mark an empty one.
	ev.AgentType = f.FirstStr("agent_type", "subagent_type")
	ev.HasAgentType = ev.AgentType != ""
	for _, k := range []string{"tool_output", "tool_result"} {
		if ev.ToolResponse != nil {
			break
		}
		common.SetToolResponse(ev, f[k])
	}
	if ev.DurationMS == nil {
		if ms, ok := f.Int64("duration"); ok {
			ev.DurationMS = &ms
		}
	}
	switch ne.Event {
	case model.EventAgentResponse:
		ev.LastAssistantMessage, ev.HasLastAssistantMessage = f.Str("text")
	case model.EventStop, model.EventSubagentStop:
		if !ev.HasLastAssistantMessage {
			ev.LastAssistantMessage, ev.HasLastAssistantMessage = f.Str("text")
		}
		if n, ok := f.Int64("loop_count"); ok && n > 0 {
			ev.StopHookActive = true
		}
	}

	if ne.ToolHint != "" {
		tc := a.hintedTool(ne.ToolHint, f)
		tc.UseID, _ = f.Str("tool_use_id")
		ev.Tool = &tc
		return ev, nil
	}
	common.FillTools(ev, f, a.NormalizeTool)
	return ev, nil
}

// hintedTool builds the ToolCall of a dedicated gate event from its
// top-level payload fields (NativeEvent.ToolHint).
func (a *Adapter) hintedTool(hint string, f common.Fields) model.ToolCall {
	switch hint {
	case model.ToolBash:
		in, _ := json.Marshal(map[string]string{"command": f.FirstStr("command")})
		tc, _ := a.NormalizeTool("Shell", in)
		return tc
	case model.ToolRead:
		in, _ := json.Marshal(map[string]string{"file_path": f.FirstStr("file_path", "path")})
		tc, _ := a.NormalizeTool("Read", in)
		return tc
	}
	// mcp__*: beforeMCPExecution carries server/tool_name and a (possibly
	// string-encoded) tool_input.
	tool := strings.TrimPrefix(f.FirstStr("tool_name"), "MCP:")
	tc, _, _ := common.NewToolCall(tool, mcpName(f.FirstStr("server", "server_name"), tool), common.UnwrapInput(f["tool_input"]))
	return tc
}

func mcpName(server, tool string) string {
	if strings.HasPrefix(tool, "mcp__") {
		return tool
	}
	if server == "" {
		return "mcp__" + tool
	}
	return "mcp__" + server + "__" + tool
}

func rawStrings(raw json.RawMessage) []string {
	var s []string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return s
}

// NormalizeTool implements ports.Adapter.
func (a *Adapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	name := rawName
	switch {
	case rawName == "Shell":
		name = model.ToolBash
	case rawName == "Task":
		name = model.ToolAgent
	case strings.HasPrefix(rawName, "MCP:"):
		name = mcpName("", strings.TrimPrefix(rawName, "MCP:"))
	}
	tc, f, err := common.NewToolCall(rawName, name, input)
	switch name {
	case model.ToolBash:
		common.ApplyShell(&tc, f, model.ShellBash, "command", "cmd")
	case model.ToolWrite, model.ToolRead, "Delete":
		common.ApplyFile(&tc, f, "path")
		if name == "Delete" {
			tc.TargetDeletes = tc.Targets
		}
	}
	return tc, err
}

// Render implements ports.Adapter.
func (a *Adapter) Render(ev *model.Event, d *model.Decision) ([]byte, error) {
	if d == nil || d.Empty() {
		return nil, nil
	}
	name, _ := common.EventOf(ev, d)
	cs := a.Capabilities()
	if err := common.CheckAction(cs, name, d.Action); err != nil {
		return nil, err
	}
	switch d.Action {
	case model.ActionDeny, model.ActionAsk:
		if name.IsStopFamily() {
			return common.RenderKV("followup_message", d.Reason)
		}
		return common.MarshalCompact(permission{string(d.Action), d.Reason, d.Reason})
	case model.ActionWarn:
		return injected(name, common.JoinReasonContext(d.Reason, d.Context))
	}
	if !cs.CanInject(name) {
		return nil, nil
	}
	return injected(name, d.Context)
}

// injected renders context text: sessionStart/postToolUse use
// additional_context, beforeSubmitPrompt the nested Claude shape.
func injected(name model.EventName, text string) ([]byte, error) {
	if text == "" {
		return nil, nil
	}
	if name == model.EventUserPromptSubmit {
		return common.RenderHSOContext(string(name), text)
	}
	return common.RenderKV("additional_context", text)
}

type permission struct {
	Permission   string `json:"permission"`
	UserMessage  string `json:"user_message"`
	AgentMessage string `json:"agent_message"`
}
