package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cmdTree prints one line per entry below DIR, sorted: "<sha256> <rel>" for
// regular files and "dir <rel>" for directories. Entries whose base name is
// in the skip list are omitted with their subtree. Two trees are equal iff
// their listings are equal (used for byte-exact round-trip checks).
func cmdTree(args []string) int {
	if len(args) < 1 {
		return fail("tree: want DIR [skip-name ...]")
	}
	root := args[0]
	skip := map[string]bool{}
	for _, s := range args[1:] {
		skip[s] = true
	}
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if skip[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			lines = append(lines, "dir "+rel)
		case d.Type().IsRegular():
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(b)
			lines = append(lines, hex.EncodeToString(sum[:])+" "+rel)
		}
		return nil
	})
	if err != nil {
		return fail("tree: %v", err)
	}
	sort.Slice(lines, func(i, j int) bool {
		return lines[i][strings.IndexByte(lines[i], ' ')+1:] < lines[j][strings.IndexByte(lines[j], ' ')+1:]
	})
	for _, l := range lines {
		fmt.Println(l)
	}
	return 0
}

// cmdSleep sleeps for a (fractional) number of seconds.
func cmdSleep(args []string) int {
	if len(args) != 1 {
		return fail("sleep: want SECONDS")
	}
	s, err := strconv.ParseFloat(args[0], 64)
	if err != nil || s < 0 {
		return fail("sleep: bad duration %q", args[0])
	}
	time.Sleep(time.Duration(s * float64(time.Second)))
	return 0
}

// cmdWaitFile blocks until FILE exists (exit 0) or the timeout (default 120
// seconds) passes (exit 1). Scripts use it as a verify command that finishes
// exactly when the test releases it, with no sleeps in the test itself.
func cmdWaitFile(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		return fail("wait-file: want FILE [SECONDS]")
	}
	limit := 120.0
	if len(args) == 2 {
		v, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return fail("wait-file: bad timeout %q", args[1])
		}
		limit = v
	}
	deadline := time.Now().Add(time.Duration(limit * float64(time.Second)))
	for time.Now().Before(deadline) {
		if _, err := os.Stat(args[0]); err == nil {
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 1
}

// cmdExitFile exits with the integer stored in FILE (0 when it is missing),
// letting a test flip a verify command between pass and fail without
// touching any tracked file.
func cmdExitFile(args []string) int {
	if len(args) != 1 {
		return fail("exit-file: want FILE")
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 || n > 120 {
		return fail("exit-file: %s does not hold a small exit code", args[0])
	}
	return n
}

// cmdCtl is a scripted stand-in for a test command, steered by files in DIR
// so a script can pass, fail, block or time out a verify run without editing
// any tracked file:
//
//	DIR/hold     present at start: create DIR/started, then wait until
//	             DIR/release exists (120s at most, exit 99 otherwise)
//	DIR/exit     the exit status (default 0)
//
// Every call appends a line to DIR/calls.
func cmdCtl(args []string) int {
	if len(args) != 1 {
		return fail("ctl: want DIR")
	}
	dir := args[0]
	if f, err := os.OpenFile(filepath.Join(dir, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "\n")
		_ = f.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "hold")); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "started"), []byte("started\n"), 0o644)
		if cmdWaitFile([]string{filepath.Join(dir, "release"), "120"}) != 0 {
			return 99
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "exit"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 || n > 120 {
		return fail("ctl: %s/exit does not hold a small exit code", dir)
	}
	return n
}
