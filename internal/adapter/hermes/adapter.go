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
	if ev.Name == model.EventStop {
		// pre_verify: the final answer rides in extra.final_response and
		// extra.attempt counts the continue nudges already issued this turn
		// (Cursor's loop_count analogue).
		if !ev.HasLastAssistantMessage {
			ev.LastAssistantMessage, ev.HasLastAssistantMessage = extra.Str("final_response")
		}
		if n, ok := extra.Int64("attempt"); ok && n > 0 {
			ev.StopHookActive = true
		}
	}
	common.FillTools(ev, f, a.NormalizeTool)
	return ev, nil
}

// NormalizeTool implements ports.Adapter.
//
// Tool names are Hermes' registry names (verified against Hermes 9a0a162536,
// tools/*.py): terminal, read_file, write_file, patch, search_files,
// execute_code. patch_file / edit_file / shell / execute_shell / create_file
// are older spellings kept for payloads that still use them.
// execute_code (a Python script) keeps its raw name: rules can match it by
// name, but no matcher can see which files the script writes (trust-model
// section 2.1, category 1).
func (a *Adapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	name := rawName
	switch rawName {
	case "terminal", "shell", "execute_shell":
		name = model.ToolBash
	case "read_file":
		name = model.ToolRead
	case "write_file", "create_file":
		name = model.ToolWrite
	case "patch", "patch_file", "edit_file":
		return normalizePatchTool(rawName, input)
	}
	tc, f, err := common.NewToolCall(rawName, name, input)
	if name == model.ToolBash {
		common.ApplyShell(&tc, f, model.ShellBash, "command", "cmd")
	} else {
		common.ApplyFile(&tc, f, "path")
	}
	return tc, err
}

// normalizePatchTool maps Hermes' `patch` tool onto Edit. Replace mode
// (path, old_string, new_string) is an ordinary Edit of `path`. V4A mode
// (mode "patch" with the envelope in `patch`) adds every file the envelope
// names. Hermes' handler checks `path` too in that mode, so the target list is
// the union, `path` first. The envelope is parsed whenever the `patch`
// argument is a string, whatever `mode` says: extra targets are safer than
// missed ones.
func normalizePatchTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	tc, f, err := common.NewToolCall(rawName, model.ToolEdit, input)
	common.ApplyFile(&tc, f, "path")
	if len(tc.Edits) == 1 && tc.Edits[0] == (model.FileEdit{}) {
		tc.Edits = nil // patch mode without path/old/new: nothing to report yet
	}
	envelope, ok := f.Str("patch")
	if !ok || envelope == "" {
		return tc, err
	}
	sum, perr := parsePatch(envelope)
	targets := tc.Targets
	for _, p := range sum.Targets {
		if !contains(targets, p) {
			targets = append(targets, p)
		}
	}
	tc.Targets = targets
	tc.TargetAdds = sum.Adds
	tc.TargetDeletes = sum.Deletes
	tc.Edits = append(tc.Edits, sum.Edits...)
	if err == nil && perr != nil {
		err = perr
	}
	return tc, err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
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
