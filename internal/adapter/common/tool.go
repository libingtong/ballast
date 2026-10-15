package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// NewToolCall starts a ToolCall for rawName/input: Name, RawName, Input
// (only when input is valid JSON) and Fields are set. The returned Fields view is nil when input is absent or not
// an object; a non-object, non-null input yields model.ErrBadInput (the
// ToolCall is still usable).
func NewToolCall(rawName, name string, input json.RawMessage) (model.ToolCall, Fields, error) {
	tc := model.ToolCall{Name: name, RawName: rawName}
	input = bytes.TrimSpace(input)
	if len(input) == 0 || string(input) == "null" {
		return tc, nil, nil
	}
	// Input keeps only valid JSON so the ToolCall stays marshalable for
	// direct callers (replay/audit); the parse error is still returned.
	if json.Valid(input) {
		tc.Input = append(json.RawMessage(nil), input...)
	}
	tc.Fields = FlattenStrings(input)
	if input[0] != '{' {
		return tc, nil, fmt.Errorf("%w: tool_input of %q is not an object", model.ErrBadInput, rawName)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err != nil {
		return tc, nil, fmt.Errorf("%w: tool_input of %q: %v", model.ErrBadInput, rawName, err)
	}
	return tc, Fields(m), nil
}

// ApplyShell marks tc as a shell tool and extracts the command from the first
// usable key (string, or argv array joined with shell quoting).
func ApplyShell(tc *model.ToolCall, f Fields, kind model.ShellKind, keys ...string) {
	tc.Shell = kind
	tc.Command = ShellCommand(f, keys...)
	tc.RunInBackground = f.Bool("run_in_background")
}

// ShellCommand returns the command text from the first of keys holding a
// non-empty string or a non-empty string array (JoinArgv).
func ShellCommand(f Fields, keys ...string) string {
	for _, k := range keys {
		raw := f[k]
		if len(raw) == 0 {
			continue
		}
		switch raw[0] {
		case '"':
			if s, _ := f.Str(k); s != "" {
				return s
			}
		case '[':
			var argv []string
			if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
				if s := JoinArgv(argv); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// JoinArgv joins argv with POSIX shell quoting. A single element is returned
// unquoted (it is already a command line).
func JoinArgv(argv []string) string {
	if len(argv) == 1 {
		return argv[0]
	}
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = ShellQuote(a)
	}
	return strings.Join(q, " ")
}

// ShellQuote quotes s for a POSIX shell (same alphabet as Python's shlex.quote).
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("_@%+=:,./-", c) >= 0) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// ApplyFile fills Targets and Edits for file tools from the payload fields.
// Targets come from file_path / notebook_path / extraPathKeys. Edits:
//
//	Edit, Write   ⇒ one {path, old_string, new_string|content}
//	MultiEdit     ⇒ one per edits[] element
//	NotebookEdit  ⇒ {notebook_path, "", new_source}
//
// Other tools only get Targets.
func ApplyFile(tc *model.ToolCall, f Fields, extraPathKeys ...string) {
	keys := append([]string{"file_path", "notebook_path"}, extraPathKeys...)
	path := f.FirstStr(keys...)
	if path != "" {
		tc.Targets = []string{path}
	}
	switch tc.Name {
	case model.ToolEdit, model.ToolWrite:
		tc.Edits = []model.FileEdit{{
			Path:      path,
			OldString: f.FirstStr("old_string", "old_str"),
			NewString: f.FirstStr("new_string", "new_str", "content", "contents", "text", "file_text"),
		}}
	case model.ToolNotebookEdit:
		tc.Edits = []model.FileEdit{{Path: path, NewString: f.FirstStr("new_source")}}
	case model.ToolMultiEdit:
		for _, ef := range ObjectArray(f["edits"]) {
			if ef == nil {
				continue
			}
			o, _ := ef.Str("old_string")
			n, _ := ef.Str("new_string")
			tc.Edits = append(tc.Edits, model.FileEdit{Path: path, OldString: o, NewString: n})
		}
	}
}

// ---------------------------------------------------------------------------
// Rendering helpers

// hookSpecific is the Claude-style nested output object.
type hookSpecific struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

type hookOutput struct {
	HookSpecificOutput hookSpecific `json:"hookSpecificOutput"`
}

// RenderHSODecision renders {"hookSpecificOutput":{"hookEventName":E,
// "permissionDecision":D,"permissionDecisionReason":R}}.
func RenderHSODecision(event, decision, reason string) ([]byte, error) {
	return MarshalCompact(hookOutput{hookSpecific{HookEventName: event, PermissionDecision: decision, PermissionDecisionReason: reason}})
}

// RenderHSOContext renders {"hookSpecificOutput":{"hookEventName":E,"additionalContext":C}}.
func RenderHSOContext(event, text string) ([]byte, error) {
	return MarshalCompact(hookOutput{hookSpecific{HookEventName: event, AdditionalContext: text}})
}

// RenderBlock renders {"decision":"block","reason":R}.
func RenderBlock(reason string) ([]byte, error) {
	return MarshalCompact(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"block", reason})
}

// RenderKV renders a single-key object {key: value}.
func RenderKV(key, value string) ([]byte, error) {
	return MarshalCompact(map[string]string{key: value})
}

// EventOf returns the normalized and native event names of a render call,
// preferring ev and falling back to the decision.
func EventOf(ev *model.Event, d *model.Decision) (model.EventName, string) {
	var name model.EventName
	var native string
	if ev != nil {
		name, native = ev.Name, ev.Native
	}
	if name == "" {
		name = d.Event
	}
	if native == "" {
		native = d.Native
	}
	if native == "" {
		native = string(name)
	}
	return name, native
}

// CheckAction returns an error when d.Action is a blocking/advisory action
// that client cs does not support on event name. Empty and log actions are
// always acceptable.
func CheckAction(cs model.CapabilitySet, name model.EventName, a model.Action) error {
	if a == model.ActionNone || a == model.ActionLog {
		return nil
	}
	if _, ok := cs.Lookup(name, a); !ok {
		return fmt.Errorf("adapter: %s does not support action %q on event %s", cs.Client, a, name)
	}
	return nil
}
