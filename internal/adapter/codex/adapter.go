package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/adapter/common"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Adapter implements ports.Adapter for model.ClientCodex.
type Adapter struct{}

var _ ports.Adapter = (*Adapter)(nil)

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// ID implements ports.Adapter.
func (*Adapter) ID() model.ClientID { return model.ClientCodex }

// Capabilities implements ports.Adapter.
func (*Adapter) Capabilities() model.CapabilitySet { return model.Capabilities(model.ClientCodex) }

// NativeEvents implements ports.Adapter.
func (*Adapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[model.ClientCodex] }

// ParseEvent implements ports.Adapter.
func (a *Adapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	ne, err := common.Lookup(model.ClientCodex, native)
	if err != nil {
		return nil, err
	}
	f, err := common.DecodeObject(stdin)
	if err != nil {
		return nil, err
	}
	ev := &model.Event{Client: model.ClientCodex, Name: ne.Event, Native: native}
	common.FillCommon(ev, f)
	common.FillTools(ev, f, a.NormalizeTool)
	return ev, nil
}

// patchKeys are the tool_input keys that may carry an apply_patch envelope.
var patchKeys = []string{"input", "command", "cmd", "patch", "diff"}

// argvApplyPatch reports whether a shell tool_input carries the argv form
// {"cmd"|"command": ["apply_patch"|"applypatch", …, "<envelope>"]}: argv[0]'s
// basename is apply_patch/applypatch and a later element contains
// "*** Begin Patch". The string form (`apply_patch <<EOF`) stays Bash.
func argvApplyPatch(input json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return false
	}
	for _, k := range [...]string{"cmd", "command"} {
		var argv []string
		if len(m[k]) == 0 || m[k][0] != '[' || json.Unmarshal(m[k], &argv) != nil || len(argv) < 2 {
			continue
		}
		base := argv[0]
		if i := strings.LastIndexAny(base, `/\`); i >= 0 {
			base = base[i+1:]
		}
		base = strings.TrimSuffix(strings.ToLower(base), ".exe")
		if base != "apply_patch" && base != "applypatch" {
			continue
		}
		for _, a := range argv[1:] {
			if strings.Contains(a, patchBegin) {
				return true
			}
		}
	}
	return false
}

// NormalizeTool implements ports.Adapter.
func (a *Adapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	switch rawName {
	case "apply_patch":
		return normalizePatch(rawName, input)
	case "exec_command", "shell", "shell_command", "local_shell", "Bash":
		if argvApplyPatch(input) {
			// ["apply_patch", "<envelope>"] is a file edit, not a shell
			// command (contract v1.3, architecture §4.4).
			return normalizePatch(rawName, input)
		}
		tc, f, err := common.NewToolCall(rawName, model.ToolBash, input)
		common.ApplyShell(&tc, f, model.ShellBash, "cmd", "command")
		return tc, err
	case "Task":
		tc, _, err := common.NewToolCall(rawName, model.ToolAgent, input)
		return tc, err
	}
	tc, f, err := common.NewToolCall(rawName, rawName, input)
	common.ApplyFile(&tc, f)
	return tc, err
}

// normalizePatch maps apply_patch onto Edit with every patch target.
func normalizePatch(rawName string, input json.RawMessage) (model.ToolCall, error) {
	input = bytes.TrimSpace(input)
	var envelope string
	var tc model.ToolCall
	var err error
	if len(input) > 0 && input[0] == '"' {
		// Bare string argument (captured on Codex 0.130).
		tc = model.ToolCall{Name: model.ToolEdit, RawName: rawName}
		if json.Unmarshal(input, &envelope) == nil {
			tc.Input = append(json.RawMessage(nil), input...)
		} else {
			err = fmt.Errorf("%w: tool_input of %q is not valid JSON", model.ErrBadInput, rawName)
		}
		tc.Fields = map[string][]string{"input": {envelope}}
	} else {
		var f common.Fields
		tc, f, err = common.NewToolCall(rawName, model.ToolEdit, input)
		envelope = patchText(f)
	}
	sum, perr := ParseApplyPatch(envelope)
	tc.Targets = sum.Targets
	tc.TargetAdds = sum.Adds
	tc.TargetDeletes = sum.Deletes
	tc.Edits = sum.Edits
	if err == nil && perr != nil {
		err = perr
	}
	return tc, err
}

// patchText finds the envelope in the first patchKeys entry that holds one,
// as a string or as an argv array (["apply_patch", "<envelope>"]).
func patchText(f common.Fields) string {
	var fallback string
	for _, k := range patchKeys {
		raw := f[k]
		if len(raw) == 0 {
			continue
		}
		var cand string
		switch raw[0] {
		case '"':
			cand, _ = f.Str(k)
		case '[':
			var argv []string
			if json.Unmarshal(raw, &argv) == nil {
				for _, s := range argv {
					if len(s) > len(cand) {
						cand = s
					}
				}
			}
		}
		if bytes.Contains([]byte(cand), []byte(patchBegin)) {
			return cand
		}
		if fallback == "" {
			fallback = cand
		}
	}
	return fallback
}

// Render implements ports.Adapter.
func (a *Adapter) Render(ev *model.Event, d *model.Decision) ([]byte, error) {
	if d == nil || d.Empty() {
		return nil, nil
	}
	name, native := common.EventOf(ev, d)
	cs := a.Capabilities()
	if err := common.CheckAction(cs, name, d.Action); err != nil {
		return nil, err
	}
	event := string(name)
	switch d.Action {
	case model.ActionDeny:
		switch {
		case name == model.EventPreToolUse && native == "PermissionRequest":
			return common.MarshalCompact(permissionRequest{hookSpecificPermission{
				HookEventName: native,
				Decision:      permissionDecision{Behavior: "deny", Message: d.Reason},
			}})
		case name == model.EventPreToolUse:
			return common.RenderHSODecision(event, "deny", d.Reason)
		}
		return common.RenderBlock(d.Reason)
	case model.ActionWarn:
		return additionalContext(event, common.JoinReasonContext(d.Reason, d.Context))
	}
	if !cs.CanInject(name) {
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

type permissionDecision struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message"`
}

type hookSpecificPermission struct {
	HookEventName string             `json:"hookEventName"`
	Decision      permissionDecision `json:"decision"`
}

type permissionRequest struct {
	HookSpecificOutput hookSpecificPermission `json:"hookSpecificOutput"`
}
