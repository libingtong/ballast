// Package tier (P2 unit P2-TIER; architecture v2.0, docs/p2-design.md §3.2)
// decides whether this host runs Ballast at trust tier T1 (managed,
// agent-unwritable) or T0 (best effort), and owns the single list of
// protected paths shared by `org render`, the runner sandbox and detection.
//
// Layer L2 (imports model, sys, layout). Hot path: yes (the hook calls it
// through ports.TierProbe, lazily); no os/exec, no net, no package-level
// regexp, no init().
//
// Frozen rule: T1 iff ALL of the following hold, otherwise T0 with every
// failed condition listed in TierInfo.Missing (model.TierMissing*):
//
//  1. GOOS is darwin or linux (WSL2 reports linux). Native Windows ⇒ T0.
//  2. None of BALLAST_HOME, BALLAST_MANAGED_DIR, BALLAST_INSTALL_ROOT is set
//     (test redirections; an agent-controlled environment must never be able
//     to point detection at files it owns).
//  3. Self (absolute, symlinks resolved) is a regular file owned by uid 0
//     and not group/other-writable, and so is every parent directory up
//     to "/".
//  4. <managed>/ballast/org.json is a root-owned, non-group/other-writable
//     regular file whose "tier" is "T1".
//  5. The merged Claude managed settings (managed-settings.json plus
//     managed-settings.d/*.json in the merge order established by M0b; each
//     file root-owned and not group/other-writable) contain:
//     allowManagedHooksOnly == true; at least one hook entry with
//     command == Self and args[0:2] == ["hook","--client"]; sandbox.enabled
//     == true, allowUnsandboxedCommands == false, failIfUnavailable == true;
//     sandbox.filesystem.denyWrite covering every ProtectedPaths entry;
//     permissions.deny containing Edit/Write/NotebookEdit rules for every
//     protected path; allowManagedPermissionRulesOnly == true.
//
// Ownership checks go through Options.Owner so unit tests can fake uid 0
// without root; production uses syscall.Stat_t (unix) and reports "not
// root-owned" on Windows. An agent cannot satisfy 3–5 without root (an agent
// with root is outside every Ballast boundary; docs/trust-model.md).
//
// Acceptance (P2-TIER): testdata/detect.json table (each condition flipped
// on its own ⇒ T0 with exactly that Missing id; all satisfied ⇒ T1);
// BenchmarkProbe ≤ 200 µs on a warm page cache; Probe memoizes (second call
// does no I/O, asserted with a counting FS).
package tier

import (
	"io/fs"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Options configures detection.
type Options struct {
	FS     sys.FS
	Env    sys.Env
	Layout layout.Layout
	// Self is the absolute path of the running ballast binary.
	Self string
	// GOOS defaults to Layout.GOOS.
	GOOS string
	// Owner returns the owning uid of fi and ok=false when the platform
	// cannot tell (Windows, fake FS without ownership). nil ⇒ platform
	// default.
	Owner func(fi fs.FileInfo) (uid uint32, ok bool)
}

// OrgConfig is <managed>/ballast/org.json (root-owned; written by the
// administrator, rendered by `ballast org render`). Unknown keys are
// ignored; a file that does not decode counts as absent (TierMissingOrgConfig).
type OrgConfig struct {
	V    int        `json:"v"` // 1
	Tier model.Tier `json:"tier,omitempty"`
	// RunnerSandbox defaults to "require" when Tier is T1.
	RunnerSandbox model.SandboxPolicy `json:"runner_sandbox,omitempty"`
	// ProtectedExtra are extra protected paths ("~/" allowed), appended to
	// ProtectedPaths for denyWrite / permissions.deny / runner sandbox.
	ProtectedExtra []string `json:"protected_extra,omitempty"`
}

// Prober implements ports.TierProbe with per-process memoization.
type Prober struct {
	opt Options
}

// New returns a Prober.
func New(opt Options) *Prober { return &Prober{opt: opt} }

// Probe implements ports.TierProbe.
func (p *Prober) Probe() model.TierInfo {
	panic("unimplemented: tier.(*Prober).Probe (unit P2-TIER)")
}

// Detect runs detection once without memoization (doctor, tests).
func Detect(opt Options) model.TierInfo {
	panic("unimplemented: tier.Detect (unit P2-TIER)")
}

// LoadOrgConfig reads and decodes org.json. It does not check ownership
// (Detect does).
func LoadOrgConfig(fsys sys.FS, path string) (OrgConfig, error) {
	panic("unimplemented: tier.LoadOrgConfig (unit P2-TIER)")
}

// ProtectedKind classifies a protected path.
type ProtectedKind string

const (
	ProtectedOrgDir      ProtectedKind = "org_dir"      // <managed>/ballast/
	ProtectedBallastHome ProtectedKind = "ballast_home" // ~/.ballast/
	ProtectedRepoBallast ProtectedKind = "repo_ballast" // <repo>/.ballast/ (only when repo != "")
	ProtectedBinaryDir   ProtectedKind = "binary_dir"   // dir(Self)
	ProtectedClaudeUser  ProtectedKind = "claude_user"  // ~/.claude/settings*.json (CLAUDE_CONFIG_DIR honoured)
	ProtectedClaudeRepo  ProtectedKind = "claude_repo"  // <repo>/.claude/settings*.json (only when repo != "")
	ProtectedExtraPath   ProtectedKind = "extra"        // OrgConfig.ProtectedExtra
)

// ProtectedPath is one protected location. Dir true ⇒ the whole subtree;
// Glob true ⇒ Path contains a '*' in its last element (settings*.json).
// Path is an absolute OS path; Resolved is its EvalSymlinks form when that
// differs (macOS /var → /private/var), "" otherwise. Consumers protect both.
type ProtectedPath struct {
	Kind     ProtectedKind
	Path     string
	Resolved string
	Dir      bool
	Glob     bool
}

// ProtectedPaths returns the plan B0 protected-path list in a stable order.
// repo is the work-tree root ("" when unknown: org render emits the
// "<repo>" placeholder rules via its own template, see docs/p2-design.md
// §3.5). It performs EvalSymlinks through fsys and never fails: unresolved
// paths keep Resolved "".
func ProtectedPaths(fsys sys.FS, lay layout.Layout, self, repo string, extra []string) []ProtectedPath {
	panic("unimplemented: tier.ProtectedPaths (unit P2-TIER)")
}
