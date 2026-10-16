//go:build ballast_fault

package cli

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestFaultBuildForcedLock (bench scenarios 13/14): BALLAST_TEST_LOCKED=1
// forces the locked wiring, performs the real passwd lookup and home_root
// judgement (here: a counting fake, whose failure is discarded), and keeps the
// redirected layout so a real ~/.ballast is never touched.
func TestFaultBuildForcedLock(t *testing.T) {
	var calls atomic.Int64
	env := agentEnv(map[string]string{
		"BALLAST_TEST_LOCKED": "1", "BALLAST_TEST_WEAKENED": "deny",
		"BALLAST_HOME": "/redirected/home", "BALLAST_INSTALL_ROOT": "/redirected/root",
	})
	deps, err := wireHook(env, wireSeams{
		FS: lockedFS(""), Owner: wireOwner, Euid: func() int { return 1000 },
		Lookup: func(int) (string, error) { calls.Add(1); return "", errors.New("discarded") },
	})
	if err != nil {
		t.Fatalf("the forced lock must discard the lookup outcome: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("passwd lookup called %d times, want 1", calls.Load())
	}
	if deps.Layout.Home != "/redirected/home" {
		t.Errorf("Layout.Home = %q: the redirected home must be kept", deps.Layout.Home)
	}
	if deps.ProjectSandbox == nil || deps.WeakenedProject != model.WeakenedProjectDeny {
		t.Errorf("forced lock wiring: %v %q", deps.ProjectSandbox, deps.WeakenedProject)
	}
	if len(deps.RedirectsIgnored) != 2 {
		t.Errorf("RedirectsIgnored = %v", deps.RedirectsIgnored)
	}
}

// TestFaultBuildFakeTier (bench scenario 12): BALLAST_TEST_TIER=T1 reports a
// healthy T1 host to the hook.
func TestFaultBuildFakeTier(t *testing.T) {
	deps, err := wireHook(agentEnv(map[string]string{"BALLAST_TEST_TIER": "T1"}), wireSeams{FS: lockedFS(""), Owner: wireOwner})
	if err != nil {
		t.Fatal(err)
	}
	if info := deps.Tier.Probe(); info.Tier != model.TierT1 || info.Declared != model.TierT1 {
		t.Errorf("Probe() = %+v", info)
	}
	other, _ := wireHook(agentEnv(map[string]string{}), wireSeams{FS: lockedFS(""), Owner: wireOwner})
	if info := other.Tier.Probe(); info.Tier == model.TierT1 {
		t.Errorf("without the switch the real (T0 here) probe must answer: %+v", info)
	}
}
