//go:build !ballast_test

package cli

import (
	"testing"

	"github.com/libingtong/ballast/internal/testenv"
)

// A production build never wires the CI-only watchdog override, whatever the
// hook's environment says (the 150 ms fail-open deadline must not be movable by
// an agent). CI runs this in a step without -tags ballast_test.
func TestWireHookIgnoresWatchdogEnv(t *testing.T) {
	sb := testenv.New(t)
	sb.Vars["BALLAST_TEST_WATCHDOG_MS"] = "15000"
	deps, err := WireHook(sb.Env())
	if err != nil {
		t.Fatal(err)
	}
	if deps.Watchdog != 0 {
		t.Errorf("Deps.Watchdog = %v in a production build, want 0 (= the 150 ms default)", deps.Watchdog)
	}
}
