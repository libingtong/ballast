package hook

import (
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestParseArgs(t *testing.T) {
	ok := func(c, e string) Args { return Args{Client: model.ClientID(c), Native: e} }
	cases := []struct {
		name string
		argv []string
		want Args
		err  bool
	}{
		{"canonical", []string{"--client", "claude-code", "--event", "PreToolUse"}, ok("claude-code", "PreToolUse"), false},
		{"reversed", []string{"--event", "stop", "--client", "cursor"}, ok("cursor", "stop"), false},
		{"equals", []string{"--client=codex", "--event=PermissionRequest"}, ok("codex", "PermissionRequest"), false},
		{"mixed", []string{"--client=hermes", "--event", "pre_tool_call"}, ok("hermes", "pre_tool_call"), false},
		{"value with equals", []string{"--client", "x", "--event=a=b"}, ok("x", "a=b"), false},
		{"unknown client is not a syntax error", []string{"--client", "nope", "--event", "E"}, ok("nope", "E"), false},
		{"empty", nil, Args{}, true},
		{"missing event", []string{"--client", "codex"}, Args{}, true},
		{"missing client", []string{"--event", "Stop"}, Args{}, true},
		{"unknown flag", []string{"--client", "codex", "--event", "Stop", "--verbose"}, Args{}, true},
		{"unknown flag with value", []string{"--client", "codex", "--event", "Stop", "--x=1"}, Args{}, true},
		{"positional", []string{"hook", "--client", "codex", "--event", "Stop"}, Args{}, true},
		{"single dash", []string{"-client", "codex", "--event", "Stop"}, Args{}, true},
		{"flag without value", []string{"--client", "codex", "--event"}, Args{}, true},
		{"value is a flag", []string{"--client", "--event", "Stop"}, Args{}, true},
		{"empty value", []string{"--client=", "--event", "Stop"}, Args{}, true},
		{"empty separate value", []string{"--client", "", "--event", "Stop"}, Args{}, true},
		{"duplicate client", []string{"--client", "a", "--client", "b", "--event", "Stop"}, Args{}, true},
		{"duplicate event", []string{"--client", "a", "--event", "x", "--event=y"}, Args{}, true},
		{"bare double dash", []string{"--", "--client", "a", "--event", "b"}, Args{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseArgs(c.argv)
			if (err != nil) != c.err {
				t.Fatalf("err = %v, want error=%v", err, c.err)
			}
			if err == nil && !reflect.DeepEqual(got, c.want) {
				t.Errorf("args = %+v, want %+v", got, c.want)
			}
			if err != nil && got != (Args{}) {
				t.Errorf("args on error = %+v, want zero", got)
			}
		})
	}
}

func FuzzParseArgs(f *testing.F) {
	f.Add("--client", "claude-code", "--event", "PreToolUse")
	f.Add("--client=codex", "--event=Stop", "", "")
	f.Add("--event", "--client", "x", "")
	f.Add("", "", "", "")
	f.Add("--client", "a=b", "--event", "--")
	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		args, err := ParseArgs([]string{a, b, c, d})
		if err == nil && (args.Client == "" || args.Native == "") {
			t.Fatalf("accepted empty fields: %+v from %q", args, []string{a, b, c, d})
		}
	})
}
