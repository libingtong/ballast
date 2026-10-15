//go:build windows

package snapshot

import (
	"os"
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

func newProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNewProcessGroup}
}

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

// procTree is a Job Object holding the git child and, from then on, every
// helper it spawns (git-lfs filter-process, credential helpers, ...).
// Windows has no process groups to signal. The job is created after the
// child has started, so a helper spawned in that window escapes; if the job
// cannot be set up the child alone is killed (best effort).
type procTree struct {
	mu     sync.Mutex
	job    syscall.Handle
	killed bool
}

// attach is called right after the child started.
func (t *procTree) attach(p *os.Process) {
	if p == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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

// kill terminates the job, or just the child when there is none.
func (t *procTree) kill(p *os.Process) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.killed = true
	if t.job != 0 {
		if r, _, err := procTerminateJobObject.Call(uintptr(t.job), 1); r == 0 {
			return err
		}
		return nil
	}
	if p == nil {
		return nil
	}
	return p.Kill()
}

// close releases the job; processes still in it are killed.
func (t *procTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = syscall.CloseHandle(t.job)
		t.job = 0
	}
}
