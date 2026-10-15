package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Exit codes of "run" besides the child's own: 124 timeout (as GNU timeout),
// 125 accutil failure.
const exitTimeout = 124

// cmdRun runs a command with an optional timeout and stream redirection and
// exits with its status. -stats writes "ms=<elapsed>" and "rss_kb=<peak>"
// (rss_kb=0 where the platform cannot tell).
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeout := fs.Duration("timeout", 0, "")
	stdin := fs.String("stdin", "", "")
	stdout := fs.String("stdout", "", "")
	stderr := fs.String("stderr", "", "")
	stats := fs.String("stats", "", "")
	if err := fs.Parse(args); err != nil {
		return fail("run: %v", err)
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return fail("run: no command")
	}
	ctx := context.Background()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, rest[0], rest[1:]...)
	var closers []io.Closer
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	if *stdin != "" {
		f, err := os.Open(*stdin)
		if err != nil {
			return fail("run: %v", err)
		}
		closers = append(closers, f)
		cmd.Stdin = f
	}
	open := func(path string, std *os.File) (io.Writer, error) {
		if path == "" {
			return std, nil
		}
		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		closers = append(closers, f)
		return f, nil
	}
	var err error
	if cmd.Stdout, err = open(*stdout, os.Stdout); err != nil {
		return fail("run: %v", err)
	}
	if cmd.Stderr, err = open(*stderr, os.Stderr); err != nil {
		return fail("run: %v", err)
	}
	start := time.Now()
	// Start and Wait are separate so the Windows peak-memory probe can open
	// the child before it exits (see rss_windows.go).
	var probe *rssProbe
	runErr := cmd.Start()
	if runErr == nil {
		probe = openRSSProbe(cmd.Process.Pid)
		runErr = cmd.Wait()
	}
	defer probe.close()
	elapsed := time.Since(start)
	if *stats != "" {
		rss := int64(0)
		if cmd.ProcessState != nil {
			rss = peakRSSKB(cmd.ProcessState, probe)
		}
		s := fmt.Sprintf("ms=%d\nrss_kb=%d\n", elapsed.Milliseconds(), rss)
		if werr := os.WriteFile(*stats, []byte(s), 0o644); werr != nil {
			return fail("run: %v", werr)
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return exitTimeout
	}
	if runErr == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
		return 137 // killed by a signal
	}
	return fail("run: %v", runErr)
}
