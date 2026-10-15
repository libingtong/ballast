package codex

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

const envelope = "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** Add File: b.go\n+z\n*** Delete File: c.go\n*** End Patch\n"

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestNormalizeToolApplyPatch(t *testing.T) {
	wantTargets := []string{"a.go", "b.go", "c.go"}
	tests := []struct {
		name string
		in   string
	}{
		{"bare string", jsonString(envelope)},
		{"input key", `{"input":` + jsonString(envelope) + `}`},
		{"command key", `{"command":` + jsonString(envelope) + `}`},
		{"patch key", `{"patch":` + jsonString(envelope) + `}`},
		{"diff key", `{"diff":` + jsonString(envelope) + `}`},
		{"command argv", `{"command":["apply_patch",` + jsonString(envelope) + `]}`},
		{"wrong key first", `{"input":"nothing","patch":` + jsonString(envelope) + `}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New().NormalizeTool("apply_patch", json.RawMessage(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != model.ToolEdit || got.RawName != "apply_patch" {
				t.Errorf("name %q raw %q", got.Name, got.RawName)
			}
			if !reflect.DeepEqual(got.Targets, wantTargets) ||
				!reflect.DeepEqual(got.TargetAdds, []string{"b.go"}) ||
				!reflect.DeepEqual(got.TargetDeletes, []string{"c.go"}) {
				t.Errorf("targets %v adds %v deletes %v", got.Targets, got.TargetAdds, got.TargetDeletes)
			}
			if len(got.Edits) != 3 || got.Edits[0] != (model.FileEdit{Path: "a.go", OldString: "x", NewString: "y"}) {
				t.Errorf("edits %+v", got.Edits)
			}
			if len(got.Fields) == 0 {
				t.Error("Fields empty: input_regex has nothing to read")
			}
			if string(got.Input) != tc.in {
				t.Errorf("Input not verbatim: %s", got.Input)
			}
		})
	}
}

func TestNormalizeToolApplyPatchMalformed(t *testing.T) {
	for _, in := range []string{``, `null`, `{}`, `{"input":5}`, `"just text"`, `{"input":"*** Begin Patch\n*** Update File: a.go\n+x\n"}`, `[1]`, `"abc`, `{"input":`} {
		got, err := New().NormalizeTool("apply_patch", json.RawMessage(in))
		if err == nil {
			t.Errorf("%s: want error", in)
		}
		if got.Name != model.ToolEdit {
			t.Errorf("%s: ToolCall must stay usable, got %+v", in, got)
		}
		if _, err := json.Marshal(got); err != nil {
			t.Errorf("%s: ToolCall must stay marshalable: %v", in, err)
		}
	}
	// A truncated envelope still yields the targets that were parsed.
	got, _ := New().NormalizeTool("apply_patch", json.RawMessage(`{"input":"*** Begin Patch\n*** Update File: a.go\n+x\n"}`))
	if !reflect.DeepEqual(got.Targets, []string{"a.go"}) {
		t.Errorf("partial targets: %v", got.Targets)
	}
}

func TestNormalizeToolShell(t *testing.T) {
	tests := []struct {
		raw, in, cmd string
	}{
		{"exec_command", `{"cmd":"ls -la"}`, "ls -la"},
		{"exec_command", `{"cmd":["bash","-lc","sleep 300"]}`, "bash -lc 'sleep 300'"},
		{"exec_command", `{"cmd":"","command":"pwd"}`, "pwd"},
		{"shell", `{"command":["bash","-lc","git status"]}`, "bash -lc 'git status'"},
		{"shell", `{"command":"echo hi","workdir":"/r"}`, "echo hi"},
		{"shell_command", `{"command":"echo hi"}`, "echo hi"},
		{"local_shell", `{"command":["ls"]}`, "ls"},
		{"Bash", `{"command":"echo hi"}`, "echo hi"},
		{"exec_command", `{"cmd":7}`, ""},
		{"exec_command", ``, ""},
	}
	for _, tc := range tests {
		got, err := New().NormalizeTool(tc.raw, json.RawMessage(tc.in))
		if err != nil {
			t.Errorf("%s %s: %v", tc.raw, tc.in, err)
		}
		if got.Name != model.ToolBash || got.Shell != model.ShellBash || got.Command != tc.cmd || got.RawName != tc.raw {
			t.Errorf("%s %s: %+v", tc.raw, tc.in, got)
		}
	}
	if _, err := New().NormalizeTool("exec_command", json.RawMessage(`"ls"`)); !errors.Is(err, model.ErrBadInput) {
		t.Errorf("non-object shell input: %v", err)
	}
}

func TestNormalizeToolOthers(t *testing.T) {
	a := New()
	if tc, _ := a.NormalizeTool("Task", nil); tc.Name != model.ToolAgent {
		t.Errorf("Task: %+v", tc)
	}
	tc, _ := a.NormalizeTool("mcp__github__get_issue", json.RawMessage(`{"n":"1"}`))
	if tc.Name != "mcp__github__get_issue" || tc.Fields["n"][0] != "1" {
		t.Errorf("mcp passthrough: %+v", tc)
	}
	tc, _ = a.NormalizeTool("Write", json.RawMessage(`{"file_path":"/a","content":"c"}`))
	if tc.Name != "Write" || len(tc.Targets) != 1 || len(tc.Edits) != 1 {
		t.Errorf("Write passthrough: %+v", tc)
	}
	if tc, err := a.NormalizeTool("", nil); err != nil || tc.Name != "" {
		t.Errorf("empty name: %+v %v", tc, err)
	}
}

func TestParseEvent(t *testing.T) {
	a := New()
	ev, err := a.ParseEvent("PermissionRequest", []byte(`{"session_id":"s","cwd":"/r","hook_event_name":"PermissionRequest","tool_name":"apply_patch","tool_use_id":"u","tool_input":{"input":`+jsonString(envelope)+`}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != model.EventPreToolUse || ev.Native != "PermissionRequest" || ev.Client != model.ClientCodex {
		t.Errorf("event: %+v", ev)
	}
	if ev.Tool == nil || ev.Tool.Name != model.ToolEdit || ev.Tool.UseID != "u" || len(ev.Tool.Targets) != 3 {
		t.Errorf("tool: %+v", ev.Tool)
	}

	ev, err = a.ParseEvent("PostToolUse", []byte(`{"session_id":"s","tool_name":"exec_command","tool_input":{"cmd":"ls"},"tool_response":"a.go\nb.go"}`))
	if err != nil || ev.ToolResponseText != "a.go\nb.go" || ev.Tool.Command != "ls" {
		t.Errorf("post: %+v %v", ev, err)
	}

	ev, err = a.ParseEvent("Stop", []byte(`{"session_id":"s","stop_hook_active":false,"last_assistant_message":"done"}`))
	if err != nil || ev.Name != model.EventStop || ev.LastAssistantMessage != "done" || ev.Tool != nil {
		t.Errorf("stop: %+v %v", ev, err)
	}

	// A malformed patch must not make ParseEvent fail.
	ev, err = a.ParseEvent("PreToolUse", []byte(`{"tool_name":"apply_patch","tool_input":"garbage"}`))
	if err != nil || ev.Tool == nil || ev.Tool.Name != model.ToolEdit {
		t.Errorf("malformed patch: %+v %v", ev, err)
	}

	if _, err := a.ParseEvent("SubagentStop", []byte(`{}`)); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("SubagentStop does not exist on codex: %v", err)
	}
	if _, err := a.ParseEvent("PreToolUse", []byte(`[]`)); !errors.Is(err, model.ErrBadInput) {
		t.Errorf("array: %v", err)
	}
}

func TestRender(t *testing.T) {
	a := New()
	tests := []struct {
		name    string
		native  string
		event   model.EventName
		action  model.Action
		reason  string
		context string
		want    string
		fail    bool
	}{
		{"pre deny", "PreToolUse", model.EventPreToolUse, model.ActionDeny, "no", "",
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no"}}` + "\n", false},
		{"permission deny", "PermissionRequest", model.EventPreToolUse, model.ActionDeny, "no", "",
			`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"no"}}}` + "\n", false},
		{"permission deny ignores context", "PermissionRequest", model.EventPreToolUse, model.ActionDeny, "no", "ctx",
			`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"no"}}}` + "\n", false},
		{"pre warn unsupported", "PreToolUse", model.EventPreToolUse, model.ActionWarn, "w", "", "", true},
		{"pre ask unsupported", "PreToolUse", model.EventPreToolUse, model.ActionAsk, "w", "", "", true},
		{"pre context dropped", "PreToolUse", model.EventPreToolUse, model.ActionNone, "", "ctx", "", false},
		{"ups warn joins", "UserPromptSubmit", model.EventUserPromptSubmit, model.ActionWarn, "r", "c",
			`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"r\n\nc"}}` + "\n", false},
		{"ups deny unsupported", "UserPromptSubmit", model.EventUserPromptSubmit, model.ActionDeny, "r", "", "", true},
		{"session start", "SessionStart", model.EventSessionStart, model.ActionNone, "", "full",
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"full"}}` + "\n", false},
		{"stop warn unsupported", "Stop", model.EventStop, model.ActionWarn, "r", "", "", true},
		{"warn without text", "PostToolUse", model.EventPostToolUse, model.ActionWarn, "", "", "", false},
		{"log", "PreToolUse", model.EventPreToolUse, model.ActionLog, "r", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := a.Render(&model.Event{Name: tc.event, Native: tc.native},
				&model.Decision{Action: tc.action, Reason: tc.reason, Context: tc.context})
			if tc.fail != (err != nil) {
				t.Fatalf("err = %v, want fail=%v", err, tc.fail)
			}
			if string(out) != tc.want {
				t.Errorf("got %q, want %q", out, tc.want)
			}
		})
	}
}
