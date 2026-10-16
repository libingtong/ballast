//go:build unix

package runner

import (
	"io/fs"
	"syscall"
)

func fileNlink(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
