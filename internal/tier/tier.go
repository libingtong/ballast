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
//     is "T1"; and (v1.4) its parent chain — <managed>/ballast,
//     <managed> and every ancestor up to "/", after EvalSymlinks of the
//     managed dir (macOS /etc → /private/etc style links), each checked
//     with Lstat — consists of directories owned by uid 0 that are not
//     group/other-writable (a group-writable managed dir would let a
//     non-root member replace org.json or managed settings).
//  5. The merged Claude managed settings under ManagedDirFor(GOOS)
//     (managed-settings.json plus managed-settings.d/*.json in the merge
//     order established by M0b; each file root-owned and not
//     group/other-writable, and managed-settings.d itself plus the parent
//     chain of condition 4 root-owned and not group/other-writable)
//     contain: allowManagedHooksOnly == true; at
//     least one hook entry whose command is an ABSOLUTE path with
//     EvalSymlinks(command) == EvalSymlinks(Self) and args[0:2] ==
//     ["hook","--client"]; sandbox.enabled == true,
//     allowUnsandboxedCommands == false, failIfUnavailable == true;
//     allowManagedPermissionRulesOnly == true; and, with
//     (dw, pd) = ManagedForms(lay, command, org.ProtectedExtra) where
//     command is that hook entry's command string exactly as written,
//     every string of dw appears verbatim in sandbox.filesystem.denyWrite
//     and every string of pd appears verbatim in permissions.deny
//     ("covers" == exact string-set inclusion; no glob reasoning). The
//     managed sandbox itself must have sandbox.excludedCommands and
//     sandbox.network.allowUnixSockets absent or [], and
//     sandbox.enableWeakerNestedSandbox and
//     sandbox.network.allowAllUnixSockets absent or false (else Missing
//     "sandbox"); other managed sandbox keys (allowedDomains, allowWrite)
//     are the administrator's choice. v1.5: CLAUDE_CONFIG_DIR set
//     (non-empty) and, after filepath.Clean, different from <home>/.claude
//     ⇒ Missing "sandbox_deny_write": the user-layer files Claude actually
//     loads are then outside the literal "~/.claude/settings*.json" forms
//     of ManagedForms, so nothing in the managed configuration keeps the
//     agent from writing them (condition 6 is only a point-in-time check).
//  6. No lower Claude settings layer weakens the managed sandbox
//     (model.TierMissingLowerSandbox, "lower_sandbox_weakened"). Claude
//     merges array settings across layers, so a managed
//     "excludedCommands": [] does not stop a lower layer from appending
//     "npm" or "docker", which would let the agent run those outside the
//     sandbox and write ~/.ballast directly. Until M0b case
//     lower-sandbox-merge shows otherwise, Detect applies
//     WeakenedSandboxKeys(doc, LayerUser) to every user-layer file
//     <claude dir>/settings*.json, where <claude dir> is $CLAUDE_CONFIG_DIR
//     when set AND <home>/.claude in every case (both are checked).
//     Detect cannot see projects. The project layer
//     <dir>/.claude/settings.json (committed with the repo, so in-session
//     denyWrite does not govern where it comes from) and the local layer
//     settings.local.json are checked with RepoSandboxWeakened (LayerRepo)
//     by the hook for the session's $CLAUDE_PROJECT_DIR on its first event
//     (ports.ProjectSandboxChecker) and by the runner for the job repo AND
//     HookStartRequest.ProjectDir before deciding the record tier
//     (docs/p2-design.md §3.3, §3.4). An absent file is fine; an
//     unreadable or unparsable one counts as weakening (fail closed).
//     LIMIT (docs/trust-model.md §2.3): the runner's downgrade only labels
//     honest records. A session whose project layer weakens the sandbox
//     can write ~/.ballast/evidence directly, with any tier it likes, so
//     T1 evidence on a host is only as good as the absence of weakened
//     project layers in every Claude session on that host; org.json
//     weakened_project:"deny" turns such sessions' tool calls into denies.
//
// Ownership checks go through Options.Owner so unit tests can fake uid 0
// without root; production uses syscall.Stat_t (unix) and reports "not
// root-owned" on Windows. An agent cannot satisfy 3–5 without root (an agent
// with root is outside every Ballast boundary; docs/trust-model.md).
//
// Acceptance (P2-TIER): testdata/detect.json table (each condition flipped
// on its own ⇒ T0 with exactly that Missing id — condition 6 once per
// weakening key of WeakenedSandboxKeys, in user settings.json and in
// settings.local.json, and once with CLAUDE_CONFIG_DIR pointing elsewhere
// while <home>/.claude/settings.json weakens; a user-layer statusLine /
// env / apiKeyHelper ⇒ still T1 (LayerUser); the managed dir or
// <managed>/ballast group-writable ⇒ org_config (v1.4); all satisfied ⇒
// T1; a row where the managed hook command is a symlink to Self ⇒ T1; a
// row with a relative command ⇒ managed_hooks); testdata/weakened.json
// (both layers); testdata/managed_forms.json golden (shared with
// internal/install); Locked table (absent / non-root / group-writable /
// group-writable parent dir / tier T0 / invalid org.json ⇒ false);
// testdata/locked_home.json incl. a Lookup that blocks ⇒
// ErrLockedHomeTimeout within Timeout + 5 ms; BenchmarkProbe ≤ 200 µs and
// BenchmarkLocked ≤ 30 µs on a warm page cache (v1.4: the parent chain adds
// a few Lstat calls); Probe memoizes (second call does no I/O, asserted
// with a counting FS).
package tier

import (
	"errors"
	"io/fs"
	"sync"
	"time"

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
//     be "", deny, loopback or allow; WeakenedProject must be "", audit or
//     deny;
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
	// RunnerNetwork is the sandboxed test command's IP network policy;
	// default "deny" (darwin: no outbound IP, loopback included; linux:
	// private network namespace). "loopback" additionally lets darwin tests
	// reach localhost:* — a RESIDUAL RISK, host loopback services become
	// reachable (docs/p2-design.md §11). Unix-socket isolation is
	// independent of this value (model.NetworkPolicy).
	RunnerNetwork model.NetworkPolicy `json:"runner_network,omitempty"`
	// WeakenedProject (v1.4) is what the hook does in a session whose
	// project layer weakens the managed sandbox (model.WeakenedProjectPolicy);
	// "" ⇒ DefaultWeakenedProject (v1.5 conditional contract).
	WeakenedProject model.WeakenedProjectPolicy `json:"weakened_project,omitempty"`
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
	opt  Options
	once sync.Once
	info model.TierInfo
}

// New returns a Prober.
func New(opt Options) *Prober { return &Prober{opt: opt} }

// Probe implements ports.TierProbe.
func (p *Prober) Probe() model.TierInfo {
	p.once.Do(func() { p.info = Detect(p.opt) })
	info := p.info
	// Hand out copies of the slices so callers cannot alter the memo.
	info.Missing = append([]string(nil), info.Missing...)
	info.ProtectedExtra = append([]string(nil), info.ProtectedExtra...)
	info.RunnerAllowWrite = append([]string(nil), info.RunnerAllowWrite...)
	return info
}

// Detect runs detection once without memoization (doctor, tests).
func Detect(opt Options) model.TierInfo {
	return detect(opt)
}

// LoadOrgConfig reads, decodes and validates org.json (rules on
// OrgConfig). It does not check ownership (Detect and Locked do).
func LoadOrgConfig(fsys sys.FS, path string) (OrgConfig, error) {
	return loadOrgConfig(fsys, path)
}

// Locked reports whether the host is "redirect-locked": a regular file
// ManagedDirFor(goos)/ballast/org.json exists, is owned by uid 0, is not
// group/other-writable, passes LoadOrgConfig validation and declares
// "tier":"T1", and (v1.4) its parent chain satisfies condition 4 (root-owned,
// not group/other-writable directories up to "/"). It costs one Lstat plus,
// only when that file exists, the parent-chain Lstat calls and one read of a
// few hundred bytes; it never fails (any doubt ⇒ false, i.e. the current
// behaviour).
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
	_, ok := lockState(fsys, goos, owner)
	return ok
}

// LockState is Locked that also returns the validated org.json it read
// (zero value when not locked), so the CLI can pass OrgConfig.HomeRoot to
// LockedHome without reading org.json twice. Locked(...) ==
// second result of LockState(...).
func LockState(fsys sys.FS, goos string, owner func(fi fs.FileInfo) (uid uint32, ok bool)) (OrgConfig, bool) {
	return lockState(fsys, goos, owner)
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
	// Timeout bounds the passwd lookup (v1.4); 0 ⇒ DefaultLockedHomeTimeout.
	Timeout time.Duration
}

// DefaultLockedHomeTimeout bounds LockedHome's passwd lookup. LockedHome runs
// inside layout resolution in internal/cli (WireHook), i.e. BEFORE the
// hook's watchdog is armed, so it carries its own deadline: a darwin
// directory account that is not cached locally or a blocking NSS backend
// must not stall every tool call. 50 ms leaves the B10 budget intact.
const DefaultLockedHomeTimeout = 50 * time.Millisecond

// ErrLockedHomeTimeout is returned by LockedHome when the lookup did not
// finish within the timeout. Layout resolution then fails and the hook fails
// open per B7 (exit 1, empty stdout, one stderr line naming the timeout; no
// audit is possible because the home directory is unknown); doctor
// tier.locked_home times the same lookup and reports fail above the
// timeout.
var ErrLockedHomeTimeout = errors.New("tier: locked home lookup timed out")

// LockedHome resolves <home> on a locked host. Production wiring
// (internal/cli) passes it, closed over HomeOptions, as
// layout.Options.PasswdHome. Frozen rule (docs/p2-design.md §3.2):
//
//  0. The lookup runs in a goroutine bounded by Timeout
//     (DefaultLockedHomeTimeout); expiry ⇒ ("", "", ErrLockedHomeTimeout)
//     at once, the goroutine is abandoned (the hook process exits soon
//     after), and the home_root rule is NOT tried (a slow directory is not
//     "no entry").
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
	return lockedHome(opt)
}

// SettingsLayer selects the WeakenedSandboxKeys rule set (v1.4).
type SettingsLayer string

const (
	// LayerUser: <claude dir>/settings*.json. These files are in denyWrite
	// and permissions.deny (agent-unwritable) and authored by the human, so
	// keys that only run HUMAN-authored commands or set human-authored
	// environment outside the sandbox are equivalent to the human's own
	// tooling; only keys that let AGENT-controlled commands leave the
	// sandbox are weakening.
	LayerUser SettingsLayer = "user"
	// LayerRepo: <dir>/.claude/settings.json and settings.local.json. Their
	// content arrives with the repository (clone, pull, checkout of a
	// third-party branch), so every key that runs commands or changes the
	// environment of Claude, its hooks or the runner counts as weakening.
	LayerRepo SettingsLayer = "repo"
)

// WeakenedSandboxKeys inspects ONE Claude settings document of a lower layer
// and returns, sorted and deduplicated, the dotted keys whose value could
// weaken the managed sandbox or run code outside it (condition 6). Frozen,
// conservative rule (docs/p2-design.md §3.2 condition 6) — an allowlist at
// EVERY level, so keys Claude adds in the future count as weakening until
// they are classified here:
//
//   - For any key, the values null, [], {} and false are harmless (they add
//     nothing).
//   - Top level, harmless in both layers: "$schema", "model",
//     "includeCoAuthoredBy", "cleanupPeriodDays", "outputStyle",
//     "spinnerTipsEnabled", "alwaysThinkingEnabled" (any value); "hooks"
//     and "disableAllHooks" (any value: governed by allowManagedHooksOnly,
//     condition 5); "permissions" and "sandbox" (descended, below).
//   - Top level, harmless in LayerUser only: "env", "statusLine",
//     "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport",
//     "otelHeadersHelper", "enabledPlugins", "extraKnownMarketplaces" (any
//     value). In LayerRepo they are reported, notably "env" (it reaches the
//     process environment of Claude, of the managed hook and of every
//     process Ballast starts outside the sandbox: loader variables such as
//     DYLD_INSERT_LIBRARIES / LD_PRELOAD, GIT_CONFIG_* injection) and every
//     command-running helper; whether the project layer honours each of
//     them is unverified (M0b lower-sandbox-merge).
//   - Every other top-level key with a value other than null/[]/{}/false is
//     reported by its name (e.g. "enableAllProjectMcpServers",
//     "enabledMcpjsonServers").
//   - "permissions": "allow", "deny", "ask" harmless (any value: rules are
//     governed by allowManagedPermissionRulesOnly); "defaultMode" harmless
//     when "default", "acceptEdits" or "plan"; "disableBypassPermissionsMode"
//     harmless; "additionalDirectories" non-empty is reported (whether
//     allowManagedPermissionRulesOnly governs it is unverified); any other
//     key is reported as "permissions.<key>". "permissions" not an object ⇒
//     ["permissions"].
//   - "sandbox" absent, null or {} ⇒ nothing; "sandbox" not an object ⇒
//     ["sandbox"].
//   - harmless inside "sandbox" (can only make it stricter or equal):
//     enabled: true; allowUnsandboxedCommands: false; failIfUnavailable:
//     true; autoAllowBashIfSandboxed (any boolean; a permission-prompt
//     setting, not a barrier); filesystem.denyWrite and filesystem.denyRead
//     (any array of strings: additions only restrict); and, for every other
//     key, the values null, [], {} and false.
//   - everything else is reported, notably sandbox.excludedCommands,
//     sandbox.network.allowUnixSockets, sandbox.network.allowAllUnixSockets,
//     sandbox.network.allowLocalBinding, sandbox.network.allowedDomains,
//     sandbox.network.httpProxyPort, sandbox.network.socksProxyPort,
//     sandbox.filesystem.allowWrite, sandbox.enableWeakerNestedSandbox,
//     sandbox.enabled:false, sandbox.allowUnsandboxedCommands:true,
//     sandbox.failIfUnavailable:false, and any unknown key with a value
//     other than null/[]/{}/false (reported by its dotted path; objects are
//     descended, arrays are not).
//
// doc that is not a JSON object ⇒ error (callers treat it as weakening).
// Pure; table-tested in testdata/weakened.json (every row run in both
// layers with the expected difference).
func WeakenedSandboxKeys(doc []byte, layer SettingsLayer) ([]string, error) {
	return weakenedSandboxKeys(doc, layer)
}

// RepoSandboxWeakened applies WeakenedSandboxKeys(doc, LayerRepo) to every
// regular file matching <repo>/.claude/settings*.json (the project layer
// settings.json and the local layer settings.local.json; repo is any
// directory: a job repo or a session's project dir) and returns
// "<basename>:<key>" entries, sorted; a matching file that cannot be read or parsed yields
// "<basename>:unreadable"; a symlinked .claude directory or settings file
// yields "<basename>:symlink" (the runner does not follow agent-made links
// to decide trust). Empty result ⇒ the repo layer does not weaken the
// sandbox. Callers: the runner, once per job before deciding the record
// tier, for the job repo and HookStartRequest.ProjectDir (non-empty ⇒
// record tier T0 with tier_missing ["lower_sandbox_weakened"] and audit
// diag "tier_mismatch"); doctor for the current directory and its repo;
// the hook through ProjectChecker (first event of a session; every
// PreToolUse only under weakened_project:"deny"). Cost when <repo>/.claude
// does not exist: one Lstat. No exec, no network.
func RepoSandboxWeakened(fsys sys.FS, repo string) []string {
	return repoSandboxWeakened(fsys, repo)
}

// ProjectChecker implements ports.ProjectSandboxChecker with
// RepoSandboxWeakened (internal/cli wires it into hook.Deps.ProjectSandbox
// on a locked host).
type ProjectChecker struct {
	FS sys.FS
}

// Weakened implements ports.ProjectSandboxChecker.
func (c ProjectChecker) Weakened(dir string) []string {
	return repoSandboxWeakened(c.FS, dir)
}

// ManagedForms returns the exact denyWrite and permissions.deny strings
// that `org render` emits and that Detect requires (condition 5). It is
// pure: it reads only lay.GOOS and the directory of lay.OrgRules; home and
// repo entries are the literal "~/", "./" and (v1.5) any-depth "**/" forms,
// independent of any user or repo, so the same strings work for every user
// on the host.
//
//	denyWrite = [dir(OrgRules), "~/.ballast", "./.ballast", "**/.ballast",
//	             dir(self), extra..., "~/.claude/settings*.json",
//	             "./.claude/settings*.json", "**/.claude/settings*.json"]
//	permDeny  = for tool in [Edit, Write, NotebookEdit]:
//	              tool(dir(OrgRules)/**), tool(~/.ballast/**), tool(./.ballast/**),
//	              tool(**/.ballast/**), tool(dir(self)/**),
//	              for each extra e: tool(e), tool(e/**),
//	              tool(~/.claude/settings*.json), tool(./.claude/settings*.json),
//	              tool(**/.claude/settings*.json)
//
// The "**/" forms (v1.5) target project layers the agent could plant in any
// subdirectory — or, from a session started in a monorepo subdirectory, at
// the repository root — for later sessions. Whether Claude honours them in
// managed denyWrite and permissions.deny, and what they are anchored at, is
// unverified (M0b case managed-denywrite-anydepth); the frozen conditional
// contract on that result is DefaultWeakenedProject.
//
// self is used literally (never resolved): org render passes --binary as
// given, Detect passes the matched managed hook's command string. dir()
// splits on '/' (and '\' when GOOS is windows); duplicates keep the first
// occurrence. testdata/managed_forms.json is the frozen golden.
func ManagedForms(lay layout.Layout, self string, extra []string) (denyWrite, permDeny []string) {
	return managedForms(lay, self, extra)
}

// DefaultWeakenedProject (v1.5) is the effective weakened_project of a T1
// host whose org.json omits the key, and the value `org render` emits.
// FROZEN CONDITIONAL CONTRACT (docs/p2-design.md §3.2 "任意深度形式"):
//
//   - M0b case managed-denywrite-anydepth "pass" (the "**/" forms of
//     ManagedForms refuse Bash and Edit/Write writes both at any depth below
//     the session directory and at the repository root of a session started
//     in a subdirectory) ⇒ the default is model.WeakenedProjectAudit;
//   - "fail", "needs_credentials" or "unverifiable" (not proven) ⇒ the
//     default is model.WeakenedProjectDeny.
//
// The skeleton freezes the not-proven branch (deny). P2-CLI sets the final
// value at integration from docs/facts/m0b-cases.json, together with the
// org render golden of P2-INSTALL; flipping it to audit requires that pass
// row (contract change recorded in docs/p2-design.md §13).
const DefaultWeakenedProject = model.WeakenedProjectDeny

// EffectiveWeakenedProject returns org.WeakenedProject, or
// DefaultWeakenedProject when it is "". internal/cli fills
// hook.Deps.WeakenedProject with this value (never "") on a locked host.
func EffectiveWeakenedProject(org OrgConfig) model.WeakenedProjectPolicy {
	return effectiveWeakened(org)
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
	return protectedPaths(fsys, lay, self, repo, extra)
}
