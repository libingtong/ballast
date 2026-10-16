package layout

import (
	"errors"
	"fmt"

	"github.com/libingtong/ballast/internal/sys"
)

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
	if !o.IgnoreRedirects {
		return Resolve(env)
	}
	var home string
	if o.PasswdHome != nil {
		h, err := o.PasswdHome()
		if err != nil {
			return Layout{}, fmt.Errorf("layout: locked host home directory: %w", err)
		}
		home = h
	} else {
		h, err := env.HomeDir()
		if err != nil {
			return Layout{}, fmt.Errorf("layout: cannot determine home directory: %w", err)
		}
		home = h
	}
	if home == "" {
		return Layout{}, errors.New("layout: locked host home directory is empty")
	}
	return Resolve(lockedEnv{Env: env, home: home})
}

// lockedEnv hides the three Ballast redirection variables and replaces the
// home directory; everything else (CLAUDE_CONFIG_DIR, CODEX_HOME,
// HERMES_HOME, GOOS) is passed through.
type lockedEnv struct {
	sys.Env
	home string
}

func (e lockedEnv) Getenv(k string) (string, bool) {
	switch k {
	case EnvHome, EnvManagedDir, EnvInstallRoot:
		return "", false
	}
	return e.Env.Getenv(k)
}

func (e lockedEnv) HomeDir() (string, error) { return e.home, nil }

// Environ implements sys.Env without the ignored variables.
func (e lockedEnv) Environ() []string {
	all := e.Env.Environ()
	out := make([]string, 0, len(all))
	for _, kv := range all {
		drop := false
		for _, k := range [...]string{EnvHome, EnvManagedDir, EnvInstallRoot} {
			if len(kv) > len(k) && kv[:len(k)] == k && kv[len(k)] == '=' {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
