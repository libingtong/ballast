package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// fakeBinary is a stable-looking absolute path (not required to exist).
func fakeBinary() string {
	if runtime.GOOS == "windows" {
		return `C:\opt\ballast\bin\ballast.exe`
	}
	return "/opt/ballast/bin/ballast"
}

const testMDC = "---\nalwaysApply: true\n---\n<!-- ballast-managed: do not edit; regenerate with `ballast install --client cursor` -->\nrules v1\n"

type tenv struct {
	t     *testing.T
	sb    *testenv.Sandbox
	lay   layout.Layout
	fs    sys.FS
	clock *sys.FakeClock
	ids   *sys.SeqID
}

func newTenv(t *testing.T) *tenv {
	t.Helper()
	sb := testenv.New(t)
	return &tenv{
		t: t, sb: sb, lay: sb.Layout(t), fs: sys.OSFS{},
		clock: sys.NewFakeClock(time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)),
		ids:   &sys.SeqID{},
	}
}

// opts builds Options for the given clients with all client dirs created
// (so detection succeeds) unless force is used by the caller.
func (e *tenv) opts(clients ...model.ClientID) Options {
	return Options{
		Layout: e.lay, FS: e.fs, Env: e.sb.Env(), Clock: e.clock, IDs: e.ids,
		Clients: clients, Binary: fakeBinary(), MDC: []byte(testMDC),
	}
}

func (e *tenv) mkClientDirs() {
	e.t.Helper()
	for _, c := range model.AllClients {
		if err := os.MkdirAll(e.lay.ClientDir(c), 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *tenv) write(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *tenv) read(path string) string {
	e.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *tenv) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (e *tenv) claudeSettings() string { return filepath.Join(e.lay.ClaudeDir, "settings.json") }
func (e *tenv) codexHooks() string     { return filepath.Join(e.lay.CodexDir, "hooks.json") }
func (e *tenv) codexConfig() string    { return filepath.Join(e.lay.CodexDir, "config.toml") }
func (e *tenv) cursorHooks() string    { return filepath.Join(e.lay.CursorDir, "hooks.json") }
func (e *tenv) cursorMDC() string      { return filepath.Join(e.lay.CursorDir, "rules", "ballast.mdc") }
func (e *tenv) hermesConfig() string   { return filepath.Join(e.lay.HermesDir, "config.yaml") }

func mustInstall(t *testing.T, opt Options) *Report {
	t.Helper()
	rep, err := Install(opt)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return rep
}

func mustUninstall(t *testing.T, opt Options) *Report {
	t.Helper()
	rep, err := Uninstall(opt)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	return rep
}

func jsonNumber(s string) json.Number { return json.Number(s) }

func runtimeIsUnix() bool { return runtime.GOOS != "windows" }
