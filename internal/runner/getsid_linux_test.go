//go:build linux

package runner

import "syscall"

func getsidSelf() int {
	s, _, errno := syscall.RawSyscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		return -1
	}
	return int(s)
}
