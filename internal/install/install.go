package install

import (
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Format of a target file.
type Format string

const (
	FormatJSON Format = "json"
	FormatTOML Format = "toml"
	FormatYAML Format = "yaml"
	FormatMDC  Format = "mdc"
)

// EntrySpec describes what to install for one client.
type EntrySpec struct {
	Client     model.ClientID
	Binary     string              // absolute path of the ballast binary
	Events     []model.NativeEvent // NativeEventTable entries with Install == true
	GOOS       string              // quoting rules for single-string commands
	MDC        []byte              // Cursor: rendered ballast.mdc (inject.RenderMDC)
	CodexTrust bool                // --codex-trust: also write trust entries
}

// PlanResult is the pure outcome of planning one target.
type PlanResult struct {
	Path     string
	Format   Format
	Before   []byte // nil when the file does not exist
	After    []byte // nil ⇒ delete the file
	Created  bool
	Entries  []model.ManagedEntry
	Files    []model.ManagedFile
	Flags    []model.ManagedFlag
	Warnings []string
}

// Changed reports whether applying the plan modifies the file system.
func (p *PlanResult) Changed() bool {
	return string(p.Before) != string(p.After) || (p.Before == nil) != (p.After == nil)
}

// Target is one file of a client installation. Plan and Remove are PURE
// (bytes in, bytes out) so they serve --dry-run and golden tests.
type Target interface {
	Path() string
	Format() Format
	// Plan computes the new content. cur == nil ⇒ file absent.
	Plan(cur []byte, spec EntrySpec, m *model.InstallManifest) (*PlanResult, error)
	// Remove computes the content after removing owned items recorded in m.
	Remove(cur []byte, m *model.InstallManifest) (*PlanResult, error)
}

// Detection describes whether a client is present.
type Detection struct {
	Installed bool // client binary on PATH or config dir exists
	ConfigDir string
	Version   string // best effort, "" when unknown (never runs the client)
	Managed   bool   // managed-level Ballast present (Claude only)
	Notes     []string
}

// Client groups the install-side behaviour of one AI client (the Detect /
// InstallTargets / Doctor part of the v0.4 Adapter interface).
type Client interface {
	ID() model.ClientID
	Detect(lay layout.Layout, fsys sys.FS, env sys.Env) Detection
	Targets(lay layout.Layout, spec EntrySpec) []Target
	Doctor(lay layout.Layout, fsys sys.FS, m *model.InstallManifest) []Check
	// PostInstallNotes are printed after install (Cursor: Reload Window;
	// Codex: /hooks trust unless --codex-trust; Hermes: --accept-hooks).
	PostInstallNotes() []string
}

// Options for Install/Uninstall.
type Options struct {
	Layout     layout.Layout
	FS         sys.FS
	Env        sys.Env
	Clock      sys.Clock
	IDs        sys.IDGen
	Clients    []model.ClientID // install: required (explicit); uninstall: empty ⇒ all in manifest
	Binary     string           // "" ⇒ resolved executable path (EvalSymlinks); refused if under a temp/build dir
	DryRun     bool
	CodexTrust bool
	MDC        []byte // pre-rendered Cursor rules file
	Force      bool   // install even if the client is not detected
}

// Report summarizes an install/uninstall.
type Report struct {
	Plans    []*PlanResult       `json:"plans"`
	Diff     string              `json:"diff,omitempty"` // unified diff (dry-run)
	Backup   *model.BackupRecord `json:"backup,omitempty"`
	Warnings []string            `json:"warnings,omitempty"`
	Notes    []string            `json:"notes,omitempty"`
}

// Install installs hook entries for opt.Clients.
// Errors: model.ErrSettingsParse (exit 3), model.ErrManagedPresent (exit 1),
// client not detected without Force (exit 2), I/O (exit 5). Beyond the
// frozen list (see errors.go): ErrUsage, ErrClientNotDetected and
// ErrBinaryUnstable are exit 2, ErrLocked is exit 5. Nothing is written
// before every target has been planned, so any error leaves the file system
// untouched (a failure while writing rolls the written files back).
func Install(opt Options) (*Report, error) { return runInstall(opt) }

// Uninstall removes owned entries/files/flags recorded in the manifest.
// Entries the user modified are kept and reported in Report.Warnings (the
// CLI exits 1 when it is non-empty); a file that cannot be parsed is an
// error (model.ErrSettingsParse), never a silent success.
func Uninstall(opt Options) (*Report, error) { return runUninstall(opt) }

// HookArgv returns the frozen hook argv: [binary, "hook", "--client", id, "--event", native].
func HookArgv(binary string, c model.ClientID, native string) []string {
	return []string{binary, "hook", "--client", string(c), "--event", native}
}
