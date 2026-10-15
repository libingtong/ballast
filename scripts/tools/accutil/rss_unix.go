//go:build !windows

package main

import (
	"os"
	"runtime"
	"syscall"
)

// rssProbe is the Windows-only handle used to read a child's peak working
// set; on Unix the kernel reports it in the child's rusage, so no probe is
// needed.
type rssProbe struct{}

// openRSSProbe is a no-op on Unix.
func openRSSProbe(int) *rssProbe { return nil }

func (*rssProbe) close() {}

// peakRSSKB returns the peak resident set size of a finished child in KiB.
func peakRSSKB(ps *os.ProcessState, _ *rssProbe) int64 {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) / 1024 // bytes on macOS
	}
	return int64(ru.Maxrss) // KiB elsewhere
}
