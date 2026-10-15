package install

import (
	"strings"
	"testing"
)

func TestEntryHash(t *testing.T) {
	const want = "0755a585f5488c296a4b9da043aecab0eb61d7e46eab92927b8fb8311f77e29f" // python reference
	variants := []string{
		`{"type":"command","command":"/opt/ballast/bin/ballast","args":["hook","--client","claude-code","--event","PreToolUse"]}`,
		`{"args":["hook","--client","claude-code","--event","PreToolUse"],"command":"/opt/ballast/bin/ballast","type":"command"}`,
		"{\n  \"type\": \"command\",\n\t\"command\": \"/opt/ballast/bin/ballast\",\n  \"args\": [ \"hook\", \"--client\", \"claude-code\", \"--event\", \"PreToolUse\" ]\n}\n",
	}
	for i, v := range variants {
		got, err := EntryHash([]byte(v))
		if err != nil || got != want {
			t.Errorf("variant %d: got %q, %v; want %q", i, got, err, want)
		}
	}
	other, _ := EntryHash([]byte(`{"type":"command","command":"/opt/ballast/bin/ballast","args":["hook","--client","claude-code","--event","Stop"]}`))
	if other == want {
		t.Error("different entries share a hash")
	}
	for _, bad := range []string{``, `{`, `{} {}`, `{"a":1} x`} {
		if _, err := EntryHash([]byte(bad)); err == nil {
			t.Errorf("EntryHash(%q) should fail", bad)
		}
	}
	// numbers keep their literal form: 1.0 and 1 are different entries
	a, _ := EntryHash([]byte(`{"n":1}`))
	b, _ := EntryHash([]byte(`{"n":1.0}`))
	if a == b {
		t.Error("number literals must not be normalized")
	}
}

func TestCodexTrustHash(t *testing.T) {
	// reference values computed with pinrule's algorithm (python json.dumps,
	// sort_keys, ensure_ascii=False, compact separators)
	cases := []struct {
		event, cmd string
		timeout    int
		want       string
	}{
		{"pre_tool_use", "/opt/ballast/bin/ballast hook --client codex --event PreToolUse", 30,
			"sha256:4c52bb0133a2ba33b43c322753624ddde3c79f0579230fa261ce0d698626f419"},
		{"stop", "'/opt/x y/ballast' hook --client codex --event Stop", 30,
			"sha256:be5c7ea31550a16ae9314f00d30f3b920929c3c17dacd79c5fcd1771fb06083a"},
		{"session_start", "/opt/日本/ballast \"q\" \\ \t hook\u2028 <&>", 5,
			"sha256:00c6207f6b4ce5f6704860af7c87f475a7358e56f20fc8b91e9b28ee12dcc729"},
	}
	for _, c := range cases {
		if got := CodexTrustHash(c.event, c.cmd, c.timeout); got != c.want {
			t.Errorf("CodexTrustHash(%q) = %s, want %s", c.event, got, c.want)
		}
	}
}

func TestSnakeEvent(t *testing.T) {
	for in, want := range map[string]string{
		"PreToolUse": "pre_tool_use", "PermissionRequest": "permission_request",
		"Stop": "stop", "SessionStart": "session_start", "UserPromptSubmit": "user_prompt_submit",
	} {
		if got := snakeEvent(in); got != want {
			t.Errorf("snakeEvent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	got := strings.Join(sortedKeys(map[string]int{"b": 1, "a": 2, "c": 3}), ",")
	if got != "a,b,c" {
		t.Fatal(got)
	}
}
