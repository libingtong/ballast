package install

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// doctorEnv installs every client with a real (executable) binary file.
func doctorEnv(t *testing.T, trust bool) (*tenv, Options) {
	t.Helper()
	e := newTenv(t)
	e.mkClientDirs()
	bin := filepath.Join(e.sb.Root, "bin", "ballast")
	e.write(bin, "#!/bin/sh\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	opt := e.opts(model.AllClients...)
	opt.Binary = bin
	opt.CodexTrust = trust
	mustInstall(t, opt)
	return e, opt
}

func (e *tenv) doctor(bin string, mdc []byte, lint func() []model.Diagnostic) []Check {
	// TierManagedDir keeps tier.Detect away from the real managed directory.
	return Doctor(DoctorOptions{Layout: e.lay, FS: e.fs, Env: e.sb.Env(), Binary: bin, MDC: mdc, LintRules: lint, TierManagedDir: e.lay.ManagedDir,
		TierLock: func() (tier.OrgConfig, bool) { return tier.OrgConfig{}, false }})
}

func byID(cs []Check) map[string]Check {
	out := map[string]Check{}
	for _, c := range cs {
		out[c.ID] = c
	}
	return out
}

func mustStatus(t *testing.T, cs map[string]Check, id string, want CheckStatus) Check {
	t.Helper()
	c, ok := cs[id]
	if !ok {
		t.Errorf("check %s missing", id)
		return c
	}
	if c.Status != want {
		t.Errorf("check %s = %s (%s), want %s", id, c.Status, c.Message, want)
	}
	if c.Message == "" {
		t.Errorf("check %s has no message", id)
	}
	return c
}

func TestDoctorHealthyInstall(t *testing.T) {
	e, opt := doctorEnv(t, true)
	cs := e.doctor(opt.Binary, []byte(testMDC), func() []model.Diagnostic { return nil })
	by := byID(cs)
	if len(by) != len(cs) {
		t.Error("check ids must be unique")
	}
	for _, c := range cs {
		if c.Status == CheckFail {
			t.Errorf("unexpected failure: %s: %s", c.ID, c.Message)
		}
		if c.Status != CheckOK && c.Status != CheckWarn && c.Status != CheckFail {
			t.Errorf("%s has invalid status %q", c.ID, c.Status)
		}
		if _, err := json.Marshal(c); err != nil {
			t.Error(err)
		}
	}
	mustStatus(t, by, "binary.exists", CheckOK)
	mustStatus(t, by, "binary.stable_path", CheckWarn) // the sandbox lives in a temp dir
	mustStatus(t, by, "home.writable", CheckOK)
	mustStatus(t, by, "audit.writable", CheckOK)
	mustStatus(t, by, "state.permissions", CheckOK)
	mustStatus(t, by, "manifest.valid", CheckOK)
	mustStatus(t, by, "rules.lint", CheckOK)
	mustStatus(t, by, "ci.final_arbiter", CheckOK)
	mustStatus(t, by, "claude.managed_duplicate", CheckOK)
	mustStatus(t, by, "codex.features_hooks", CheckOK)
	mustStatus(t, by, "codex.trust", CheckOK)
	mustStatus(t, by, "codex.tui_only", CheckWarn)
	mustStatus(t, by, "cursor.mdc_current", CheckOK)
	mustStatus(t, by, "cursor.reload", CheckOK)
	mustStatus(t, by, "hermes.accept_hooks", CheckWarn)
	for _, c := range model.AllClients {
		for _, ne := range defaultEvents(c) {
			mustStatus(t, by, checkPrefix(c)+".entry."+ne.Native, CheckOK)
		}
	}
	if c := by["claude.entry.PreToolUse"]; c.Client != model.ClientClaude {
		t.Errorf("client not set: %+v", c)
	}
	if c := by["tier.level"]; c.Status != CheckWarn || !strings.Contains(c.Message, "T0") {
		t.Errorf("tier.level = %+v", c)
	}
	if _, ok := by["tier.windows_t0"]; ok && runtimeIsUnix() {
		t.Error("tier.windows_t0 is Windows-only")
	}
	if c, ok := by["disk.space"]; !ok || c.Status == "" {
		t.Error("disk.space missing")
	} else if free, ok := diskFree(e.lay.Home); ok && free > diskWarnBytes {
		mustStatus(t, by, "disk.space", CheckOK)
	}
}

func TestDoctorNothingInstalled(t *testing.T) {
	e := newTenv(t)
	by := byID(e.doctor("", nil, nil))
	mustStatus(t, by, "manifest.valid", CheckOK)
	mustStatus(t, by, "binary.exists", CheckWarn)
	mustStatus(t, by, "binary.stable_path", CheckWarn)
	mustStatus(t, by, "rules.lint", CheckWarn)
	mustStatus(t, by, "home.writable", CheckOK) // ~/.ballast does not exist yet; its parent is writable
	for id := range by {
		if strings.Contains(id, ".entry.") || strings.HasPrefix(id, "codex.") || strings.HasPrefix(id, "cursor.") || strings.HasPrefix(id, "hermes.") {
			t.Errorf("client check %s emitted for a client that is not installed", id)
		}
	}
	if e.exists(e.lay.Home) {
		t.Error("doctor must not create ~/.ballast")
	}
}

func TestDoctorBinaryChecks(t *testing.T) {
	e := newTenv(t)
	by := byID(e.doctor("/opt/ballast/bin/ballast", nil, nil))
	mustStatus(t, by, "binary.exists", CheckFail)
	mustStatus(t, by, "binary.stable_path", CheckOK)
	if by["binary.exists"].Fix == "" {
		t.Error("a failing check should say how to fix it")
	}
	dir := filepath.Join(e.sb.Root, "adir")
	_ = os.MkdirAll(dir, 0o755)
	mustStatus(t, byID(e.doctor(dir, nil, nil)), "binary.exists", CheckFail)
	if runtimeIsUnix() {
		plain := filepath.Join(e.sb.Root, "plain")
		e.write(plain, "x")
		mustStatus(t, byID(e.doctor(plain, nil, nil)), "binary.exists", CheckFail)
		_ = os.Chmod(plain, 0o755)
		mustStatus(t, byID(e.doctor(plain, nil, nil)), "binary.exists", CheckOK)
	}
	// without an explicit binary the manifest's is used
	e2, _ := doctorEnv(t, false)
	mustStatus(t, byID(e2.doctor("", nil, nil)), "binary.exists", CheckOK)
}

func TestDoctorDetectsDamagedEntries(t *testing.T) {
	e, opt := doctorEnv(t, false)

	// claude: remove Stop, modify PreToolUse
	s := e.read(e.claudeSettings())
	var doc map[string]any
	_ = json.Unmarshal([]byte(s), &doc)
	hooks := doc["hooks"].(map[string]any)
	delete(hooks, "Stop")
	pre := hooks["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	pre["args"] = append(pre["args"].([]any), "--extra")
	b, _ := json.MarshalIndent(doc, "", "  ")
	e.write(e.claudeSettings(), string(b))
	// cursor: delete the file; hermes: corrupt it
	_ = os.Remove(e.cursorHooks())
	e.write(e.hermesConfig(), "hooks: [oops\n")
	// codex: switch features.hooks off
	e.write(e.codexConfig(), strings.Replace(e.read(e.codexConfig()), "hooks = true", "hooks = false", 1))

	by := byID(e.doctor(opt.Binary, []byte(testMDC), nil))
	mustStatus(t, by, "claude.entry.Stop", CheckFail)
	mustStatus(t, by, "claude.entry.PreToolUse", CheckWarn)
	mustStatus(t, by, "claude.entry.SessionStart", CheckOK)
	mustStatus(t, by, "cursor.entry.stop", CheckFail)
	mustStatus(t, by, "cursor.entry.preToolUse", CheckFail)
	mustStatus(t, by, "hermes.entry.pre_tool_call", CheckFail)
	mustStatus(t, by, "codex.features_hooks", CheckFail)
	if !strings.Contains(by["cursor.entry.stop"].Message, "missing") {
		t.Errorf("message = %q", by["cursor.entry.stop"].Message)
	}
	if by["claude.entry.Stop"].Fix == "" || !strings.Contains(by["claude.entry.Stop"].Fix, "ballast install --client claude-code") {
		t.Errorf("fix = %q", by["claude.entry.Stop"].Fix)
	}

	// features.hooks missing entirely / config.toml gone
	_ = os.Remove(e.codexConfig())
	mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.features_hooks", CheckFail)
	e.write(e.codexConfig(), "[features\n")
	mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.features_hooks", CheckFail)
}

func TestDoctorCodexTrust(t *testing.T) {
	e, opt := doctorEnv(t, false)
	by := byID(e.doctor(opt.Binary, nil, nil))
	mustStatus(t, by, "codex.trust", CheckWarn)
	if !strings.Contains(by["codex.trust"].Fix, "--codex-trust") {
		t.Errorf("fix = %q", by["codex.trust"].Fix)
	}

	opt.CodexTrust = true
	mustInstall(t, opt)
	mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.trust", CheckOK)

	// a changed hash (Codex re-trusted a modified hook) is stale
	cfg := e.read(e.codexConfig())
	i := strings.Index(cfg, "trusted_hash = \"sha256:")
	e.write(e.codexConfig(), cfg[:i]+"trusted_hash = \"sha256:deadbeef\""+cfg[i+strings.Index(cfg[i:], "\n"):])
	c := mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.trust", CheckWarn)
	if !strings.Contains(c.Message, "1 of 6") {
		t.Errorf("message = %q", c.Message)
	}
}

func TestDoctorCursorMDC(t *testing.T) {
	e, opt := doctorEnv(t, false)
	stale := []byte(strings.Replace(testMDC, "rules v1", "rules v2", 1))
	mustStatus(t, byID(e.doctor(opt.Binary, []byte(testMDC), nil)), "cursor.mdc_current", CheckOK)
	c := mustStatus(t, byID(e.doctor(opt.Binary, stale, nil)), "cursor.mdc_current", CheckWarn)
	if !strings.Contains(c.Fix, "ballast install --client cursor") {
		t.Errorf("fix = %q", c.Fix)
	}
	mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "cursor.mdc_current", CheckWarn)
	if m := byID(e.doctor(opt.Binary, nil, nil))["cursor.mdc_current"].Message; !strings.Contains(m, "not evaluated") {
		t.Errorf("nil MDC message = %q", m)
	}
	_ = os.Remove(e.cursorMDC())
	mustStatus(t, byID(e.doctor(opt.Binary, []byte(testMDC), nil)), "cursor.mdc_current", CheckFail)

	// Cursor not installed ⇒ the check is not emitted
	e2 := newTenv(t)
	if _, ok := byID(e2.doctor("", []byte(testMDC), nil))["cursor.mdc_current"]; ok {
		t.Error("cursor.mdc_current for a client that is not installed")
	}
}

func TestDoctorLint(t *testing.T) {
	e := newTenv(t)
	diag := func(sev model.Severity) []model.Diagnostic {
		return []model.Diagnostic{{Severity: sev, Code: "some-code", Message: "msg"}}
	}
	mustStatus(t, byID(e.doctor("", nil, func() []model.Diagnostic { return nil })), "rules.lint", CheckOK)
	mustStatus(t, byID(e.doctor("", nil, func() []model.Diagnostic { return diag(model.SevWarning) })), "rules.lint", CheckWarn)
	c := mustStatus(t, byID(e.doctor("", nil, func() []model.Diagnostic { return diag(model.SevError) })), "rules.lint", CheckFail)
	if !strings.Contains(c.Message, "some-code") {
		t.Errorf("message = %q", c.Message)
	}
	mustStatus(t, byID(e.doctor("", nil, nil)), "rules.lint", CheckWarn)
}

func TestDoctorManifestProblems(t *testing.T) {
	e := newTenv(t)
	e.write(e.lay.Manifest, "{ nope")
	by := byID(e.doctor("", nil, nil))
	mustStatus(t, by, "manifest.valid", CheckFail)
	if _, ok := by["home.writable"]; !ok {
		t.Error("a corrupt manifest must not stop the remaining checks")
	}
}

func TestDoctorManagedDuplicate(t *testing.T) {
	e, opt := doctorEnv(t, false)
	writeManaged(e, "managed-settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/libexec/ballast/ballast","args":["hook"]}]}]}}`)
	c := mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "claude.managed_duplicate", CheckFail)
	if !strings.Contains(c.Fix, "uninstall") {
		t.Errorf("fix = %q", c.Fix)
	}
	// managed only (nothing at user level): fine
	e2 := newTenv(t)
	writeManaged(e2, "managed-settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/libexec/ballast/ballast","args":["hook"]}]}]}}`)
	c = mustStatus(t, byID(e2.doctor("", nil, nil)), "claude.managed_duplicate", CheckOK)
	if !strings.Contains(c.Message, "managed deployment present") {
		t.Errorf("message = %q", c.Message)
	}
}

type fakeProbe struct{ info model.TierInfo }

func (f fakeProbe) Probe() model.TierInfo { return f.info }

func TestDoctorTierLevel(t *testing.T) {
	e := newTenv(t)
	// no managed deployment: T0, every failed condition is listed by id.
	c := byID(e.doctor("", nil, nil))["tier.level"]
	for _, missing := range []string{"binary_root_owned", "org_config", "managed_settings"} {
		if !strings.Contains(c.Message, missing) {
			t.Errorf("T0 message must list %q: %s", missing, c.Message)
		}
	}
	if c.Status != CheckWarn || !strings.HasPrefix(c.Message, "T0") || !strings.Contains(c.Fix, "ballast org render") {
		t.Errorf("tier.level = %+v", c)
	}

	// a probe that says T1 makes the check pass
	opt := DoctorOptions{Layout: e.lay, FS: e.fs, Env: e.sb.Env(), Tier: fakeProbe{model.TierInfo{Tier: model.TierT1, Declared: model.TierT1}}}
	c = byID(Doctor(opt))["tier.level"]
	if c.Status != CheckOK || !strings.HasPrefix(c.Message, "T1") {
		t.Errorf("T1 probe: %+v", c)
	}

	// each missing component is reported with its explanation
	opt.Tier = fakeProbe{model.TierInfo{Tier: model.TierT0, Missing: []string{model.TierMissingSandbox, model.TierMissingPermDeny}}}
	c = byID(Doctor(opt))["tier.level"]
	if c.Status != CheckWarn || !strings.Contains(c.Message, "sandbox(") || !strings.Contains(c.Message, "permissions_deny(") || strings.Contains(c.Message, "managed_hooks") {
		t.Errorf("partial: %+v", c)
	}
}

// tier.level calls tier.Detect: the template that `org render` writes is
// accepted by every condition-5 check (ownership is faked through
// TierOwner). Whether the whole verdict is T1 depends on the permissions of
// the real parent directories of the temp dir (a sticky, world-writable /tmp
// on Linux fails the parent-chain conditions), so only the managed-settings
// conditions are asserted.
func TestDoctorTierDetectsRenderedTemplate(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("Detect is darwin/linux only")
	}
	e := newTenv(t)
	lay := e.lay
	lay.GOOS = "linux"
	self := filepath.Join(e.sb.Root, "bin", "ballast")
	e.write(self, "#!/bin/sh\n")
	defer func(f func() int) { geteuid = f }(geteuid)
	geteuid = func() int { return 0 }
	if _, err := OrgRenderFull(lay, e.fs, OrgRenderOptions{GOOS: "linux", Binary: self, Write: true, IAmAdmin: true, Env: sys.MapEnv{}}); err != nil {
		t.Fatal(err)
	}
	cs := byID(Doctor(DoctorOptions{
		Layout: lay, FS: e.fs, Env: e.sb.Env(), Self: self, TierManagedDir: lay.ManagedDir,
		TierOwner: func(fs.FileInfo) (uint32, bool) { return 0, true },
	}))
	c := cs["tier.level"]
	for _, id := range []string{
		model.TierMissingManagedFile, model.TierMissingManagedHooks, model.TierMissingManagedOnly, model.TierMissingSandbox,
		model.TierMissingDenyWrite, model.TierMissingPermDeny, model.TierMissingManagedPerms, model.TierMissingUnreadable,
	} {
		if strings.Contains(c.Message, id+"(") {
			t.Errorf("rendered template fails condition %s: %s", id, c.Message)
		}
	}
}

// v1.5: CLAUDE_CONFIG_DIR pointing elsewhere is listed with its reason.
func TestDoctorTierLevelClaudeConfigDirRedirect(t *testing.T) {
	e := newTenv(t)
	vars := map[string]string{}
	for k, v := range e.sb.Vars {
		vars[k] = v
	}
	vars["CLAUDE_CONFIG_DIR"] = filepath.Join(e.sb.Home, "alt")
	mk := func(vars map[string]string) Check {
		return byID(Doctor(DoctorOptions{
			Layout: e.lay, FS: e.fs, Env: sys.MapEnv{Vars: vars},
			Tier: fakeProbe{model.TierInfo{Tier: model.TierT0, Missing: []string{model.TierMissingDenyWrite}}},
		}))["tier.level"]
	}
	c := mk(vars)
	if !strings.Contains(c.Message, "sandbox_deny_write(") || !strings.Contains(c.Message, "CLAUDE_CONFIG_DIR") ||
		!strings.Contains(c.Message, "挡不住 agent 改写它") {
		t.Errorf("redirected: %s", c.Message)
	}
	vars["CLAUDE_CONFIG_DIR"] = filepath.Join(e.sb.Home, ".claude") + "/" // Clean-equal ⇒ not a redirect
	if c := mk(vars); strings.Contains(c.Message, "CLAUDE_CONFIG_DIR") {
		t.Errorf("not redirected, yet: %s", c.Message)
	}
}

func TestDoctorWindowsTier(t *testing.T) {
	e := newTenv(t)
	lay := e.lay
	lay.GOOS = "windows"
	cs := byID(Doctor(DoctorOptions{Layout: lay, FS: e.fs, Env: e.sb.Env()}))
	c, ok := cs["tier.windows_t0"]
	if !ok || !strings.Contains(c.Message, "如需 T1 请使用 WSL2") {
		t.Fatalf("tier.windows_t0 = %+v", c)
	}
	if cs["tier.level"].Status != CheckOK || !strings.Contains(cs["tier.level"].Message, "T0") {
		t.Errorf("tier.level = %+v", cs["tier.level"])
	}
}

func TestDoctorStatePermissions(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("permission bits")
	}
	e := newTenv(t)
	_ = os.MkdirAll(e.lay.StateDir, 0o700)
	_ = os.Chmod(e.lay.StateDir, 0o755)
	c := mustStatus(t, byID(e.doctor("", nil, nil)), "state.permissions", CheckWarn)
	if !strings.Contains(c.Message, e.lay.StateDir) {
		t.Errorf("message = %q", c.Message)
	}
	_ = os.Chmod(e.lay.StateDir, 0o700)
	mustStatus(t, byID(e.doctor("", nil, nil)), "state.permissions", CheckOK)
}

func TestDoctorUnwritableHome(t *testing.T) {
	if !runtimeIsUnix() || os.Geteuid() == 0 {
		t.Skip("needs a non-root unix user")
	}
	e := newTenv(t)
	_ = os.MkdirAll(e.lay.Home, 0o700)
	if err := os.Chmod(e.lay.Home, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(e.lay.Home, 0o700)
	mustStatus(t, byID(e.doctor("", nil, nil)), "home.writable", CheckFail)
	mustStatus(t, byID(e.doctor("", nil, nil)), "audit.writable", CheckFail)
}

func TestDoctorFrozenCheckIDsAreCovered(t *testing.T) {
	e, opt := doctorEnv(t, true)
	lay := e.lay
	lay.GOOS = "windows"
	win := byID(Doctor(DoctorOptions{Layout: lay, FS: e.fs, Env: e.sb.Env(), Binary: opt.Binary, MDC: []byte(testMDC), LintRules: func() []model.Diagnostic { return nil }}))
	all := byID(e.doctor(opt.Binary, []byte(testMDC), func() []model.Diagnostic { return nil }))
	for _, id := range []string{
		"binary.exists", "binary.stable_path", "home.writable", "audit.writable", "state.permissions", "manifest.valid",
		"rules.lint", "tier.level", "ci.final_arbiter", "claude.managed_duplicate", "claude.entry.PreToolUse",
		"codex.features_hooks", "codex.trust", "codex.tui_only", "cursor.entry.preToolUse", "cursor.mdc_current",
		"cursor.reload", "hermes.entry.pre_tool_call", "hermes.accept_hooks", "disk.space",
	} {
		if _, ok := all[id]; !ok {
			t.Errorf("frozen check id %s is never emitted", id)
		}
	}
	if _, ok := win["tier.windows_t0"]; !ok {
		t.Error("tier.windows_t0 is never emitted")
	}
}
