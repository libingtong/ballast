package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// orgLayout builds a layout by hand so the rendered paths do not depend on
// the OS running the test.
func orgLayout(t *testing.T, goos string) layout.Layout {
	t.Helper()
	managed := layout.ManagedDirFor(goos)
	sep := "/"
	if goos == "windows" {
		sep = `\`
	}
	return layout.Layout{GOOS: goos, ManagedDir: managed, OrgRules: managed + sep + "ballast" + sep + "rules.json"}
}

func TestOrgRenderGolden(t *testing.T) {
	out, err := OrgRender(orgLayout(t, "linux"), sys.OSFS{}, OrgRenderOptions{GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "org_linux", string(out))

	out, err = OrgRender(orgLayout(t, "linux"), sys.OSFS{}, OrgRenderOptions{
		GOOS: "linux", Binary: "/opt/ballast/bin/ballast", Protected: []string{"/srv/policy", "/srv/policy"}, MCPAllow: []string{"github", "memory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "org_linux_custom", string(out))

	out, err = OrgRender(orgLayout(t, "windows"), sys.OSFS{}, OrgRenderOptions{GOOS: "windows"})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "org_windows", string(out))
}

func TestOrgRenderShape(t *testing.T) {
	out, err := OrgRender(orgLayout(t, "darwin"), sys.OSFS{}, OrgRenderOptions{GOOS: "darwin"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		AllowManagedHooksOnly bool `json:"allowManagedHooksOnly"`
		Hooks                 map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
		Sandbox struct {
			Enabled                  bool     `json:"enabled"`
			AllowUnsandboxedCommands bool     `json:"allowUnsandboxedCommands"`
			FailIfUnavailable        bool     `json:"failIfUnavailable"`
			ExcludedCommands         []string `json:"excludedCommands"`
			Filesystem               struct {
				DenyWrite []string `json:"denyWrite"`
			} `json:"filesystem"`
		} `json:"sandbox"`
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		AllowManagedPermissionRulesOnly bool `json:"allowManagedPermissionRulesOnly"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.AllowManagedHooksOnly || !doc.AllowManagedPermissionRulesOnly || !doc.Sandbox.Enabled || doc.Sandbox.AllowUnsandboxedCommands || !doc.Sandbox.FailIfUnavailable {
		t.Errorf("security switches wrong: %+v", doc)
	}
	if doc.Sandbox.ExcludedCommands == nil || len(doc.Sandbox.ExcludedCommands) != 0 {
		t.Error("excludedCommands must be an empty list, not absent")
	}
	if len(doc.Hooks) != 10 {
		t.Fatalf("%d hook events, want the 10 claude natives", len(doc.Hooks))
	}
	for ev, groups := range doc.Hooks {
		ne, ok := model.LookupNative(model.ClientClaude, ev)
		if !ok || len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Errorf("%s: %+v", ev, groups)
			continue
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "/usr/local/libexec/ballast/ballast" {
			t.Errorf("%s: %+v", ev, h)
		}
		if !reflect.DeepEqual(h.Args, []string{"hook", "--client", "claude-code", "--event", ev}) {
			t.Errorf("%s: args %v (command and args must be separate)", ev, h.Args)
		}
		if (groups[0].Matcher == "*") != ne.Event.IsToolEvent() {
			t.Errorf("%s: matcher %q", ev, groups[0].Matcher)
		}
	}
	joined := strings.Join(doc.Permissions.Deny, "\n")
	for _, want := range []string{"Edit(//Library/Application Support/ClaudeCode/ballast/**)", "Edit(~/.ballast/**)", "Edit(//usr/local/libexec/ballast/**)", "Edit(~/.claude/settings*.json)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("permissions.deny lacks %q:\n%s", want, joined)
		}
	}
	for _, want := range []string{"/Library/Application Support/ClaudeCode/ballast", "~/.ballast", "./.ballast", "/usr/local/libexec/ballast", "~/.claude/settings*.json"} {
		found := false
		for _, p := range doc.Sandbox.Filesystem.DenyWrite {
			found = found || p == want
		}
		if !found {
			t.Errorf("denyWrite lacks %q: %v", want, doc.Sandbox.Filesystem.DenyWrite)
		}
	}
	if strings.Contains(string(out), "allowedMcpServers") {
		t.Error("no MCP allowlist was requested")
	}
	if !strings.HasSuffix(string(out), "}\n") {
		t.Error("output must end with a newline")
	}
}

func TestOrgRenderIsPrintOnlyByDefault(t *testing.T) {
	e := newTenv(t)
	if _, err := OrgRender(e.lay, e.fs, OrgRenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if e.exists(filepath.Join(e.lay.ManagedDir, "managed-settings.d")) {
		t.Error("org render must not write without --write --i-am-admin")
	}
}

func TestOrgRenderWriteGating(t *testing.T) {
	e := newTenv(t)
	defer func(f func() int) { geteuid = f }(geteuid)
	dst := filepath.Join(e.lay.ManagedDir, "managed-settings.d", "ballast.json")
	noSudo := sys.MapEnv{Vars: map[string]string{}} // keep the pre-flight independent of the real SUDO_UID

	geteuid = func() int { return 0 }
	if out, err := OrgRender(e.lay, e.fs, OrgRenderOptions{Write: true}); !errors.Is(err, ErrOrgWriteDenied) || len(out) == 0 {
		t.Errorf("--write without --i-am-admin: %v", err)
	}
	geteuid = func() int { return 1000 }
	if _, err := OrgRender(e.lay, e.fs, OrgRenderOptions{Write: true, IAmAdmin: true, Env: noSudo}); !errors.Is(err, ErrOrgWriteDenied) {
		t.Errorf("--write as non-root: %v", err)
	}
	geteuid = func() int { return -1 } // windows
	if _, err := OrgRender(e.lay, e.fs, OrgRenderOptions{Write: true, IAmAdmin: true, Env: noSudo}); !errors.Is(err, ErrOrgWriteDenied) {
		t.Errorf("--write on windows: %v", err)
	}
	if e.exists(dst) {
		t.Fatal("a denied write created the file")
	}

	geteuid = func() int { return 0 }
	out, err := OrgRender(e.lay, e.fs, OrgRenderOptions{Write: true, IAmAdmin: true, MCPAllow: []string{"github"}, Env: noSudo})
	if err != nil {
		t.Fatal(err)
	}
	if e.read(dst) != string(out) {
		t.Error("the written file differs from the printed output")
	}
	if runtimeIsUnix() {
		fi, _ := os.Stat(dst)
		di, _ := os.Stat(filepath.Dir(dst))
		if fi.Mode().Perm() != 0o644 || di.Mode().Perm()&0o055 != 0o055 {
			t.Errorf("managed files must be world-readable: file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	// the written template is recognised as a managed Ballast deployment
	if managed, path, _ := DetectManaged(e.lay, e.fs); !managed || path != dst {
		t.Errorf("DetectManaged = %v %q", managed, path)
	}
	// ... and a user-level install is refused from then on
	e.mkClientDirs()
	if _, err := Install(e.opts(model.ClientClaude)); !errors.Is(err, model.ErrManagedPresent) {
		t.Errorf("install after org render: %v", err)
	}
	// rewriting is atomic and idempotent
	out2, err := OrgRender(e.lay, e.fs, OrgRenderOptions{Write: true, IAmAdmin: true, MCPAllow: []string{"github"}, Env: noSudo})
	if err != nil || string(out2) != string(out) || e.read(dst) != string(out) {
		t.Errorf("second write: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Dir(dst))
	if len(ents) != 1 {
		t.Errorf("temp files left in the managed directory: %d entries", len(ents))
	}
}

func TestDirOf(t *testing.T) {
	for _, c := range []struct{ in, goos, want string }{
		{"/usr/local/libexec/ballast/ballast", "linux", "/usr/local/libexec/ballast"},
		{`C:\Program Files\Ballast\ballast.exe`, "windows", `C:\Program Files\Ballast`},
		{"C:/x/ballast.exe", "windows", "C:/x"},
		{"/ballast", "linux", "/"},
		{"ballast", "linux", "."},
	} {
		if got := dirOf(c.in, c.goos); got != c.want {
			t.Errorf("dirOf(%q,%s) = %q, want %q", c.in, c.goos, got, c.want)
		}
	}
}
