//go:build unix

package sandbox

import (
	"io/fs"
	"os/exec"
	"syscall"
)

// setsid starts cmd in a new session (the same thing the runner must do with
// a wrapped argv, package doc "Terminal").
func setsid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// fileUID returns the owner uid of fi when the platform reports one.
func fileUID(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// fileKey identifies a file system object.
type fileKey struct{ dev, ino uint64 }

func keyOf(fi fs.FileInfo) (fileKey, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{uint64(st.Dev), uint64(st.Ino)}, true //nolint:unconvert // widths differ per platform
}
