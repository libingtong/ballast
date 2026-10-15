//go:build !windows

package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExecGitKillsProcessGroup runs the real ExecGit against a fake `git`
// script that leaves a background child behind; when the context ends both
// must be dead.
func TestExecGitKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix-only")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsleep 60 &\necho $! > \"" + pidFile + "\"\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ExecGit{}.Run(ctx, dir, []string{"PATH=" + os.Getenv("PATH")}, "add", "-u")
		done <- err
	}()
	// Cancel only once the script has started its background child.
	var data []byte
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		var err error
		if data, err = os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(data), "\n") {
			break
		}
		if time.Since(start) > 20*time.Second {
			t.Fatal("script did not start")
		}
	}
	cancelled := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error")
		}
		if d := time.Since(cancelled); d > 5*time.Second {
			t.Errorf("Run blocked for %v after cancellation", d)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil { // ESRCH: gone
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
