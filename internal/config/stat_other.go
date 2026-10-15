//go:build !unix

package config

import "io/fs"

// inodeOf has no cheap equivalent off unix (a Windows file index needs an
// open handle); size and mtime_ns still detect changes.
func inodeOf(fs.FileInfo) uint64 { return 0 }
