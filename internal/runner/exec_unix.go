//go:build unix

package runner

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// procTree is the process group of one verify command.
type procTree struct{ pgid int }

// prepare puts the command in a new process group so the whole tree can be
// signalled.
//
// P2 contract (docs/p2-design.md §3.4 "终端", unit P2-RUNNER): when the
// command is wrapped by internal/sandbox (Wrapped.NewSession), it must be
// started with Setsid instead of Setpgid — a new session has no controlling
// terminal, so the sandboxed test can neither open /dev/tty nor inject
// keystrokes into the human's shell with ioctl(TIOCSTI). The session leader
// is also its process-group leader (pgid == pid), so terminate/kill below
// are unchanged. stdin stays /dev/null and stdout/stderr stay the log file
// or a pipe (never the terminal fds), including in Foreground.
func (t *procTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func (t *procTree) attach(p *os.Process) { t.pgid = p.Pid }

// terminate asks the group to stop (SIGTERM).
func (t *procTree) terminate() {
	if t.pgid > 0 {
		_ = syscall.Kill(-t.pgid, syscall.SIGTERM)
	}
}

// kill kills the whole group (SIGKILL).
func (t *procTree) kill() {
	if t.pgid > 0 {
		_ = syscall.Kill(-t.pgid, syscall.SIGKILL)
	}
}

func (t *procTree) close() {}

// terminateProcess asks the runner process pid to stop.
func terminateProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// exitSignal reports the signal that ended the command, if any.
func exitSignal(err error) (string, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return "", false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return "", false
	}
	return signalName(ws.Signal()), true
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return "signal " + strconv.Itoa(int(s))
}
