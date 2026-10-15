//go:build windows

package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const detachedProcess = 0x00000008

// spawnDetached starts argv with CreationFlags DETACHED_PROCESS |
// CREATE_NEW_PROCESS_GROUP, stdin NUL, stdout+stderr to logPath.
func spawnDetached(argv []string, env []string, logPath string) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, fmt.Errorf("runner: empty argv")
	}
	name := logPath
	if name == "" {
		name = os.DevNull
	} else if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin = nil // NUL
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | createNewProcessGroup}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}
