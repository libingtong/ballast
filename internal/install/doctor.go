package install

import (
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// CheckStatus of a doctor check.
type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
)

// Check is one doctor finding.
type Check struct {
	ID      string         `json:"id"` // stable, e.g. "binary.executable", "claude.entry.PreToolUse"
	Client  model.ClientID `json:"client,omitempty"`
	Status  CheckStatus    `json:"status"`
	Message string         `json:"message"`
	Fix     string         `json:"fix,omitempty"`
}

// DoctorOptions configures Doctor.
//
// Check ids (frozen list; clients add "<client>.*" ids):
//
//	binary.exists, binary.stable_path, home.writable, audit.writable,
//	state.permissions, manifest.valid, rules.lint, tier.level (T0/T1 +
//	missing T1 components: managed hooks, allowManagedHooksOnly, sandbox,
//	permissions.deny, MCP allowlist), tier.windows_t0 ("如需 T1 请使用 WSL2"),
//	ci.final_arbiter (always info: server-side CI is the final arbiter),
//	claude.managed_duplicate, claude.entry.<Event>, codex.features_hooks,
//	codex.trust, codex.tui_only, cursor.entry.<event>, cursor.mdc_current,
//	cursor.reload, hermes.entry.<event>, hermes.accept_hooks, disk.space.
type DoctorOptions struct {
	Layout layout.Layout
	FS     sys.FS
	Env    sys.Env
	Binary string
	// LintRules returns lint diagnostics of the current 3-layer stack (injected, U3/U4).
	LintRules func() []model.Diagnostic
	// MDC is the expected ~/.cursor/rules/ballast.mdc content rendered from
	// the CURRENT rules (the CLI calls inject.RenderMDC, exactly as for
	// install.Options.MDC). cursor.mdc_current compares the file bytes with
	// it: equal ⇒ ok; different ⇒ warn ("run ballast install --client
	// cursor"); missing while Cursor is in the manifest ⇒ fail. nil ⇒ the
	// check is reported as warn ("not evaluated").
	MDC []byte
}

// Doctor runs every check. The CLI exits 1 when any check is fail.
func Doctor(opt DoctorOptions) []Check { return runDoctor(opt) }

// OrgRenderOptions configures `ballast org render`.
type OrgRenderOptions struct {
	GOOS      string
	Binary    string   // default /usr/local/libexec/ballast/ballast (windows: C:\Program Files\Ballast\ballast.exe)
	Protected []string // extra protected paths
	MCPAllow  []string // managed MCP allowlist server names
	Write     bool     // --write --i-am-admin
	IAmAdmin  bool
}

// OrgRender renders the T1 Claude managed-settings fragment (JSON) with
// hooks for every claude-code NativeEvent (command+args, absolute root-owned
// binary), allowManagedHooksOnly, sandbox {enabled, allowUnsandboxedCommands
// false, failIfUnavailable true, excludedCommands [], filesystem.denyWrite
// = protected paths}, permissions.deny Edit/Write/NotebookEdit on protected
// paths, allowManagedPermissionRulesOnly true. Exact keys: architecture §5.5
// (M0b-unverified). With Write+IAmAdmin and root it writes
// <ManagedDir>/managed-settings.d/ballast.json; otherwise prints only.
func OrgRender(lay layout.Layout, fsys sys.FS, opt OrgRenderOptions) ([]byte, error) {
	return renderOrg(lay, fsys, opt)
}
