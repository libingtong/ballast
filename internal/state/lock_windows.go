//go:build windows

package state

import (
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorLockViolation syscall.Errno = 33
	errorIOPending     syscall.Errno = 997
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// lockNB attempts LockFileEx(EXCLUSIVE|FAIL_IMMEDIATELY) via
// syscall.NewLazyDLL("kernel32.dll"); returns errWouldBlock when held.
func lockNB(fd uintptr) error {
	var ol syscall.Overlapped
	r1, _, e1 := procLockFileEx.Call(fd, lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r1 != 0 {
		return nil
	}
	if errno, ok := e1.(syscall.Errno); ok && (errno == errorLockViolation || errno == errorIOPending) {
		return errWouldBlock
	}
	return e1
}

// unlockFD calls UnlockFileEx.
func unlockFD(fd uintptr) error {
	var ol syscall.Overlapped
	r1, _, e1 := procUnlockFileEx.Call(fd, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r1 == 0 {
		return e1
	}
	return nil
}
