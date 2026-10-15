package claudecode

import (
	"encoding/json"

	"github.com/libingtong/ballast/internal/adapter/common"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Adapter implements ports.Adapter for model.ClientClaude.
type Adapter struct{}

var _ ports.Adapter = (*Adapter)(nil)

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// ID implements ports.Adapter.
func (*Adapter) ID() model.ClientID { return model.ClientClaude }

// Capabilities implements ports.Adapter.
func (*Adapter) Capabilities() model.CapabilitySet { return model.Capabilities(model.ClientClaude) }

// NativeEvents implements ports.Adapter.
func (*Adapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[model.ClientClaude] }

// ParseEvent implements ports.Adapter.
func (a *Adapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	ne, err := common.Lookup(model.ClientClaude, native)
	if err != nil {
		return nil, err
	}
	name := ne.Event
	f, err := common.DecodeObject(stdin)
	if err != nil {
		return nil, err
	}
	ev := &model.Event{Client: model.ClientClaude, Name: name, Native: native}
	common.FillCommon(ev, f)
	common.FillTools(ev, f, a.NormalizeTool)
	return ev, nil
}

// NormalizeTool implements ports.Adapter.
func (a *Adapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	name := rawName
	if rawName == "Task" {
		name = model.ToolAgent
	}
	tc, f, err := common.NewToolCall(rawName, name, input)
	switch name {
	case model.ToolBash:
		common.ApplyShell(&tc, f, model.ShellBash, "command")
	case model.ToolPowerShell:
		common.ApplyShell(&tc, f, model.ShellPowerShell, "command")
	default:
		common.ApplyFile(&tc, f)
	}
	return tc, err
}

// Render implements ports.Adapter. Shapes: model.Capabilities(claude-code).
func (a *Adapter) Render(ev *model.Event, d *model.Decision) ([]byte, error) {
	if d == nil || d.Empty() {
		return nil, nil
	}
	name, _ := common.EventOf(ev, d)
	if err := common.CheckAction(a.Capabilities(), name, d.Action); err != nil {
		return nil, err
	}
	event := string(name)
	switch d.Action {
	case model.ActionDeny, model.ActionAsk:
		if name == model.EventPreToolUse {
			return common.RenderHSODecision(event, string(d.Action), d.Reason)
		}
		return common.RenderBlock(d.Reason)
	case model.ActionWarn:
		if name.IsStopFamily() {
			return systemMessage(d.Reason)
		}
		return additionalContext(event, common.JoinReasonContext(d.Reason, d.Context))
	}
	if !a.Capabilities().CanInject(name) {
		return nil, nil
	}
	return additionalContext(event, d.Context)
}

func additionalContext(event, text string) ([]byte, error) {
	if text == "" {
		return nil, nil
	}
	return common.RenderHSOContext(event, text)
}

func systemMessage(text string) ([]byte, error) {
	if text == "" {
		return nil, nil
	}
	return common.RenderKV("systemMessage", text)
}
