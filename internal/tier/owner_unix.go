//go:build unix

package tier

import (
	"io/fs"
	"syscall"
)

// defaultOwner reports the owning uid recorded in fi (a *syscall.Stat_t).
func defaultOwner(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return uint32(st.Uid), true
}
