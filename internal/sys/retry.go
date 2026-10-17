package sys

import "time"

// Transient-error retries (Windows only in effect).
//
// Windows opens files without FILE_SHARE_DELETE, so MoveFileEx over a file
// that a reader currently has open fails with "access denied", and a reader
// that opens the file while it is being replaced gets a sharing violation.
// Both last for microseconds in practice. POSIX has neither failure mode, so
// isTransientRead / isTransientRename are always false there and the helpers
// below add no cost.
//
// The budgets are bounded by the hook's 150 ms watchdog and a failed
// state/rules operation is already a fail-open path. Rename gets the longer
// one because the destination can stay unavailable for tens of milliseconds
// after a previous rename-over: antivirus (Defender) keeps a handle on the
// replaced file while it scans it, and the name answers "access denied"
// (delete pending) until that handle closes. A collision that outlasts the
// budget is reported to the caller like any other I/O error.
const (
	readRetryBudget   = 30 * time.Millisecond
	renameRetryBudget = 100 * time.Millisecond
	retryFirstSleep   = 500 * time.Microsecond
	retryMaxSleep     = 5 * time.Millisecond
)

// retryTransient runs op until it succeeds, fails with an error that
// transient does not accept, or budget elapses; the last error is returned.
func retryTransient(budget time.Duration, transient func(error) bool, op func() error) error {
	err := op()
	if err == nil || !transient(err) {
		return err
	}
	deadline := time.Now().Add(budget)
	sleep := retryFirstSleep
	for time.Now().Before(deadline) {
		time.Sleep(sleep)
		if err = op(); err == nil || !transient(err) {
			return err
		}
		if sleep < retryMaxSleep {
			sleep *= 2
		}
	}
	return err
}
