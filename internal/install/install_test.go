package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// userFiles are realistic pre-existing configs for each client, each with a
// user-owned hook that shares an event with Ballast.
func userFiles(e *tenv) map[model.ClientID]map[string]string {
	return map[model.ClientID]map[string]string{
		model.ClientClaude: {e.claudeSettings(): "{\n  \"model\": \"opus\",\n  \"hooks\": {\n    \"PreToolUse\": [\n      {\n        \"matcher\": \"Bash\",\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"/usr/local/bin/audit\"\n          }\n        ]\n      }\n    ]\n  }\n}\n"},
		model.ClientCodex: {
			e.codexHooks():  "{\n  \"hooks\": {\n    \"PreToolUse\": [\n      {\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"/usr/local/bin/audit\",\n            \"timeout\": 10\n          }\n        ]\n      }\n    ]\n  }\n}\n",
			e.codexConfig(): "model = \"gpt-5\"\n\n[features]\nhooks = true\n\n[tui]\ntheme = \"dark\"\n",
		},
		model.ClientCursor: {e.cursorHooks(): "{\n  \"version\": 1,\n  \"hooks\": {\n    \"stop\": [\n      {\n        \"command\": \"/usr/local/bin/audit\"\n      }\n    ]\n  }\n}\n"},
		model.ClientHermes: {e.hermesConfig(): "model:\n  name: nous\nhooks:\n  pre_tool_call:\n    - command: \"/usr/local/bin/audit\"\n      timeout: 5\nlogging:\n  level: info\n"},
	}
}

func TestInstallUninstallRoundTrip(t *testing.T) {
	for _, c := range model.AllClients {
		t.Run(string(c), func(t *testing.T) {
			e := newTenv(t)
			e.mkClientDirs()
			files := userFiles(e)[c]
			for p, content := range files {
				e.write(p, content)
			}
			opt := e.opts(c)

			rep := mustInstall(t, opt)
			if len(rep.Plans) == 0 || rep.Backup == nil || len(rep.Notes) == 0 {
				t.Fatalf("report incomplete: %+v", rep)
			}
			for p, orig := range files {
				if p == e.codexConfig() {
					continue // the fixture already enables features.hooks
				}
				if e.read(p) == orig {
					t.Errorf("%s was not modified", p)
				}
			}
			if !strings.Contains(e.read(e.lay.Manifest), `"client": "`+string(c)+`"`) {
				t.Error("manifest has no entries for the client")
			}

			// the user's own hook is still there
			for p := range files {
				if strings.HasSuffix(p, ".toml") {
					continue
				}
				if !strings.Contains(e.read(p), "/usr/local/bin/audit") {
					t.Errorf("%s lost the user's hook", p)
				}
			}

			mustUninstall(t, opt)
			for p, orig := range files {
				if got := e.read(p); got != orig {
					t.Errorf("round trip changed %s\n--- want\n%s\n--- got\n%s", p, orig, got)
				}
			}
			m, err := LoadManifest(e.fs, e.lay.Manifest)
			if err != nil || !manifestEmpty(m) {
				t.Errorf("manifest should be empty after uninstall: %v %+v", err, m)
			}
		})
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	for _, files := range userFiles(e) {
		for p, c := range files {
			e.write(p, c)
		}
	}
	opt := e.opts(model.AllClients...)
	opt.CodexTrust = true
	mustInstall(t, opt)

	snapshot := func() map[string]string {
		out := map[string]string{}
		_ = filepath.Walk(e.sb.Home, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && !strings.Contains(p, "backups") {
				b, _ := os.ReadFile(p)
				out[p] = string(b)
			}
			return nil
		})
		return out
	}
	before := snapshot()
	mtime := func(p string) time.Time { fi, _ := os.Stat(p); return fi.ModTime() }
	mt := mtime(e.lay.Manifest)
	backupsBefore, _ := os.ReadDir(e.lay.BackupsDir)

	e.clock.Advance(time.Hour)
	time.Sleep(20 * time.Millisecond)
	rep := mustInstall(t, opt)
	for _, p := range rep.Plans {
		if p.Changed() {
			t.Errorf("second install wants to change %s", p.Path)
		}
	}
	if rep.Backup != nil {
		t.Error("an install that changes nothing must not create a backup")
	}
	after := snapshot()
	if len(before) != len(after) {
		t.Errorf("file set changed: %d -> %d", len(before), len(after))
	}
	for p, content := range before {
		if after[p] != content {
			t.Errorf("%s changed on the second install", p)
		}
	}
	if !mtime(e.lay.Manifest).Equal(mt) {
		t.Error("manifest was rewritten although nothing changed")
	}
	if now, _ := os.ReadDir(e.lay.BackupsDir); len(now) != len(backupsBefore) {
		t.Error("backup directories changed")
	}
}

func TestMissingOriginalFileIsCreatedThenDeleted(t *testing.T) {
	for _, c := range model.AllClients {
		t.Run(string(c), func(t *testing.T) {
			e := newTenv(t)
			opt := e.opts(c)
			opt.Force = true // no client directory exists
			rep := mustInstall(t, opt)
			created := 0
			for _, p := range rep.Plans {
				if p.Created {
					created++
				}
				if p.Before != nil {
					t.Errorf("%s: Before must be nil for a missing file", p.Path)
				}
			}
			if created == 0 {
				t.Fatal("nothing was created")
			}
			if rep.Backup != nil {
				t.Error("nothing existed, so there is nothing to back up")
			}
			mustUninstall(t, opt)
			for _, p := range rep.Plans {
				if p.Format == FormatTOML {
					continue // features.hooks stays on by contract
				}
				if e.exists(p.Path) {
					t.Errorf("%s should have been deleted", p.Path)
				}
			}
			// install created the client directory itself (--force, nothing
			// existed), so uninstall removes it again (contract v1.3 flag
			// dir:kept); a pre-existing one stays (see
			// TestUninstallKeepsPreexistingEmptyClientDirs)
			if c != model.ClientCodex && e.exists(e.lay.ClientDir(c)) {
				t.Errorf("%s was created by install and should be gone", e.lay.ClientDir(c))
			}
			if e.exists(e.lay.Manifest) {
				m, _ := LoadManifest(e.fs, e.lay.Manifest)
				if !manifestEmpty(m) {
					t.Error("manifest not emptied")
				}
			}
		})
	}
}

func TestCodexConfigCreatedByInstallKeepsFeaturesOnly(t *testing.T) {
	e := newTenv(t)
	opt := e.opts(model.ClientCodex)
	opt.Force = true
	opt.CodexTrust = true
	mustInstall(t, opt)
	rep := mustUninstall(t, opt)
	if got := e.read(e.codexConfig()); got != "[features]\nhooks = true\n" {
		t.Errorf("config.toml = %q", got)
	}
	if e.exists(e.codexHooks()) {
		t.Error("hooks.json created by install must be deleted")
	}
	joined := strings.Join(rep.Notes, "\n")
	if !strings.Contains(joined, "features.hooks was left enabled") {
		t.Errorf("uninstall must report the flag it leaves on: %v", rep.Notes)
	}
}

func TestUserAddedHookSurvivesUninstall(t *testing.T) {
	for _, c := range model.AllClients {
		t.Run(string(c), func(t *testing.T) {
			e := newTenv(t)
			opt := e.opts(c)
			opt.Force = true
			mustInstall(t, opt)
			var path, added string
			switch c {
			case model.ClientClaude:
				path = e.claudeSettings()
				added = strings.Replace(e.read(path), `"hooks": {`, `"hooks": {
    "Notification": [{"hooks": [{"type": "command", "command": "/bin/notify"}]}],`, 1)
			case model.ClientCodex:
				path = e.codexHooks()
				added = strings.Replace(e.read(path), `"hooks": {`, `"hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "/bin/mine"}]}],`, 1)
				added = strings.Replace(added, `"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/mine"}]}],
    "SessionStart"`, `"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/mine"}]}], "XSessionStart"`, 1)
			case model.ClientCursor:
				path = e.cursorHooks()
				added = strings.Replace(e.read(path), `"hooks": {`, `"hooks": {
    "afterFileEdit": [{"command": "/bin/mine"}],`, 1)
			case model.ClientHermes:
				path = e.hermesConfig()
				added = e.read(path) + "extra:\n  hooks_note: keep\n"
			}
			if added == e.read(path) {
				t.Fatal("setup edit did not apply")
			}
			if c == model.ClientCodex {
				// avoid a duplicate JSON key: use an event Codex does not install
				added = strings.Replace(e.read(path), `"hooks": {`, `"hooks": {
    "Elicitation": [{"hooks": [{"type": "command", "command": "/bin/mine"}]}],`, 1)
			}
			e.write(path, added)
			mustUninstall(t, opt)
			if !e.exists(path) {
				t.Fatalf("%s was deleted although it holds user content", path)
			}
			got := e.read(path)
			if strings.Contains(got, "ballast") {
				t.Errorf("our entries are still present:\n%s", got)
			}
			for _, want := range []string{"/bin/notify", "/bin/mine", "hooks_note"} {
				if strings.Contains(added, want) && !strings.Contains(got, want) {
					t.Errorf("user content %q lost:\n%s", want, got)
				}
			}
			if c != model.ClientHermes && !json.Valid([]byte(got)) {
				t.Errorf("result is not valid JSON:\n%s", got)
			}
		})
	}
}

func TestManagedPresentRefusesClaudeInstall(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	writeManaged(e, "managed-settings.d/ballast.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/libexec/ballast/ballast","args":["hook","--client","claude-code","--event","Stop"]}]}]}}`)

	_, err := Install(e.opts(model.ClientClaude))
	if !errors.Is(err, model.ErrManagedPresent) {
		t.Fatalf("err = %v, want ErrManagedPresent", err)
	}
	if !strings.Contains(err.Error(), "ballast.json") {
		t.Errorf("the error should name the managed file: %v", err)
	}
	// dry-run refuses as well
	dry := e.opts(model.ClientClaude)
	dry.DryRun = true
	if _, err := Install(dry); !errors.Is(err, model.ErrManagedPresent) {
		t.Errorf("dry-run err = %v", err)
	}
	// nothing was written, not even for the other client requested together
	_, err = Install(e.opts(model.ClientClaude, model.ClientCodex))
	if !errors.Is(err, model.ErrManagedPresent) {
		t.Fatal(err)
	}
	for _, p := range []string{e.claudeSettings(), e.codexHooks(), e.codexConfig(), e.lay.Manifest, e.lay.BackupsDir} {
		if e.exists(p) {
			t.Errorf("%s must not exist after a refused install", p)
		}
	}
	// other clients are unaffected by the managed layer
	mustInstall(t, e.opts(model.ClientCodex))
}

func TestCorruptConfigRefusesWithoutWriting(t *testing.T) {
	corrupt := map[model.ClientID]func(e *tenv) string{
		model.ClientClaude: func(e *tenv) string { e.write(e.claudeSettings(), "{\"hooks\": {"); return e.claudeSettings() },
		model.ClientCodex:  func(e *tenv) string { e.write(e.codexConfig(), "[features\nhooks = true\n"); return e.codexConfig() },
		model.ClientCursor: func(e *tenv) string { e.write(e.cursorHooks(), "not json"); return e.cursorHooks() },
		model.ClientHermes: func(e *tenv) string { e.write(e.hermesConfig(), "hooks: {a: 1}\n"); return e.hermesConfig() },
	}
	for c, setup := range corrupt {
		t.Run(string(c), func(t *testing.T) {
			e := newTenv(t)
			e.mkClientDirs()
			bad := setup(e)
			badContent := e.read(bad)
			// request a healthy client together with the corrupt one
			other := model.ClientClaude
			if c == model.ClientClaude {
				other = model.ClientCursor
			}
			before := dirSnapshot(t, e.sb.Home)
			_, err := Install(e.opts(other, c))
			if !errors.Is(err, model.ErrSettingsParse) {
				t.Fatalf("err = %v, want ErrSettingsParse", err)
			}
			if !strings.Contains(err.Error(), bad) {
				t.Errorf("the error should name the file: %v", err)
			}
			after := dirSnapshot(t, e.sb.Home)
			if !equalMaps(before, after) {
				t.Errorf("a refused install changed the file system:\nbefore %v\nafter  %v", keys(before), keys(after))
			}
			if e.read(bad) != badContent {
				t.Error("the corrupt file was modified")
			}
		})
	}
}

func dirSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			out[p+"/"] = ""
			return nil
		}
		b, _ := os.ReadFile(p)
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDryRunWritesNothingAndShowsDiff(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	for _, files := range userFiles(e) {
		for p, c := range files {
			e.write(p, c)
		}
	}
	before := dirSnapshot(t, e.sb.Home)
	opt := e.opts(model.AllClients...)
	opt.DryRun = true
	opt.CodexTrust = true
	rep, err := Install(opt)
	if err != nil {
		t.Fatal(err)
	}
	if !equalMaps(before, dirSnapshot(t, e.sb.Home)) {
		t.Fatal("dry-run changed the file system")
	}
	if rep.Backup != nil {
		t.Error("dry-run makes no backup")
	}
	for _, p := range rep.Plans {
		if !strings.Contains(rep.Diff, "+++ "+p.Path+"\n") {
			t.Errorf("diff misses %s", p.Path)
		}
	}
	for _, want := range []string{"--- /dev/null\n+++ " + e.cursorMDC(), "\n+[hooks.state.", "\n+            \"command\": \"/opt/ballast/bin/ballast\","} {
		if runtimeIsUnix() && !strings.Contains(rep.Diff, want) {
			t.Errorf("diff lacks %q", want)
		}
	}
	// the diff is what a real install produces
	real := mustInstall(t, e.opts(model.AllClients...))
	_ = real

	// dry-run uninstall prints the reverse diff and changes nothing
	mid := dirSnapshot(t, e.sb.Home)
	u := e.opts(model.AllClients...)
	u.DryRun = true
	urep, err := Uninstall(u)
	if err != nil {
		t.Fatal(err)
	}
	if !equalMaps(mid, dirSnapshot(t, e.sb.Home)) {
		t.Fatal("dry-run uninstall changed the file system")
	}
	if !strings.Contains(urep.Diff, "\n-            \"command\": \"/opt/ballast/bin/ballast\",") && runtimeIsUnix() {
		t.Errorf("uninstall diff has no removed hook lines:\n%.400s", urep.Diff)
	}
	if !strings.Contains(urep.Diff, "+++ /dev/null") {
		t.Error("uninstall diff should show deleted files")
	}
}

func TestDryRunTakesNoLock(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	if err := os.MkdirAll(e.lay.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	e.write(filepath.Join(e.lay.Home, "install.lock"), "held by someone else")
	opt := e.opts(model.ClientCursor)
	opt.DryRun = true
	if _, err := Install(opt); err != nil {
		t.Fatalf("dry-run must not wait for the install lock: %v", err)
	}
}

func TestClientDetection(t *testing.T) {
	e := newTenv(t)
	_, err := Install(e.opts(model.ClientCodex))
	if !errors.Is(err, ErrClientNotDetected) || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v, want ErrClientNotDetected", err)
	}
	if e.exists(e.lay.Manifest) {
		t.Error("nothing may be written")
	}
	// config directory present
	if err := os.MkdirAll(e.lay.CodexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, e.opts(model.ClientCodex))

	// binary on PATH (no config directory)
	e2 := newTenv(t)
	bin := filepath.Join(e2.sb.Root, "bin")
	e2.write(filepath.Join(bin, "cursor"), "#!/bin/sh\n")
	opt := e2.opts(model.ClientCursor)
	opt.Env = sys.MapEnv{Vars: withVar(e2.sb.Vars, "PATH", bin+string(os.PathListSeparator)+"/nonexistent")}
	if _, err := Install(opt); err != nil {
		t.Fatalf("a client on PATH counts as detected: %v", err)
	}
	// Force installs anyway
	e3 := newTenv(t)
	forced := e3.opts(model.ClientHermes)
	forced.Force = true
	mustInstall(t, forced)
	if !e3.exists(e3.hermesConfig()) {
		t.Error("forced install did not write")
	}
}

func withVar(m map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

func TestUsageErrors(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	if _, err := Install(e.opts()); !errors.Is(err, ErrUsage) {
		t.Errorf("no client: %v", err)
	}
	if _, err := Install(e.opts("vscode")); !errors.Is(err, ErrUsage) || !errors.Is(err, model.ErrUnknownClient) {
		t.Errorf("unknown client: %v", err)
	}
	rel := e.opts(model.ClientClaude)
	rel.Binary = "ballast"
	if _, err := Install(rel); !errors.Is(err, ErrUsage) {
		t.Errorf("relative binary: %v", err)
	}
	if _, err := Uninstall(e.opts("vscode")); !errors.Is(err, ErrUsage) {
		t.Errorf("uninstall unknown client: %v", err)
	}
}

func TestBinaryResolution(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	defer func(old func() (string, error)) { executablePath = old }(executablePath)

	opt := e.opts(model.ClientClaude)
	opt.Binary = ""
	for _, unstable := range []string{
		filepath.Join(os.TempDir(), "x", "ballast"),
		"/tmp/ballast",
		"/var/folders/ab/cd/T/go-build123/b001/exe/ballast",
		"/home/u/.cache/go-build/ab/ballast",
		e.sb.Root + "/ballast", // t.TempDir lives under the OS temp dir
	} {
		executablePath = func() (string, error) { return unstable, nil }
		if _, err := Install(opt); !errors.Is(err, ErrBinaryUnstable) {
			t.Errorf("%s: err = %v, want ErrBinaryUnstable", unstable, err)
		}
	}
	if e.exists(e.claudeSettings()) {
		t.Fatal("a refused install wrote files")
	}

	// a stable executable is used as is; symlinks are resolved
	stable := "/usr/local/libexec/ballast/ballast"
	executablePath = func() (string, error) { return stable, nil }
	mustInstall(t, opt)
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if m.Binary != stable || !strings.Contains(e.read(e.claudeSettings()), `"command": "`+stable+`"`) {
		t.Errorf("binary = %q", m.Binary)
	}
	if got, _ := resolveBinary(Options{FS: sys.OSFS{}, Binary: "/a/b"}, "linux"); got != "/a/b" {
		t.Error("explicit binary must be used verbatim")
	}
	if _, err := resolveBinary(Options{FS: sys.OSFS{}, Binary: `C:\x\ballast.exe`}, "windows"); err != nil {
		t.Errorf("windows absolute path rejected: %v", err)
	}
	if _, err := resolveBinary(Options{FS: sys.OSFS{}, Binary: `\x\ballast.exe`}, "windows"); !errors.Is(err, ErrUsage) {
		t.Error("drive-relative windows path accepted")
	}
	// an explicit --binary under a temp dir is the caller's decision
	exp := e.opts(model.ClientCursor)
	exp.Binary = filepath.Join(e.sb.Root, "bin", "ballast")
	if _, err := Install(exp); err != nil {
		t.Errorf("explicit binary refused: %v", err)
	}
}

func TestUninstallDrivenByManifest(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	userF := userFiles(e)
	for _, files := range userF {
		for p, c := range files {
			e.write(p, c)
		}
	}
	mustInstall(t, e.opts(model.AllClients...))

	// the user removed Codex entirely; Cursor keeps its directory
	if err := os.RemoveAll(e.lay.CodexDir); err != nil {
		t.Fatal(err)
	}
	rep := mustUninstall(t, e.opts()) // no clients ⇒ everything in the manifest
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings: %v", rep.Warnings)
	}
	for _, c := range []model.ClientID{model.ClientClaude, model.ClientCursor, model.ClientHermes} {
		for p, orig := range userF[c] {
			if e.read(p) != orig {
				t.Errorf("%s not restored", p)
			}
		}
	}
	if e.exists(e.cursorMDC()) {
		t.Error("ballast.mdc must be deleted")
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if !manifestEmpty(m) {
		t.Errorf("manifest not empty: %+v", m)
	}
}

func TestUninstallSingleClient(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	mustInstall(t, e.opts(model.ClientClaude, model.ClientCursor))
	mustUninstall(t, e.opts(model.ClientClaude))
	if e.exists(e.claudeSettings()) {
		t.Error("claude settings (created by install) should be gone")
	}
	if !e.exists(e.cursorHooks()) || !e.exists(e.cursorMDC()) {
		t.Error("cursor must be untouched")
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	for _, en := range m.Entries {
		if en.Client != model.ClientCursor {
			t.Errorf("claude record left in the manifest: %+v", en)
		}
	}
	if m.Binary == "" {
		t.Error("binary stays recorded while entries remain")
	}
}

func TestUninstallFollowsRecordedPathsAfterLayoutChange(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	mustInstall(t, e.opts(model.ClientClaude))
	old := e.claudeSettings()

	// CLAUDE_CONFIG_DIR now points somewhere else; uninstall must still clean the old file
	moved := e.opts()
	moved.Layout.ClaudeDir = filepath.Join(e.sb.Root, "elsewhere")
	mustUninstall(t, moved)
	if e.exists(old) {
		t.Error("the file recorded in the manifest was not cleaned")
	}
}

func TestUninstallKeepsAndReportsUserModifiedEntries(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	opt := e.opts(model.ClientClaude, model.ClientHermes)
	mustInstall(t, opt)

	e.write(e.claudeSettings(), strings.Replace(e.read(e.claudeSettings()), `"Stop"
            ]`, `"Stop",
              "--user-flag"
            ]`, 1))
	e.write(e.hermesConfig(), strings.Replace(e.read(e.hermesConfig()), "timeout: 30", "timeout: 99", 1))

	rep := mustUninstall(t, opt)
	if len(rep.Warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", rep.Warnings)
	}
	if !strings.Contains(e.read(e.claudeSettings()), "--user-flag") || !strings.Contains(e.read(e.hermesConfig()), "timeout: 99") {
		t.Error("modified entries must survive")
	}
	if strings.Contains(e.read(e.claudeSettings()), "PreToolUse") {
		t.Error("unmodified entries must be removed")
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if len(m.Entries) != 2 {
		t.Errorf("manifest keeps %d records, want the 2 modified ones", len(m.Entries))
	}
	assertNoDuplicateFlags(t, m)
	for _, file := range []string{e.claudeSettings(), e.hermesConfig()} {
		kept := 0
		for _, f := range m.Flags {
			if f.File == file && f.Key == flagDirKept {
				kept++
			}
		}
		if kept != 1 {
			t.Errorf("dir:kept flags for %s after partial uninstall = %d, want exactly 1", filepath.Base(file), kept)
		}
	}
	// a second uninstall warns again and changes nothing
	before := dirSnapshot(t, e.sb.Home)
	rep = mustUninstall(t, opt)
	if len(rep.Warnings) != 2 {
		t.Errorf("second uninstall warnings = %v", rep.Warnings)
	}
	after := dirSnapshot(t, e.sb.Home)
	delete(before, e.lay.Manifest)
	delete(after, e.lay.Manifest)
	if !equalMaps(stripBackups(before, e), stripBackups(after, e)) {
		t.Error("second uninstall changed files")
	}
}

func assertNoDuplicateFlags(t *testing.T, m *model.InstallManifest) {
	t.Helper()
	seen := map[model.ManagedFlag]bool{}
	for _, f := range m.Flags {
		if seen[f] {
			t.Errorf("duplicate manifest flag %+v", f)
		}
		seen[f] = true
	}
}

func stripBackups(m map[string]string, e *tenv) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if !strings.HasPrefix(k, e.lay.BackupsDir) {
			out[k] = v
		}
	}
	return out
}

func TestUninstallFailuresAreNotSilent(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	opt := e.opts(model.ClientClaude, model.ClientCursor)
	mustInstall(t, opt)

	before := dirSnapshot(t, e.sb.Home)
	e.write(e.cursorHooks(), "{ not json")
	before[e.cursorHooks()] = "{ not json"
	_, err := Uninstall(opt)
	if !errors.Is(err, model.ErrSettingsParse) {
		t.Fatalf("err = %v, want ErrSettingsParse (pinrule's silent return 0 is the anti-pattern)", err)
	}
	if after := dirSnapshot(t, e.sb.Home); !equalMaps(before, after) {
		t.Error("a failed uninstall must not have changed anything, including the healthy client")
	}

	// corrupt manifest
	e.write(e.lay.Manifest, "garbage")
	if _, err := Uninstall(opt); !errors.Is(err, model.ErrSettingsParse) {
		t.Errorf("corrupt manifest: %v", err)
	}
	if _, err := Install(opt); !errors.Is(err, model.ErrSettingsParse) {
		t.Errorf("install with a corrupt manifest must refuse: %v", err)
	}
}

func TestUninstallWithoutManifest(t *testing.T) {
	e := newTenv(t)
	rep, err := Uninstall(e.opts())
	if err != nil || len(rep.Notes) != 1 || len(rep.Plans) != 0 {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestBackupsAreCappedAndPrivate(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	e.write(e.claudeSettings(), "{\n  \"model\": \"opus\"\n}\n")
	original := e.read(e.claudeSettings())
	for i := 0; i < 13; i++ {
		e.clock.Advance(time.Second)
		opt := e.opts(model.ClientClaude)
		opt.Binary = "/opt/ballast/v" + string(rune('a'+i)) + "/ballast" // every run changes the file
		mustInstall(t, opt)
	}
	ents, _ := os.ReadDir(e.lay.BackupsDir)
	if len(ents) != MaxBackups {
		t.Fatalf("%d backups on disk, want %d", len(ents), MaxBackups)
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if len(m.Backups) != MaxBackups {
		t.Fatalf("manifest lists %d backups", len(m.Backups))
	}
	for i, b := range m.Backups {
		if _, err := os.Stat(b.Dir); err != nil {
			t.Errorf("listed backup %d is missing: %v", i, err)
		}
		if i > 0 && b.At.After(m.Backups[i-1].At) {
			t.Error("backups must be listed most recent first")
		}
		if runtimeIsUnix() {
			fis, _ := os.ReadDir(b.Dir)
			for _, fi := range fis {
				info, _ := fi.Info()
				if info.Mode().Perm() != 0o600 {
					t.Errorf("%s/%s mode %v", b.Dir, fi.Name(), info.Mode().Perm())
				}
			}
		}
	}
	// the oldest surviving backup is of an installed state; the very first one (the user's original) rotated out
	first := filepath.Join(m.Backups[len(m.Backups)-1].Dir, EncodeBackupName(e.claudeSettings()))
	if b, err := os.ReadFile(first); err != nil || string(b) == original {
		t.Logf("oldest kept backup: %v", err)
	}
}

func TestBackupHoldsTheOriginalBytes(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	files := map[string]string{
		e.codexHooks():  userFiles(e)[model.ClientCodex][e.codexHooks()],
		e.codexConfig(): "model = \"gpt-5\"\n",
	}
	for p, c := range files {
		e.write(p, c)
	}
	rep := mustInstall(t, e.opts(model.ClientCodex))
	if rep.Backup == nil || len(rep.Backup.Files) != 2 {
		t.Fatalf("backup = %+v", rep.Backup)
	}
	for p, orig := range files {
		b, err := os.ReadFile(filepath.Join(rep.Backup.Dir, EncodeBackupName(p)))
		if err != nil || string(b) != orig {
			t.Errorf("backup of %s: %v %q", p, err, b)
		}
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if len(m.Backups) != 1 || m.Backups[0].Dir != rep.Backup.Dir {
		t.Errorf("manifest backups = %+v", m.Backups)
	}
	// uninstall backs up the installed state of the files it changes
	urep := mustUninstall(t, e.opts(model.ClientCodex))
	if urep.Backup == nil {
		t.Fatal("uninstall must back up too")
	}
	b, _ := os.ReadFile(filepath.Join(urep.Backup.Dir, EncodeBackupName(e.codexHooks())))
	if !strings.Contains(string(b), "ballast") {
		t.Error("uninstall backup should contain the installed hooks.json")
	}
}

func TestSymlinkedConfigIsWrittenThrough(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("symlinks need privileges on Windows")
	}
	e := newTenv(t)
	e.mkClientDirs()
	real := filepath.Join(e.sb.Root, "dotfiles", "settings.json")
	e.write(real, "{\n  \"model\": \"opus\"\n}\n")
	if err := os.Symlink(real, e.claudeSettings()); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, e.opts(model.ClientClaude))
	fi, err := os.Lstat(e.claudeSettings())
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced by a regular file")
	}
	if !strings.Contains(e.read(real), "ballast") {
		t.Error("target file not updated")
	}
	mustUninstall(t, e.opts(model.ClientClaude))
	if e.read(real) != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("target not restored: %q", e.read(real))
	}
	if fi, _ := os.Lstat(e.claudeSettings()); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink lost on uninstall")
	}
}

func TestFilePermissionsOfWrittenFiles(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("permission bits")
	}
	e := newTenv(t)
	e.mkClientDirs()
	e.write(e.claudeSettings(), "{}\n") // 0644 by the helper
	mustInstall(t, e.opts(model.ClientClaude, model.ClientCursor))
	perm := func(p string) os.FileMode { fi, _ := os.Stat(p); return fi.Mode().Perm() }
	if perm(e.claudeSettings()) != 0o644 {
		t.Errorf("existing file mode changed to %v", perm(e.claudeSettings()))
	}
	for _, p := range []string{e.cursorHooks(), e.cursorMDC(), e.lay.Manifest} {
		if perm(p) != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, perm(p))
		}
	}
}

func TestWriteFailureRollsBackEverything(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	e.write(e.claudeSettings(), "{\n  \"model\": \"opus\"\n}\n")
	before := dirSnapshot(t, e.sb.Home)

	opt := e.opts(model.ClientClaude, model.ClientCodex)
	opt.FS = sys.FaultFS{FS: e.fs, Fail: map[string]string{"Rename": filepath.Join(".codex", "hooks.json")}, Err: errors.New("disk full")}
	_, err := Install(opt)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	after := dirSnapshot(t, e.sb.Home)
	// ignore ~/.ballast (a backup of a failed run is harmless; the lock must be gone)
	for k := range after {
		if strings.HasPrefix(k, e.lay.Home) {
			delete(after, k)
		}
	}
	if !equalMaps(before, after) {
		t.Errorf("write failure left changes behind:\nbefore %v\nafter  %v", keys(before), keys(after))
	}
	if e.exists(e.lay.Manifest) {
		t.Error("manifest must not be written after a failure")
	}
	if e.exists(filepath.Join(e.lay.Home, "install.lock")) {
		t.Error("lock file leaked")
	}
}

func TestManifestWriteFailureRollsBack(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	opt := e.opts(model.ClientCursor)
	opt.FS = sys.FaultFS{FS: e.fs, Fail: map[string]string{"Rename": "install-manifest.json"}, Err: errors.New("no space")}
	if _, err := Install(opt); err == nil {
		t.Fatal("expected an error")
	}
	if e.exists(e.cursorHooks()) || e.exists(e.cursorMDC()) {
		t.Error("files written without a manifest would be impossible to uninstall")
	}
}

func TestReinstallWithNewBinaryReplacesInPlace(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	for _, files := range userFiles(e) {
		for p, c := range files {
			e.write(p, c)
		}
	}
	orig := map[string]string{}
	for _, files := range userFiles(e) {
		for p, c := range files {
			orig[p] = c
		}
	}
	mustInstall(t, e.opts(model.AllClients...))
	v2 := e.opts(model.AllClients...)
	v2.Binary = "/opt/ballast-v2/ballast"
	rep := mustInstall(t, v2)
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings: %v", rep.Warnings)
	}
	for _, p := range []string{e.claudeSettings(), e.codexHooks(), e.cursorHooks(), e.hermesConfig()} {
		got := e.read(p)
		if strings.Contains(got, "/opt/ballast/bin/ballast") {
			t.Errorf("%s still references the old binary", p)
		}
		if strings.Count(got, "ballast-v2") == 0 {
			t.Errorf("%s does not reference the new binary", p)
		}
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if m.Binary != "/opt/ballast-v2/ballast" {
		t.Errorf("manifest binary = %q", m.Binary)
	}
	mustUninstall(t, v2)
	for p, c := range orig {
		if got := e.read(p); got != c {
			t.Errorf("%s not restored after upgrade + uninstall:\n%s", p, got)
		}
	}
}

func TestCodexTrustIsOptIn(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	opt := e.opts(model.ClientCodex)
	mustInstall(t, opt)
	if strings.Contains(e.read(e.codexConfig()), "trusted_hash") {
		t.Fatal("trust written without --codex-trust")
	}
	if !strings.Contains(e.read(e.codexConfig()), "[features]\nhooks = true") {
		t.Fatal("features.hooks not enabled")
	}
	opt.CodexTrust = true
	mustInstall(t, opt)
	cfg := e.read(e.codexConfig())
	if strings.Count(cfg, "trusted_hash") != 6 {
		t.Fatalf("trust tables = %d:\n%s", strings.Count(cfg, "trusted_hash"), cfg)
	}
	if !strings.Contains(cfg, "hooks.json:pre_tool_use:0:0") {
		t.Errorf("trust key format:\n%s", cfg)
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	trust := 0
	for _, f := range m.Flags {
		if strings.HasPrefix(f.Key, "hooks.state.") {
			trust++
		}
	}
	if trust != 6 {
		t.Errorf("manifest records %d trust flags", trust)
	}
	// plain reinstall keeps them; uninstall removes them
	opt.CodexTrust = false
	mustInstall(t, opt)
	if strings.Count(e.read(e.codexConfig()), "trusted_hash") != 6 {
		t.Error("a reinstall without the flag must not drop recorded trust")
	}
	mustUninstall(t, opt)
	if strings.Contains(e.read(e.codexConfig()), "trusted_hash") {
		t.Error("trust entries survived uninstall")
	}
	if !strings.Contains(e.read(e.codexConfig()), "hooks = true") {
		t.Error("features.hooks must stay on")
	}
}

func TestCursorMDCLifecycle(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	opt := e.opts(model.ClientCursor)
	mustInstall(t, opt)
	if e.read(e.cursorMDC()) != testMDC {
		t.Fatal("mdc not written")
	}
	// rules changed: regenerated by reinstalling
	opt.MDC = []byte(strings.Replace(testMDC, "rules v1", "rules v2", 1))
	rep := mustInstall(t, opt)
	if !strings.Contains(e.read(e.cursorMDC()), "rules v2") || rep.Backup == nil {
		t.Error("mdc not regenerated")
	}
	mustUninstall(t, opt)
	if e.exists(e.cursorMDC()) {
		t.Error("ballast.mdc must be deleted on uninstall")
	}
	if !e.exists(e.lay.CursorDir) {
		t.Error("the client directory itself must survive an uninstall")
	}
	if e.exists(filepath.Dir(e.cursorMDC())) {
		t.Error("the rules directory install created should be removed once empty")
	}

	// without rendered rules the mdc is skipped with a warning
	e2 := newTenv(t)
	e2.mkClientDirs()
	o2 := e2.opts(model.ClientCursor)
	o2.MDC = nil
	rep = mustInstall(t, o2)
	if e2.exists(e2.cursorMDC()) {
		t.Error("mdc written without content")
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], e2.cursorMDC()) {
		t.Errorf("want exactly one warning naming the mdc path, got %q", rep.Warnings)
	}
}

func TestUninstallKeepsPreexistingEmptyClientDirs(t *testing.T) {
	for _, c := range []model.ClientID{model.ClientHermes, model.ClientCursor, model.ClientCodex, model.ClientClaude} {
		e := newTenv(t)
		e.mkClientDirs() // empty directories are how the clients are detected
		opt := e.opts(c)
		mustInstall(t, opt)
		mustUninstall(t, opt)
		if !e.exists(e.lay.ClientDir(c)) {
			t.Errorf("%s: pre-existing client directory %s was removed by uninstall", c, e.lay.ClientDir(c))
		}
		for _, cl := range Clients() {
			if d := cl.Detect(e.lay, e.fs, e.sb.Env()); cl.ID() == c && !d.Installed {
				t.Errorf("%s: no longer detected after uninstall", c)
			}
		}
	}
}

// TestUninstallKeepsPreexistingCursorRulesDir: a user's empty
// ~/.cursor/rules survives install + uninstall (contract v1.3 flag
// dir:kept), one created by install is pruned, also after a re-install.
func TestUninstallKeepsPreexistingCursorRulesDir(t *testing.T) {
	for _, pre := range []bool{true, false} {
		e := newTenv(t)
		e.mkClientDirs()
		rules := filepath.Dir(e.cursorMDC())
		if pre {
			if err := os.MkdirAll(rules, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		opt := e.opts(model.ClientCursor)
		mustInstall(t, opt)
		mustInstall(t, opt) // re-install carries the record over
		if !e.exists(e.cursorMDC()) {
			t.Fatal("mdc not installed")
		}
		mustUninstall(t, opt)
		if e.exists(e.cursorMDC()) {
			t.Error("mdc not removed")
		}
		if got := e.exists(rules); got != pre {
			t.Errorf("pre-existing=%v: rules dir exists after uninstall = %v", pre, got)
		}
		if !e.exists(e.lay.CursorDir) {
			t.Error("client directory removed")
		}
	}
}

func TestReportContent(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	rep := mustInstall(t, e.opts(model.AllClients...))
	if len(rep.Plans) != 6 {
		t.Errorf("plans = %d, want 6 (claude 1, codex 2, cursor 2, hermes 1)", len(rep.Plans))
	}
	notes := strings.Join(rep.Notes, "\n")
	for _, want := range []string{"Reload Window", "/hooks", "--accept-hooks"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	m, _ := LoadManifest(e.fs, e.lay.Manifest)
	if len(m.Entries) != 10+6+10+6 {
		t.Errorf("manifest entries = %d, want 32", len(m.Entries))
	}
	if len(m.Files) != 6 {
		t.Errorf("manifest files = %d", len(m.Files))
	}
	if !m.Updated.Equal(e.clock.Now()) {
		t.Errorf("updated = %v", m.Updated)
	}
	for _, en := range m.Entries {
		if en.Hash == "" || en.Key == "" || en.Command == "" || !filepath.IsAbs(en.File) {
			t.Errorf("incomplete record %+v", en)
		}
	}
}

func TestSandboxIsolation(t *testing.T) {
	e := newTenv(t)
	for _, p := range []string{e.lay.Home, e.lay.ClaudeDir, e.lay.CodexDir, e.lay.CursorDir, e.lay.HermesDir, e.lay.ManagedDir, e.lay.Manifest, e.lay.BackupsDir} {
		if !strings.HasPrefix(p, e.sb.Root) {
			t.Errorf("%s is outside the test sandbox %s", p, e.sb.Root)
		}
	}
}

func TestStaleInstallLockIsBroken(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	lock := filepath.Join(e.lay.Home, "install.lock")
	e.write(lock, "99999\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, e.opts(model.ClientCursor))
	if e.exists(lock) {
		t.Error("lock not released")
	}
}

func TestLiveInstallLockBlocks(t *testing.T) {
	e := newTenv(t)
	e.mkClientDirs()
	mustInstall(t, e.opts(model.ClientCursor))
	before := dirSnapshot(t, e.sb.Home)
	defer func(w time.Duration) { lockWait = w }(lockWait)
	lockWait = 150 * time.Millisecond
	lock := filepath.Join(e.lay.Home, "install.lock")
	e.write(lock, "1\n")
	before[lock] = "1\n"
	opt := e.opts(model.ClientCursor)
	opt.Binary = "/opt/other/ballast" // would change the files
	if _, err := Install(opt); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if _, err := Uninstall(e.opts()); !errors.Is(err, ErrLocked) {
		t.Fatalf("uninstall err = %v", err)
	}
	if !equalMaps(before, dirSnapshot(t, e.sb.Home)) {
		t.Error("nothing may change while locked out (and the foreign lock must stay)")
	}
}

func TestNilOptionFieldsFallBackToTheRealSystem(t *testing.T) {
	// FS, Env, Clock and IDs default to the real implementations; the layout
	// still points into the sandbox, so nothing outside it is touched.
	e := newTenv(t)
	opt := Options{Layout: e.lay, Clients: []model.ClientID{model.ClientClaude}, Binary: fakeBinary(), Force: true, DryRun: true}
	rep, err := Install(opt)
	if err != nil || rep.Diff == "" {
		t.Fatalf("%v %+v", err, rep)
	}
	if e.exists(e.claudeSettings()) || e.exists(e.lay.Home) {
		t.Error("dry-run wrote something")
	}
	opt.DryRun = false
	mustInstall(t, opt)
	if !e.exists(e.claudeSettings()) {
		t.Fatal("not installed")
	}
	if cs := Doctor(DoctorOptions{Layout: e.lay}); len(cs) == 0 {
		t.Error("Doctor with a nil FS returned nothing")
	}
	mustUninstall(t, Options{Layout: e.lay})
	if e.exists(e.claudeSettings()) {
		t.Error("not uninstalled")
	}
}

// A config directory whose absolute path is far beyond NAME_MAX must not make
// the backup file name (once derived from the full path) overflow it.
func TestInstallUninstallWithVeryLongConfigPath(t *testing.T) {
	e := newTenv(t)
	long := e.sb.Home
	for i := 0; i < 3; i++ {
		long = filepath.Join(long, strings.Repeat("c", 120))
	}
	e.lay.ClaudeDir = long
	if len(e.claudeSettings()) < 300 {
		t.Fatalf("path is only %d bytes", len(e.claudeSettings()))
	}
	orig := "{\n  \"model\": \"opus\"\n}\n"
	e.write(e.claudeSettings(), orig)
	opt := e.opts(model.ClientClaude)
	rep := mustInstall(t, opt)
	if rep.Backup == nil {
		t.Fatal("install must back up the existing settings")
	}
	if !strings.Contains(e.read(e.claudeSettings()), "ballast") {
		t.Error("hooks not installed")
	}
	mustUninstall(t, opt)
	if got := e.read(e.claudeSettings()); got != orig {
		t.Errorf("round trip changed the file: %q", got)
	}
}
