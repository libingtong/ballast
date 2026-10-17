//go:build !ballast_fault

package cli

import (
	"time"

	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// testLock and testTier are the test overrides of binaries built with
// `-tags ballast_fault` (bench scenarios 12–14, scripts/bench). Production
// builds ignore every BALLAST_TEST_* variable: the lock and the tier come
// from the real root-owned inputs only.
func testLock(sys.Env) (tier.OrgConfig, bool) { return tier.OrgConfig{}, false }

func testSeams(_ sys.Env, sm wireSeams) wireSeams { return sm }

func testTier(sys.Env) ports.TierProbe { return nil }

// testWatchdog: production builds always use hook.DefaultWatchdog.
func testWatchdog(sys.Env) time.Duration { return 0 }
