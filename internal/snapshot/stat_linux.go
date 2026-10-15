//go:build linux

package snapshot

import (
	"io/fs"
	"syscall"
)

func fillSys(st *fileStat, fi fs.FileInfo) bool {
	s, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	st.mtime = tspec{uint32(s.Mtim.Sec), uint32(s.Mtim.Nsec)}
	st.ctime = tspec{uint32(s.Ctim.Sec), uint32(s.Ctim.Nsec)}
	st.dev, st.ino, st.full = uint32(s.Dev), uint32(s.Ino), true
	return true
}
