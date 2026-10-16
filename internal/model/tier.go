package model

// P2 contract (architecture v2.0, docs/p2-design.md §3). Trust-tier
// detection result and the T1 runner sandbox policy. Produced by
// internal/tier (ports.TierProbe) and consumed by the hook (verify
// interception), the runner (evidence tier) and doctor.

// SandboxPolicy says what the verify runner does about the OS sandbox
// (org.json "runner_sandbox"; docs/p2-design.md §3.4).
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

// Tier-missing component ids (TierInfo.Missing). Stable strings: doctor,
// audit and boundary scripts match on them.
const (
	TierMissingOS           = "os"                       // native Windows (or unknown GOOS): T0 only
	TierMissingEnvOverride  = "env_override"             // BALLAST_HOME / BALLAST_MANAGED_DIR / BALLAST_INSTALL_ROOT set
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
	// SandboxPolicy is the effective runner policy: org.json value when T1
	// (default SandboxRequire), SandboxOff under T0 unless org.json says
	// otherwise (org.json is only trusted when root-owned).
	SandboxPolicy SandboxPolicy `json:"sandbox_policy,omitempty"`
	// ProtectedExtra are additional protected paths from org.json
	// ("protected_extra"), already ~-expanded.
	ProtectedExtra []string `json:"protected_extra,omitempty"`
}
