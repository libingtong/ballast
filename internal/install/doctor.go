package install

import (
	"io/fs"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
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
//	state.permissions, manifest.valid, rules.lint, tier.level (T0/T1 from
//	tier.Detect, every Missing id listed), tier.declared_mismatch,
//	tier.runner_sandbox, tier.redirect_ignored, tier.locked_home,
//	tier.repo_sandbox_weakened, tier.binary_hardening (P2),
//	tier.windows_t0 ("如需 T1 请使用 WSL2"),
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

	// P2 (docs/p2-design.md §3.5), added by P2-INSTALL.

	// Self is the absolute path of the running ballast binary, the input of
	// tier.Detect and of tier.binary_hardening; "" ⇒ Binary.
	Self string
	// Cwd is the current directory for tier.repo_sandbox_weakened (the
	// check also looks at the repository top found by walking up to a .git
	// entry, without running git); "" ⇒ the check is reported as not
	// evaluated.
	Cwd string
	// SandboxAvailable reports whether the runner sandbox backend works
	// (the CLI wires it to sandbox availability, doctor does not import
	// internal/sandbox): nil error ⇒ available. nil ⇒ tier.runner_sandbox is
	// reported as not evaluated on a T1 host.
	SandboxAvailable func() error
	// HomeLookup is the passwd lookup of tier.locked_home; nil ⇒
	// sys.LookupHome. HomeTimeout bounds it; 0 ⇒ tier.DefaultLockedHomeTimeout.
	HomeLookup  func(uid int) (string, error)
	HomeTimeout time.Duration

	// Test seams; the CLI leaves them zero.
	//
	// Tier replaces tier.Detect; TierOwner and TierManagedDir are
	// tier.Options.Owner / .ManagedDir (and TierOwner is also passed to
	// tier.LockState and tier.LockedHome); TierLock replaces tier.LockState.
	Tier           ports.TierProbe
	TierOwner      func(fi fs.FileInfo) (uid uint32, ok bool)
	TierManagedDir string
	TierLock       func() (tier.OrgConfig, bool)
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

	// P2 (docs/p2-design.md §3.5), added by P2-INSTALL.

	// HomeRoot are the --home-root values (repeatable): written to org.json
	// "home_root". Each must be an absolute, Clean path other than "/" with
	// no "~" and no glob characters (else ErrUsage).
	HomeRoot []string
	// Protected (above) is also written to org.json "protected_extra", so
	// tier.Detect recomputes exactly the strings rendered here.

	// Env supplies SUDO_UID for the --write home-directory pre-flight; nil
	// reads the process environment.
	Env sys.Env
	// LookupHome is the passwd lookup of the pre-flight; nil ⇒ sys.LookupHome.
	LookupHome func(uid int) (string, error)
}

// OrgRender renders the T1 Claude managed-settings fragment (pure JSON) with
// hooks for every claude-code NativeEvent (command+args, absolute root-owned
// binary), allowManagedHooksOnly, sandbox {enabled, allowUnsandboxedCommands
// false, failIfUnavailable true, excludedCommands [], filesystem.denyWrite
// = tier.ManagedForms}, permissions.deny (Edit(...) entries only, "//" form
// for absolute paths) = tier.ManagedForms, allowManagedPermissionRulesOnly true. Exact keys:
// architecture §5.5 (M0b-unverified). With Write+IAmAdmin and root it writes
// <ManagedDir>/managed-settings.d/ballast.json and
// <ManagedDir>/ballast/org.json; otherwise prints only.
//
// OrgRender returns ONLY the settings fragment (so its output is a valid
// JSON document and equals the written ballast.json). org.json (a second
// pure JSON document), its risk notes, the written paths and the stderr
// hints come from OrgRenderFull (OrgRendered).
func OrgRender(lay layout.Layout, fsys sys.FS, opt OrgRenderOptions) ([]byte, error) {
	return renderOrg(lay, fsys, opt)
}
