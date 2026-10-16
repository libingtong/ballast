package layout

import "github.com/libingtong/ballast/internal/sys"

// P2 contract (architecture v2.0, docs/p2-design.md §3.2 "重定向锁").
// Body owned by unit P2-TIER (this file only); wired by P2-CLI. The
// passwd lookup itself lives in internal/sys/passwd*.go and
// tier.LockedHome (also P2-TIER).

// Options adjusts Resolve.
type Options struct {
	// IgnoreRedirects (set from tier.Locked on a T1-declared host) makes
	// ResolveWith ignore BALLAST_HOME, BALLAST_MANAGED_DIR and
	// BALLAST_INSTALL_ROOT: Home is <home>/.ballast, InstallRoot is <home>,
	// ManagedDir is ManagedDirFor(GOOS). CLAUDE_CONFIG_DIR, CODEX_HOME and
	// HERMES_HOME keep their meaning (they redirect the clients themselves,
	// not Ballast's trust inputs).
	IgnoreRedirects bool
	// PasswdHome, when IgnoreRedirects is set, supplies <home> instead of
	// $HOME (env.HomeDir). Production wiring (P2-CLI) passes a closure
	// over tier.LockedHome (passwd lookup of the effective uid via
	// sys.LookupHome, then — only for sys.ErrNoPasswdEntry — the org.json
	// home_root rule). nil ⇒ env.HomeDir (tests only). An error ⇒
	// ResolveWith fails (the hook then fails open per B7 and doctor check
	// tier.locked_home reports fail with fix hints; a locked host whose
	// home cannot be determined is misconfigured, not a reason to trust
	// $HOME). Ignored when IgnoreRedirects is false (never called).
	// Because it runs before the hook's watchdog is armed, the closure is
	// bounded by tier.HomeOptions.Timeout (default
	// tier.DefaultLockedHomeTimeout, 50 ms); expiry is an error like any
	// other (v2.0.4).
	PasswdHome func() (string, error)
}

// ResolveWith is Resolve with Options. ResolveWith(env, Options{}) must
// equal Resolve(env) for every env (table test). It performs no I/O other
// than calling o.PasswdHome.
func ResolveWith(env sys.Env, o Options) (Layout, error) {
	panic("unimplemented: layout.ResolveWith (unit P2-TIER)")
}
