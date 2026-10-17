//go:build ballast_fault

package cli

import (
	"io/fs"
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
	// EnvTestLocked=1 forces Locked=true (bench scenarios 13a/13b and 14). The
	// layout keeps the redirected home: the real passwd lookup (13a) or, with
	// EnvTestPasswd, the home_root judgement (13b) runs and is timed, but its
	// outcome is discarded.
	EnvTestLocked = "BALLAST_TEST_LOCKED"
	// EnvTestPasswd=missing makes the forced lock's passwd lookup answer "no
	// entry" (the sssd/LDAP case), so the org.json home_root rule really runs
	// in the cold process (bench scenario 13b). The same switch makes the
	// ownership probe report every directory as root-owned except the
	// redirected home (owned by the effective uid): the bench's scratch
	// directories belong to the current user, which would otherwise end the
	// rule's walk at its first Lstat. The walk still stops at the first
	// symlink or group/other-writable component of the real path.
	EnvTestPasswd = "BALLAST_TEST_PASSWD"
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

// testSeams returns sm adjusted by BALLAST_TEST_PASSWD=missing (see
// EnvTestPasswd). It is applied only on the forced-lock path.
func testSeams(env sys.Env, sm wireSeams) wireSeams {
	if v, _ := env.Getenv(EnvTestPasswd); v != "missing" {
		return sm
	}
	sm.Lookup = func(int) (string, error) { return "", sys.ErrNoPasswdEntry }
	home, _ := env.HomeDir()
	euid := sys.Euid()
	if sm.Euid != nil {
		euid = sm.Euid()
	}
	sm.Owner = func(fi fs.FileInfo) (uint32, bool) {
		if home != "" && fi.IsDir() && fi.Name() == filepath.Base(home) {
			return uint32(euid), true
		}
		return 0, true
	}
	return sm
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
