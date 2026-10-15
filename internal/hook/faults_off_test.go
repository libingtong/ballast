//go:build !ballast_fault

package hook

import (
	"strings"
	"testing"
)

// Production builds ignore BALLAST_FAULT entirely: an agent must not be able
// to disable enforcement through the environment.
func TestProductionBuildIgnoresFaultEnv(t *testing.T) {
	getenv := func(k string) (string, bool) { return "decided:panic", true }
	if inj := FaultsFromEnv(getenv); inj != nil {
		t.Fatalf("FaultsFromEnv = %v, want nil", inj)
	}
	for _, p := range []FaultPoint{FaultDecided, FaultRender} {
		for _, m := range allFaultModes {
			t.Run(string(p)+"/"+string(m), func(t *testing.T) {
				t.Parallel()
				r := runChild(t, preToolBash, []string{"BALLAST_FAULT=" + string(p) + ":" + string(m)}, "--client", "claude-code", "--event", "PreToolUse")
				if r.code != 0 || !strings.Contains(r.stdout, denyJSON) || r.stderr != "" {
					t.Errorf("child = %+v", r)
				}
			})
		}
	}
}
