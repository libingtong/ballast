//go:build !windows

package snapshot

import (
	"os"
	"syscall"
)

func newProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// procTree is the set of processes started by one git child: its process
// group.
type procTree struct{}

// attach is called right after the child started.
func (*procTree) attach(*os.Process) {}

// kill kills the child's whole process group.
func (*procTree) kill(p *os.Process) error {
	if p == nil {
		return nil
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// close releases what attach acquired.
func (*procTree) close() {}
