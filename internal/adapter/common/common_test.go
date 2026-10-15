package common

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestDecodeObject(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		ok    bool
		nkeys int
	}{
		{"object", `{"a":1,"b":"x"}`, true, 2},
		{"empty object", `{}`, true, 0},
		{"whitespace", " \n{\"a\":1}\n", true, 1},
		{"bom", "\xEF\xBB\xBF{\"a\":1}", true, 1},
		{"array", `[1,2]`, false, 0},
		{"null", `null`, false, 0},
		{"string", `"x"`, false, 0},
		{"number", `7`, false, 0},
		{"empty", ``, false, 0},
		{"truncated", `{"a":`, false, 0},
		{"trailing garbage", `{"a":1} x`, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := DecodeObject([]byte(tc.in))
			if tc.ok {
				if err != nil || len(f) != tc.nkeys || f == nil {
					t.Fatalf("got %v, %v", f, err)
				}
				return
			}
			if !errors.Is(err, model.ErrBadInput) {
				t.Fatalf("err = %v, want ErrBadInput", err)
			}
		})
	}
}

func TestFieldAccessors(t *testing.T) {
	f, err := DecodeObject([]byte(`{
		"s":"hi","empty":"","n":7,"f":12.9,"big":1e30,"neg":-3,"t":true,"fs":"true","nul":null,
		"obj":{"level":"high"},"arr":[1],
		"conversation_id":"c","session_id":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := f.Str("s"); !ok || s != "hi" {
		t.Errorf("Str(s) = %q,%v", s, ok)
	}
	for _, k := range []string{"n", "t", "nul", "obj", "arr", "missing"} {
		if s, ok := f.Str(k); ok || s != "" {
			t.Errorf("Str(%s) = %q,%v; want miss", k, s, ok)
		}
	}
	if s, ok := f.Str("empty"); !ok || s != "" {
		t.Errorf("Str(empty) = %q,%v", s, ok)
	}
	if got := f.FirstStr("missing", "empty", "s"); got != "hi" {
		t.Errorf("FirstStr skips empties: %q", got)
	}
	if got := f.FirstStr("session_id", "conversation_id"); got != "c" {
		t.Errorf("FirstStr fallback: %q", got)
	}
	if !f.Bool("t") || f.Bool("fs") || f.Bool("n") || f.Bool("missing") {
		t.Error("Bool")
	}
	for k, want := range map[string]int64{"n": 7, "f": 12, "neg": -3} {
		if got, ok := f.Int64(k); !ok || got != want {
			t.Errorf("Int64(%s) = %d,%v want %d", k, got, ok, want)
		}
	}
	for _, k := range []string{"s", "big", "t", "nul", "missing", "obj"} {
		if _, ok := f.Int64(k); ok {
			t.Errorf("Int64(%s) should miss", k)
		}
	}
	if !f.Has("nul") || f.Has("zzz") {
		t.Error("Has")
	}
	if f.Obj("obj") == nil || f.Obj("arr") != nil || f.Obj("s") != nil {
		t.Error("Obj")
	}
	var nilF Fields
	if nilF.Obj("x") != nil || nilF.Bool("x") || nilF.FirstStr("x") != "" {
		t.Error("nil Fields must be safe")
	}
}

func TestEffort(t *testing.T) {
	tests := map[string]string{
		`{"effort":{"level":"medium"}}`: "medium",
		`{"effort":"high"}`:             "high",
		`{"effort":{"level":3}}`:        "",
		`{"effort":{}}`:                 "",
		`{"effort":7}`:                  "",
		`{"effort":null}`:               "",
		`{"effort":["x"]}`:              "",
		`{}`:                            "",
	}
	for in, want := range tests {
		f, _ := DecodeObject([]byte(in))
		if got := f.Effort(); got != want {
			t.Errorf("%s: Effort = %q, want %q", in, got, want)
		}
	}
}

func TestFillCommon(t *testing.T) {
	in := `{"conversation_id":"c1","subagent_id":"sa","agent_type":"","cwd":"/r","transcript_path":5,
	"prompt":"p","reason":"other","source":"compact","stop_hook_active":true,
	"last_assistant_message":"done","error":"Exit code 3\nout","is_interrupt":true,"duration_ms":12.0,
	"tool_response":{"stdout":"o","stderr":"e"},"effort":{"level":"low"},"permission_mode":"plan","prompt_id":"pid"}`
	f, _ := DecodeObject([]byte(in))
	var ev model.Event
	FillCommon(&ev, f)
	if ev.SessionID != "c1" || ev.AgentID != "sa" || !ev.HasAgentType || ev.AgentType != "" {
		t.Errorf("ids: %+v", ev)
	}
	if ev.Cwd != "/r" || ev.TranscriptPath != "" || ev.Prompt != "p" || ev.Reason != "other" || ev.Source != "compact" {
		t.Errorf("strings: %+v", ev)
	}
	if !ev.StopHookActive || !ev.HasLastAssistantMessage || ev.LastAssistantMessage != "done" {
		t.Errorf("stop: %+v", ev)
	}
	if ev.ExitCode == nil || *ev.ExitCode != 3 || !ev.IsInterrupt || ev.DurationMS == nil || *ev.DurationMS != 12 {
		t.Errorf("failure: %+v", ev)
	}
	if ev.ToolResponseText != "o\ne" || ev.Effort != "low" || ev.PermissionMode != "plan" || ev.PromptID != "pid" {
		t.Errorf("response/effort: %+v", ev)
	}

	// Everything mistyped ⇒ zero event, no panic.
	bad := `{"session_id":1,"cwd":[],"prompt":{},"stop_hook_active":"yes","last_assistant_message":null,
	"error":5,"is_interrupt":"no","duration_ms":"x","tool_response":null,"effort":false,"agent_type":3}`
	f, _ = DecodeObject([]byte(bad))
	ev = model.Event{}
	FillCommon(&ev, f)
	if !reflect.DeepEqual(ev, model.Event{}) {
		t.Errorf("mistyped fields must be ignored: %+v", ev)
	}
}

func TestFillTools(t *testing.T) {
	norm := func(name string, in json.RawMessage) (model.ToolCall, error) {
		return model.ToolCall{Name: "N:" + name, Input: in}, errors.New("ignored")
	}
	f, _ := DecodeObject([]byte(`{"tool_name":"Bash","tool_input":"{\"command\":\"ls\"}","tool_use_id":"u1",
		"tool_calls":[{"tool_name":"A","tool_use_id":"a"},5,null,{"tool_name":"B","tool_input":{"x":1}}]}`))
	ev := &model.Event{Name: model.EventPostToolBatch}
	FillTools(ev, f, norm)
	if ev.Tool == nil || ev.Tool.Name != "N:Bash" || ev.Tool.UseID != "u1" || string(ev.Tool.Input) != `{"command":"ls"}` {
		t.Errorf("tool: %+v", ev.Tool)
	}
	if len(ev.BatchCalls) != 2 || ev.BatchCalls[0].UseID != "a" || ev.BatchCalls[1].Name != "N:B" {
		t.Errorf("batch: %+v", ev.BatchCalls)
	}

	// Tool events always get a (possibly empty) Tool; other events without tool_name do not.
	f, _ = DecodeObject([]byte(`{}`))
	ev = &model.Event{Name: model.EventPreToolUse}
	FillTools(ev, f, norm)
	if ev.Tool == nil {
		t.Error("PreToolUse must carry a Tool")
	}
	ev = &model.Event{Name: model.EventStop}
	FillTools(ev, f, norm)
	if ev.Tool != nil || ev.BatchCalls != nil {
		t.Error("Stop must not carry a Tool")
	}
	f, _ = DecodeObject([]byte(`{"tool_calls":"nope"}`))
	FillTools(ev, f, norm)
	if ev.BatchCalls != nil {
		t.Error("mistyped tool_calls ignored")
	}
}

func TestUnwrapInput(t *testing.T) {
	tests := []struct{ in, want string }{
		{``, ``},
		{`null`, ``},
		{` {"a":1} `, `{"a":1}`},
		{`"{\"a\":1}"`, `{"a":1}`},
		{`"[1,2]"`, `[1,2]`},
		{`"*** Begin Patch"`, `"*** Begin Patch"`},
		{`"{broken"`, `"{broken"`},
		{`"plain"`, `"plain"`},
	}
	for _, tc := range tests {
		if got := string(UnwrapInput(json.RawMessage(tc.in))); got != tc.want {
			t.Errorf("UnwrapInput(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLookup(t *testing.T) {
	ne, err := Lookup(model.ClientCursor, "beforeShellExecution")
	if err != nil || ne.Event != model.EventPreToolUse || ne.ToolHint != "Bash" {
		t.Errorf("%+v %v", ne, err)
	}
	if _, err := Lookup(model.ClientCursor, "PreToolUse"); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("err = %v", err)
	}
	if _, err := Lookup("nope", "x"); !errors.Is(err, model.ErrUnknownEvent) {
		t.Errorf("err = %v", err)
	}
}

func TestFlattenStrings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string][]string
	}{
		{"flat", `{"command":"sleep 300","n":1,"b":true,"z":null}`, map[string][]string{"command": {"sleep 300"}}},
		{"nested", `{"a":{"b":{"c":"x"}}}`, map[string][]string{"a.b.c": {"x"}}},
		{"array of objects drops index", `{"edits":[{"new_string":"a"},{"new_string":"b","old_string":"c"}]}`,
			map[string][]string{"edits.new_string": {"a", "b"}, "edits.old_string": {"c"}}},
		{"array of strings", `{"args":["x","y"]}`, map[string][]string{"args": {"x", "y"}}},
		{"nested arrays", `{"m":[["a"],[{"k":"b"}]]}`, map[string][]string{"m": {"a"}, "m.k": {"b"}}},
		{"empty string kept", `{"a":""}`, map[string][]string{"a": {""}}},
		{"no strings", `{"a":1}`, nil},
		{"top-level scalar", `"x"`, nil},
		{"top-level array of objects", `[{"a":"1"}]`, map[string][]string{"a": {"1"}}},
		{"empty", ``, nil},
		{"invalid", `{`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FlattenStrings(json.RawMessage(tc.in)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlattenStringsDeepLeaves(t *testing.T) {
	// No depth cut-off (contract v1.3): deep leaves stay visible to
	// input_regex, and the cost stays linear.
	for _, depth := range []int{10, 200, 9000} {
		deep := strings.Repeat(`{"a":`, depth) + `"x"` + strings.Repeat(`}`, depth)
		got := FlattenStrings(json.RawMessage(deep))
		want := strings.TrimSuffix(strings.Repeat("a.", depth), ".")
		if len(got) != 1 || len(got[want]) != 1 || got[want][0] != "x" {
			t.Errorf("depth %d: leaf not collected (%d keys)", depth, len(got))
		}
	}
	arr := `{"k":` + strings.Repeat(`[`, 500) + `"y"` + strings.Repeat(`]`, 500) + `}`
	if got := FlattenStrings(json.RawMessage(arr)); len(got["k"]) != 1 {
		t.Errorf("deep array leaf: %v", got)
	}
}

func TestParseExitCode(t *testing.T) {
	ip := func(n int) *int { return &n }
	tests := []struct {
		in   string
		want *int
	}{
		{"Exit code 3\nout\nerr", ip(3)},
		{"Exit code 0", ip(0)},
		{"Exit code 127", ip(127)},
		{"Exit code -1\nx", ip(-1)},
		{"  \nExit code 2\r\nx", ip(2)},
		{"Exit code 3 extra", ip(3)},
		{"Exit code 3x", nil},
		{"Exit code ", nil},
		{"Exit code abc", nil},
		{"exit code 3", nil},
		{"out\nExit code 3", nil},
		{"Exit code 99999999999999999999", nil},
		{"", nil},
	}
	for _, tc := range tests {
		got := ParseExitCode(tc.in)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("ParseExitCode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestResponseText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"string", `"hello"`, "hello"},
		{"stdout+stderr", `{"stdout":"o","stderr":"e","interrupted":false}`, "o\ne"},
		{"empty stderr", `{"stdout":"hello","stderr":""}`, "hello"},
		{"only stderr", `{"stderr":"boom"}`, "boom"},
		{"both empty", `{"stdout":"","stderr":""}`, ""},
		{"object without streams", `{"status": "async_launched", "n": 1}`, `{"status":"async_launched","n":1}`},
		{"text blocks", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb"},
		{"non-text array", `[1, 2]`, `[1,2]`},
		{"mixed array", `[{"text":"a"},{"x":1}]`, `[{"text":"a"},{"x":1}]`},
		{"null", `null`, ""},
		{"empty", ``, ""},
		{"number", `42`, "42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResponseText(json.RawMessage(tc.in)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMarshalCompact(t *testing.T) {
	got, err := MarshalCompact(map[string]string{"a": "<b>&\"中文\""})
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":\"<b>&\\\"中文\\\"\"}\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := MarshalCompact(make(chan int)); err == nil {
		t.Error("unencodable value must error")
	}
}

func TestJoinReasonContext(t *testing.T) {
	tests := []struct{ r, c, want string }{
		{"", "", ""}, {"r", "", "r"}, {"", "c", "c"}, {"r", "c", "r\n\nc"},
	}
	for _, tc := range tests {
		if got := JoinReasonContext(tc.r, tc.c); got != tc.want {
			t.Errorf("(%q,%q) = %q", tc.r, tc.c, got)
		}
	}
}

func TestShellQuoteAndJoin(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"bash", "-lc", "sleep 300"}, "bash -lc 'sleep 300'"},
		{[]string{"echo", "it's"}, `echo 'it'"'"'s'`},
		{[]string{"ls", ""}, "ls ''"},
		{[]string{"a/b-c_d.e:f=1,2@3%4+5"}, "a/b-c_d.e:f=1,2@3%4+5"},
		{[]string{"git", "commit", "-m", "a;b"}, "git commit -m 'a;b'"},
		{[]string{"echo", "$HOME", "`x`", "*"}, "echo '$HOME' '`x`' '*'"},
		{[]string{"ls -la | wc"}, "ls -la | wc"}, // single element is a command line
		{nil, ""},
	}
	for _, tc := range tests {
		if got := JoinArgv(tc.argv); got != tc.want {
			t.Errorf("JoinArgv(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

func TestShellCommand(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"cmd":"ls","command":"pwd"}`, "ls"},
		{`{"cmd":"","command":"pwd"}`, "pwd"},
		{`{"cmd":["ls","a b"]}`, "ls 'a b'"},
		{`{"cmd":[],"command":"pwd"}`, "pwd"},
		{`{"cmd":7,"command":["echo","x"]}`, "echo x"},
		{`{"cmd":[1,2]}`, ""},
		{`{}`, ""},
	}
	for _, tc := range tests {
		f, _ := DecodeObject([]byte(tc.in))
		if got := ShellCommand(f, "cmd", "command"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewToolCall(t *testing.T) {
	tc, f, err := NewToolCall("Shell", "Bash", json.RawMessage(`{"command":"ls","d":{"e":"x"}}`))
	if err != nil || f == nil || tc.Name != "Bash" || tc.RawName != "Shell" {
		t.Fatalf("%+v %v %v", tc, f, err)
	}
	if !reflect.DeepEqual(tc.Fields, map[string][]string{"command": {"ls"}, "d.e": {"x"}}) {
		t.Errorf("fields: %v", tc.Fields)
	}
	for _, in := range []string{``, `null`} {
		tc, f, err := NewToolCall("X", "X", json.RawMessage(in))
		if err != nil || f != nil || tc.Input != nil || tc.Fields != nil {
			t.Errorf("%q: %+v %v %v", in, tc, f, err)
		}
	}
	tc, f, err = NewToolCall("X", "X", json.RawMessage(`["a"]`))
	if !errors.Is(err, model.ErrBadInput) || f != nil || tc.Name != "X" {
		t.Errorf("non-object: %+v %v %v", tc, f, err)
	}
	if _, _, err = NewToolCall("X", "X", json.RawMessage(`{"a":`)); !errors.Is(err, model.ErrBadInput) {
		t.Errorf("broken: %v", err)
	}
}

func TestApplyFile(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		in    string
		extra []string
		want  model.ToolCall
	}{
		{"edit", model.ToolEdit, `{"file_path":"/a","old_string":"x","new_string":"y"}`, nil,
			model.ToolCall{Targets: []string{"/a"}, Edits: []model.FileEdit{{Path: "/a", OldString: "x", NewString: "y"}}}},
		{"write", model.ToolWrite, `{"file_path":"/a","content":"c"}`, nil,
			model.ToolCall{Targets: []string{"/a"}, Edits: []model.FileEdit{{Path: "/a", NewString: "c"}}}},
		{"write with path key", model.ToolWrite, `{"path":"/a","contents":"c"}`, []string{"path"},
			model.ToolCall{Targets: []string{"/a"}, Edits: []model.FileEdit{{Path: "/a", NewString: "c"}}}},
		{"notebook", model.ToolNotebookEdit, `{"notebook_path":"/n.ipynb","new_source":"s"}`, nil,
			model.ToolCall{Targets: []string{"/n.ipynb"}, Edits: []model.FileEdit{{Path: "/n.ipynb", NewString: "s"}}}},
		{"multiedit", model.ToolMultiEdit, `{"file_path":"/a","edits":[{"old_string":"1","new_string":"2"},5,{"new_string":"4"}]}`, nil,
			model.ToolCall{Targets: []string{"/a"}, Edits: []model.FileEdit{{Path: "/a", OldString: "1", NewString: "2"}, {Path: "/a", NewString: "4"}}}},
		{"read", model.ToolRead, `{"file_path":"/a"}`, nil, model.ToolCall{Targets: []string{"/a"}}},
		{"other with path", "Glob", `{"file_path":"/a"}`, nil, model.ToolCall{Targets: []string{"/a"}}},
		{"edit without path keeps edit", model.ToolEdit, `{"old_string":"x"}`, nil,
			model.ToolCall{Edits: []model.FileEdit{{OldString: "x"}}}},
		{"mistyped", model.ToolEdit, `{"file_path":5,"old_string":[1],"new_string":null}`, nil,
			model.ToolCall{Edits: []model.FileEdit{{}}}},
		{"multiedit mistyped edits", model.ToolMultiEdit, `{"file_path":"/a","edits":"nope"}`, nil,
			model.ToolCall{Targets: []string{"/a"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var f Fields
			_ = json.Unmarshal([]byte(tc.in), (*map[string]json.RawMessage)(&f))
			got := model.ToolCall{Name: tc.tool}
			ApplyFile(&got, f, tc.extra...)
			want := tc.want
			want.Name = tc.tool
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestEventOfAndCheckAction(t *testing.T) {
	name, native := EventOf(&model.Event{Name: "A", Native: "a"}, &model.Decision{Event: "B", Native: "b"})
	if name != "A" || native != "a" {
		t.Errorf("%s %s", name, native)
	}
	name, native = EventOf(nil, &model.Decision{Event: "B"})
	if name != "B" || native != "B" {
		t.Errorf("%s %s", name, native)
	}
	cs := model.Capabilities(model.ClientCodex)
	if err := CheckAction(cs, model.EventPreToolUse, model.ActionAsk); err == nil {
		t.Error("ask on codex must fail")
	}
	if err := CheckAction(cs, model.EventPreToolUse, model.ActionDeny); err != nil {
		t.Error(err)
	}
	if err := CheckAction(cs, model.EventSessionEnd, model.ActionNone); err != nil {
		t.Error(err)
	}
}

func TestRenderHelpers(t *testing.T) {
	tests := []struct {
		got  func() ([]byte, error)
		want string
	}{
		{func() ([]byte, error) { return RenderHSODecision("PreToolUse", "deny", "r") },
			`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"r"}}` + "\n"},
		{func() ([]byte, error) { return RenderHSOContext("Stop", "c") },
			`{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"c"}}` + "\n"},
		{func() ([]byte, error) { return RenderBlock("r") }, `{"decision":"block","reason":"r"}` + "\n"},
		{func() ([]byte, error) { return RenderBlock("") }, `{"decision":"block","reason":""}` + "\n"},
		{func() ([]byte, error) { return RenderKV("k", "<v>") }, `{"k":"<v>"}` + "\n"},
	}
	for i, tc := range tests {
		out, err := tc.got()
		if err != nil || string(out) != tc.want {
			t.Errorf("%d: %q %v, want %q", i, out, err, tc.want)
		}
	}
}
