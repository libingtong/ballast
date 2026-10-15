//go:build windows

package state

// lockNB attempts LockFileEx(EXCLUSIVE|FAIL_IMMEDIATELY) via
// syscall.NewLazyDLL("kernel32.dll"); returns errWouldBlock when held.
func lockNB(fd uintptr) error {
	panic("unimplemented: state.lockNB (windows)")
}

// unlockFD calls UnlockFileEx.
func unlockFD(fd uintptr) error {
	panic("unimplemented: state.unlockFD (windows)")
}
