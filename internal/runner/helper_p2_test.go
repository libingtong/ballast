package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// helperP2 holds the P2 verify-command modes of the test binary.
func helperP2(mode string) int {
	switch mode {
	case "report":
		// One KEY=value line per interesting environment variable, and
		// the process identity (pid sid) as childInfo reports it.
		fmt.Println("info=" + childInfo())
		fmt.Printf("sid=%d pid=%d\n", getsidSelf(), os.Getpid())
		fmt.Println("stdout=" + stdoutKind())
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_") || strings.HasPrefix(k, "GIT_") ||
				k == "TMPDIR" || k == "SSH_AUTH_SOCK" || k == "DOCKER_HOST" {
				fmt.Println("env:" + kv)
			}
		}
		fmt.Println("stdin-null=" + stdinIsDevNull())
		wd, _ := os.Getwd()
		fmt.Println("cwd=" + wd)
		return 0
	case "writeprobe":
		// BALLAST_PROBE_PATHS: newline-separated absolute paths. Each is
		// created (files) and reported as "ok <path>" or "fail <path>".
		for _, p := range strings.Split(os.Getenv("BALLAST_PROBE_PATHS"), "\n") {
			if p == "" {
				continue
			}
			// "@tmp/x" is relative to the temp dir, other relative paths to
			// the working directory.
			target := p
			switch {
			case strings.HasPrefix(p, "@tmp/"):
				target = filepath.Join(os.TempDir(), strings.TrimPrefix(p, "@tmp/"))
			case !filepath.IsAbs(p):
				target = p
			}
			if dir := filepath.Dir(target); dir != "." && !strings.HasPrefix(p, "/") {
				_ = os.MkdirAll(dir, 0o755)
			}
			if err := os.WriteFile(target, []byte("written by the verify command\n"), 0o644); err != nil {
				fmt.Println("fail " + p)
			} else {
				fmt.Println("ok " + p)
			}
		}
		return 0
	case "stat":
		// Report whether BALLAST_HELPER_FILE exists relative to the cwd.
		if _, err := os.Lstat(os.Getenv("BALLAST_HELPER_FILE")); err == nil {
			fmt.Println("exists")
		} else {
			fmt.Println("missing")
		}
		return 0
	case "cat":
		b, err := os.ReadFile(os.Getenv("BALLAST_HELPER_FILE"))
		if err != nil {
			fmt.Println("error " + err.Error())
			return 1
		}
		fmt.Print(string(b))
		return 0
	}
	return 96
}

func stdinIsDevNull() string {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "false"
	}
	null, err := os.Stat(os.DevNull)
	if err != nil {
		return "false"
	}
	if os.SameFile(fi, null) {
		return "true"
	}
	return "false"
}

// stdoutKind says what fd 1 is: "pipe", "file", "tty" or "other".
func stdoutKind() string {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return "other"
	}
	switch m := fi.Mode(); {
	case m&os.ModeNamedPipe != 0:
		return "pipe"
	case m&os.ModeCharDevice != 0:
		return "tty"
	case m.IsRegular():
		return "file"
	}
	return "other"
}
