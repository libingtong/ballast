//go:build unix

package state

// lockNB attempts a non-blocking exclusive flock; returns errWouldBlock when held.
func lockNB(fd uintptr) error {
	panic("unimplemented: state.lockNB (unix)")
}

// unlockFD releases the flock.
func unlockFD(fd uintptr) error {
	panic("unimplemented: state.unlockFD (unix)")
}
