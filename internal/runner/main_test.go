package runner

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/sys"
)

// TestMain turns the test binary into the programs the tests need:
//
//   - `<exe> verify --run-job <id>`: the detached child Start spawns (the
//     stand-in for the ballast binary);
//   - BALLAST_HELPER=<mode> : a verify command with scripted behaviour;
//   - BALLAST_SPAWN_PARENT=<dir> : a process that spawns a detached child and
//     exits at once.
func TestMain(m *testing.M) {
	switch {
	case len(os.Args) >= 4 && os.Args[1] == "verify" && os.Args[2] == "--run-job":
		os.Exit(childRunJob(os.Args[3]))
	case os.Getenv("BALLAST_HELPER") != "":
		os.Exit(helperMain(os.Getenv("BALLAST_HELPER")))
	case os.Getenv("BALLAST_SPAWN_PARENT") != "":
		os.Exit(spawnParentMain(os.Getenv("BALLAST_SPAWN_PARENT")))
	case os.Getenv("BALLAST_SPAWN_CHILD") != "":
		os.Exit(spawnChildMain(os.Getenv("BALLAST_SPAWN_CHILD")))
	}
	os.Exit(m.Run())
}

func childRunJob(id string) int {
	l, err := layout.Resolve(sys.OSEnv{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 5
	}
	self, _ := os.Executable()
	rec, err := RunJob(Options{Layout: l, Self: self}, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-job:", err)
		return 5
	}
	return StatusExitCode(rec)
}

func helperMain(mode string) int {
	code, _ := strconv.Atoi(os.Getenv("BALLAST_HELPER_CODE"))
	file := os.Getenv("BALLAST_HELPER_FILE")
	pidfile := os.Getenv("BALLAST_HELPER_PIDFILE")
	writePID := func(extra ...int) {
		if pidfile == "" {
			return
		}
		s := strconv.Itoa(os.Getpid())
		for _, p := range extra {
			s += " " + strconv.Itoa(p)
		}
		_ = os.WriteFile(pidfile, []byte(s), 0o644)
	}
	switch mode {
	case "exit":
		return code
	case "echo":
		fmt.Println("stdout-line")
		fmt.Fprintln(os.Stderr, "stderr-line")
		return code
	case "sleep":
		writePID()
		time.Sleep(60 * time.Second)
	case "waitfile":
		writePID()
		for i := 0; i < 600; i++ {
			if _, err := os.Stat(file); err == nil {
				return 0
			}
			time.Sleep(100 * time.Millisecond)
		}
		return 99
	case "tree":
		exe, _ := os.Executable()
		child := exec.Command(exe)
		child.Env = append(os.Environ(), "BALLAST_HELPER=sleep", "BALLAST_HELPER_PIDFILE=")
		if err := child.Start(); err != nil {
			return 98
		}
		writePID(child.Process.Pid)
		time.Sleep(60 * time.Second)
	case "ignoreterm":
		signal.Ignore(syscall.SIGTERM)
		writePID()
		time.Sleep(60 * time.Second)
	case "kill-self":
		selfKill()
		time.Sleep(10 * time.Second)
	case "touch":
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return 97
		}
		fmt.Fprintln(f, "touched by the verify command")
		f.Close()
		return 0
	case "probe":
		wd, _ := os.Getwd()
		_ = os.WriteFile(file, []byte(wd+"\n"+os.Getenv("BALLAST_PROBE")+"\n"+os.Getenv("BALLAST_PROBE2")), 0o644)
		return 0
	}
	return 96
}

func spawnParentMain(dir string) int {
	exe, _ := os.Executable()
	env := append(os.Environ(), "BALLAST_SPAWN_PARENT=", "BALLAST_SPAWN_CHILD="+dir)
	pid, err := Spawner{}.SpawnDetached([]string{exe}, env, filepath.Join(dir, "child.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.WriteFile(filepath.Join(dir, "parent.pid"), []byte(fmt.Sprintf("%d %d", os.Getpid(), pid)), 0o644)
	return 0
}

func spawnChildMain(dir string) int {
	fmt.Println("hello from the detached child")
	// Outlive the parent: wait for the test to say the parent is gone.
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	info := childInfo()
	_ = os.WriteFile(filepath.Join(dir, "child.done"), []byte(info), 0o644)
	return 0
}
