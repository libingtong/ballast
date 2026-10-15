package state

import (
	"errors"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// errWouldBlock is returned by lockNB when another holder owns the lock.
var errWouldBlock = errors.New("state: lock held")

// TryLock takes an exclusive, non-blocking OS lock on f, retrying every
// LockRetryInterval until budget elapses (measured with clk). It returns an
// unlock function and the time waited, or model.ErrLockTimeout.
// Unix: flock(LOCK_EX|LOCK_NB). Windows: LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|
// LOCKFILE_FAIL_IMMEDIATELY) on byte range [0,1).
//
// The retry loop is also bounded by an attempt count derived from budget, so
// a frozen test clock cannot make it spin forever.
func TryLock(f sys.File, budget time.Duration, clk sys.Clock) (unlock func() error, waited time.Duration, err error) {
	if clk == nil {
		clk = sys.RealClock{}
	}
	fd := f.Fd()
	start := clk.Now()
	maxAttempts := int(budget/LockRetryInterval) + 1
	for attempt := 1; ; attempt++ {
		lerr := lockNB(fd)
		if lerr == nil {
			return func() error { return unlockFD(fd) }, clk.Now().Sub(start), nil
		}
		waited = clk.Now().Sub(start)
		if !errors.Is(lerr, errWouldBlock) {
			return nil, waited, lerr
		}
		if waited >= budget || attempt >= maxAttempts {
			return nil, waited, model.ErrLockTimeout
		}
		time.Sleep(LockRetryInterval)
	}
}
