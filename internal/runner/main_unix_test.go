//go:build unix

package runner

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func selfKill() { _ = syscall.Kill(os.Getpid(), syscall.SIGKILL) }

// childInfo reports "pid sid" of the calling process. The session id comes
// from /proc where it exists (the syscall package has no Getsid on Linux);
// elsewhere the process group id stands in, which Setsid also makes equal
// to the pid.
func childInfo() string {
	sid := syscall.Getpgrp()
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i > 0 {
			if f := strings.Fields(s[i+1:]); len(f) > 4 {
				if n, err := strconv.Atoi(f[3]); err == nil {
					sid = n
				}
			}
		}
	}
	return fmt.Sprintf("%d %d", os.Getpid(), sid)
}
