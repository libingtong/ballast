//go:build ballast_fault

package cli

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/hook"
	"github.com/libingtong/ballast/internal/model"
)

// TestFaultBuildForcedLock (bench scenarios 13a/14): BALLAST_TEST_LOCKED=1
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

// TestFaultBuildWatchdogOverride: the e2e tests relax the 150 ms watchdog
// through BALLAST_TEST_WATCHDOG; an unset or invalid value keeps the default.
func TestFaultBuildWatchdogOverride(t *testing.T) {
	for v, want := range map[string]time.Duration{"5s": 5 * time.Second, "250ms": 250 * time.Millisecond, "": 0, "x": 0, "-1s": 0, "0": 0} {
		deps, err := wireHook(agentEnv(map[string]string{"BALLAST_TEST_WATCHDOG": v}), wireSeams{FS: lockedFS(""), Owner: wireOwner})
		if err != nil {
			t.Fatal(err)
		}
		if deps.Watchdog != want {
			t.Errorf("BALLAST_TEST_WATCHDOG=%q: Watchdog = %v, want %v", v, deps.Watchdog, want)
		}
	}
}

// TestFaultBuildForcedHomeRoot (bench scenario 13b): with
// BALLAST_TEST_PASSWD=missing the forced lock's passwd lookup answers "no
// entry", so the org.json home_root rule really runs (its last step Lstats
// $HOME); without the switch (13a) the passwd answer ends it before any
// file-system access. The outcome is discarded either way and the redirected
// layout is kept.
func TestFaultBuildForcedHomeRoot(t *testing.T) {
	run := func(passwd string) (homeStats int64, deps *hook.Deps) {
		m := lockedFS("")
		m.mkdirAll("/home", 0, 0o755)
		m.mkdirAll("/home/agent", 1000, 0o755)
		var n atomic.Int64
		m.hook = func(op, name string) {
			if op == "Lstat" && name == "/home/agent" {
				n.Add(1)
			}
		}
		vars := map[string]string{"BALLAST_TEST_LOCKED": "1", "BALLAST_HOME": "/redirected/home"}
		if passwd != "" {
			vars["BALLAST_TEST_PASSWD"] = passwd
		}
		d, err := wireHook(agentEnv(vars), wireSeams{FS: m, Owner: wireOwner, Euid: func() int { return 1000 },
			Lookup: func(int) (string, error) { return "/home/real", nil }})
		if err != nil {
			t.Fatalf("passwd=%q: the forced lock must discard the outcome: %v", passwd, err)
		}
		return n.Load(), d
	}
	if n, d := run(""); n != 0 || d.Layout.Home != "/redirected/home" {
		t.Errorf("13a: $HOME Lstats = %d (want 0: the passwd answer ends the lookup), home %q", n, d.Layout.Home)
	}
	if n, d := run("missing"); n == 0 || d.Layout.Home != "/redirected/home" {
		t.Errorf("13b: $HOME Lstats = %d (want >0: the home_root rule must run), home %q", n, d.Layout.Home)
	}
}
