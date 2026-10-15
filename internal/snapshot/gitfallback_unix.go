//go:build !windows

package snapshot

import (
	"os"
	"syscall"
)

func newProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// killProcessTree kills the child's whole process group.
func killProcessTree(p *os.Process) error {
	if p == nil {
		return nil
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
