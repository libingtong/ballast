//go:build !ballast_test

package hook

import "testing"

// Production builds ignore BALLAST_TEST_WATCHDOG_MS entirely (as they ignore
// BALLAST_FAULT): whoever controls a hook's environment must not be able to
// stretch the 150 ms fail-open deadline. CI runs this file in a step without
// -tags ballast_test.
func TestProductionBuildIgnoresWatchdogEnv(t *testing.T) {
	for _, v := range []string{"150", "3000", "15000", "60000"} {
		got := WatchdogFromEnv(func(k string) (string, bool) { return v, true })
		if got != 0 {
			t.Errorf("WatchdogFromEnv(%q) = %v in a production build, want 0 (= the 150 ms default)", v, got)
		}
	}
}
