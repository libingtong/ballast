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

// Clients returns the four clients in model.AllClients order.
func Clients() []Client {
	panic("unimplemented: install.Clients")
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
// client not detected without Force (exit 2), I/O (exit 5).
func Install(opt Options) (*Report, error) {
	panic("unimplemented: install.Install")
}

// Uninstall removes owned entries/files/flags recorded in the manifest.
func Uninstall(opt Options) (*Report, error) {
	panic("unimplemented: install.Uninstall")
}

// LoadManifest reads install-manifest.json (missing ⇒ empty manifest, V set).
func LoadManifest(fsys sys.FS, path string) (*model.InstallManifest, error) {
	panic("unimplemented: install.LoadManifest")
}

// SaveManifest writes the manifest atomically with fsync.
func SaveManifest(fsys sys.FS, ids sys.IDGen, path string, m *model.InstallManifest) error {
	panic("unimplemented: install.SaveManifest")
}

// EntryHash = hex(sha256(canonical JSON of the entry object)).
// Canonical = encoding/json of the decoded value (sorted map keys, no spaces).
func EntryHash(entryJSON []byte) (string, error) {
	panic("unimplemented: install.EntryHash")
}

// Backup copies files into a new backup directory and rotates (keep 10).
func Backup(opt Options, files []string) (*model.BackupRecord, error) {
	panic("unimplemented: install.Backup")
}

// MaxBackups is the number of backup directories kept.
const MaxBackups = 10

// UnifiedDiff renders a unified diff of before/after for --dry-run.
func UnifiedDiff(path string, before, after []byte) string {
	panic("unimplemented: install.UnifiedDiff")
}

// DetectManaged reports whether Ballast hooks are present in Claude managed settings.
func DetectManaged(lay layout.Layout, fsys sys.FS) (bool, string, error) {
	panic("unimplemented: install.DetectManaged")
}

// QuoteCommand renders argv as ONE command string for clients whose hook
// entries have no args field (Codex, Cursor, Hermes): POSIX single-quote
// quoting on unix ("'/opt/x y/ballast' hook --client codex --event Stop"),
// CommandLineToArgvW-compatible quoting on Windows (list2cmdline rules).
func QuoteCommand(argv []string, goos string) string {
	panic("unimplemented: install.QuoteCommand")
}

// SplitCommand is the inverse of QuoteCommand (for ownership checks).
func SplitCommand(cmd, goos string) ([]string, error) {
	panic("unimplemented: install.SplitCommand")
}

// HookArgv returns the frozen hook argv: [binary, "hook", "--client", id, "--event", native].
func HookArgv(binary string, c model.ClientID, native string) []string {
	return []string{binary, "hook", "--client", string(c), "--event", native}
}

// CodexTrustHash mirrors Codex's trust hash: "sha256:" + hex(sha256(canonical
// JSON of {"event_name":<snake_event>,"hooks":[{"type":"command","command":C,
// "timeout":T,"async":false}]})) (pinrule codex.py:623-647; unverified for
// Codex versions other than 0.130).
func CodexTrustHash(snakeEvent, command string, timeout int) string {
	panic("unimplemented: install.CodexTrustHash")
}
