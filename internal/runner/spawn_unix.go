//go:build unix

package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// spawnDetached starts argv with Setsid, stdin /dev/null, stdout+stderr to
// logPath (O_APPEND|O_CREATE, 0600; "" ⇒ /dev/null), then Release()s it.
func spawnDetached(argv []string, env []string, logPath string) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, fmt.Errorf("runner: empty argv")
	}
	var out *os.File
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		out = f
	} else {
		f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		out = f
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin = nil // /dev/null
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}
