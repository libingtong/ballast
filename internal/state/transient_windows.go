//go:build windows

package state

import (
	"errors"
	"syscall"
)

const (
	errorAccessDenied   syscall.Errno = 5
	errorSharingViolatn syscall.Errno = 32
)

// isTransientWindows reports whether err is a sharing/access/lock conflict
// that clears once the other handle holder lets go.
func isTransientWindows(err error) bool {
	for _, e := range []syscall.Errno{errorAccessDenied, errorSharingViolatn, errorLockViolation} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
