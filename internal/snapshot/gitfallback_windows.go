//go:build windows

package snapshot

import (
	"os"
	"syscall"
)

const createNewProcessGroup = 0x00000200

func newProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNewProcessGroup}
}

// killProcessTree kills the child; Windows has no process groups to signal,
// so grandchildren (git helpers) end with their stdout pipe.
func killProcessTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
