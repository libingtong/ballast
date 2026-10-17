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
// about the hook. It stretches ONLY the guard's kill timer: the deadline of the
// expensive predicates stays DefaultWatchdog-DeadlineMargin (see
// invocation.deadlineSpan) unless DeadlineEnv is set as well, so by default
// decisions are the product's.
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

// DeadlineEnv names the second, SEPARATE override: the time after start at
// which expensive predicates (S snapshot, verify_fresh, git fallback) give up
// and answer unknown. Unlike WatchdogEnv it changes decisions (a slow runner
// gets the verdict instead of unknown), so it is a distinct variable that CI
// sets only where the hosted file system cannot walk a 10k-file tree inside
// the product's 120 ms (Windows); see architecture.md section 9.
const DeadlineEnv = "BALLAST_TEST_DEADLINE_MS"

// deadlineFromEnv returns the span in BALLAST_TEST_DEADLINE_MS, or 0 (= the
// product's DefaultWatchdog-DeadlineMargin) when unset, not a plain integer,
// below that product value or above one minute.
func deadlineFromEnv(getenv func(string) (string, bool)) time.Duration {
	v, ok := getenv(DeadlineEnv)
	if !ok {
		return 0
	}
	ms, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	d := time.Duration(ms) * time.Millisecond
	if d < DefaultWatchdog-DeadlineMargin || d > maxWatchdogOverride {
		return 0
	}
	return d
}
