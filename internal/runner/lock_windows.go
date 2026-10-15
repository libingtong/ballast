//go:build windows

package runner

import (
	"syscall"
	"unsafe"

	"github.com/libingtong/ballast/internal/sys"
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
)

var (
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// tryLock takes a non-blocking exclusive LockFileEx on the first byte of f.
// ok is false (and err nil) when another handle holds it.
func tryLock(f sys.File) (ok bool, err error) {
	var ol syscall.Overlapped
	r, _, e := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r != 0 {
		return true, nil
	}
	if en, isErrno := e.(syscall.Errno); isErrno && en == errorLockViolation {
		return false, nil
	}
	return false, e
}

// unlock releases a lock taken by tryLock.
func unlock(f sys.File) {
	var ol syscall.Overlapped
	procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
}
