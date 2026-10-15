//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockExclusive(fd uintptr) error {
	var ol syscall.Overlapped
	const exclusiveFailImmediately = 0x2 | 0x1
	r1, _, e1 := procLockFileEx.Call(fd, exclusiveFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r1 == 0 {
		return e1
	}
	return nil
}
