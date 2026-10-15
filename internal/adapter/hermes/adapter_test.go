package hermes

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestNormalizeTool(t *testing.T) {
	tests := []struct {
		raw, in, name string
		check         func(t *testing.T, tc model.ToolCall)
	}{
		{"terminal", `{"command":"sleep 300"}`, "Bash", func(t *testing.T, tc model.ToolCall) {
			if tc.Command != "sleep 300" || tc.Shell != model.ShellBash {
				t.Errorf("%+v", tc)
			}
		}},
		{"shell", `{"cmd":["ls","-la"]}`, "Bash", func(t *testing.T, tc model.ToolCall) {
			if tc.Command != "ls -la" {
				t.Errorf("%+v", tc)
			}
		}},
		{"execute_shell", `{"command":"pwd"}`, "Bash", nil},
		{"read_file", `{"path":"/r/a.go"}`, "Read", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Targets, []string{"/r/a.go"}) {
				t.Errorf("%+v", tc)
			}
		}},
		{"write_file", `{"path":"/r/a.go","content":"c"}`, "Write", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Edits, []model.FileEdit{{Path: "/r/a.go", NewString: "c"}}) {
				t.Errorf("%+v", tc)
			}
		}},
		{"create_file", `{"file_path":"/r/b.go","content":"c"}`, "Write", nil},
		{"patch_file", `{"path":"/r/a.go","old_string":"x","new_string":"y"}`, "Edit", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Edits, []model.FileEdit{{Path: "/r/a.go", OldString: "x", NewString: "y"}}) {
				t.Errorf("%+v", tc)
			}
		}},
		{"edit_file", `{"path":"/r/a.go","old_str":"x","new_str":"y"}`, "Edit", nil},
		{"web_search", `{"query":"q"}`, "web_search", func(t *testing.T, tc model.ToolCall) {
			if tc.Fields["query"][0] != "q" {
				t.Errorf("%+v", tc)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := New().NormalizeTool(tc.raw, json.RawMessage(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.name || got.RawName != tc.raw {
				t.Errorf("name %q raw %q", got.Name, got.RawName)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestParseEvent(t *testing.T) {
	a := New()
	ev, err := a.ParseEvent("pre_llm_call", []byte(`{"hook_event_name":"pre_llm_call","session_id":"h","cwd":"/r","extra":{"user_message":"hi","is_first_turn":true,"model":"m"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Name != model.EventUserPromptSubmit || ev.Prompt != "hi" || !ev.IsFirstTurn || ev.SessionID != "h" || ev.Tool != nil {
		t.Errorf("pre_llm_call: %+v", ev)
	}

	ev, err = a.ParseEvent("pre_llm_call", []byte(`{"extra":{"user_message":5,"is_first_turn":"yes"},"prompt":"top"}`))
	if err != nil || ev.Prompt != "top" || ev.IsFirstTurn {
		t.Errorf("mistyped extra: %+v %v", ev, err)
	}
	ev, err = a.ParseEvent("pre_llm_call", []byte(`{"extra":"nope"}`))
	if err != nil || ev.Prompt != "" {
		t.Errorf("extra not an object: %+v %v", ev, err)
	}

	ev, err = a.ParseEvent("post_tool_call", []byte(`{"session_id":"h","tool_name":"terminal","tool_input":{"command":"ls"},"extra":{"result":"a.go"}}`))
	if err != nil || ev.Name != model.EventPostToolUse || ev.ToolResponseText != "a.go" || ev.Tool.Command != "ls" {
		t.Errorf("post_tool_call: %+v %v", ev, err)
	}

	ev, err = a.ParseEvent("on_session_end", []byte(`{"session_id":"h"}`))
	if err != nil || ev.Name != model.EventSessionEnd {
		t.Errorf("on_session_end: %+v %v", ev, err)
	}

	if _, err := a.ParseEvent("Stop", []byte(`{}`)); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("Stop does not exist on hermes: %v", err)
	}
	if _, err := a.ParseEvent("pre_tool_call", []byte(`7`)); !errors.Is(err, model.ErrBadInput) {
		t.Errorf("bad input: %v", err)
	}
}

func TestRender(t *testing.T) {
	a := New()
	tests := []struct {
		name    string
		native  string
		action  model.Action
		reason  string
		context string
		want    string
		fail    bool
	}{
		{"deny", "pre_tool_call", model.ActionDeny, "r", "", `{"decision":"block","reason":"r"}` + "\n", false},
		{"ask unsupported", "pre_tool_call", model.ActionAsk, "r", "", "", true},
		{"warn on pre_tool_call unsupported", "pre_tool_call", model.ActionWarn, "r", "", "", true},
		{"context", "pre_llm_call", model.ActionNone, "", "full", `{"context":"full"}` + "\n", false},
		{"warn joins", "pre_llm_call", model.ActionWarn, "r", "c", `{"context":"r\n\nc"}` + "\n", false},
		{"deny on pre_llm_call unsupported", "pre_llm_call", model.ActionDeny, "r", "", "", true},
		{"post tool warn unsupported", "post_tool_call", model.ActionWarn, "r", "", "", true},
		{"post tool context ignored", "post_tool_call", model.ActionNone, "", "c", "", false},
		{"session start context ignored", "on_session_start", model.ActionNone, "", "c", "", false},
		{"log", "pre_tool_call", model.ActionLog, "r", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ne, _ := model.LookupNative(model.ClientHermes, tc.native)
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
