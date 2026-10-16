package hermes

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
		{"patch", `{"path":"/r/a.go","old_string":"x","new_string":"y","replace_all":true}`, "Edit", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Targets, []string{"/r/a.go"}) ||
				!reflect.DeepEqual(tc.Edits, []model.FileEdit{{Path: "/r/a.go", OldString: "x", NewString: "y"}}) {
				t.Errorf("replace mode: %+v", tc)
			}
		}},
		{"patch", `{"mode":"patch","patch":"*** Begin Patch\n*** Update File: /r/a.go\n@@\n-x\n+y\n*** Add File: /r/b.go\n+z\n*** Delete File: /r/c.go\n*** End Patch"}`, "Edit", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Targets, []string{"/r/a.go", "/r/b.go", "/r/c.go"}) ||
				!reflect.DeepEqual(tc.TargetAdds, []string{"/r/b.go"}) ||
				!reflect.DeepEqual(tc.TargetDeletes, []string{"/r/c.go"}) ||
				!reflect.DeepEqual(tc.Edits, []model.FileEdit{
					{Path: "/r/a.go", OldString: "x", NewString: "y"}, {Path: "/r/b.go", NewString: "z"}, {Path: "/r/c.go"}}) {
				t.Errorf("V4A mode: %+v", tc)
			}
			if got := tc.Fields["patch"]; len(got) != 1 {
				t.Errorf("patch text must stay visible to input_regex: %v", tc.Fields)
			}
		}},
		{"patch", `{"mode":"patch","path":"/r/p.go","patch":"*** Begin Patch\n*** Update File: /r/a.go\n+y\n*** End Patch"}`, "Edit", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Targets, []string{"/r/p.go", "/r/a.go"}) {
				t.Errorf("path plus envelope targets: %v", tc.Targets)
			}
		}},
		{"patch", `{"mode":"patch","patch":"*** Begin Patch\n*** Move File: /r/a.go -> /r/b.go\n*** End Patch"}`, "Edit", func(t *testing.T, tc model.ToolCall) {
			if !reflect.DeepEqual(tc.Targets, []string{"/r/a.go", "/r/b.go"}) {
				t.Errorf("Move File targets: %v", tc.Targets)
			}
		}},
		{"execute_code", `{"code":"open('/r/a','w').write('x')"}`, "execute_code", func(t *testing.T, tc model.ToolCall) {
			if tc.Fields["code"][0] != "open('/r/a','w').write('x')" || len(tc.Targets) != 0 || tc.Shell != "" {
				t.Errorf("execute_code stays raw: %+v", tc)
			}
		}},
		{"search_files", `{"pattern":"x","path":"/r"}`, "search_files", nil},
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

// TestPathIsAuthoritative: Hermes' file tools read only args["path"] and
// ignore other keys, so an agent can send a decoy file_path/notebook_path
// next to the real path. The real path must stay in Targets (first) and be
// the Edit path, and a tamper-style path matcher must still fire.
func TestPathIsAuthoritative(t *testing.T) {
	const v4a = `*** Begin Patch\n*** Update File: /r/v.go\n+y\n*** End Patch`
	tests := []struct {
		raw, in string
		targets []string
		edit    *model.FileEdit // expected Edits[0] when non-nil
	}{
		{"write_file", `{"path":"~/.ballast/rules.json","file_path":"src/ok.go","content":"c"}`,
			[]string{"~/.ballast/rules.json", "src/ok.go"}, &model.FileEdit{Path: "~/.ballast/rules.json", NewString: "c"}},
		{"create_file", `{"path":"~/.ballast/rules.json","file_path":"src/ok.go","notebook_path":"n.ipynb","content":"c"}`,
			[]string{"~/.ballast/rules.json", "src/ok.go", "n.ipynb"}, &model.FileEdit{Path: "~/.ballast/rules.json", NewString: "c"}},
		{"patch", `{"path":"~/.ballast/rules.json","file_path":"src/ok.go","old_string":"o","new_string":"n"}`,
			[]string{"~/.ballast/rules.json", "src/ok.go"}, &model.FileEdit{Path: "~/.ballast/rules.json", OldString: "o", NewString: "n"}},
		{"patch", `{"mode":"patch","path":"~/.ballast/rules.json","file_path":"src/ok.go","patch":"` + v4a + `"}`,
			[]string{"~/.ballast/rules.json", "src/ok.go", "/r/v.go"}, &model.FileEdit{Path: "~/.ballast/rules.json"}},
		{"read_file", `{"path":"~/.ballast/rules.json","file_path":"src/ok.go"}`,
			[]string{"~/.ballast/rules.json", "src/ok.go"}, nil},
		// duplicate spellings collapse
		{"write_file", `{"path":"a.go","file_path":"a.go","content":"c"}`, []string{"a.go"}, &model.FileEdit{Path: "a.go", NewString: "c"}},
		// without path the other spellings still count (Hermes would reject the call)
		{"write_file", `{"file_path":"src/ok.go","content":"c"}`, []string{"src/ok.go"}, &model.FileEdit{Path: "src/ok.go", NewString: "c"}},
		{"patch", `{"file_path":"src/ok.go","old_string":"o","new_string":"n"}`, []string{"src/ok.go"}, &model.FileEdit{Path: "src/ok.go", OldString: "o", NewString: "n"}},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := New().NormalizeTool(tc.raw, json.RawMessage(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Targets, tc.targets) {
				t.Errorf("targets %v, want %v", got.Targets, tc.targets)
			}
			if tc.edit != nil && (len(got.Edits) == 0 || got.Edits[0] != *tc.edit) {
				t.Errorf("edits %+v, want first %+v", got.Edits, *tc.edit)
			}
			if len(tc.targets) > 0 && strings.Contains(tc.targets[0], ".ballast/") {
				if len(got.Targets) == 0 || got.Targets[0] != tc.targets[0] {
					t.Errorf("tamper path must lead targets: %v", got.Targets)
				}
			}
		})
	}
}

func TestNormalizePatchTolerance(t *testing.T) {
	a := New()
	// Missing arguments: still an Edit, no panic, no phantom edit.
	for _, in := range []string{``, `null`, `{}`, `{"mode":"patch"}`, `{"mode":"patch","patch":""}`, `{"patch":7}`, `[]`, `"x"`} {
		tc, _ := a.NormalizeTool("patch", json.RawMessage(in))
		if tc.Name != "Edit" || tc.RawName != "patch" || len(tc.Targets) != 0 || len(tc.Edits) != 0 {
			t.Errorf("%q: %+v", in, tc)
		}
	}
	// A malformed envelope reports an error but keeps the partial targets, so
	// the hook still evaluates path rules against them.
	tc, err := a.NormalizeTool("patch", json.RawMessage(`{"mode":"patch","patch":"*** Update File: /r/a.go\n+x\n"}`))
	if err == nil || !reflect.DeepEqual(tc.Targets, []string{"/r/a.go"}) {
		t.Errorf("partial envelope: %+v err=%v", tc, err)
	}
	// Replace-mode fields next to an envelope are both reported.
	tc, _ = a.NormalizeTool("patch", json.RawMessage(`{"path":"/r/p.go","old_string":"o","new_string":"n","patch":"*** Begin Patch\n*** Add File: /r/q.go\n+z\n*** End Patch"}`))
	if len(tc.Edits) != 2 || tc.Edits[0].Path != "/r/p.go" || tc.Edits[1].Path != "/r/q.go" {
		t.Errorf("edits: %+v", tc.Edits)
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
		t.Errorf("Stop is not a hermes native event (pre_verify is): %v", err)
	}
	if _, err := a.ParseEvent("subagent_stop", []byte(`{}`)); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("subagent_stop is not mapped: %v", err)
	}

	// pre_verify (hooks test payload shape, Hermes 9a0a162536).
	ev, err = a.ParseEvent("pre_verify", []byte(`{"hook_event_name":"pre_verify","tool_name":null,"tool_input":null,"session_id":"h","cwd":"/r","profile":"default","extra":{"platform":"cli","model":"m","coding":true,"attempt":0,"final_response":"All done.","changed_paths":["src/app.tsx"]}}`))
	if err != nil || ev.Name != model.EventStop || ev.Native != "pre_verify" || ev.SessionID != "h" ||
		ev.LastAssistantMessage != "All done." || !ev.HasLastAssistantMessage || ev.StopHookActive || ev.Tool != nil {
		t.Errorf("pre_verify: %+v %v", ev, err)
	}
	ev, err = a.ParseEvent("pre_verify", []byte(`{"session_id":"h","extra":{"attempt":2,"final_response":""}}`))
	if err != nil || !ev.StopHookActive || !ev.HasLastAssistantMessage || ev.LastAssistantMessage != "" {
		t.Errorf("pre_verify attempt>0 / empty final_response: %+v %v", ev, err)
	}
	ev, err = a.ParseEvent("pre_verify", []byte(`{"extra":{"attempt":"x","final_response":5}}`))
	if err != nil || ev.StopHookActive || ev.HasLastAssistantMessage {
		t.Errorf("pre_verify mistyped extra: %+v %v", ev, err)
	}
	ev, err = a.ParseEvent("pre_verify", []byte(`{"extra":"nope"}`))
	if err != nil || ev.Name != model.EventStop || ev.HasLastAssistantMessage {
		t.Errorf("pre_verify extra not an object: %+v %v", ev, err)
	}

	// pre_tool_call with the patch tool: the envelope's targets reach the event.
	ev, err = a.ParseEvent("pre_tool_call", []byte(`{"hook_event_name":"pre_tool_call","tool_name":"patch","tool_input":{"mode":"patch","patch":"*** Begin Patch\n*** Update File: ~/.ballast/rules.json\n-a\n+b\n*** End Patch"},"session_id":"h"}`))
	if err != nil || ev.Tool == nil || ev.Tool.Name != "Edit" || !reflect.DeepEqual(ev.Tool.Targets, []string{"~/.ballast/rules.json"}) {
		t.Errorf("pre_tool_call patch: %+v %v", ev, err)
	}
	ev, err = a.ParseEvent("pre_tool_call", []byte(`{"tool_name":"execute_code","tool_input":{"code":"print(1)"}}`))
	if err != nil || ev.Tool == nil || ev.Tool.Name != "execute_code" {
		t.Errorf("pre_tool_call execute_code: %+v %v", ev, err)
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
		{"pre_verify deny continues", "pre_verify", model.ActionDeny, "[r] keep going", "", `{"decision":"block","reason":"[r] keep going"}` + "\n", false},
		{"pre_verify warn unsupported", "pre_verify", model.ActionWarn, "r", "", "", true},
		{"pre_verify ask unsupported", "pre_verify", model.ActionAsk, "r", "", "", true},
		{"pre_verify context ignored", "pre_verify", model.ActionNone, "", "c", "", false},
		{"pre_verify log", "pre_verify", model.ActionLog, "r", "", "", false},
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

func TestNativeTable(t *testing.T) {
	a := New()
	got := map[string]model.EventName{}
	for _, ne := range a.NativeEvents() {
		got[ne.Native] = ne.Event
		if !ne.Install || !ne.Verified {
			t.Errorf("%s: install=%v verified=%v, want both true (offline-verified, docs/facts/hermes-cases.json)", ne.Native, ne.Install, ne.Verified)
		}
	}
	want := map[string]model.EventName{
		"on_session_start": model.EventSessionStart,
		"pre_llm_call":     model.EventUserPromptSubmit,
		"pre_tool_call":    model.EventPreToolUse,
		"post_tool_call":   model.EventPostToolUse,
		"pre_verify":       model.EventStop,
		"on_session_end":   model.EventSessionEnd,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("native events %v, want %v", got, want)
	}
	cs := a.Capabilities()
	cell, ok := cs.Lookup(model.EventStop, model.ActionDeny)
	if !ok || !cell.Verified {
		t.Errorf("Stop deny must be supported and verified: %+v", cell)
	}
	if _, ok := cs.Lookup(model.EventStop, model.ActionWarn); ok {
		t.Error("Stop warn must stay unsupported")
	}
	if cs.CanInject(model.EventStop) {
		t.Error("Stop cannot carry injected context")
	}
}

func FuzzNormalizeTool(f *testing.F) {
	for _, s := range []string{
		`{"command":"sleep 300"}`,
		`{"path":"/r/a.go","old_string":"x","new_string":"y"}`,
		`{"mode":"patch","patch":"*** Begin Patch\n*** Update File: a\n+x\n*** Move File: a -> b\n*** End Patch"}`,
		`{"mode":"patch","path":"p","patch":"*** Delete File: \u0000\n*** Add File: z\n+1"}`,
		`{"code":"print(1)"}`, `"str"`, `[1]`, `null`, ``,
	} {
		for _, name := range []string{"terminal", "patch", "write_file", "read_file", "execute_code", "patch_file"} {
			f.Add(name, s)
		}
	}
	f.Fuzz(func(t *testing.T, name, input string) {
		tc, _ := New().NormalizeTool(name, json.RawMessage(input))
		if tc.RawName != name {
			t.Fatalf("raw name %q != %q", tc.RawName, name)
		}
		if name == "patch" || name == "patch_file" || name == "edit_file" {
			if tc.Name != model.ToolEdit {
				t.Fatalf("name %q", tc.Name)
			}
			in := map[string]bool{}
			for _, p := range tc.Targets {
				if in[p] {
					t.Fatalf("duplicate target %q in %v", p, tc.Targets)
				}
				in[p] = true
			}
			for _, p := range append(append([]string{}, tc.TargetAdds...), tc.TargetDeletes...) {
				if !in[p] {
					t.Fatalf("%q not in targets %v", p, tc.Targets)
				}
			}
		}
	})
}
