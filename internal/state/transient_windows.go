//go:build windows

package state

import (
	"errors"
	"syscall"
)

const (
	errorAccessDenied     syscall.Errno = 5
	errorSharingViolation syscall.Errno = 32
	errorDirNotEmpty      syscall.Errno = 145
)

// isTransientWindows reports whether err is a sharing/access/lock conflict
// that clears once the other handle holder lets go.
func isTransientWindows(err error) bool {
	for _, e := range []syscall.Errno{errorAccessDenied, errorSharingViolation, errorLockViolation} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// isTransientWindowsRemove is isTransientWindows plus ERROR_DIR_NOT_EMPTY: when
// RemoveAll deletes a tree, a child file in delete-pending state (another
// handle opened it with FILE_SHARE_DELETE) makes RemoveDirectory report the
// directory as not empty until that handle closes.
func isTransientWindowsRemove(err error) bool {
	return isTransientWindows(err) || errors.Is(err, errorDirNotEmpty)
}
