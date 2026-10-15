package install

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func TestClientsOrderAndTargets(t *testing.T) {
	e := newTenv(t)
	cs := Clients()
	if len(cs) != len(model.AllClients) {
		t.Fatalf("%d clients", len(cs))
	}
	want := map[model.ClientID][]string{
		model.ClientClaude: {e.claudeSettings()},
		model.ClientCodex:  {e.codexHooks(), e.codexConfig()},
		model.ClientCursor: {e.cursorHooks(), e.cursorMDC()},
		model.ClientHermes: {e.hermesConfig()},
	}
	formats := map[string][]Format{
		"claude-code": {FormatJSON}, "codex": {FormatJSON, FormatTOML}, "cursor": {FormatJSON, FormatMDC}, "hermes": {FormatYAML},
	}
	for i, c := range cs {
		if c.ID() != model.AllClients[i] {
			t.Errorf("client %d = %s, want %s", i, c.ID(), model.AllClients[i])
		}
		var paths []string
		var fm []Format
		for _, tg := range c.Targets(e.lay, EntrySpec{}) {
			paths = append(paths, tg.Path())
			fm = append(fm, tg.Format())
		}
		if strings.Join(paths, "|") != strings.Join(want[c.ID()], "|") {
			t.Errorf("%s targets = %v, want %v", c.ID(), paths, want[c.ID()])
		}
		if len(fm) != len(formats[string(c.ID())]) {
			t.Errorf("%s formats = %v", c.ID(), fm)
		}
		for j := range fm {
			if fm[j] != formats[string(c.ID())][j] {
				t.Errorf("%s format %d = %s", c.ID(), j, fm[j])
			}
		}
		if len(c.PostInstallNotes()) == 0 {
			t.Errorf("%s has no post-install notes", c.ID())
		}
	}
}

func TestTargetsFollowRedirectedConfigDirs(t *testing.T) {
	e := newTenv(t)
	lay := e.lay
	lay.ClaudeDir = filepath.Join(e.sb.Root, "cc")
	lay.CodexDir = filepath.Join(e.sb.Root, "cx")
	lay.HermesDir = filepath.Join(e.sb.Root, "hm")
	got := Clients()[0].Targets(lay, EntrySpec{})[0].Path()
	if got != filepath.Join(lay.ClaudeDir, "settings.json") {
		t.Errorf("claude path %s", got)
	}
	if got := Clients()[1].Targets(lay, EntrySpec{})[0].Path(); got != filepath.Join(lay.CodexDir, "hooks.json") {
		t.Errorf("codex path %s", got)
	}
	if got := Clients()[3].Targets(lay, EntrySpec{})[0].Path(); got != filepath.Join(lay.HermesDir, "config.yaml") {
		t.Errorf("hermes path %s", got)
	}
	empty := lay
	empty.ClaudeDir, empty.CodexDir, empty.CursorDir, empty.HermesDir = "", "", "", ""
	for _, c := range Clients() {
		if len(c.Targets(empty, EntrySpec{})) != 0 {
			t.Errorf("%s: targets without a config dir", c.ID())
		}
	}
}

func TestDetect(t *testing.T) {
	e := newTenv(t)
	for _, c := range Clients() {
		if d := c.Detect(e.lay, e.fs, e.sb.Env()); d.Installed || d.ConfigDir != e.lay.ClientDir(c.ID()) {
			t.Errorf("%s detected in an empty home: %+v", c.ID(), d)
		}
	}
	// config directory
	_ = os.MkdirAll(e.lay.CursorDir, 0o755)
	if d := (cursorClient{}).Detect(e.lay, e.fs, e.sb.Env()); !d.Installed {
		t.Error("cursor config dir should count as installed")
	}
	// a regular file in place of the directory does not
	e.write(e.lay.HermesDir, "not a dir")
	if d := (hermesClient{}).Detect(e.lay, e.fs, e.sb.Env()); d.Installed {
		t.Error("a file is not a config dir")
	}
	// PATH lookup (never executes anything)
	bin := filepath.Join(e.sb.Root, "bin")
	e.write(filepath.Join(bin, "codex"), "x")
	env := sys.MapEnv{Vars: withVar(e.sb.Vars, "PATH", "/nonexistent"+string(os.PathListSeparator)+bin)}
	d := (codexClient{}).Detect(e.lay, e.fs, env)
	if !d.Installed || len(d.Notes) != 1 || d.Version != "" {
		t.Errorf("codex on PATH: %+v", d)
	}
	// a directory named like the client is not an executable
	_ = os.MkdirAll(filepath.Join(bin, "claude"), 0o755)
	if d := (claudeClient{}).Detect(e.lay, e.fs, env); d.Installed {
		t.Error("directory matched as executable")
	}
	// windows extensions
	winLay := e.lay
	winLay.GOOS = "windows"
	e.write(filepath.Join(bin, "cursor.cmd"), "x")
	if p := lookPath(e.fs, sys.MapEnv{Vars: map[string]string{"PATH": bin}, OS: "windows"}, "windows", "cursor"); !strings.HasSuffix(p, "cursor.cmd") {
		t.Errorf("windows lookup = %q", p)
	}
	if p := lookPath(e.fs, nil, "linux", "x"); p != "" {
		t.Error("nil env")
	}
}

func TestClaudeDetectReportsManaged(t *testing.T) {
	e := newTenv(t)
	writeManaged(e, "managed-settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/ballast","args":["hook"]}]}]}}`)
	d := (claudeClient{}).Detect(e.lay, e.fs, e.sb.Env())
	if !d.Managed || len(d.Notes) == 0 {
		t.Errorf("%+v", d)
	}
	e2 := newTenv(t)
	writeManaged(e2, "managed-settings.json", "{broken")
	d = (claudeClient{}).Detect(e2.lay, e2.fs, e2.sb.Env())
	if d.Managed || len(d.Notes) == 0 {
		t.Errorf("an unreadable managed file should be noted: %+v", d)
	}
}

func TestHookArgv(t *testing.T) {
	got := HookArgv("/b", model.ClientCodex, "PreToolUse")
	want := []string{"/b", "hook", "--client", "codex", "--event", "PreToolUse"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("HookArgv = %q", got)
	}
}

func TestTargetForRecordedPaths(t *testing.T) {
	for _, c := range []struct {
		client model.ClientID
		path   string
		kind   string
	}{
		{model.ClientClaude, "/x/settings.json", "*install.jsonHooksTarget"},
		{model.ClientCodex, "/x/hooks.json", "*install.jsonHooksTarget"},
		{model.ClientCodex, "/x/config.toml", "*install.codexConfigTarget"},
		{model.ClientCursor, "/x/hooks.json", "*install.jsonHooksTarget"},
		{model.ClientCursor, "/x/rules/ballast.mdc", "*install.mdcTarget"},
		{model.ClientHermes, "/x/config.yaml", "*install.hermesTarget"},
	} {
		tg := targetFor(c.client, c.path, "linux")
		if tg == nil || tg.Path() != c.path || reflect.TypeOf(tg).String() != c.kind {
			t.Errorf("targetFor(%s,%s) = %T", c.client, c.path, tg)
		}
	}
	if targetFor("vscode", "/x", "linux") != nil {
		t.Error("unknown client")
	}
}
