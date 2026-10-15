//go:build !windows

package main

import (
	"os"
	"runtime"
	"syscall"
)

// peakRSSKB returns the peak resident set size of a finished child in KiB.
func peakRSSKB(ps *os.ProcessState) int64 {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) / 1024 // bytes on macOS
	}
	return int64(ru.Maxrss) // KiB elsewhere
}
