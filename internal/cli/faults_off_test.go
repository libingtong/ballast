//go:build !ballast_fault

package cli

import (
	"sync/atomic"
	"testing"
)

// TestProductionIgnoresTestSwitches: a production build ignores
// BALLAST_TEST_LOCKED / BALLAST_TEST_WEAKENED / BALLAST_TEST_TIER (they exist
// only under -tags ballast_fault), so an agent that sets them gains nothing.
func TestProductionIgnoresTestSwitches(t *testing.T) {
	var calls atomic.Int64
	env := agentEnv(map[string]string{
		"BALLAST_TEST_LOCKED": "1", "BALLAST_TEST_WEAKENED": "audit", "BALLAST_TEST_TIER": "T1",
		"BALLAST_HOME": "/redirected/home",
	})
	deps, err := wireHook(env, wireSeams{
		FS: lockedFS(""), Owner: wireOwner, Euid: func() int { return 1000 },
		Lookup: lookupCounter("/home/real", &calls),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || deps.RedirectsIgnored != nil || deps.ProjectSandbox != nil || deps.WeakenedProject != "" {
		t.Errorf("the test switches had an effect: lookups=%d %v %v %q", calls.Load(), deps.RedirectsIgnored, deps.ProjectSandbox, deps.WeakenedProject)
	}
	if deps.Layout.Home != "/redirected/home" {
		t.Errorf("Layout.Home = %q", deps.Layout.Home)
	}
	if testTier(env) != nil {
		t.Error("testTier must be nil in a production build")
	}
}
