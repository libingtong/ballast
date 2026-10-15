//go:build ballast_fault

package hook

import (
	"strings"
	"testing"
)

func TestFaultsFromEnvArmed(t *testing.T) {
	env := func(v string, set bool) func(string) (string, bool) {
		return func(k string) (string, bool) {
			if k != EnvFault {
				return "", false
			}
			return v, set
		}
	}
	for _, p := range AllFaultPoints {
		for _, m := range allFaultModes {
			inj := FaultsFromEnv(env(string(p)+":"+string(m), true))
			if inj == nil {
				t.Fatalf("%s:%s: nil injector", p, m)
			}
			for _, q := range AllFaultPoints {
				got := inj.Hit(q)
				if (q == p) != (got != nil) || (got != nil && *got != m) {
					t.Errorf("%s:%s: Hit(%s) = %v", p, m, q, got)
				}
			}
		}
	}
	for _, bad := range []struct {
		v   string
		set bool
	}{{"", true}, {"", false}, {"decided", true}, {"nope:panic", true}, {"decided:nope", true}} {
		if inj := FaultsFromEnv(env(bad.v, bad.set)); inj != nil {
			t.Errorf("%q: injector %v, want nil", bad.v, inj)
		}
	}
}

// TestFaultScript is the Go equivalent of scripts/fault.sh: it re-executes the
// test binary as a `ballast hook` process (real stdio, exit code, watchdog,
// os.Exit) for every FaultPoint × {error, panic, hang}.
func TestFaultScript(t *testing.T) {
	for _, p := range AllFaultPoints {
		for _, m := range allFaultModes {
			t.Run(string(p)+"/"+string(m), func(t *testing.T) {
				t.Parallel()
				env := []string{"BALLAST_FAULT=" + string(p) + ":" + string(m), "BALLAST_U7_WD_MS=100"}
				r := runChild(t, preToolBash, env, "--client", "claude-code", "--event", "PreToolUse")
				if r.code != 1 {
					t.Errorf("exit = %d", r.code)
				}
				if r.stdout != "" {
					t.Errorf("stdout = %q", r.stdout)
				}
				if !strings.HasPrefix(r.stderr, "ballast:") {
					t.Errorf("stderr = %q", r.stderr)
				}
				if m == FaultHang && !strings.Contains(r.stderr, "watchdog") {
					t.Errorf("stderr = %q", r.stderr)
				}
			})
		}
	}
	// Control: no fault armed ⇒ the deny is delivered.
	r := runChild(t, preToolBash, nil, "--client", "claude-code", "--event", "PreToolUse")
	if r.code != 0 || !strings.Contains(r.stdout, denyJSON) {
		t.Errorf("control = %+v", r)
	}
}
