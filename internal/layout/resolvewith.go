package layout

import "github.com/libingtong/ballast/internal/sys"

// P2 contract (architecture v2.0, docs/p2-design.md §3.2 "重定向锁").
// Body owned by unit P2-TIER (this file only); wired by P2-CLI.

// Options adjusts Resolve.
type Options struct {
	// IgnoreRedirects (set from tier.Locked on a T1-declared host) makes
	// ResolveWith ignore BALLAST_HOME, BALLAST_MANAGED_DIR and
	// BALLAST_INSTALL_ROOT: Home is <home>/.ballast, InstallRoot is <home>,
	// ManagedDir is ManagedDirFor(GOOS). CLAUDE_CONFIG_DIR, CODEX_HOME and
	// HERMES_HOME keep their meaning (they redirect the clients themselves,
	// not Ballast's trust inputs).
	IgnoreRedirects bool
	// PasswdHome, when IgnoreRedirects is set, supplies <home> from the
	// passwd database of the effective uid instead of $HOME (env.HomeDir).
	// nil ⇒ env.HomeDir. An error ⇒ ResolveWith fails (the hook then
	// fails open per B7 and doctor reports fail; a locked host whose home
	// cannot be determined is misconfigured, not a reason to trust $HOME).
	PasswdHome func() (string, error)
}

// ResolveWith is Resolve with Options. ResolveWith(env, Options{}) must
// equal Resolve(env) for every env (table test). It performs no I/O other
// than calling o.PasswdHome.
func ResolveWith(env sys.Env, o Options) (Layout, error) {
	panic("unimplemented: layout.ResolveWith (unit P2-TIER)")
}
