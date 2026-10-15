//go:build unix

package config

import (
	"io/fs"
	"syscall"
)

func inodeOf(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
