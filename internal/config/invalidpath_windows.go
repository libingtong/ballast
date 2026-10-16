//go:build windows

package config

import (
	"errors"
	"syscall"
)

const (
	errorInvalidName syscall.Errno = 123 // ERROR_INVALID_NAME: "The filename, directory name, or volume label syntax is incorrect."
	errorDirectory   syscall.Errno = 267 // ERROR_DIRECTORY: a path component is not a directory
	errorBadPathname syscall.Errno = 161 // ERROR_BAD_PATHNAME
)

// isInvalidPath reports the Windows errors for a path that names no file.
func isInvalidPath(err error) bool {
	return errors.Is(err, errorInvalidName) || errors.Is(err, errorDirectory) || errors.Is(err, errorBadPathname)
}
