package claudecode

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestNormalizeTool(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		in   string
		want model.ToolCall
	}{
		{"bash", "Bash", `{"command":"sleep 300","run_in_background":true,"description":"d"}`,
			model.ToolCall{Name: "Bash", RawName: "Bash", Shell: model.ShellBash, Command: "sleep 300", RunInBackground: true,
				Fields: map[string][]string{"command": {"sleep 300"}, "description": {"d"}}}},
		{"powershell stays powershell", "PowerShell", `{"command":"Start-Sleep 300"}`,
			model.ToolCall{Name: "PowerShell", RawName: "PowerShell", Shell: model.ShellPowerShell, Command: "Start-Sleep 300",
				Fields: map[string][]string{"command": {"Start-Sleep 300"}}}},
		{"bash command mistyped", "Bash", `{"command":7}`,
			model.ToolCall{Name: "Bash", RawName: "Bash", Shell: model.ShellBash}},
		{"bash no input", "Bash", ``,
			model.ToolCall{Name: "Bash", RawName: "Bash", Shell: model.ShellBash}},
		{"task is agent", "Task", `{"prompt":"p","subagent_type":"x"}`,
			model.ToolCall{Name: "Agent", RawName: "Task", Fields: map[string][]string{"prompt": {"p"}, "subagent_type": {"x"}}}},
		{"agent unchanged", "Agent", `{"prompt":"p"}`,
			model.ToolCall{Name: "Agent", RawName: "Agent", Fields: map[string][]string{"prompt": {"p"}}}},
		{"edit", "Edit", `{"file_path":"/a.go","old_string":"x","new_string":"y"}`,
			model.ToolCall{Name: "Edit", RawName: "Edit", Targets: []string{"/a.go"},
				Edits:  []model.FileEdit{{Path: "/a.go", OldString: "x", NewString: "y"}},
				Fields: map[string][]string{"file_path": {"/a.go"}, "old_string": {"x"}, "new_string": {"y"}}}},
		{"multiedit", "MultiEdit", `{"file_path":"/a.go","edits":[{"old_string":"1","new_string":"2"},{"old_string":"3","new_string":"4"}]}`,
			model.ToolCall{Name: "MultiEdit", RawName: "MultiEdit", Targets: []string{"/a.go"},
				Edits: []model.FileEdit{{Path: "/a.go", OldString: "1", NewString: "2"}, {Path: "/a.go", OldString: "3", NewString: "4"}},
				Fields: map[string][]string{"file_path": {"/a.go"},
					"edits.old_string": {"1", "3"}, "edits.new_string": {"2", "4"}}}},
		{"write", "Write", `{"file_path":"/a.go","content":"hello"}`,
			model.ToolCall{Name: "Write", RawName: "Write", Targets: []string{"/a.go"},
				Edits:  []model.FileEdit{{Path: "/a.go", NewString: "hello"}},
				Fields: map[string][]string{"file_path": {"/a.go"}, "content": {"hello"}}}},
		{"notebook edit", "NotebookEdit", `{"notebook_path":"/n.ipynb","new_source":"print(1)","edit_mode":"replace"}`,
			model.ToolCall{Name: "NotebookEdit", RawName: "NotebookEdit", Targets: []string{"/n.ipynb"},
				Edits:  []model.FileEdit{{Path: "/n.ipynb", NewString: "print(1)"}},
				Fields: map[string][]string{"notebook_path": {"/n.ipynb"}, "new_source": {"print(1)"}, "edit_mode": {"replace"}}}},
		{"read", "Read", `{"file_path":"/a.go","offset":3}`,
			model.ToolCall{Name: "Read", RawName: "Read", Targets: []string{"/a.go"}, Fields: map[string][]string{"file_path": {"/a.go"}}}},
		{"mcp passthrough", "mcp__gh__issue", `{"n":"1"}`,
			model.ToolCall{Name: "mcp__gh__issue", RawName: "mcp__gh__issue", Fields: map[string][]string{"n": {"1"}}}},
		{"unknown tool", "Frobnicate", `{"x":["a","b"]}`,
			model.ToolCall{Name: "Frobnicate", RawName: "Frobnicate", Fields: map[string][]string{"x": {"a", "b"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New().NormalizeTool(tc.raw, json.RawMessage(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if tc.in != "" {
				want.Input = json.RawMessage(tc.in)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestNormalizeToolBadInput(t *testing.T) {
	for _, in := range []string{`"str"`, `[1]`, `{"a":`, `7`} {
		got, err := New().NormalizeTool("Bash", json.RawMessage(in))
		if !errors.Is(err, model.ErrBadInput) {
			t.Errorf("%s: err = %v", in, err)
		}
		if got.Name != "Bash" || got.Shell != model.ShellBash {
			t.Errorf("%s: unusable tool %+v", in, got)
		}
	}
}

func TestParseEventTolerance(t *testing.T) {
	a := New()
	// Every optional field mistyped: the event still parses, fields stay zero.
	in := `{"session_id":5,"transcript_path":[],"cwd":{},"prompt_id":true,"permission_mode":1,"effort":7,
		"agent_id":9,"agent_type":null,"tool_name":12,"tool_input":"x","tool_use_id":[],
		"tool_response":null,"duration_ms":"fast","stop_hook_active":"y","tool_calls":5}`
	ev, err := a.ParseEvent("PreToolUse", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if ev.SessionID != "" || ev.Cwd != "" || ev.Effort != "" || ev.AgentID != "" || ev.HasAgentType || ev.DurationMS != nil {
		t.Errorf("mistyped fields leaked: %+v", ev)
	}
	if ev.Tool == nil || ev.Tool.Name != "" {
		t.Errorf("Tool must exist but be empty: %+v", ev.Tool)
	}
	if ev.ReceivedAt.IsZero() == false {
		t.Error("ReceivedAt must be left to the hook")
	}

	// effort as string and as object.
	for in, want := range map[string]string{`{"effort":"high"}`: "high", `{"effort":{"level":"low"}}`: "low"} {
		ev, err := a.ParseEvent("UserPromptSubmit", []byte(in))
		if err != nil || ev.Effort != want {
			t.Errorf("%s: %q %v", in, ev.Effort, err)
		}
	}

	// Unknown keys are ignored.
	if _, err := a.ParseEvent("Stop", []byte(`{"zzz":{"a":[1,2]},"hook_event_name":"Stop"}`)); err != nil {
		t.Error(err)
	}
}

func TestParseEventErrors(t *testing.T) {
	a := New()
	if _, err := a.ParseEvent("NoSuchEvent", []byte(`{}`)); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("unknown event: %v", err)
	}
	for _, in := range []string{``, `null`, `[]`, `"x"`, `{`} {
		if _, err := a.ParseEvent("Stop", []byte(in)); !errors.Is(err, model.ErrBadInput) {
			t.Errorf("%q: %v", in, err)
		}
	}
	// Unknown event wins over bad input.
	if _, err := a.ParseEvent("NoSuchEvent", []byte(`[]`)); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("order: %v", err)
	}
}

func TestParseEventKinds(t *testing.T) {
	a := New()
	tests := []struct {
		native string
		want   model.EventName
	}{
		{"PreCompact", model.EventPreCompact},
		{"PostCompact", model.EventPostCompact},
		{"SubagentStart", model.EventSubagentStart},
		{"SessionEnd", model.EventSessionEnd},
		{"PostToolBatch", model.EventPostToolBatch},
	}
	for _, tc := range tests {
		ev, err := a.ParseEvent(tc.native, []byte(`{"session_id":"s"}`))
		if err != nil || ev.Name != tc.want || ev.Native != tc.native || ev.Client != model.ClientClaude {
			t.Errorf("%s: %+v %v", tc.native, ev, err)
		}
	}
	// Parse-only events are not installable natives.
	for _, ne := range a.NativeEvents() {
		if _, ok := parseOnly(ne.Native); ok {
			t.Errorf("%s leaked into NativeEvents", ne.Native)
		}
	}
}

func TestParsePostToolUseResponses(t *testing.T) {
	a := New()
	tests := []struct {
		name, in, text string
	}{
		{"bash object", `{"tool_name":"Bash","tool_input":{"command":"x"},"tool_response":{"stdout":"out","stderr":"err","interrupted":false}}`, "out\nerr"},
		{"string", `{"tool_name":"Bash","tool_response":"plain"}`, "plain"},
		{"agent object", `{"tool_name":"Agent","tool_response":{"status":"async_launched"}}`, `{"status":"async_launched"}`},
		{"missing", `{"tool_name":"Bash"}`, ""},
	}
	for _, tc := range tests {
		ev, err := a.ParseEvent("PostToolUse", []byte(tc.in))
		if err != nil || ev.ToolResponseText != tc.text {
			t.Errorf("%s: %q %v", tc.name, ev.ToolResponseText, err)
		}
	}
}

func TestRender(t *testing.T) {
	a := New()
	tests := []struct {
		name    string
		event   model.EventName
		action  model.Action
		reason  string
		context string
		want    string
		fail    bool
	}{
		{"pre deny with context ignores context", model.EventPreToolUse, model.ActionDeny, "no", "ctx",
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"no"}}` + "\n", false},
		{"pre warn joins", model.EventPreToolUse, model.ActionWarn, "r", "c",
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"r\n\nc"}}` + "\n", false},
		{"post tool failure warn", model.EventPostToolUseFailure, model.ActionWarn, "r", "",
			`{"hookSpecificOutput":{"hookEventName":"PostToolUseFailure","additionalContext":"r"}}` + "\n", false},
		{"ups deny", model.EventUserPromptSubmit, model.ActionDeny, "r", "", `{"decision":"block","reason":"r"}` + "\n", false},
		{"ups ask unsupported", model.EventUserPromptSubmit, model.ActionAsk, "r", "", "", true},
		{"stop deny", model.EventStop, model.ActionDeny, "r", "", `{"decision":"block","reason":"r"}` + "\n", false},
		{"stop warn is systemMessage only", model.EventStop, model.ActionWarn, "r", "c", `{"systemMessage":"r"}` + "\n", false},
		{"subagent stop deny", model.EventSubagentStop, model.ActionDeny, "r", "", `{"decision":"block","reason":"r"}` + "\n", false},
		{"subagent stop warn", model.EventSubagentStop, model.ActionWarn, "r", "", `{"systemMessage":"r"}` + "\n", false},
		{"stop warn empty", model.EventStop, model.ActionWarn, "", "", "", false},
		{"stop context dropped", model.EventStop, model.ActionNone, "", "c", "", false},
		{"post deny unsupported", model.EventPostToolUse, model.ActionDeny, "r", "", "", true},
		{"session start context", model.EventSessionStart, model.ActionNone, "", "full",
			`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"full"}}` + "\n", false},
		{"session start warn unsupported", model.EventSessionStart, model.ActionWarn, "r", "", "", true},
		{"non-ascii and html untouched", model.EventPreToolUse, model.ActionDeny, "<a> & 中", "",
			"{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"<a> & 中\"}}\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := &model.Event{Name: tc.event, Native: string(tc.event)}
			out, err := a.Render(ev, &model.Decision{Action: tc.action, Reason: tc.reason, Context: tc.context})
			if tc.fail != (err != nil) {
				t.Fatalf("err = %v, want fail=%v", err, tc.fail)
			}
			if string(out) != tc.want {
				t.Errorf("got %q, want %q", out, tc.want)
			}
		})
	}
}
