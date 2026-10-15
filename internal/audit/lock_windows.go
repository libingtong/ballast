//go:build windows

package audit

import (
	"errors"
	"syscall"
	"time"
	"unsafe"

	"github.com/libingtong/ballast/internal/sys"
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorLockViolation syscall.Errno = 33
	errorIOPending     syscall.Errno = 997

	// lockOffsetHigh puts the one-byte lock range far beyond any real file
	// size, so readers of the log are never blocked by it.
	lockOffsetHigh = 0x40000000
	lockAttempts   = 250
	lockInterval   = 200 * time.Microsecond
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// lockAppend serializes appenders with LockFileEx on a one-byte range at a
// far offset (the file is opened with FILE_APPEND_DATA through O_APPEND).
func lockAppend(f sys.File) (func(), error) {
	fd := f.Fd()
	for i := 0; i < lockAttempts; i++ {
		ol := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
		r1, _, e1 := procLockFileEx.Call(fd, lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
		if r1 != 0 {
			return func() {
				ul := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
				_, _, _ = procUnlockFileEx.Call(fd, 0, 1, 0, uintptr(unsafe.Pointer(&ul)))
			}, nil
		}
		if errno, ok := e1.(syscall.Errno); !ok || (errno != errorLockViolation && errno != errorIOPending) {
			return nil, e1
		}
		time.Sleep(lockInterval)
	}
	return nil, errors.New("audit: timed out waiting for the append lock")
}
