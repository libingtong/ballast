//go:build windows

package main

import "os"

// peakRSSKB is unavailable on Windows (reported as 0, scripts then skip the
// memory assertion explicitly).
func peakRSSKB(*os.ProcessState) int64 { return 0 }
