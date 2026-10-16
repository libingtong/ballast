package install

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func roundTrip(t *testing.T, tg Target, client model.ClientID, orig string) *PlanResult {
	t.Helper()
	spec := gspec(client)
	var cur []byte
	if orig != absent {
		cur = []byte(orig)
	}
	p, err := tg.Plan(cur, spec, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	m := manifestFrom(gBinary, p)
	r, err := tg.Remove(p.After, m)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	return r
}

func TestRoundTripPreservesFormatting(t *testing.T) {
	type client struct {
		id   model.ClientID
		tg   Target
		user map[string]any // a user hook in an event that ballast also uses
	}
	clients := []client{
		{model.ClientClaude, gClaudeT(), map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "/bin/user"}}}}}},
		{model.ClientCodex, gCodexHT(), map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/bin/user", "timeout": 5}}}}}},
		{model.ClientCursor, gCursorT(), map[string]any{"preToolUse": []any{map[string]any{"command": "/bin/user"}}}},
	}
	styles := map[string]func(v any) string{
		"two spaces":                func(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) + "\n" },
		"four spaces":               func(v any) string { b, _ := json.MarshalIndent(v, "", "    "); return string(b) + "\n" },
		"tabs, no trailing newline": func(v any) string { b, _ := json.MarshalIndent(v, "", "\t"); return string(b) },
		"compact":                   func(v any) string { b, _ := json.Marshal(v); return string(b) },
		"crlf": func(v any) string {
			b, _ := json.MarshalIndent(v, "", "  ")
			return strings.ReplaceAll(string(b), "\n", "\r\n") + "\r\n"
		},
	}
	for _, c := range clients {
		for sname, render := range styles {
			for _, withHooks := range []bool{false, true} {
				doc := map[string]any{"model": "opus", "env": map[string]any{"A": "é<&>"}, "n": json.Number("1.50e+3")}
				if c.id == model.ClientCursor {
					doc["version"] = 1
				}
				if withHooks {
					doc["hooks"] = c.user
				}
				orig := render(doc)
				r := roundTrip(t, c.tg, c.id, orig)
				if r.After == nil || string(r.After) != orig {
					t.Errorf("%s/%s/hooks=%v: round trip changed the file\n--- want\n%q\n--- got\n%q", c.id, sname, withHooks, orig, r.After)
				}
			}
		}
	}
	// odd but valid layouts of untouched parts survive verbatim
	odd := "{\n  \"a\": [1,2 ,3],\n  \"b\": {  \"c\":null },\n  \"s\": \"\\u00e9\\n\"\n}\n"
	if r := roundTrip(t, gClaudeT(), model.ClientClaude, odd); string(r.After) != odd {
		t.Errorf("odd layout changed: %q", r.After)
	}
	for _, empty := range []string{"{}\n", "{}"} {
		if r := roundTrip(t, gClaudeT(), model.ClientClaude, empty); string(r.After) != empty {
			t.Errorf("empty object %q became %q", empty, r.After)
		}
	}
}

func TestRoundTripAbsentFileIsDeleted(t *testing.T) {
	for _, tc := range []struct {
		tg     Target
		client model.ClientID
	}{{gClaudeT(), model.ClientClaude}, {gCodexHT(), model.ClientCodex}, {gCursorT(), model.ClientCursor}} {
		r := roundTrip(t, tc.tg, tc.client, absent)
		if r.After != nil {
			t.Errorf("%s: created file should be deleted, got %q", tc.client, r.After)
		}
		if !r.Changed() {
			t.Errorf("%s: Changed() must be true for a deletion", tc.client)
		}
		if len(r.Entries)+len(r.Files)+len(r.Flags) != 0 {
			t.Errorf("%s: nothing should stay recorded", tc.client)
		}
	}
}

func TestCreatedFileWithUserContentIsKept(t *testing.T) {
	tg := gClaudeT()
	p := planAfter(t, tg, absent, gspec(model.ClientClaude), nil)
	// the user adds a setting after install
	edited := strings.Replace(string(p.After), "{\n  \"hooks\"", "{\n  \"model\": \"opus\",\n  \"hooks\"", 1)
	r, err := tg.Remove([]byte(edited), manifestFrom(gBinary, p))
	if err != nil {
		t.Fatal(err)
	}
	if string(r.After) != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("got %q", r.After)
	}
}

func TestIdempotentPlan(t *testing.T) {
	for _, tc := range []struct {
		tg     Target
		client model.ClientID
	}{{gClaudeT(), model.ClientClaude}, {gCodexHT(), model.ClientCodex}, {gCursorT(), model.ClientCursor}} {
		spec := gspec(tc.client)
		p1, err := tc.tg.Plan(nil, spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		m := manifestFrom(gBinary, p1)
		p2, err := tc.tg.Plan(p1.After, spec, m)
		if err != nil {
			t.Fatal(err)
		}
		if string(p2.After) != string(p1.After) || p2.Changed() {
			t.Errorf("%s: second install changed the file", tc.client)
		}
		if !p2.Created {
			t.Errorf("%s: Created must be remembered across installs", tc.client)
		}
		if len(p2.Entries) != len(p1.Entries) {
			t.Errorf("%s: entries %d vs %d", tc.client, len(p2.Entries), len(p1.Entries))
		}
		for i := range p1.Entries {
			if !equalEntry(p1.Entries[i], p2.Entries[i]) {
				t.Errorf("%s: entry %d differs: %+v vs %+v", tc.client, i, p1.Entries[i], p2.Entries[i])
			}
		}
	}
}

func equalEntry(a, b model.ManagedEntry) bool {
	if a.Client != b.Client || a.File != b.File || a.Native != b.Native || a.Key != b.Key || a.Hash != b.Hash || a.Command != b.Command {
		return false
	}
	return strings.Join(a.Args, "\x00") == strings.Join(b.Args, "\x00")
}

func TestUserHooksAreNeverTouched(t *testing.T) {
	tg := gClaudeT()
	orig := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/bin/a"}]},
      {"matcher": "*", "hooks": [{"type": "command", "command": "/bin/b"}, {"type": "command", "command": "/bin/c", "async": true}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "/bin/s", "args": ["x"]}]}]
  }
}
`
	p := planAfter(t, tg, orig, gspec(model.ClientClaude), nil)
	var got map[string]any
	if err := json.Unmarshal(p.After, &got); err != nil {
		t.Fatal(err)
	}
	pre := got["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 3 {
		t.Fatalf("PreToolUse has %d groups, want 3 (two user + ours)", len(pre))
	}
	if !strings.Contains(string(p.After), `{"matcher": "Bash", "hooks": [{"type": "command", "command": "/bin/a"}]}`) {
		t.Error("untouched user group lost its formatting")
	}
	r, err := tg.Remove(p.After, manifestFrom(gBinary, p))
	if err != nil {
		t.Fatal(err)
	}
	if string(r.After) != orig {
		t.Errorf("uninstall did not restore the original\n%s", r.After)
	}
}

func TestInPlaceReplacementKeepsOrder(t *testing.T) {
	tg := gClaudeT()
	p1 := planAfter(t, tg, absent, gspec(model.ClientClaude), nil)
	// the user prepends a group before ours in PreToolUse
	var doc map[string]any
	_ = json.Unmarshal(p1.After, &doc)
	pre := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
	user := map[string]any{"matcher": "Edit", "hooks": []any{map[string]any{"type": "command", "command": "/bin/user"}}}
	doc["hooks"].(map[string]any)["PreToolUse"] = append([]any{user}, pre...)
	cur, _ := json.MarshalIndent(doc, "", "  ")

	spec := gspec(model.ClientClaude)
	spec.Binary = "/opt/ballast-v2/bin/ballast"
	p2, err := tg.Plan(cur, spec, manifestFrom(gBinary, p1))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(p2.After, &out); err != nil {
		t.Fatal(err)
	}
	groups := out["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(groups) != 2 {
		t.Fatalf("expected user group + ours, got %d groups", len(groups))
	}
	if groups[0].(map[string]any)["matcher"] != "Edit" {
		t.Error("user group must stay first")
	}
	h := groups[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if h["command"] != "/opt/ballast-v2/bin/ballast" {
		t.Errorf("ours was not replaced in place: %v", h)
	}
	var key string
	for _, e := range p2.Entries {
		if e.Native == "PreToolUse" {
			key = e.Key
			if e.Command != "/opt/ballast-v2/bin/ballast" {
				t.Errorf("manifest command %q", e.Command)
			}
		}
	}
	if key != "hooks.PreToolUse[1].hooks[0]" {
		t.Errorf("key = %q, want position after the user group", key)
	}
	// the old binary no longer appears anywhere
	if strings.Contains(string(p2.After), "/opt/ballast/bin/ballast") {
		t.Error("old binary path still present")
	}
}

func TestForeignModifiedEntryIsLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tg     Target
		client model.ClientID
		tweak  func(string) string
	}{
		{"claude args", gClaudeT(), model.ClientClaude, func(s string) string {
			return strings.Replace(s, `"PreToolUse"
            ]`, `"PreToolUse",
              "--extra"
            ]`, 1)
		}},
		{"codex timeout", gCodexHT(), model.ClientCodex, func(s string) string {
			return strings.Replace(s, `hook --client codex --event PreToolUse",
            "timeout": 30`, `hook --client codex --event PreToolUse",
            "timeout": 99`, 1)
		}},
		{"cursor extra field", gCursorT(), model.ClientCursor, func(s string) string {
			return strings.Replace(s, `hook --client cursor --event preToolUse"`, `hook --client cursor --event preToolUse",
        "timeout": 5`, 1)
		}},
	} {
		spec := gspec(tc.client)
		p1 := planAfter(t, tc.tg, absent, spec, nil)
		m := manifestFrom(gBinary, p1)
		edited := tc.tweak(string(p1.After))
		if edited == string(p1.After) {
			t.Fatalf("%s: tweak did not apply", tc.name)
		}
		p2, err := tc.tg.Plan([]byte(edited), spec, m)
		if err != nil {
			t.Fatal(err)
		}
		if string(p2.After) != edited {
			t.Errorf("%s: install must not touch a user-modified entry or add a duplicate", tc.name)
		}
		if len(p2.Warnings) != 1 || !strings.Contains(p2.Warnings[0], "modified by the user") {
			t.Errorf("%s: warnings = %v", tc.name, p2.Warnings)
		}
		// the original record stays so that uninstall can warn
		found := false
		for _, e := range p2.Entries {
			if e.Native == "PreToolUse" || e.Native == "preToolUse" {
				found = e.Hash != ""
			}
		}
		if !found {
			t.Errorf("%s: the old record of the modified entry was dropped", tc.name)
		}

		r, err := tc.tg.Remove([]byte(edited), manifestFrom(gBinary, p2))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Warnings) != 1 {
			t.Errorf("%s: uninstall warnings = %v", tc.name, r.Warnings)
		}
		if !strings.Contains(string(r.After), "PreToolUse") && !strings.Contains(string(r.After), "preToolUse") {
			t.Errorf("%s: the modified entry must survive uninstall", tc.name)
		}
		if len(r.Entries) != 1 {
			t.Errorf("%s: kept records = %d, want 1", tc.name, len(r.Entries))
		}
		if r.Entries[0].Native != "PreToolUse" && r.Entries[0].Native != "preToolUse" {
			t.Errorf("%s: kept record %+v", tc.name, r.Entries[0])
		}
		// the other events are gone
		if strings.Contains(string(r.After), "SessionStart") || strings.Contains(string(r.After), "sessionStart") {
			t.Errorf("%s: other owned entries should have been removed", tc.name)
		}
		if r.After == nil {
			t.Errorf("%s: file must not be deleted", tc.name)
		}
	}
}

func TestEntryWithoutManifestIsForeign(t *testing.T) {
	tg := gClaudeT()
	p1 := planAfter(t, tg, absent, gspec(model.ClientClaude), nil)
	// manifest lost: nothing can be proven to be ours
	p2, err := tg.Plan(p1.After, gspec(model.ClientClaude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(p2.After) != string(p1.After) {
		t.Error("file must be unchanged")
	}
	if len(p2.Warnings) != 10 {
		t.Errorf("warnings = %d, want 10", len(p2.Warnings))
	}
	if len(p2.Entries) != 0 {
		t.Error("nothing can be claimed without a manifest")
	}
}

func TestSubstringIsNeverOwnership(t *testing.T) {
	tg := gClaudeT()
	orig := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/bin/other","args":["hook","--client","claude-code","--event","PreToolUse","/opt/ballast/bin/ballast"]},{"type":"command","command":"/opt/ballast/bin/ballast-wrapper","args":["hook","--client","claude-code"]},{"type":"command","command":"sh -c '/opt/ballast/bin/ballast hook --client claude-code'"}]}]}}`
	p := planAfter(t, tg, orig, gspec(model.ClientClaude), nil)
	if len(p.Warnings) != 0 {
		t.Errorf("look-alikes must not count as ballast entries: %v", p.Warnings)
	}
	var doc map[string]any
	_ = json.Unmarshal(p.After, &doc)
	groups := doc["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(groups) != 2 {
		t.Errorf("our own group should have been appended, got %d groups", len(groups))
	}
}

func TestUserDeletedOurEntry(t *testing.T) {
	tg := gClaudeT()
	spec := gspec(model.ClientClaude)
	p1 := planAfter(t, tg, absent, spec, nil)
	m := manifestFrom(gBinary, p1)
	var doc map[string]any
	_ = json.Unmarshal(p1.After, &doc)
	delete(doc["hooks"].(map[string]any), "Stop")
	cur, _ := json.MarshalIndent(doc, "", "  ")
	// uninstall: no warning for something that is simply gone
	r, err := tg.Remove(cur, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 0 || len(r.Entries) != 0 {
		t.Errorf("warnings=%v entries=%v", r.Warnings, r.Entries)
	}
	// install: the entry is restored
	p2, err := tg.Plan(cur, spec, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p2.After), `"Stop"`) {
		t.Error("deleted entry was not restored")
	}
}

func TestOwnedGroupSharedWithUserHandler(t *testing.T) {
	tg := gClaudeT()
	spec := gspec(model.ClientClaude)
	p1 := planAfter(t, tg, absent, spec, nil)
	m := manifestFrom(gBinary, p1)
	var doc map[string]any
	_ = json.Unmarshal(p1.After, &doc)
	stop := doc["hooks"].(map[string]any)["Stop"].([]any)
	g := stop[0].(map[string]any)
	g["hooks"] = append(g["hooks"].([]any), map[string]any{"type": "command", "command": "/bin/mine"})
	g["note"] = "user key on the group"
	cur, _ := json.MarshalIndent(doc, "", "  ")
	r, err := tg.Remove(cur, m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(r.After, &out); err != nil {
		t.Fatal(err)
	}
	groups := out["hooks"].(map[string]any)["Stop"].([]any)
	hs := groups[0].(map[string]any)["hooks"].([]any)
	if len(groups) != 1 || len(hs) != 1 || hs[0].(map[string]any)["command"] != "/bin/mine" {
		t.Errorf("user handler must survive in its group: %s", r.After)
	}
	if len(out["hooks"].(map[string]any)) != 1 {
		t.Errorf("only Stop should remain: %s", r.After)
	}
}

func TestEmptyGroupWithUserKeysIsKept(t *testing.T) {
	tg := gClaudeT()
	spec := gspec(model.ClientClaude)
	p1 := planAfter(t, tg, absent, spec, nil)
	m := manifestFrom(gBinary, p1)
	edited := strings.Replace(string(p1.After), `"Stop": [
      {`, `"Stop": [
      {
        "comment": "mine",`, 1)
	r, err := tg.Remove([]byte(edited), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.After), `"comment": "mine"`) {
		t.Errorf("a group carrying user keys must be kept: %s", r.After)
	}
}

func TestParseFailuresRefuse(t *testing.T) {
	type target struct {
		tg     Target
		client model.ClientID
		event  string
	}
	targets := []target{
		{gClaudeT(), model.ClientClaude, "Stop"},
		{gCodexHT(), model.ClientCodex, "Stop"},
		{gCursorT(), model.ClientCursor, "stop"},
	}
	bad := map[string]string{
		"truncated":            `{"hooks": {`,
		"not json":             `hello`,
		"root array":           `[]`,
		"root string":          `"x"`,
		"hooks not object":     `{"hooks": []}`,
		"hooks null":           `{"hooks": null}`,
		"event not array":      `{"hooks": {"%s": {}}}`,
		"duplicate hooks":      `{"hooks": {}, "hooks": {}}`,
		"duplicate event":      `{"hooks": {"%s": [], "%s": []}}`,
		"trailing comma":       `{"a": 1,}`,
		"comment":              "{\n// c\n}",
		"single quotes":        `{'a': 1}`,
		"concatenated objects": `{} {}`,
	}
	for name, tpl := range bad {
		for _, tc := range targets {
			content := strings.ReplaceAll(tpl, "%s", tc.event)
			p, err := tc.tg.Plan(nil, gspec(tc.client), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tc.tg.Plan([]byte(content), gspec(tc.client), nil); !errors.Is(err, model.ErrSettingsParse) {
				t.Errorf("%s/%s: Plan error = %v, want ErrSettingsParse", tc.client, name, err)
			}
			if _, err := tc.tg.Remove([]byte(content), manifestFrom(gBinary, p)); !errors.Is(err, model.ErrSettingsParse) {
				t.Errorf("%s/%s: Remove error = %v, want ErrSettingsParse", tc.client, name, err)
			}
		}
	}
}

func TestCursorVersionHandling(t *testing.T) {
	tg := gCursorT()
	spec := gspec(model.ClientCursor)

	// existing file without version: version is added in front and, being
	// recorded as set by Ballast, removed again on uninstall (contract v1.3)
	p := planAfter(t, tg, `{"other": true}`, spec, nil)
	if !strings.HasPrefix(string(p.After), `{"version": 1, "other": true, "hooks"`) {
		t.Errorf("version not inserted first: %.60s", p.After)
	}
	r, err := tg.Remove(p.After, manifestFrom(gBinary, p))
	if err != nil {
		t.Fatal(err)
	}
	if string(r.After) != `{"other": true}` {
		t.Errorf("after uninstall = %s", r.After)
	}
	// ... unless the user still has hooks of their own
	p = planAfter(t, tg, `{"hooks": {"stop": [{"command": "mine"}]}}`, spec, nil)
	r, err = tg.Remove(p.After, manifestFrom(gBinary, p))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.After), `"version": 1`) || !strings.Contains(string(r.After), `"mine"`) {
		t.Errorf("version must stay while user hooks remain: %s", r.After)
	}

	// an existing version (even another value) is never rewritten
	p = planAfter(t, tg, `{"version": 2}`, spec, nil)
	if !strings.Contains(string(p.After), `"version": 2`) {
		t.Errorf("existing version changed: %s", p.After)
	}

	// created by install and left with only version:1 ⇒ deleted
	p = planAfter(t, tg, absent, spec, nil)
	r, _ = tg.Remove(p.After, manifestFrom(gBinary, p))
	if r.After != nil {
		t.Errorf("created file with only version:1 must be deleted, got %s", r.After)
	}

	// not created by install: {"version":1} stays
	p = planAfter(t, tg, `{"version": 1}`, spec, nil)
	r, _ = tg.Remove(p.After, manifestFrom(gBinary, p))
	if string(r.After) != `{"version": 1}` {
		t.Errorf("got %s", r.After)
	}
}

func TestCursorLoopLimitOnStopEvents(t *testing.T) {
	p := planAfter(t, gCursorT(), absent, gspec(model.ClientCursor), nil)
	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(p.After, &doc); err != nil {
		t.Fatal(err)
	}
	for ev, list := range doc.Hooks {
		_, has := list[0]["loop_limit"]
		if want := ev == "stop" || ev == "subagentStop"; has != want {
			t.Errorf("%s: loop_limit present=%v want %v", ev, has, want)
		}
	}
}

func TestCodexKeysFollowGroupPosition(t *testing.T) {
	tg := gCodexHT()
	cur := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/bin/user"}]},{"hooks":[{"type":"command","command":"/bin/user2"}]}]}}`
	p := planAfter(t, tg, cur, gspec(model.ClientCodex), nil)
	for _, e := range p.Entries {
		if e.Native == "PreToolUse" && e.Key != "hooks.PreToolUse[2].hooks[0]" {
			t.Errorf("key = %s", e.Key)
		}
	}
}

func TestEventSubsetAndRetainedRecords(t *testing.T) {
	tg := gClaudeT()
	full := gspec(model.ClientClaude)
	p1 := planAfter(t, tg, absent, full, nil)
	m := manifestFrom(gBinary, p1)

	small := full
	small.Events = nil
	for _, ne := range defaultEvents(model.ClientClaude) {
		if ne.Native == "PreToolUse" || ne.Native == "Stop" {
			small.Events = append(small.Events, ne)
		}
	}
	p2, err := tg.Plan(p1.After, small, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Entries) != 10 {
		t.Errorf("records for events outside this install must stay recorded: %d", len(p2.Entries))
	}
	if string(p2.After) != string(p1.After) {
		t.Error("nothing should change")
	}
	// on an absent file only the requested events appear
	p3 := planAfter(t, tg, absent, small, nil)
	var doc struct {
		Hooks map[string]any `json:"hooks"`
	}
	_ = json.Unmarshal(p3.After, &doc)
	if len(doc.Hooks) != 2 {
		t.Errorf("events written = %d", len(doc.Hooks))
	}
}

func TestRemoveOnMissingFileIsNoOp(t *testing.T) {
	r, err := gClaudeT().Remove(nil, &model.InstallManifest{})
	if err != nil || r.Changed() || r.After != nil {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestRemoveIgnoresRecordsOfOtherFiles(t *testing.T) {
	p := planAfter(t, gClaudeT(), absent, gspec(model.ClientClaude), nil)
	other := &jsonHooksTarget{client: model.ClientClaude, path: "/elsewhere/settings.json", goos: "linux"}
	r, err := other.Remove(p.After, manifestFrom(gBinary, p))
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed() {
		t.Error("records of a different file must be ignored")
	}
}

func TestWindowsCommandStrings(t *testing.T) {
	tg := &jsonHooksTarget{client: model.ClientCodex, path: `C:\Users\u\.codex\hooks.json`, goos: "windows"}
	spec := EntrySpec{Client: model.ClientCodex, Binary: `C:\Program Files\Ballast\ballast.exe`, Events: defaultEvents(model.ClientCodex), GOOS: "windows"}
	p, err := tg.Plan(nil, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.After), `\"C:\\Program Files\\Ballast\\ballast.exe\" hook --client codex --event Stop`) {
		t.Errorf("windows quoting wrong:\n%s", p.After)
	}
	r, err := tg.Remove(p.After, manifestFrom(spec.Binary, p))
	if err != nil || r.After != nil {
		t.Errorf("windows round trip: %v %s", err, r.After)
	}
}

// TestJSONUserContainersRoundTrip: an empty "hooks" object the user already
// had survives the uninstall, and the Cursor "version" Ballast added is
// taken back (contract v1.3 structural flags, review u10 minor).
func TestJSONUserContainersRoundTrip(t *testing.T) {
	type tgt struct {
		name string
		tg   func() *jsonHooksTarget
		c    model.ClientID
	}
	for _, x := range []tgt{{"claude", gClaudeT, model.ClientClaude}, {"codex", gCodexHT, model.ClientCodex}, {"cursor", gCursorT, model.ClientCursor}} {
		for _, orig := range []string{`{"hooks": {}}` + "\n", "{}\n", `{"a": 1, "hooks": {}}` + "\n"} {
			p := planAfter(t, x.tg(), orig, gspec(x.c), nil)
			m := manifestFrom(gBinary, p)
			// Re-install keeps the original state on record.
			p2, err := x.tg().Plan(p.After, gspec(x.c), m)
			if err != nil {
				t.Fatal(err)
			}
			r, err := x.tg().Remove(p2.After, manifestFrom(gBinary, p2))
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal([]byte(orig), &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(r.After, &got); err != nil {
				t.Fatalf("%s %q: uninstall output %q: %v", x.name, orig, r.After, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %q: round trip = %s", x.name, orig, r.After)
			}
		}
	}
}
