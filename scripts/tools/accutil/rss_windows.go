//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

const processQueryLimitedInformation = 0x1000

var procGetProcessMemoryInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS (SIZE_T fields are
// uintptr, so the layout is right on 32 and 64 bit).
type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// rssProbe holds a second handle to the child. The Windows process object
// (and its peak counters) outlives the child as long as any handle to it is
// open, so the probe can read PeakWorkingSetSize after Wait has returned and
// the os/exec handle is gone.
type rssProbe struct{ h syscall.Handle }

// openRSSProbe opens the process right after Start, while os/exec still holds
// its own handle (so the pid cannot be reused). It returns nil when the
// process cannot be opened; the peak is then reported as 0.
func openRSSProbe(pid int) *rssProbe {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return nil
	}
	return &rssProbe{h: h}
}

func (p *rssProbe) close() {
	if p != nil {
		_ = syscall.CloseHandle(p.h)
	}
}

// peakRSSKB returns the peak working set of the finished child in KiB, or 0
// when it could not be read.
func peakRSSKB(_ *os.ProcessState, p *rssProbe) int64 {
	if p == nil {
		return 0
	}
	var c processMemoryCounters
	c.Cb = uint32(unsafe.Sizeof(c))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(p.h), uintptr(unsafe.Pointer(&c)), uintptr(c.Cb))
	if r == 0 {
		return 0
	}
	return int64(c.PeakWorkingSetSize / 1024)
}
