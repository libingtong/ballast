//go:build ignore

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	d, _ := os.MkdirTemp("", "diag")
	defer os.RemoveAll(d)
	f := filepath.Join(d, "afile")
	os.WriteFile(f, []byte("x"), 0o600)
	_, err := os.ReadDir(f)
	fmt.Printf("ReadDir(file): err=%v notexist=%v enotdir=%v\n", err, errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR))
	_, err = os.Stat(filepath.Join(f, "child"))
	fmt.Printf("Stat(file/child): err=%v notexist=%v\n", err, errors.Is(err, fs.ErrNotExist))
	_, err = os.Stat(`<probe-root>\work\x`)
	fmt.Printf("Stat(<probe-root>): err=%v notexist=%v\n", err, errors.Is(err, fs.ErrNotExist))
	// sleep granularity
	t0 := time.Now()
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Microsecond)
	}
	fmt.Printf("50 x Sleep(200us) = %v\n", time.Since(t0))
	// rename over an open file
	a := filepath.Join(d, "a")
	b := filepath.Join(d, "b")
	os.WriteFile(a, []byte("a"), 0o600)
	os.WriteFile(b, []byte("b"), 0o600)
	h, _ := os.Open(a)
	fmt.Printf("rename over open: %v\n", os.Rename(b, a))
	h.Close()
	fmt.Printf("rename after close: %v\n", os.Rename(b, a))
}
