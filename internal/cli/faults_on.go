//go:build ballast_fault

package cli

import (
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// Environment variables read ONLY by binaries built with
// `-tags ballast_fault` (never by a production build).
const (
	// EnvTestLocked=1 forces Locked=true (bench scenarios 13 and 14). The
	// layout keeps the redirected home: the real passwd lookup and the
	// home_root judgement run and are timed, but their outcome is discarded.
	EnvTestLocked = "BALLAST_TEST_LOCKED"
	// EnvTestWeakened sets the forced lock's org.json weakened_project
	// ("audit" or "deny"; anything else leaves the default).
	EnvTestWeakened = "BALLAST_TEST_WEAKENED"
	// EnvTestTier=T1 makes the tier probe report a healthy T1 host (bench
	// scenario 12: the canonical-verify deny path).
	EnvTestTier = "BALLAST_TEST_TIER"
	// EnvTestWatchdog (a Go duration, for example "5s") replaces the hook's
	// 150 ms watchdog. Only the e2e tests use it: they run the real binary
	// under `go test -race ./...` on loaded machines, where 150 ms of wall
	// clock is not a stable assertion. The production default and the 30 ms
	// predicate margin are not touched; production builds ignore the variable.
	EnvTestWatchdog = "BALLAST_TEST_WATCHDOG"
)

// testWatchdog returns the overridden watchdog, 0 when unset or invalid.
func testWatchdog(env sys.Env) time.Duration {
	v, ok := env.Getenv(EnvTestWatchdog)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func testLock(env sys.Env) (tier.OrgConfig, bool) {
	if v, ok := env.Getenv(EnvTestLocked); !ok || v != "1" {
		return tier.OrgConfig{}, false
	}
	org := tier.OrgConfig{V: 1, Tier: model.TierT1}
	if home, err := env.HomeDir(); err == nil && home != "" {
		org.HomeRoot = []string{filepath.Dir(home)}
	}
	if v, _ := env.Getenv(EnvTestWeakened); v == string(model.WeakenedProjectAudit) || v == string(model.WeakenedProjectDeny) {
		org.WeakenedProject = model.WeakenedProjectPolicy(v)
	}
	return org, true
}

type fakeTier struct{}

func (fakeTier) Probe() model.TierInfo {
	return model.TierInfo{
		Tier: model.TierT1, Declared: model.TierT1, Locked: true,
		SandboxPolicy: model.SandboxRequire, Network: model.NetworkDeny,
		WeakenedProject: tier.DefaultWeakenedProject,
	}
}

func testTier(env sys.Env) ports.TierProbe {
	if v, ok := env.Getenv(EnvTestTier); ok && v == string(model.TierT1) {
		return fakeTier{}
	}
	return nil
}
