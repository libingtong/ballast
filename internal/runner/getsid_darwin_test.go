//go:build darwin

package runner

import "syscall"

func getsidSelf() int {
	s, err := syscall.Getsid(0)
	if err != nil {
		return -1
	}
	return s
}
