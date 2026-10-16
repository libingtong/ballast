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
type NetworkPolicy string

const (
	// NetworkDeny (default): loopback IP and unix sockets inside the
	// writable set only (darwin profile filter; linux --unshare-net plus
	// masked socket directories).
	NetworkDeny NetworkPolicy = "deny"
	// NetworkAllow: unrestricted network (administrator opt-in).
	NetworkAllow NetworkPolicy = "allow"
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
	TierMissingDenyWrite    = "sandbox_deny_write"       // sandbox.filesystem.denyWrite does not cover every protected path
	TierMissingPermDeny     = "permissions_deny"         // permissions.deny / allowManagedPermissionRulesOnly incomplete
	TierMissingManagedPerms = "managed_permissions_only" // allowManagedPermissionRulesOnly != true
	TierMissingUnreadable   = "unreadable"               // an input could not be read/parsed (detection fails closed to T0)
	// TierMissingLowerSandbox: a lower Claude settings layer (user
	// <claude dir>/settings*.json in detection; additionally the job repo's
	// <repo>/.claude/settings*.json in the runner's own judgement) carries a
	// sandbox key that could weaken the managed sandbox (tier.WeakenedSandboxKeys:
	// excludedCommands, network.allowUnixSockets, filesystem.allowWrite,
	// enableWeakerNestedSandbox, network.allowedDomains, …). Conservative
	// until M0b case lower-sandbox-merge shows how Claude merges them
	// (docs/p2-design.md §3.2 condition 6).
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
