//go:build !ballast_test

package hook

import "time"

// watchdogFromEnv is a no-op in production builds: the watchdog is always
// DefaultWatchdog.
func watchdogFromEnv(func(string) (string, bool)) time.Duration { return 0 }

// deadlineFromEnv is a no-op in production builds as well.
func deadlineFromEnv(func(string) (string, bool)) time.Duration { return 0 }
