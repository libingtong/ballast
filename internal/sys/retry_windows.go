//go:build windows

package sys

import (
	"errors"
	"syscall"
)

const (
	errorAccessDenied     syscall.Errno = 5
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

// isTransientRead: a sharing or lock violation while opening or reading a
// file that is being replaced.
func isTransientRead(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}

// isTransientRename: the destination (or source) is open elsewhere.
func isTransientRename(err error) bool {
	return errors.Is(err, errorAccessDenied) || errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}
