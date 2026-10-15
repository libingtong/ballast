//go:build windows

package install

import (
	"syscall"
	"unsafe"
)

// diskFree returns the bytes available to the caller on the volume holding dir.
func diskFree(dir string) (uint64, bool) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	var avail, total, free uint64
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&free)))
	if r == 0 {
		return 0, false
	}
	return avail, true
}
