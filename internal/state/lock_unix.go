//go:build unix

package state

import "syscall"

// lockNB attempts a non-blocking exclusive flock; returns errWouldBlock when held.
func lockNB(fd uintptr) error {
	for {
		err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
		switch err {
		case nil:
			return nil
		case syscall.EINTR:
			continue
		case syscall.EWOULDBLOCK:
			return errWouldBlock
		}
		return err
	}
}

// unlockFD releases the flock.
func unlockFD(fd uintptr) error {
	for {
		err := syscall.Flock(int(fd), syscall.LOCK_UN)
		if err != syscall.EINTR {
			return err
		}
	}
}
