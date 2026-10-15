package cursor

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestParseEventGateEvents(t *testing.T) {
	a := New()
	tests := []struct {
		name, native, in string
		check            func(t *testing.T, ev *model.Event)
	}{
		{"before shell", "beforeShellExecution",
			`{"conversation_id":"c1","command":"git commit -m x","cwd":"/r","workspace_roots":["/w"],"hook_event_name":"beforeShellExecution"}`,
			func(t *testing.T, ev *model.Event) {
				tc := ev.Tool
				if ev.Name != model.EventPreToolUse || ev.SessionID != "c1" || ev.Cwd != "/r" {
					t.Errorf("event: %+v", ev)
				}
				if tc.Name != "Bash" || tc.RawName != "Shell" || tc.Shell != model.ShellBash || tc.Command != "git commit -m x" {
					t.Errorf("tool: %+v", tc)
				}
			}},
		{"before shell without cwd uses workspace root", "beforeShellExecution",
			`{"conversation_id":"c1","command":"ls","workspace_roots":["/w","/x"]}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Cwd != "/w" {
					t.Errorf("cwd = %q", ev.Cwd)
				}
			}},
		{"before shell missing command", "beforeShellExecution", `{"conversation_id":"c1"}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Tool == nil || ev.Tool.Name != "Bash" || ev.Tool.Command != "" {
					t.Errorf("tool: %+v", ev.Tool)
				}
			}},
		{"before read file", "beforeReadFile", `{"conversation_id":"c1","file_path":"/r/a.go","content":"x"}`,
			func(t *testing.T, ev *model.Event) {
				tc := ev.Tool
				if tc.Name != "Read" || !reflect.DeepEqual(tc.Targets, []string{"/r/a.go"}) {
					t.Errorf("tool: %+v", tc)
				}
			}},
		{"before read file path key", "beforeReadFile", `{"path":"/r/b.go"}`,
			func(t *testing.T, ev *model.Event) {
				if !reflect.DeepEqual(ev.Tool.Targets, []string{"/r/b.go"}) {
					t.Errorf("tool: %+v", ev.Tool)
				}
			}},
		{"before mcp string input", "beforeMCPExecution",
			`{"conversation_id":"c1","server":"github","tool_name":"get_issue","tool_input":"{\"issue\":\"7\",\"nested\":{\"k\":\"v\"}}"}`,
			func(t *testing.T, ev *model.Event) {
				tc := ev.Tool
				if tc.Name != "mcp__github__get_issue" || tc.RawName != "get_issue" {
					t.Errorf("tool: %+v", tc)
				}
				if !reflect.DeepEqual(tc.Fields, map[string][]string{"issue": {"7"}, "nested.k": {"v"}}) {
					t.Errorf("fields: %v", tc.Fields)
				}
			}},
		{"before mcp object input, no server", "beforeMCPExecution",
			`{"tool_name":"MCP:search","tool_input":{"q":"x"}}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Tool.Name != "mcp__search" || ev.Tool.Fields["q"][0] != "x" {
					t.Errorf("tool: %+v", ev.Tool)
				}
			}},
		{"before mcp already prefixed", "beforeMCPExecution", `{"server":"s","tool_name":"mcp__s__t"}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Tool.Name != "mcp__s__t" {
					t.Errorf("tool: %+v", ev.Tool)
				}
			}},
		{"pre tool use", "preToolUse",
			`{"conversation_id":"c1","subagent_id":"sa","subagent_type":"explore","tool_name":"Shell","tool_input":{"command":"ls"},"tool_use_id":"u"}`,
			func(t *testing.T, ev *model.Event) {
				if ev.AgentID != "sa" || ev.AgentType != "explore" || !ev.HasAgentType || !ev.IsSubagent() {
					t.Errorf("agent: %+v", ev)
				}
				if ev.Tool.Name != "Bash" || ev.Tool.RawName != "Shell" || ev.Tool.Command != "ls" || ev.Tool.UseID != "u" {
					t.Errorf("tool: %+v", ev.Tool)
				}
			}},
		{"pre tool use write", "preToolUse",
			`{"tool_name":"Write","tool_input":{"path":"/r/a.go","contents":"c"}}`,
			func(t *testing.T, ev *model.Event) {
				tc := ev.Tool
				if tc.Name != "Write" || !reflect.DeepEqual(tc.Targets, []string{"/r/a.go"}) ||
					!reflect.DeepEqual(tc.Edits, []model.FileEdit{{Path: "/r/a.go", NewString: "c"}}) {
					t.Errorf("tool: %+v", tc)
				}
			}},
		{"pre tool use delete and task", "preToolUse", `{"tool_name":"Delete","tool_input":{"path":"/r/a.go"}}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Tool.Name != "Delete" || !reflect.DeepEqual(ev.Tool.TargetDeletes, []string{"/r/a.go"}) {
					t.Errorf("tool: %+v", ev.Tool)
				}
				tc, _ := New().NormalizeTool("Task", nil)
				if tc.Name != "Agent" {
					t.Errorf("Task: %+v", tc)
				}
			}},
		{"pre tool use mcp name", "preToolUse", `{"tool_name":"MCP:linear_get","tool_input":{}}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Tool.Name != "mcp__linear_get" {
					t.Errorf("tool: %+v", ev.Tool)
				}
			}},
		{"post tool use", "postToolUse",
			`{"conversation_id":"c1","tool_name":"Shell","tool_input":{"command":"ls"},"tool_output":"{\"stdout\":\"a\\n\"}","duration":42}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Name != model.EventPostToolUse || ev.ToolResponse == nil || ev.ToolResponseText == "" {
					t.Errorf("response: %+v", ev)
				}
				if ev.DurationMS == nil || *ev.DurationMS != 42 {
					t.Errorf("duration: %v", ev.DurationMS)
				}
			}},
		{"before submit prompt", "beforeSubmitPrompt", `{"conversation_id":"c1","prompt":"hello"}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Name != model.EventUserPromptSubmit || ev.Prompt != "hello" || ev.Tool != nil {
					t.Errorf("event: %+v", ev)
				}
			}},
		{"after agent response", "afterAgentResponse", `{"conversation_id":"c1","text":"All done"}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Name != model.EventAgentResponse || ev.LastAssistantMessage != "All done" || !ev.HasLastAssistantMessage {
					t.Errorf("event: %+v", ev)
				}
			}},
		{"stop without text", "stop", `{"conversation_id":"c1","status":"completed","loop_count":0}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Name != model.EventStop || ev.HasLastAssistantMessage || ev.StopHookActive {
					t.Errorf("event: %+v", ev)
				}
			}},
		{"stop on loop", "stop", `{"conversation_id":"c1","loop_count":2,"last_assistant_message":"x"}`,
			func(t *testing.T, ev *model.Event) {
				if !ev.StopHookActive || !ev.HasLastAssistantMessage {
					t.Errorf("event: %+v", ev)
				}
			}},
		{"subagent stop empty type is not compact agent", "subagentStop", `{"subagent_id":"s","subagent_type":""}`,
			func(t *testing.T, ev *model.Event) {
				if ev.IsInternalCompactAgent() || ev.AgentID != "s" {
					t.Errorf("event: %+v", ev)
				}
			}},
		{"session start", "sessionStart", `{"conversation_id":"c1","transcript_path":null}`,
			func(t *testing.T, ev *model.Event) {
				if ev.Name != model.EventSessionStart || ev.TranscriptPath != "" {
					t.Errorf("event: %+v", ev)
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := a.ParseEvent(tc.native, []byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if ev.Client != model.ClientCursor || ev.Native != tc.native {
				t.Errorf("identity: %+v", ev)
			}
			tc.check(t, ev)
		})
	}
}

func TestParseEventErrors(t *testing.T) {
	a := New()
	if _, err := a.ParseEvent("PreToolUse", []byte(`{}`)); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("Claude-cased event on cursor: %v", err)
	}
	if _, err := a.ParseEvent("preToolUse", []byte(`[]`)); !errors.Is(err, model.ErrBadInput) {
		t.Errorf("bad input: %v", err)
	}
}

func TestRender(t *testing.T) {
	a := New()
	deny := `{"permission":"deny","user_message":"r","agent_message":"r"}` + "\n"
	tests := []struct {
		name    string
		native  string
		action  model.Action
		reason  string
		context string
		want    string
		fail    bool
	}{
		{"pre deny", "preToolUse", model.ActionDeny, "r", "", deny, false},
		{"shell deny", "beforeShellExecution", model.ActionDeny, "r", "", deny, false},
		{"mcp deny", "beforeMCPExecution", model.ActionDeny, "r", "", deny, false},
		{"read deny", "beforeReadFile", model.ActionDeny, "r", "", deny, false},
		{"shell ask", "beforeShellExecution", model.ActionAsk, "r", "", `{"permission":"ask","user_message":"r","agent_message":"r"}` + "\n", false},
		{"pre warn unsupported", "preToolUse", model.ActionWarn, "r", "", "", true},
		{"stop deny is followup", "stop", model.ActionDeny, "r", "", `{"followup_message":"r"}` + "\n", false},
		{"subagent stop deny is followup", "subagentStop", model.ActionDeny, "r", "", `{"followup_message":"r"}` + "\n", false},
		{"stop ask unsupported", "stop", model.ActionAsk, "r", "", "", true},
		{"stop warn unsupported", "stop", model.ActionWarn, "r", "", "", true},
		{"session start", "sessionStart", model.ActionNone, "", "full", `{"additional_context":"full"}` + "\n", false},
		{"post tool use warn", "postToolUse", model.ActionWarn, "r", "c", `{"additional_context":"r\n\nc"}` + "\n", false},
		{"submit prompt warn", "beforeSubmitPrompt", model.ActionWarn, "r", "",
			`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"r"}}` + "\n", false},
		{"submit prompt deny unsupported", "beforeSubmitPrompt", model.ActionDeny, "r", "", "", true},
		{"agent response nothing", "afterAgentResponse", model.ActionNone, "", "c", "", false},
		{"agent response warn unsupported", "afterAgentResponse", model.ActionWarn, "r", "", "", true},
		{"pre context dropped", "preToolUse", model.ActionNone, "", "c", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ne, _ := model.LookupNative(model.ClientCursor, tc.native)
			out, err := a.Render(&model.Event{Name: ne.Event, Native: tc.native},
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

func TestNormalizeToolBadInput(t *testing.T) {
	tc, err := New().NormalizeTool("Shell", json.RawMessage(`"ls"`))
	if !errors.Is(err, model.ErrBadInput) || tc.Name != "Bash" {
		t.Errorf("%+v %v", tc, err)
	}
}
