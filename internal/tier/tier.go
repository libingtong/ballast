// Package tier (P2 unit P2-TIER; architecture v2.0, docs/p2-design.md §3.2)
// decides whether this host runs Ballast at trust tier T1 (managed,
// agent-unwritable) or T0 (best effort), owns the single list of protected
// paths shared by `org render`, the runner sandbox and detection, and owns
// the redirect lock (§3.2 "重定向锁").
//
// Layer L2 (imports model, sys, layout). Hot path: yes (the hook calls
// Locked on every invocation and Probe lazily through ports.TierProbe); no
// os/exec, no net, no package-level regexp, no init().
//
// Frozen rule: T1 iff ALL of the following hold, otherwise T0 with every
// failed condition listed in TierInfo.Missing (model.TierMissing*):
//
//  1. GOOS is darwin or linux (WSL2 reports linux). Native Windows ⇒ T0.
//  2. The test redirections BALLAST_HOME / BALLAST_MANAGED_DIR /
//     BALLAST_INSTALL_ROOT are not IN EFFECT. When the host is locked
//     (Locked == true) production builds ignore them (layout.ResolveWith
//     with IgnoreRedirects), so they can never be in effect on a locked
//     host; on an unlocked host their presence ⇒ T0 (env_override). An
//     agent therefore gains nothing by setting them: on a T1 host they are
//     ignored, on any other host the result is T0 anyway.
//  3. Self (absolute, symlinks resolved) is a regular file owned by uid 0
//     and not group/other-writable, and so is every parent directory up
//     to "/".
//  4. ManagedDirFor(GOOS)/ballast/org.json — the COMPILED-IN managed dir,
//     never Layout.ManagedDir — is a root-owned, non-group/other-writable
//     regular file that passes LoadOrgConfig validation and whose "tier"
//     is "T1".
//  5. The merged Claude managed settings under ManagedDirFor(GOOS)
//     (managed-settings.json plus managed-settings.d/*.json in the merge
//     order established by M0b; each file root-owned and not
//     group/other-writable) contain: allowManagedHooksOnly == true; at
//     least one hook entry whose command is an ABSOLUTE path with
//     EvalSymlinks(command) == EvalSymlinks(Self) and args[0:2] ==
//     ["hook","--client"]; sandbox.enabled == true,
//     allowUnsandboxedCommands == false, failIfUnavailable == true;
//     allowManagedPermissionRulesOnly == true; and, with
//     (dw, pd) = ManagedForms(lay, command, org.ProtectedExtra) where
//     command is that hook entry's command string exactly as written,
//     every string of dw appears verbatim in sandbox.filesystem.denyWrite
//     and every string of pd appears verbatim in permissions.deny
//     ("covers" == exact string-set inclusion; no glob reasoning).
//
// Ownership checks go through Options.Owner so unit tests can fake uid 0
// without root; production uses syscall.Stat_t (unix) and reports "not
// root-owned" on Windows. An agent cannot satisfy 3–5 without root (an agent
// with root is outside every Ballast boundary; docs/trust-model.md).
//
// Acceptance (P2-TIER): testdata/detect.json table (each condition flipped
// on its own ⇒ T0 with exactly that Missing id; all satisfied ⇒ T1; a row
// where the managed hook command is a symlink to Self ⇒ T1; a row with a
// relative command ⇒ managed_hooks); testdata/managed_forms.json golden
// (shared with internal/install); Locked table (absent / non-root /
// group-writable / tier T0 / invalid org.json ⇒ false); BenchmarkProbe
// ≤ 200 µs and BenchmarkLocked ≤ 20 µs on a warm page cache; Probe
// memoizes (second call does no I/O, asserted with a counting FS).
package tier

import (
	"io/fs"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Options configures detection.
type Options struct {
	FS  sys.FS
	Env sys.Env
	// Layout is the layout the process actually uses (resolved with
	// layout.ResolveWith; on a locked host redirects are already ignored).
	// Detection reads org.json and managed settings from
	// layout.ManagedDirFor(GOOS), never from Layout.ManagedDir.
	Layout layout.Layout
	// Self is the absolute path of the running ballast binary.
	Self string
	// GOOS defaults to Layout.GOOS.
	GOOS string
	// Owner returns the owning uid of fi and ok=false when the platform
	// cannot tell (Windows, fake FS without ownership). nil ⇒ platform
	// default.
	Owner func(fi fs.FileInfo) (uid uint32, ok bool)
	// ManagedDir overrides layout.ManagedDirFor(GOOS) for unit tests only;
	// production wiring (internal/cli) never sets it.
	ManagedDir string
}

// OrgConfig is <managed>/ballast/org.json (root-owned; written by the
// administrator, rendered by `ballast org render`). Unknown keys are
// ignored.
//
// Validation (LoadOrgConfig returns an error ⇒ treated as absent,
// TierMissingOrgConfig, and the host is not locked):
//   - V must be 1; Tier must be "", "T0" or "T1";
//   - RunnerSandbox must be "", require, prefer or off; RunnerNetwork must
//     be "", deny or allow;
//   - every RunnerAllowWrite / ProtectedExtra entry is absolute or starts
//     with "~/"; RunnerAllowWrite must not contain "~" or "~/" (a writable
//     home would let a sandboxed test persist through shell rc files), any
//     of "/", "/home", "/Users", "/root", "/var/root" or a path exactly one
//     level below "/home" or "/Users" (someone's home directory), nor any
//     path under the protected org dir. The runner re-checks every
//     expanded entry against the real home directory at wrap time
//     (sandbox.ValidateSpec);
//   - every HomeRoot entry is an absolute, Clean path other than "/"
//     with no "~" and no glob characters.
type OrgConfig struct {
	V    int        `json:"v"` // 1
	Tier model.Tier `json:"tier,omitempty"`
	// RunnerSandbox governs origin=cli verify runs only (a human or the
	// agent running `ballast verify` itself). origin=hook runs ALWAYS
	// require the sandbox regardless of this value (docs/p2-design.md
	// §3.4). Default "require" when Tier is T1, "off" otherwise.
	RunnerSandbox model.SandboxPolicy `json:"runner_sandbox,omitempty"`
	// RunnerNetwork is the sandboxed test command's network policy;
	// default "deny" (loopback and unix sockets inside the writable set
	// only).
	RunnerNetwork model.NetworkPolicy `json:"runner_network,omitempty"`
	// RunnerAllowWrite are extra writable locations for the sandboxed
	// test command, on top of the repo, its git dirs and the per-job temp
	// dir. RESIDUAL RISK (docs/trust-model.md §2.3, p2-design §11): every
	// entry is handed to code the agent can rewrite, and anything written
	// there outlives the job and lies outside the content snapshot C — a
	// shared build cache listed here can be poisoned for later builds run
	// outside the sandbox. The recommended default is an empty list: the
	// runner already points GOCACHE, XDG_CACHE_HOME and the npm/yarn/pip
	// caches at the per-job temp dir (sandbox.CacheEnv).
	RunnerAllowWrite []string `json:"runner_allow_write,omitempty"`
	// HomeRoot (optional) lists root-owned parent directories of user
	// homes, e.g. "/home" or "/home/corp.example". It is consulted ONLY on
	// a locked host and ONLY when the passwd database has no entry for the
	// effective uid (sys.ErrNoPasswdEntry — sssd/LDAP users on a static
	// linux build); see LockedHome for the exact acceptance rule.
	HomeRoot []string `json:"home_root,omitempty"`
	// ProtectedExtra are extra protected paths ("~/" allowed), appended to
	// ProtectedPaths / ManagedForms for denyWrite, permissions.deny and the
	// runner sandbox.
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

// LoadOrgConfig reads, decodes and validates org.json (rules on
// OrgConfig). It does not check ownership (Detect and Locked do).
func LoadOrgConfig(fsys sys.FS, path string) (OrgConfig, error) {
	panic("unimplemented: tier.LoadOrgConfig (unit P2-TIER)")
}

// Locked reports whether the host is "redirect-locked": a regular file
// ManagedDirFor(goos)/ballast/org.json exists, is owned by uid 0, is not
// group/other-writable, passes LoadOrgConfig validation and declares
// "tier":"T1". It costs one Lstat plus, only when that file exists, one
// read of a few hundred bytes; it never fails (any doubt ⇒ false, i.e.
// the current behaviour).
//
// Production wiring (internal/cli) calls Locked before resolving the
// layout and passes the result as layout.Options.IgnoreRedirects, so on a
// locked host BALLAST_HOME / BALLAST_MANAGED_DIR / BALLAST_INSTALL_ROOT
// are ignored and the home directory comes from the passwd database
// (LockedHome) instead of $HOME (docs/p2-design.md §3.2 "重定向锁"). The
// CLI fills hook.Deps.RedirectsIgnored with the variables that were set
// and ignored; the hook audits diag "env_redirect_ignored" on the first
// event of each session; doctor reports it as warn. Tests that need redirection never
// create a root-owned org.json at the real managed dir, so they are
// unaffected; tests of the lock itself use a fake FS and Owner.
func Locked(fsys sys.FS, goos string, owner func(fi fs.FileInfo) (uid uint32, ok bool)) bool {
	panic("unimplemented: tier.Locked (unit P2-TIER)")
}

// LockState is Locked that also returns the validated org.json it read
// (zero value when not locked), so the CLI can pass OrgConfig.HomeRoot to
// LockedHome without reading org.json twice. Locked(...) ==
// second result of LockState(...).
func LockState(fsys sys.FS, goos string, owner func(fi fs.FileInfo) (uid uint32, ok bool)) (OrgConfig, bool) {
	panic("unimplemented: tier.LockState (unit P2-TIER)")
}

// Home sources reported by LockedHome (doctor prints them).
const (
	HomeFromPasswd   = "passwd"
	HomeFromHomeRoot = "home_root"
)

// HomeOptions configures LockedHome.
type HomeOptions struct {
	FS  sys.FS
	Env sys.Env // only Env.HomeDir ($HOME) is read, and only for the home_root rule
	// Owner as in Options.Owner.
	Owner func(fi fs.FileInfo) (uid uint32, ok bool)
	// Euid is the effective uid (production: sys.Euid()).
	Euid int
	// Lookup is the passwd lookup; nil ⇒ sys.LookupHome.
	Lookup func(uid int) (string, error)
	// HomeRoot is OrgConfig.HomeRoot of the locked host.
	HomeRoot []string
}

// LockedHome resolves <home> on a locked host. Production wiring
// (internal/cli) passes it, closed over HomeOptions, as
// layout.Options.PasswdHome. Frozen rule (docs/p2-design.md §3.2):
//
//  1. h, err := Lookup(Euid). err == nil ⇒ (h, HomeFromPasswd, nil).
//     $HOME is not consulted even when it differs.
//  2. err is not sys.ErrNoPasswdEntry, or HomeRoot is empty ⇒ error
//     (layout resolution fails; the hook fails open per B7 with no side
//     effects; doctor check tier.locked_home reports fail with the fix
//     hints of §3.5).
//  3. Otherwise $HOME (Env.HomeDir) is accepted iff ALL of (a)–(d) hold.
//     (a) It is absolute and filepath.Clean($HOME) == $HOME. (b)
//     filepath.Dir($HOME) is exactly one HomeRoot entry (direct child
//     only; nested layouts list the deeper parent, e.g.
//     "/home/corp.example"). (c) That entry and every ancestor up to "/"
//     are directories (Lstat, no symlinks) owned by uid 0 and not
//     group/other-writable, so only root can create or rename its
//     children. (d) $HOME itself (Lstat) is a directory, not a symlink,
//     owned by Euid, and not group/other-writable.
//     Then ($HOME, HomeFromHomeRoot, nil); else an error naming the first
//     failed clause.
//
// Cost: one passwd lookup, plus (rule 3 only) ≤ depth+1 Lstat calls.
// BenchmarkLockedHome covers both paths with the real sys.LookupHome for
// the current user (results discarded); the cold per-process cost is
// covered by bench scenario 13 (§3.6).
func LockedHome(opt HomeOptions) (home, source string, err error) {
	panic("unimplemented: tier.LockedHome (unit P2-TIER)")
}

// ManagedForms returns the exact denyWrite and permissions.deny strings
// that `org render` emits and that Detect requires (condition 5). It is
// pure: it reads only lay.GOOS and the directory of lay.OrgRules; home and
// repo entries are the literal "~/" and "./" forms, independent of any
// user or repo, so the same strings work for every user on the host.
//
//	denyWrite = [dir(OrgRules), "~/.ballast", "./.ballast", dir(self),
//	             extra..., "~/.claude/settings*.json", "./.claude/settings*.json"]
//	permDeny  = for tool in [Edit, Write, NotebookEdit]:
//	              tool(dir(OrgRules)/**), tool(~/.ballast/**), tool(./.ballast/**),
//	              tool(dir(self)/**), for each extra e: tool(e), tool(e/**),
//	              tool(~/.claude/settings*.json), tool(./.claude/settings*.json)
//
// self is used literally (never resolved): org render passes --binary as
// given, Detect passes the matched managed hook's command string. dir()
// splits on '/' (and '\' when GOOS is windows); duplicates keep the first
// occurrence. testdata/managed_forms.json is the frozen golden.
func ManagedForms(lay layout.Layout, self string, extra []string) (denyWrite, permDeny []string) {
	panic("unimplemented: tier.ManagedForms (unit P2-TIER)")
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

// ProtectedPaths returns the plan B0 protected-path list as absolute paths
// for one user and one repo, in a stable order. It is the input of the
// runner sandbox (docs/p2-design.md §3.4) and of doctor's listing; it is
// NOT used for the managed-settings coverage check (that is ManagedForms,
// which is repo- and user-independent). repo is the work-tree root (""
// when unknown ⇒ repo entries omitted). It performs EvalSymlinks through
// fsys and never fails: unresolved paths keep Resolved "".
func ProtectedPaths(fsys sys.FS, lay layout.Layout, self, repo string, extra []string) []ProtectedPath {
	panic("unimplemented: tier.ProtectedPaths (unit P2-TIER)")
}
