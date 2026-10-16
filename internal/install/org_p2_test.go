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
	"github.com/libingtong/ballast/internal/tier"
)

type renderedSettings struct {
	Sandbox struct {
		Filesystem struct {
			DenyWrite []string `json:"denyWrite"`
		} `json:"filesystem"`
	} `json:"sandbox"`
	Permissions struct {
		Deny []string `json:"deny"`
	} `json:"permissions"`
}

func parseSettings(t *testing.T, b []byte) renderedSettings {
	t.Helper()
	var s renderedSettings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, b)
	}
	return s
}

// The shared golden of internal/tier is the single source of truth for the
// denyWrite / permissions.deny strings: `org render` must print them
// verbatim (docs/p2-design.md §3.5).
func TestOrgRenderManagedFormsGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "tier", "testdata", "managed_forms.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Name       string   `json:"name"`
			Goos       string   `json:"goos"`
			ManagedDir string   `json:"managed_dir"`
			Self       string   `json:"self"`
			Extra      []string `json:"extra"`
			DenyWrite  []string `json:"deny_write"`
			PermDeny   []string `json:"perm_deny"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 4 {
		t.Fatalf("golden has %d cases", len(f.Cases))
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			sep := "/"
			if c.Goos == "windows" {
				sep = `\`
			}
			lay := layout.Layout{GOOS: c.Goos, ManagedDir: c.ManagedDir, OrgRules: c.ManagedDir + sep + "ballast" + sep + "rules.json"}
			out, err := OrgRender(lay, sys.OSFS{}, OrgRenderOptions{GOOS: c.Goos, Binary: c.Self, Protected: c.Extra})
			if err != nil {
				t.Fatal(err)
			}
			s := parseSettings(t, out)
			if !reflect.DeepEqual(s.Sandbox.Filesystem.DenyWrite, c.DenyWrite) {
				t.Errorf("denyWrite:\n got %q\nwant %q", s.Sandbox.Filesystem.DenyWrite, c.DenyWrite)
			}
			if !reflect.DeepEqual(s.Permissions.Deny, c.PermDeny) {
				t.Errorf("permissions.deny:\n got %q\nwant %q", s.Permissions.Deny, c.PermDeny)
			}
			// the v1.5 any-depth groups and the repo-level settings form
			for _, want := range []string{"**/.ballast", "**/.claude/settings*.json", "./.claude/settings*.json"} {
				if !contains(s.Sandbox.Filesystem.DenyWrite, want) {
					t.Errorf("denyWrite lacks %q", want)
				}
			}
			for _, want := range []string{"Edit(**/.ballast/**)", "Write(**/.claude/settings*.json)", "NotebookEdit(./.claude/settings*.json)"} {
				if !contains(s.Permissions.Deny, want) {
					t.Errorf("permissions.deny lacks %q", want)
				}
			}
		})
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestOrgRenderFullShape(t *testing.T) {
	lay := orgLayout(t, "linux")
	r, err := OrgRenderFull(lay, sys.OSFS{}, OrgRenderOptions{
		GOOS: "linux", HomeRoot: []string{"/home", "/home/corp.example", "/home"},
		Protected: []string{"~/.config/acme/ci-token", "/srv/policy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// OrgRender returns the settings fragment only (valid JSON, equals the
	// written ballast.json).
	plain, err := OrgRender(lay, sys.OSFS{}, OrgRenderOptions{
		GOOS: "linux", HomeRoot: []string{"/home", "/home/corp.example", "/home"},
		Protected: []string{"~/.config/acme/ci-token", "/srv/policy"},
	})
	if err != nil || string(plain) != string(r.Settings) {
		t.Errorf("OrgRender != Settings (%v)", err)
	}
	if !json.Valid(r.Settings) || !json.Valid(r.OrgJSON) {
		t.Fatal("Settings and OrgJSON must be pure JSON")
	}
	if strings.Contains(string(r.OrgJSON), "//") || strings.Contains(string(r.OrgJSON), "\n#") {
		t.Error("written org.json must not carry comments")
	}
	var org struct {
		V                int                         `json:"v"`
		Tier             string                      `json:"tier"`
		RunnerSandbox    string                      `json:"runner_sandbox"`
		RunnerNetwork    string                      `json:"runner_network"`
		RunnerAllowWrite []string                    `json:"runner_allow_write"`
		HomeRoot         []string                    `json:"home_root"`
		WeakenedProject  model.WeakenedProjectPolicy `json:"weakened_project"`
		ProtectedExtra   []string                    `json:"protected_extra"`
	}
	if err := json.Unmarshal(r.OrgJSON, &org); err != nil {
		t.Fatal(err)
	}
	if org.V != 1 || org.Tier != "T1" || org.RunnerSandbox != "require" || org.RunnerNetwork != "deny" {
		t.Errorf("org.json: %+v", org)
	}
	if org.RunnerAllowWrite == nil || len(org.RunnerAllowWrite) != 0 || !strings.Contains(string(r.OrgJSON), `"runner_allow_write": []`) {
		t.Errorf("runner_allow_write must be spelled out as []: %s", r.OrgJSON)
	}
	if !reflect.DeepEqual(org.HomeRoot, []string{"/home", "/home/corp.example"}) {
		t.Errorf("home_root = %v", org.HomeRoot)
	}
	// v1.5 conditional contract: the rendered default is the frozen constant.
	if org.WeakenedProject != tier.DefaultWeakenedProject {
		t.Errorf("weakened_project = %q, want tier.DefaultWeakenedProject = %q", org.WeakenedProject, tier.DefaultWeakenedProject)
	}
	if !reflect.DeepEqual(org.ProtectedExtra, []string{"~/.config/acme/ci-token", "/srv/policy"}) {
		t.Errorf("protected_extra = %v", org.ProtectedExtra)
	}
	// Detect recomputes ManagedForms from org.protected_extra and the hook
	// command: the rendered strings must be exactly those.
	s := parseSettings(t, r.Settings)
	dw, pd := tier.ManagedForms(lay, "/usr/local/libexec/ballast/ballast", org.ProtectedExtra)
	if !reflect.DeepEqual(dw, s.Sandbox.Filesystem.DenyWrite) || !reflect.DeepEqual(pd, s.Permissions.Deny) {
		t.Error("settings differ from ManagedForms(org.protected_extra)")
	}

	// stdout rendering: settings first, then the commented org.json section.
	out := string(r.Stdout)
	if !strings.HasPrefix(out, string(r.Settings)) || !strings.HasSuffix(out, string(r.OrgJSON)) {
		t.Fatalf("stdout must be settings + org section:\n%s", out)
	}
	for _, want := range []string{
		"runner_allow_write:每一项都等于把该目录交给 agent 可改写的测试代码",
		"共享构建缓存会被沙箱外的构建复用",
		"建议保持为空",
		"deny 在 macOS 上连回环也拒绝",
		"loopback 让 macOS 上的测试能连主机回环上的任何服务",
		"allow 放开出站 IP 但不放开本地 socket,两者都是残余风险",
		"weakened_project:deny 会让带削弱设置的项目里所有工具调用被拒,audit 只审计",
		"managed-denywrite-anydepth",
		"home_root:仅当 passwd 查不到当前 uid 时才采信 $HOME",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(out[len(r.Settings):], string(r.OrgJSON)), "\n") {
		if line != "" && !strings.HasPrefix(line, "// ") {
			t.Errorf("non-comment line before org.json: %q", line)
		}
	}
	checkGolden(t, "org_stdout_linux_home_root", out)

	// without home_root the stdout warns about sssd/LDAP users
	r2, err := OrgRenderFull(lay, sys.OSFS{}, OrgRenderOptions{GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r2.Stdout), "home_root:未设置") || strings.Contains(string(r2.OrgJSON), "home_root") {
		t.Errorf("no --home-root:\n%s", r2.Stdout)
	}
	checkGolden(t, "org_stdout_linux", string(r2.Stdout))
	checkGolden(t, "org_json_linux", string(r2.OrgJSON))

	// Windows is T0 only: no org.json, stdout == settings.
	r3, err := OrgRenderFull(orgLayout(t, "windows"), sys.OSFS{}, OrgRenderOptions{GOOS: "windows"})
	if err != nil {
		t.Fatal(err)
	}
	if r3.OrgJSON != nil || string(r3.Stdout) != string(r3.Settings) {
		t.Error("windows must not render org.json")
	}
}

func TestOrgRenderInputValidation(t *testing.T) {
	lay := orgLayout(t, "linux")
	for _, c := range []struct {
		name string
		opt  OrgRenderOptions
		want string
	}{
		{"relative home root", OrgRenderOptions{HomeRoot: []string{"home"}}, "absolute"},
		{"unclean home root", OrgRenderOptions{HomeRoot: []string{"/home/"}}, "clean"},
		{"dotdot home root", OrgRenderOptions{HomeRoot: []string{"/home/../etc"}}, "clean"},
		{"root home root", OrgRenderOptions{HomeRoot: []string{"/"}}, `"/"`},
		{"tilde home root", OrgRenderOptions{HomeRoot: []string{"/home/~u"}}, "glob"},
		{"glob home root", OrgRenderOptions{HomeRoot: []string{"/home/*"}}, "glob"},
		{"brace home root", OrgRenderOptions{HomeRoot: []string{"/home/{a,b}"}}, "glob"},
		{"closing bracket home root", OrgRenderOptions{HomeRoot: []string{"/home/a]"}}, "glob"},
		{"relative protect", OrgRenderOptions{Protected: []string{"secrets"}}, "--protect"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.opt.GOOS = "linux"
			r, err := OrgRenderFull(lay, sys.OSFS{}, c.opt)
			if !errors.Is(err, ErrUsage) || r != nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, result %v", err, r)
			}
		})
	}
}

// The org.json that --write puts down must pass the validation Detect and
// Locked apply (tier.LoadOrgConfig), and be pure JSON.
func TestOrgRenderWritesValidOrgJSON(t *testing.T) {
	e := newTenv(t)
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 0 }
	env := sys.MapEnv{Vars: map[string]string{}} // no SUDO_UID
	// org.json exists only for unix targets: render for linux explicitly so
	// the test does not depend on the host OS (the writes go to the temp dir).
	e.lay.GOOS = "linux"
	opt := OrgRenderOptions{
		GOOS: "linux", Write: true, IAmAdmin: true, Env: env,
		HomeRoot: []string{"/home"}, Protected: []string{"/srv/policy", "~/.config/acme/ci-token"},
	}
	r, err := OrgRenderFull(e.lay, e.fs, opt)
	if err != nil {
		t.Fatal(err)
	}
	orgPath := filepath.Join(e.lay.ManagedDir, "ballast", "org.json")
	settingsPath := filepath.Join(e.lay.ManagedDir, "managed-settings.d", "ballast.json")
	if !reflect.DeepEqual(r.Written, []string{settingsPath, orgPath}) {
		t.Errorf("Written = %v (org.json must come last)", r.Written)
	}
	if e.read(orgPath) != string(r.OrgJSON) || e.read(settingsPath) != string(r.Settings) {
		t.Error("written files differ from the rendered documents")
	}
	if runtimeIsUnix() {
		fi, _ := os.Stat(orgPath)
		di, _ := os.Stat(filepath.Dir(orgPath))
		if fi.Mode().Perm() != 0o644 || di.Mode().Perm()&0o055 != 0o055 {
			t.Errorf("org.json perms: file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
		}
	}
	cfg, err := tier.LoadOrgConfig(e.fs, orgPath)
	if err != nil {
		t.Fatalf("LoadOrgConfig rejects the rendered org.json: %v\n%s", err, e.read(orgPath))
	}
	if cfg.Tier != model.TierT1 || cfg.RunnerSandbox != model.SandboxRequire || cfg.RunnerNetwork != model.NetworkDeny ||
		cfg.WeakenedProject != tier.DefaultWeakenedProject || !reflect.DeepEqual(cfg.HomeRoot, []string{"/home"}) ||
		!reflect.DeepEqual(cfg.ProtectedExtra, []string{"/srv/policy", "~/.config/acme/ci-token"}) || len(cfg.RunnerAllowWrite) != 0 {
		t.Errorf("decoded org.json: %+v", cfg)
	}
	// no home_root, no protect: still valid
	e2 := newTenv(t)
	e2.lay.GOOS = "linux"
	if _, err := OrgRenderFull(e2.lay, e2.fs, OrgRenderOptions{GOOS: "linux", Write: true, IAmAdmin: true, Env: env}); err != nil {
		t.Fatal(err)
	}
	if _, err := tier.LoadOrgConfig(e2.fs, filepath.Join(e2.lay.ManagedDir, "ballast", "org.json")); err != nil {
		t.Errorf("minimal org.json rejected: %v", err)
	}
}

// §3.5 "--write 的家目录预检" table.
func TestOrgRenderWritePreflight(t *testing.T) {
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 0 }
	boom := errors.New("backend down")
	for _, c := range []struct {
		name      string
		sudoUID   string // "" ⇒ unset
		lookup    func(int) (string, error)
		homeRoot  []string
		wantErr   bool
		wantFiles bool
		wantNote  string
		wantMsg   []string
	}{
		{
			name: "SUDO_UID, no passwd entry, no home_root: refused", sudoUID: "1000",
			lookup:  func(int) (string, error) { return "", sys.ErrNoPasswdEntry },
			wantErr: true,
			wantMsg: []string{"uid 1000", "没有 home_root", "--home-root /home", "cgo", "/etc/passwd", "未写入任何文件"},
		},
		{
			name: "SUDO_UID, no passwd entry, home_root: written", sudoUID: "1000",
			lookup:   func(int) (string, error) { return "", sys.ErrNoPasswdEntry },
			homeRoot: []string{"/home"}, wantFiles: true,
		},
		{
			name: "SUDO_UID with a passwd entry: written", sudoUID: "1000",
			lookup:    func(int) (string, error) { return "/home/u", nil },
			wantFiles: true,
		},
		{
			name: "no SUDO_UID: written with a note", sudoUID: "",
			lookup:    func(int) (string, error) { t.Fatal("must not look up without SUDO_UID"); return "", nil },
			wantFiles: true, wantNote: "无法替最终用户预检,部署后请让每个用户运行 `ballast doctor`",
		},
		{
			name: "garbage SUDO_UID: written with a note", sudoUID: "abc",
			lookup:    func(int) (string, error) { t.Fatal("must not look up"); return "", nil },
			wantFiles: true, wantNote: "无法替最终用户预检",
		},
		{
			name: "lookup failure other than no-entry: written with a note", sudoUID: "1000",
			lookup:    func(int) (string, error) { return "", boom },
			wantFiles: true, wantNote: "backend down",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newTenv(t)
			e.lay.GOOS = "linux" // org.json is a unix-target document
			vars := map[string]string{}
			if c.sudoUID != "" {
				vars["SUDO_UID"] = c.sudoUID
			}
			var gotUID int
			lookup := func(uid int) (string, error) { gotUID = uid; return c.lookup(uid) }
			r, err := OrgRenderFull(e.lay, e.fs, OrgRenderOptions{
				GOOS: "linux", Write: true, IAmAdmin: true, Env: sys.MapEnv{Vars: vars}, LookupHome: lookup, HomeRoot: c.homeRoot,
			})
			dst := filepath.Join(e.lay.ManagedDir, "managed-settings.d", "ballast.json")
			orgPath := filepath.Join(e.lay.ManagedDir, "ballast", "org.json")
			if c.wantErr {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("err = %v, want ErrUsage (exit 2)", err)
				}
				if errors.Is(err, ErrOrgWriteDenied) {
					t.Error("the pre-flight refusal is exit 2, not the exit 4 of ErrOrgWriteDenied")
				}
				for _, m := range c.wantMsg {
					if !strings.Contains(err.Error(), m) {
						t.Errorf("message lacks %q: %v", m, err)
					}
				}
				if e.exists(dst) || e.exists(orgPath) || e.exists(filepath.Join(e.lay.ManagedDir, "ballast")) || e.exists(e.lay.ClaudeManagedSettingsD()) {
					t.Error("a refused pre-flight must write nothing at all")
				}
				if gotUID != 1000 {
					t.Errorf("looked up uid %d, want SUDO_UID", gotUID)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.wantFiles && (!e.exists(dst) || !e.exists(orgPath)) {
				t.Error("files were not written")
			}
			if c.wantNote == "" && len(r.Notes) != 0 {
				t.Errorf("unexpected notes %v", r.Notes)
			}
			if c.wantNote != "" && (len(r.Notes) != 1 || !strings.Contains(r.Notes[0], c.wantNote)) {
				t.Errorf("notes = %v, want one containing %q", r.Notes, c.wantNote)
			}
		})
	}
}

// The pre-flight only runs for --write: printing is unaffected.
func TestOrgRenderPreflightOnlyForWrite(t *testing.T) {
	e := newTenv(t)
	_, err := OrgRenderFull(e.lay, e.fs, OrgRenderOptions{
		Env:        sys.MapEnv{Vars: map[string]string{"SUDO_UID": "1000"}},
		LookupHome: func(int) (string, error) { return "", sys.ErrNoPasswdEntry },
	})
	if err != nil {
		t.Fatalf("print-only render must not pre-flight: %v", err)
	}
}

// Failing before the home check keeps the earlier gates' errors intact.
func TestOrgRenderWriteGatesPrecedePreflight(t *testing.T) {
	e := newTenv(t)
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 1000 }
	_, err := OrgRenderFull(e.lay, e.fs, OrgRenderOptions{
		Write: true, IAmAdmin: true,
		Env:        sys.MapEnv{Vars: map[string]string{"SUDO_UID": "1000"}},
		LookupHome: func(int) (string, error) { return "", sys.ErrNoPasswdEntry },
	})
	if !errors.Is(err, ErrOrgWriteDenied) {
		t.Errorf("non-root --write: %v", err)
	}
}

// Every rendered org.json is run through tier.LoadOrgConfig before it can be
// printed or written: a document the hook would refuse must never be
// deployed (the host would silently stay unlocked).
func TestOrgRenderRejectsDocumentTierRefuses(t *testing.T) {
	if err := validateOrgJSON([]byte(`{"v":1,"tier":"T1","home_root":["/home/{a,b}"]}`), "/etc/ballast/org.json"); !errors.Is(err, ErrUsage) {
		t.Errorf("a home_root tier refuses must be ErrUsage: %v", err)
	}
	if err := validateOrgJSON([]byte(`{"v":1,"tier":"T1","home_root":["/home"]}`), "/etc/ballast/org.json"); err != nil {
		t.Errorf("valid document rejected: %v", err)
	}
	// the install-side home_root character set is the tier one
	for _, h := range []string{"/home/a]", "/home/{", "/home/}"} {
		if err := validateOrgInputs("linux", nil, []string{h}); !errors.Is(err, ErrUsage) {
			t.Errorf("home_root %q accepted", h)
		}
	}
}
