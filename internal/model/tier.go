package model

// P2 contract (architecture v2.0, docs/p2-design.md §3). Trust-tier
// detection result and the T1 runner sandbox policy. Produced by
// internal/tier (ports.TierProbe) and consumed by the hook (verify
// interception), the runner (evidence tier) and doctor.

// SandboxPolicy says what the verify runner does about the OS sandbox for
// origin=cli runs (org.json "runner_sandbox"; docs/p2-design.md §3.4).
// origin=hook runs ignore it: they always require the sandbox and are
// never executed without one (outcome infra, error "sandbox_unavailable").
type SandboxPolicy string

const (
	// SandboxRequire: the test command must run inside the OS sandbox;
	// sandbox unavailable ⇒ the job ends with outcome infra
	// (error "sandbox_unavailable"), never a pass. Default under T1.
	SandboxRequire SandboxPolicy = "require"
	// SandboxPrefer: sandbox when available, otherwise run unsandboxed and
	// record VerifyRecord.Sandbox = "none" (the record is then T0).
	SandboxPrefer SandboxPolicy = "prefer"
	// SandboxOff: never sandbox. Default under T0 (current behaviour).
	SandboxOff SandboxPolicy = "off"
)

// NetworkPolicy is the sandboxed test command's network access (org.json
// "runner_network"; docs/p2-design.md §3.4).
//
// The policy governs IP traffic ONLY. Local unix-socket isolation does not
// depend on it (v1.4, docs/p2-design.md §3.4 "本地 socket"): under every
// policy the darwin profile allows outbound unix-socket connections only to
// sockets inside the writable set W, and linux always masks /run and
// /var/run with tmpfs and removes SSH_AUTH_SOCK, DOCKER_HOST,
// DBUS_SESSION_BUS_ADDRESS and DBUS_SYSTEM_BUS_ADDRESS from the child.
type NetworkPolicy string

const (
	// NetworkDeny (default): no IP traffic leaves the sandbox. darwin: no
	// outbound IP at all, loopback included (SBPL cannot tell a port the
	// test bound itself from a host service listening on 127.0.0.1, so
	// tests that need loopback fail visibly); linux: --unshare-net, i.e. a
	// private loopback the test can use freely while host loopback services
	// stay unreachable.
	NetworkDeny NetworkPolicy = "deny"
	// NetworkLoopback (administrator opt-in, v1.4): as deny, except that on
	// darwin outbound connections to localhost:* are allowed. RESIDUAL RISK
	// (docs/p2-design.md §11, docs/trust-model.md §2.3): services that
	// listen on the host loopback outside the sandbox (IDE extension
	// servers, dev proxies, container daemons on TCP) become reachable from
	// agent-rewritable test code. linux behaves exactly like deny.
	NetworkLoopback NetworkPolicy = "loopback"
	// NetworkAllow (administrator opt-in): unrestricted outbound IP. It does
	// NOT relax unix-socket isolation (see above).
	NetworkAllow NetworkPolicy = "allow"
)

// WeakenedProjectPolicy is org.json "weakened_project" (v1.4,
// docs/p2-design.md §3.3 "项目层削弱"): what the hook does, on a locked
// host, in a Claude Code session whose project layer
// (<CLAUDE_PROJECT_DIR>/.claude/settings*.json) weakens the managed sandbox
// (tier.RepoSandboxWeakened non-empty).
type WeakenedProjectPolicy string

const (
	// WeakenedProjectAudit: audit diag "lower_sandbox_weakened" once, on the
	// first event of the session; nothing is blocked. Default only when M0b
	// proves the any-depth managed forms effective (v1.5 conditional
	// contract, tier.DefaultWeakenedProject).
	WeakenedProjectAudit WeakenedProjectPolicy = "audit"
	// WeakenedProjectDeny (default while the any-depth managed forms are not
	// proven effective, tier.DefaultWeakenedProject, v1.5): additionally deny
	// EVERY PreToolUse of that
	// session (synthetic deny, audit diag "lower_sandbox_deny"), so no tool
	// call can use the weakened sandbox. It cannot stop settings that act
	// before or outside tool calls (e.g. helper commands Claude runs at
	// start-up), which stay under the host-wide precondition of
	// docs/trust-model.md §2.3.
	WeakenedProjectDeny WeakenedProjectPolicy = "deny"
)

// Tier-missing component ids (TierInfo.Missing). Stable strings: doctor,
// audit and boundary scripts match on them.
const (
	TierMissingOS           = "os"                       // native Windows (or unknown GOOS): T0 only
	TierMissingEnvOverride  = "env_override"             // BALLAST_HOME / BALLAST_MANAGED_DIR / BALLAST_INSTALL_ROOT in effect (only possible on an unlocked host)
	TierMissingBinaryOwner  = "binary_root_owned"        // Self or a parent dir not root-owned, or group/other-writable
	TierMissingOrgConfig    = "org_config"               // <managed>/ballast/org.json absent, not root-owned, or tier != "T1"
	TierMissingManagedFile  = "managed_settings"         // no root-owned, non-world-writable managed settings file
	TierMissingManagedHooks = "managed_hooks"            // no managed Ballast hook whose command == Self
	TierMissingManagedOnly  = "allow_managed_hooks_only" // allowManagedHooksOnly != true
	TierMissingSandbox      = "sandbox"                  // sandbox.enabled/allowUnsandboxedCommands/failIfUnavailable not the T1 values
	TierMissingDenyWrite    = "sandbox_deny_write"       // sandbox.filesystem.denyWrite does not cover every protected path; v1.5: also CLAUDE_CONFIG_DIR set and != <home>/.claude
	TierMissingPermDeny     = "permissions_deny"         // permissions.deny / allowManagedPermissionRulesOnly incomplete
	TierMissingManagedPerms = "managed_permissions_only" // allowManagedPermissionRulesOnly != true
	TierMissingUnreadable   = "unreadable"               // an input could not be read/parsed (detection fails closed to T0)
	// TierMissingLowerSandbox: a lower Claude settings layer (user
	// <claude dir>/settings*.json in detection; additionally, in the
	// runner's own judgement, <dir>/.claude/settings*.json of the job repo
	// AND of the session's project dir HookStartRequest.ProjectDir) carries
	// a key that could weaken the managed sandbox (tier.WeakenedSandboxKeys:
	// excludedCommands, network.allowUnixSockets, filesystem.allowWrite,
	// enableWeakerNestedSandbox, network.allowedDomains, and — project and
	// local layers only — env and command-running helper keys, …).
	// Conservative until M0b case lower-sandbox-merge shows how Claude
	// merges them (docs/p2-design.md §3.2 condition 6). The record-tier
	// downgrade only affects HONEST records; see docs/trust-model.md §2.3
	// for the host-wide precondition of T1 evidence.
	TierMissingLowerSandbox = "lower_sandbox_weakened"
	// TierMissingRunnerSandbox is recorded in VerifyRecord.TierMissing (not
	// produced by detection): the host is T1 but this job ran without the
	// OS sandbox (origin=cli under prefer/off), so the record is T0.
	// (VerifyRecord.TierMissing may also carry TierMissingLowerSandbox when
	// the runner's per-repo check fails.)
	TierMissingRunnerSandbox = "runner_sandbox"
)

// TierInfo is the result of trust-tier detection (plan B0; docs/p2-design.md
// §3.2). Tier is T1 only when Missing is empty. Detection never errors: any
// doubt yields T0 with the reason in Missing.
type TierInfo struct {
	Tier Tier `json:"tier"`
	// Declared is org.json's "tier" ("" when org.json is absent/unreadable).
	// Declared T1 with Tier T0 is reported by doctor as a fail and by the
	// hook as audit diag "tier_mismatch".
	Declared Tier     `json:"declared,omitempty"`
	Missing  []string `json:"missing,omitempty"`
	// SandboxPolicy is the effective runner policy for origin=cli runs:
	// org.json value when T1 (default SandboxRequire), SandboxOff under T0
	// unless org.json says otherwise (org.json is only trusted when
	// root-owned). origin=hook runs always require the sandbox.
	SandboxPolicy SandboxPolicy `json:"sandbox_policy,omitempty"`
	// Network is the sandboxed test command's network policy (default
	// NetworkDeny).
	Network NetworkPolicy `json:"network,omitempty"`
	// WeakenedProject is the effective org.json "weakened_project"
	// (tier.EffectiveWeakenedProject; v1.4, default per the v1.5
	// conditional contract tier.DefaultWeakenedProject).
	WeakenedProject WeakenedProjectPolicy `json:"weakened_project,omitempty"`
	// ProtectedExtra are additional protected paths from org.json
	// ("protected_extra"), already ~-expanded.
	ProtectedExtra []string `json:"protected_extra,omitempty"`
	// RunnerAllowWrite are org.json "runner_allow_write" entries, already
	// ~-expanded (extra writable locations for the sandboxed test command).
	RunnerAllowWrite []string `json:"runner_allow_write,omitempty"`
	// Locked reports the redirect lock (tier.Locked): when true the
	// BALLAST_HOME / BALLAST_MANAGED_DIR / BALLAST_INSTALL_ROOT variables
	// were ignored by this process.
	Locked bool `json:"locked,omitempty"`
}
