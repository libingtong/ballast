//go:build darwin || freebsd || netbsd

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
	st.mtime = tspec{uint32(s.Mtimespec.Sec), uint32(s.Mtimespec.Nsec)}
	st.ctime = tspec{uint32(s.Ctimespec.Sec), uint32(s.Ctimespec.Nsec)}
	st.dev, st.ino, st.full = uint32(s.Dev), uint32(s.Ino), true
	return true
}
