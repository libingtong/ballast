package hermes

import (
	"encoding/json"

	"github.com/libingtong/ballast/internal/adapter/common"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Adapter implements ports.Adapter for model.ClientHermes.
type Adapter struct{}

var _ ports.Adapter = (*Adapter)(nil)

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// ID implements ports.Adapter.
func (*Adapter) ID() model.ClientID { return model.ClientHermes }

// Capabilities implements ports.Adapter.
func (*Adapter) Capabilities() model.CapabilitySet { return model.Capabilities(model.ClientHermes) }

// NativeEvents implements ports.Adapter.
func (*Adapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[model.ClientHermes] }

// ParseEvent implements ports.Adapter.
func (a *Adapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	ne, err := common.Lookup(model.ClientHermes, native)
	if err != nil {
		return nil, err
	}
	f, err := common.DecodeObject(stdin)
	if err != nil {
		return nil, err
	}
	ev := &model.Event{Client: model.ClientHermes, Name: ne.Event, Native: native}
	common.FillCommon(ev, f)

	extra := f.Obj("extra")
	if s, ok := extra.Str("user_message"); ok {
		ev.Prompt = s
	}
	ev.IsFirstTurn = extra.Bool("is_first_turn")
	if ev.ToolResponse == nil {
		common.SetToolResponse(ev, f["result"])
	}
	if ev.ToolResponse == nil {
		common.SetToolResponse(ev, extra["result"])
	}
	common.FillTools(ev, f, a.NormalizeTool)
	return ev, nil
}

// NormalizeTool implements ports.Adapter.
func (a *Adapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	name := rawName
	switch rawName {
	case "terminal", "shell", "execute_shell":
		name = model.ToolBash
	case "read_file":
		name = model.ToolRead
	case "write_file", "create_file":
		name = model.ToolWrite
	case "patch_file", "edit_file":
		name = model.ToolEdit
	}
	tc, f, err := common.NewToolCall(rawName, name, input)
	if name == model.ToolBash {
		common.ApplyShell(&tc, f, model.ShellBash, "command", "cmd")
	} else {
		common.ApplyFile(&tc, f, "path")
	}
	return tc, err
}

// Render implements ports.Adapter.
func (a *Adapter) Render(ev *model.Event, d *model.Decision) ([]byte, error) {
	if d == nil || d.Empty() {
		return nil, nil
	}
	name, _ := common.EventOf(ev, d)
	if err := common.CheckAction(a.Capabilities(), name, d.Action); err != nil {
		return nil, err
	}
	if d.Action == model.ActionDeny {
		return common.RenderBlock(d.Reason)
	}
	if name != model.EventUserPromptSubmit {
		return nil, nil
	}
	text := d.Context
	if d.Action == model.ActionWarn {
		text = common.JoinReasonContext(d.Reason, d.Context)
	}
	if text == "" {
		return nil, nil
	}
	return common.RenderKV("context", text)
}
