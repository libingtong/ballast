package main

import (
	"os"
	"strconv"
	"time"
)

// cmdHoldLock takes the exclusive lock Ballast's state store uses on FILE
// (flock / LockFileEx), creates READY once it holds it and keeps it for
// SECONDS (or until killed). Scripts use it to force a state lock timeout in
// a real hook process.
func cmdHoldLock(args []string) int {
	if len(args) != 3 {
		return fail("hold-lock: want LOCKFILE SECONDS READYFILE")
	}
	secs, err := strconv.ParseFloat(args[1], 64)
	if err != nil || secs <= 0 {
		return fail("hold-lock: bad duration %q", args[1])
	}
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fail("hold-lock: %v", err)
	}
	defer f.Close()
	if err := lockExclusive(f.Fd()); err != nil {
		return fail("hold-lock: %v", err)
	}
	if err := os.WriteFile(args[2], []byte("locked\n"), 0o644); err != nil {
		return fail("hold-lock: %v", err)
	}
	time.Sleep(time.Duration(secs * float64(time.Second)))
	return 0
}
