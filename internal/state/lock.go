package state

import (
	"time"

	"github.com/libingtong/ballast/internal/sys"
)

// TryLock takes an exclusive, non-blocking OS lock on f, retrying every
// LockRetryInterval until budget elapses (measured with clk). It returns an
// unlock function and the time waited, or model.ErrLockTimeout.
// Unix: flock(LOCK_EX|LOCK_NB). Windows: LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|
// LOCKFILE_FAIL_IMMEDIATELY) on byte range [0,1).
func TryLock(f sys.File, budget time.Duration, clk sys.Clock) (unlock func() error, waited time.Duration, err error) {
	panic("unimplemented: state.TryLock")
}
