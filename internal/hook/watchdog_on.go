//go:build ballast_test

package hook

import (
	"strconv"
	"strings"
	"time"
)

// WatchdogEnv names the override of the watchdog budget. It exists ONLY in
// binaries built with `-tags ballast_test` (CI's test builds); the product
// build neither reads nor honours it (watchdog_off.go), and the product default
// (DefaultWatchdog, 150 ms) is unchanged. CI needs it because a shared runner
// can stall a freshly started process for far more than 150 ms at random
// (file-backed page faults, CPU steal, antivirus scans), which says nothing
// about the hook.
const WatchdogEnv = "BALLAST_TEST_WATCHDOG_MS"

// maxWatchdogOverride keeps the override from turning the watchdog into an
// hour-long hang.
const maxWatchdogOverride = 60 * time.Second

// watchdogFromEnv returns the budget in BALLAST_TEST_WATCHDOG_MS (milliseconds),
// or 0 (= DefaultWatchdog) when the variable is unset, not a plain integer,
// below DefaultWatchdog or above one minute: a bad value is ignored rather than
// trusted, and the override can only relax the budget, never tighten it.
func watchdogFromEnv(getenv func(string) (string, bool)) time.Duration {
	v, ok := getenv(WatchdogEnv)
	if !ok {
		return 0
	}
	ms, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	d := time.Duration(ms) * time.Millisecond
	if d < DefaultWatchdog || d > maxWatchdogOverride {
		return 0
	}
	return d
}
