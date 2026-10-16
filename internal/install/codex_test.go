package install

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func planConfig(t *testing.T, cur string, m *model.InstallManifest) *PlanResult {
	t.Helper()
	var b []byte
	if cur != absent {
		b = []byte(cur)
	}
	p, err := gCodexCT().Plan(b, gspec(model.ClientCodex), m)
	if err != nil {
		t.Fatalf("Plan(%q): %v", cur, err)
	}
	return p
}

func TestFeaturesHooks(t *testing.T) {
	cases := []struct {
		name, in, want string
		setByUs        bool
	}{
		{"absent", absent, "[features]\nhooks = true\n", true},
		{"empty file", "", "[features]\nhooks = true\n", true},
		{"other content", "model = \"x\"\n", "model = \"x\"\n\n[features]\nhooks = true\n", true},
		{"no trailing newline", "model = 1", "model = 1\n\n[features]\nhooks = true\n", true},
		{"header only, no newline", "[features]", "[features]\nhooks = true\n", true},
		{"features without hooks", "[features]\nfoo = 1\n", "[features]\nhooks = true\nfoo = 1\n", true},
		{"features before other table", "[features]\n\n[tui]\nx = 1\n", "[features]\nhooks = true\n\n[tui]\nx = 1\n", true},
		{"false flips", "[features]\nhooks = false # off\n", "[features]\nhooks = true # off\n", true},
		{"already true", "[features]\nhooks = true\n", "[features]\nhooks = true\n", false},
		{"already true with spaces", "[features]\n  hooks   =   true  # c\n", "[features]\n  hooks   =   true  # c\n", false},
		{"dotted true", "features.hooks = true\n", "features.hooks = true\n", false},
		{"dotted false", "features.hooks = false\n", "features.hooks = true\n", true},
		{"header in multi-line string is not a table", "notes = \"\"\"\n[features]\nhooks = false\n\"\"\"\n",
			"notes = \"\"\"\n[features]\nhooks = false\n\"\"\"\n\n[features]\nhooks = true\n", true},
		{"header in literal multi-line string", "n = '''\n[features]\n'''\n", "n = '''\n[features]\n'''\n\n[features]\nhooks = true\n", true},
		{"header in array", "a = [\n  \"[features]\",\n  \"x\",\n]\n", "a = [\n  \"[features]\",\n  \"x\",\n]\n\n[features]\nhooks = true\n", true},
		{"crlf", "a = 1\r\n[features]\r\nfoo = 2\r\n", "a = 1\r\n[features]\r\nhooks = true\r\nfoo = 2\r\n", true},
		{"crlf append", "a = 1\r\n", "a = 1\r\n\r\n[features]\r\nhooks = true\r\n", true},
		{"bom", "\xef\xbb\xbfa = 1\n", "\xef\xbb\xbfa = 1\n\n[features]\nhooks = true\n", true},
		{"quoted header", "[ \"features\" ]\nx = 1\n", "[ \"features\" ]\nhooks = true\nx = 1\n", true},
		{"comments and blanks", "# c\n\n[features] # trailing\n# note\n", "# c\n\n[features] # trailing\nhooks = true\n# note\n", true},
		{"similar table untouched", "[features.extra]\nhooks = false\n", "[features.extra]\nhooks = false\n\n[features]\nhooks = true\n", true},
	}
	for _, c := range cases {
		p := planConfig(t, c.in, nil)
		if string(p.After) != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, p.After, c.want)
		}
		var f *model.ManagedFlag
		for i := range p.Flags {
			if p.Flags[i].Key == "features.hooks" {
				f = &p.Flags[i]
			}
		}
		wantOrig := "absent"
		switch {
		case !c.setByUs:
			wantOrig = "true"
		case c.name == "false flips" || c.name == "dotted false":
			wantOrig = "false"
		}
		if f == nil || f.SetByBallast != c.setByUs || f.Value != wantOrig || f.Client != model.ClientCodex || f.File != gCodexC {
			t.Errorf("%s: flag = %+v, want SetByBallast=%v", c.name, f, c.setByUs)
		}
		// idempotent
		p2 := planConfig(t, string(p.After), manifestFrom(gBinary, p))
		if string(p2.After) != string(p.After) {
			t.Errorf("%s: not idempotent", c.name)
		}
		for i := range p2.Flags {
			if p2.Flags[i].Key == "features.hooks" && (p2.Flags[i].SetByBallast != c.setByUs || p2.Flags[i].Value != wantOrig) {
				t.Errorf("%s: the original value must survive a second install (got %+v)", c.name, p2.Flags[i])
			}
		}
	}
}

func TestConfigTOMLCorruptionRefuses(t *testing.T) {
	bad := []string{
		"[features\nhooks = true\n",
		"key value\n",
		"a = \"unterminated\n",
		"a = \"\"\"\nnever closed\n",
		"a = [1, 2\n",
		"[]\n",
		"[a b]\n",
		"= 1\n",
		"[features] junk\n",
		"a = \n",
		"features = { hooks = true }\n",
		"[features]\nhooks = true\n[features]\nfoo = 1\n",
		"[features]\nhooks = \"yes\"\n",
		"a = ]\n",
	}
	for _, in := range bad {
		_, err := gCodexCT().Plan([]byte(in), gspec(model.ClientCodex), nil)
		if !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("Plan(%q) error = %v, want ErrSettingsParse", in, err)
		}
	}
	for _, in := range bad[:10] {
		if _, err := gCodexCT().Remove([]byte(in), &model.InstallManifest{}); !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("Remove(%q) error = %v, want ErrSettingsParse", in, err)
		}
	}
}

func TestTOMLScannerClassifiesLines(t *testing.T) {
	src := "# comment [not.a.table]\n" +
		"title = \"a # not a comment [x]\"\n" +
		"list = [\n  1, # [fake]\n  [2, 3],\n  \"]\",\n]\n" +
		"inline = { a = 1, b = [2] }\n" +
		"ml = '''\n[also.fake]\n'''\n" +
		"dotted.key = 1\n" +
		"\"quoted.dot\".x = 2\n" +
		"[real.table]\n" +
		"k = 1\n" +
		"[[array.of.tables]]\n" +
		"[ spaced . 'path' ]\n"
	d, err := parseTOML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var headers []string
	for _, l := range d.lines {
		if l.kind == tlHeader {
			headers = append(headers, fmt.Sprintf("%v/%v", l.path, l.arr))
		}
	}
	want := "[real table]/false [array of tables]/true [spaced path]/false"
	if got := strings.Join(headers, " "); got != want {
		t.Errorf("headers = %s, want %s", got, want)
	}
	if string(d.bytes()) != src {
		t.Error("parse + render must be lossless")
	}
	if i, ok := d.kvIn(0, d.rootEnd(), []string{"quoted.dot", "x"}); !ok || d.value(i) != "2" {
		t.Error("quoted dotted key not parsed as one segment")
	}
}

// trustInstall plans hooks.json then config.toml with trust, like Install does.
func trustInstall(t *testing.T, hooksCur, cfgCur string, m *model.InstallManifest) (*PlanResult, *PlanResult) {
	t.Helper()
	spec := gspec(model.ClientCodex)
	spec.CodexTrust = true
	var hb, cb []byte
	if hooksCur != absent {
		hb = []byte(hooksCur)
	}
	if cfgCur != absent {
		cb = []byte(cfgCur)
	}
	hp, err := gCodexHT().Plan(hb, spec, m)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := gCodexCT().planWith(cb, spec, m, []*PlanResult{hp})
	if err != nil {
		t.Fatal(err)
	}
	return hp, cp
}

func trustKeys(p *PlanResult) []string {
	var out []string
	for _, f := range p.Flags {
		if strings.HasPrefix(f.Key, trustKeyPrefix) {
			out = append(out, f.Key)
		}
	}
	return out
}

func TestTrustOnlyWithExplicitFlag(t *testing.T) {
	// no flag: no trust entries, ever
	p := planConfig(t, absent, nil)
	if strings.Contains(string(p.After), "trusted_hash") || len(trustKeys(p)) != 0 {
		t.Errorf("trust written without --codex-trust:\n%s", p.After)
	}
	// stand-alone Plan with the flag cannot see hooks.json positions: no entries, a warning
	spec := gspec(model.ClientCodex)
	spec.CodexTrust = true
	p, err := gCodexCT().Plan(nil, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(trustKeys(p)) != 0 || len(p.Warnings) != 1 {
		t.Errorf("stand-alone Plan: keys=%v warnings=%v", trustKeys(p), p.Warnings)
	}
}

func TestTrustEntries(t *testing.T) {
	hp, cp := trustInstall(t, absent, absent, nil)
	keys := trustKeys(cp)
	if len(keys) != 6 {
		t.Fatalf("trust flags = %d, want 6 (one per codex native)", len(keys))
	}
	for _, e := range hp.Entries {
		k := fmt.Sprintf("%s:%s:0:0", gCodexH, snakeEvent(e.Native))
		hash := CodexTrustHash(snakeEvent(e.Native), e.Command, codexHookTimeout)
		if !strings.Contains(string(cp.After), "[hooks.state.'"+k+"']\nenabled = true\ntrusted_hash = \""+hash+"\"\n") {
			t.Errorf("missing trust table for %s", e.Native)
		}
	}
	for _, f := range cp.Flags {
		if f.Client != model.ClientCodex || f.File != gCodexC || !f.SetByBallast {
			t.Errorf("flag %+v", f)
		}
	}
	// second install is idempotent
	m := manifestFrom(gBinary, hp, cp)
	hp2, cp2 := trustInstall(t, string(hp.After), string(cp.After), m)
	if string(cp2.After) != string(cp.After) || string(hp2.After) != string(hp.After) {
		t.Error("second trusted install changed files")
	}
	// uninstall removes exactly the trust tables, keeps features.hooks
	r, err := gCodexCT().Remove(cp.After, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(r.After) != "[features]\nhooks = true\n" {
		t.Errorf("after uninstall:\n%s", r.After)
	}
	if len(r.Warnings) != 0 || len(r.Flags) != 0 {
		t.Errorf("warnings=%v flags=%v", r.Warnings, r.Flags)
	}
}

func TestTrustRoundTripOnExistingConfig(t *testing.T) {
	orig := "model = \"gpt\"\n\n[features]\nfoo = 1\n\n[tui]\ntheme = \"dark\"\n"
	hp, cp := trustInstall(t, absent, orig, nil)
	m := manifestFrom(gBinary, hp, cp)
	r, err := gCodexCT().Remove(cp.After, m)
	if err != nil {
		t.Fatal(err)
	}
	// features.hooks stays on (by contract); everything else is back
	want := "model = \"gpt\"\n\n[features]\nhooks = true\nfoo = 1\n\n[tui]\ntheme = \"dark\"\n"
	if string(r.After) != want {
		t.Errorf("got:\n%s\nwant:\n%s", r.After, want)
	}
}

func TestTrustRecomputedWhenPositionsMove(t *testing.T) {
	hp, cp := trustInstall(t, absent, absent, nil)
	m := manifestFrom(gBinary, hp, cp)
	// the user adds a hook group before ours in PreToolUse
	moved := strings.Replace(string(hp.After), `"PreToolUse": [`, `"PreToolUse": [
      {"hooks": [{"type": "command", "command": "/bin/user"}]},`, 1)
	hp2, cp2 := trustInstall(t, moved, string(cp.After), m)
	if !strings.Contains(string(cp2.After), "hooks.json:pre_tool_use:1:0']") {
		t.Errorf("new position not trusted:\n%s", cp2.After)
	}
	if strings.Contains(string(cp2.After), "hooks.json:pre_tool_use:0:0']") {
		t.Errorf("stale position still trusted:\n%s", cp2.After)
	}
	if len(trustKeys(cp2)) != 6 {
		t.Errorf("flags = %d", len(trustKeys(cp2)))
	}
	_ = hp2
}

func TestTrustSkipsUserModifiedEntries(t *testing.T) {
	hp, _ := trustInstall(t, absent, absent, nil)
	m := manifestFrom(gBinary, hp)
	modified := strings.Replace(string(hp.After), `--event Stop"`, `--event Stop --extra"`, 1)
	_, cp := trustInstall(t, modified, absent, m)
	if strings.Contains(string(cp.After), "stop:0:0") {
		t.Errorf("a user-modified hook must not be trusted:\n%s", cp.After)
	}
	if len(trustKeys(cp)) != 5 {
		t.Errorf("trust flags = %d, want 5", len(trustKeys(cp)))
	}
}

func TestTrustWithoutFlagKeepsPreviousEntries(t *testing.T) {
	hp, cp := trustInstall(t, absent, absent, nil)
	m := manifestFrom(gBinary, hp, cp)
	// reinstall without --codex-trust: nothing is removed or added
	spec := gspec(model.ClientCodex)
	cp2, err := gCodexCT().planWith(cp.After, spec, m, []*PlanResult{hp})
	if err != nil {
		t.Fatal(err)
	}
	if string(cp2.After) != string(cp.After) || len(trustKeys(cp2)) != 6 {
		t.Error("previous trust must stay recorded and in place")
	}
}

func TestTrustTableEditedByUserIsKept(t *testing.T) {
	hp, cp := trustInstall(t, absent, absent, nil)
	m := manifestFrom(gBinary, hp, cp)
	edited := strings.Replace(string(cp.After), "trusted_hash = \""+CodexTrustHash("stop", hp.Entries[len(hp.Entries)-1].Command, 30)+"\"", `trusted_hash = "sha256:user"`, 1)
	if edited == string(cp.After) {
		t.Fatal("setup: edit did not apply")
	}
	r, err := gCodexCT().Remove([]byte(edited), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 1 || len(r.Flags) != 1 || !strings.Contains(string(r.After), "sha256:user") {
		t.Errorf("warnings=%v flags=%v\n%s", r.Warnings, r.Flags, r.After)
	}
	if strings.Count(string(r.After), "[hooks.state.") != 1 {
		t.Errorf("other trust tables must be gone:\n%s", r.After)
	}
}

func TestTrustTableWithUserKeysKeepsTheTable(t *testing.T) {
	hp, cp := trustInstall(t, absent, absent, nil)
	m := manifestFrom(gBinary, hp, cp)
	edited := strings.Replace(string(cp.After), "enabled = true\ntrusted_hash = \""+CodexTrustHash("stop", hp.Entries[len(hp.Entries)-1].Command, 30)+"\"\n",
		"note = \"mine\"\nenabled = true\ntrusted_hash = \""+CodexTrustHash("stop", hp.Entries[len(hp.Entries)-1].Command, 30)+"\"\n", 1)
	r, err := gCodexCT().Remove([]byte(edited), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.After), "note = \"mine\"") || strings.Contains(string(r.After), "trusted_hash") || strings.Contains(string(r.After), "enabled") {
		t.Errorf("only our keys may go:\n%s", r.After)
	}
}

func TestTrustAdoptsAnExistingTable(t *testing.T) {
	hp, _ := trustInstall(t, absent, absent, nil)
	var stopCmd string
	for _, e := range hp.Entries {
		if e.Native == "Stop" {
			stopCmd = e.Command
		}
	}
	// the user already trusted the stop hook through /hooks (same key, stale hash, extra key)
	pre := "[hooks.state.'" + gCodexH + ":stop:0:0']\nenabled = false\nextra = 1\n"
	_, cp := trustInstall(t, absent, pre, nil)
	want := "enabled = true\ntrusted_hash = \"" + CodexTrustHash("stop", stopCmd, 30) + "\""
	if !strings.Contains(string(cp.After), "extra = 1") || !strings.Contains(strings.ReplaceAll(string(cp.After), "extra = 1\n", ""), strings.Split(want, "\n")[0]) {
		t.Errorf("adopt failed:\n%s", cp.After)
	}
	if strings.Count(string(cp.After), "hooks.json:stop:0:0") != 1 {
		t.Errorf("duplicate table:\n%s", cp.After)
	}
}

func TestTrustKeyQuoting(t *testing.T) {
	for _, path := range []string{`C:\Users\u\.codex\hooks.json`, "/home/it's/.codex/hooks.json", "/home/a b/.codex/hooks.json", `/h/"q"/hooks.json`} {
		k := path + ":stop:0:0"
		d, err := parseTOML([]byte("[hooks.state." + tomlString(k) + "]\nenabled = true\n"))
		if err != nil {
			t.Errorf("%q: %v", path, err)
			continue
		}
		if len(d.tables(trustTablePath(k))) != 1 {
			t.Errorf("%q: table not found again (header %q)", path, d.lines[0].text)
		}
	}
	if k, ok := parseTrustFlagKey(trustFlagKey("/a:stop:0:0")); !ok || k != "/a:stop:0:0" {
		t.Errorf("flag key round trip: %q %v", k, ok)
	}
	if _, ok := parseTrustFlagKey("features.hooks"); ok {
		t.Error("features.hooks is not a trust key")
	}
}

func TestConfigRemoveLeavesFeaturesAndIsLossless(t *testing.T) {
	orig := "[features]\nhooks = true\n\n# keep\n[other]\nx = 1\n"
	p := planConfig(t, orig, nil)
	if string(p.After) != orig {
		t.Fatal("already-enabled config must be unchanged")
	}
	r, err := gCodexCT().Remove([]byte(orig), manifestFrom(gBinary, p))
	if err != nil {
		t.Fatal(err)
	}
	if r.Changed() {
		t.Error("nothing to remove")
	}
}

func featuresFlag(p *PlanResult) *model.ManagedFlag {
	for i := range p.Flags {
		if p.Flags[i].Key == flagFeaturesHooks {
			return &p.Flags[i]
		}
	}
	return nil
}

// An explicit features.hooks = false is turned on with a warning, and
// uninstall puts the false back; an absent key stays on (the documented
// exception) and an already-true key is untouched.
func TestFeaturesHooksOriginalValueRoundTrip(t *testing.T) {
	cases := []struct {
		name, in, wantUninstall string
		orig                    string
		warn                    bool
	}{
		{"table false", "[features]\nhooks = false\n", "[features]\nhooks = false\n", "false", true},
		{"table false with comment", "model = 1\n[features]\nfoo = 1\nhooks = false # off on purpose\n[tui]\nx = 1\n",
			"model = 1\n[features]\nfoo = 1\nhooks = false # off on purpose\n[tui]\nx = 1\n", "false", true},
		{"dotted false", "features.hooks = false\nmodel = 1\n", "features.hooks = false\nmodel = 1\n", "false", true},
		{"crlf false", "[features]\r\nhooks = false\r\n", "[features]\r\nhooks = false\r\n", "false", true},
		{"absent stays on", "model = 1\n", "model = 1\n\n[features]\nhooks = true\n", "absent", false},
		{"features without hooks stays on", "[features]\nfoo = 1\n", "[features]\nhooks = true\nfoo = 1\n", "absent", false},
		{"true untouched", "[features]\nhooks = true\n", "[features]\nhooks = true\n", "true", false},
	}
	for _, c := range cases {
		tg := gCodexCT()
		p, err := tg.Plan([]byte(c.in), gspec(model.ClientCodex), nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if f := featuresFlag(p); f == nil || f.Value != c.orig || f.SetByBallast != (c.orig != "true") {
			t.Errorf("%s: flag = %+v", c.name, f)
		}
		warned := false
		for _, w := range p.Warnings {
			warned = warned || strings.Contains(w, "features.hooks was explicitly false")
		}
		if warned != c.warn {
			t.Errorf("%s: warnings = %q, want warning=%v", c.name, p.Warnings, c.warn)
		}
		if !strings.Contains(string(p.After), "true") {
			t.Errorf("%s: not switched on: %q", c.name, p.After)
		}
		// Re-install on the installed file: no new warning, original kept.
		m := manifestFrom(gBinary, p)
		p2, err := tg.Plan(p.After, gspec(model.ClientCodex), m)
		if err != nil {
			t.Fatal(err)
		}
		if f := featuresFlag(p2); f == nil || f.Value != c.orig {
			t.Errorf("%s: re-install flag = %+v", c.name, f)
		}
		if len(p2.Warnings) != 0 {
			t.Errorf("%s: re-install warned: %q", c.name, p2.Warnings)
		}
		r, err := tg.Remove(p2.After, manifestFrom(gBinary, p2))
		if err != nil {
			t.Fatal(err)
		}
		if string(r.After) != c.wantUninstall {
			t.Errorf("%s: after uninstall\n got %q\nwant %q", c.name, r.After, c.wantUninstall)
		}
	}
}

// The user changed the switch between install and uninstall: their value
// wins, nothing is forced back.
func TestFeaturesHooksFalseNotRestoredOverUserEdit(t *testing.T) {
	tg := gCodexCT()
	p, err := tg.Plan([]byte("[features]\nhooks = false\n"), gspec(model.ClientCodex), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := manifestFrom(gBinary, p)
	for name, edited := range map[string]string{
		"key removed": "[features]\n",
		"user header": "[features]\nmodel = 1\n",
	} {
		r, err := tg.Remove([]byte(edited), m)
		if err != nil {
			t.Fatal(err)
		}
		if string(r.After) != edited {
			t.Errorf("%s: got %q want %q", name, r.After, edited)
		}
	}
}

// A manifest from before the original value was recorded (Value "true",
// set_by_ballast) is the old behaviour: left on, no warning.
func TestFeaturesHooksLegacyFlag(t *testing.T) {
	tg := gCodexCT()
	m := &model.InstallManifest{Flags: []model.ManagedFlag{{Client: model.ClientCodex, File: gCodexC, Key: flagFeaturesHooks, Value: "true", SetByBallast: true}}}
	cur := "[features]\nhooks = true\n"
	p, err := tg.Plan([]byte(cur), gspec(model.ClientCodex), m)
	if err != nil {
		t.Fatal(err)
	}
	if f := featuresFlag(p); f == nil || f.Value != "absent" || !f.SetByBallast {
		t.Errorf("legacy flag renewed as %+v", f)
	}
	r, err := tg.Remove([]byte(cur), m)
	if err != nil {
		t.Fatal(err)
	}
	if string(r.After) != cur {
		t.Errorf("legacy uninstall: %q", r.After)
	}
}

// With --codex-trust the trust tables go and an original false comes back.
func TestFeaturesHooksFalseWithTrust(t *testing.T) {
	hp, cp := trustInstall(t, absent, "[features]\nhooks = false\n", nil)
	if f := featuresFlag(cp); f == nil || f.Value != "false" {
		t.Fatalf("flag = %+v", f)
	}
	m := manifestFrom(gBinary, hp, cp)
	r, err := gCodexCT().Remove(cp.After, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(r.After) != "[features]\nhooks = false\n" {
		t.Errorf("after uninstall:\n%s", r.After)
	}
}
