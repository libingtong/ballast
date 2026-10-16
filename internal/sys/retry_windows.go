//go:build windows

package sys

import (
	"errors"
	"io/fs"
	"os"
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

// isTransientRename: the destination (or source) is open elsewhere. "Access
// denied" counts only while the destination still shows up in a Lstat (an open
// handle without FILE_SHARE_DELETE, or delete-pending, which Lstat itself
// reports as access denied); when the destination does not exist the denial is
// a genuine permission error and must not be waited out.
func isTransientRename(dst string, err error) bool {
	switch {
	case errors.Is(err, errorSharingViolation), errors.Is(err, errorLockViolation):
		return true
	case errors.Is(err, errorAccessDenied):
		_, serr := os.Lstat(dst)
		return serr == nil || !errors.Is(serr, fs.ErrNotExist)
	}
	return false
}
