//go:build windows

package runner

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	createNewProcessGroup = 0x00000200

	processSetQuota        = 0x0100
	processTerminate       = 0x0001
	jobObjectExtendedLimit = 9          // JobObjectExtendedLimitInformation
	jobKillOnClose         = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObject    = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJob  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject = kernel32.NewProc("TerminateJobObject")
)

// jobObjectExtendedLimitInformation mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobObjectExtendedLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	IoInfo                  [6]uint64
	ProcessMemoryLimit      uintptr
	JobMemoryLimit          uintptr
	PeakProcessMemoryUsed   uintptr
	PeakJobMemoryUsed       uintptr
}

// procTree is a Job Object holding the command and, from then on, every
// process it spawns (Windows has no signalable process groups). The job is
// created right after the command started, so a grandchild spawned in that
// window escapes; if the job cannot be set up the command alone is killed.
// KILL_ON_JOB_CLOSE also takes the tree down when the runner itself dies.
type procTree struct {
	mu     sync.Mutex
	proc   *os.Process
	job    syscall.Handle
	killed bool
}

func (t *procTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNewProcessGroup}
}

func (t *procTree) attach(p *os.Process) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.proc = p
	if t.killed {
		_ = p.Kill()
		return
	}
	job, _, _ := procCreateJobObject.Call(0, 0)
	if job == 0 {
		return
	}
	info := jobObjectExtendedLimitInformation{LimitFlags: jobKillOnClose}
	r, _, _ := procSetInformationJob.Call(job, jobObjectExtendedLimit, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if r == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return
	}
	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(p.Pid))
	if err != nil {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return
	}
	r, _, _ = procAssignProcessToJob.Call(job, uintptr(h))
	_ = syscall.CloseHandle(h)
	if r == 0 {
		_ = syscall.CloseHandle(syscall.Handle(job))
		return
	}
	t.job = syscall.Handle(job)
}

// terminate has no gentle form on Windows: it kills.
func (t *procTree) terminate() { t.kill() }

func (t *procTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.killed = true
	if t.job != 0 {
		procTerminateJobObject.Call(uintptr(t.job), 1)
		return
	}
	if t.proc != nil {
		_ = t.proc.Kill()
	}
}

func (t *procTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = syscall.CloseHandle(t.job)
		t.job = 0
	}
}

// terminateProcess stops the runner process pid.
func terminateProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// exitSignal: Windows processes do not die from signals.
func exitSignal(error) (string, bool) { return "", false }
