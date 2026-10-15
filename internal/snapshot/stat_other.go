//go:build !linux && !darwin && !freebsd && !netbsd

package snapshot

import "io/fs"

// Windows and the remaining platforms: mtime and size only (git itself has
// no inode or reliable ctime to compare there).
func fillSys(*fileStat, fs.FileInfo) bool { return false }
