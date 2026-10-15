//go:build !windows

package snapshot

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func TestSpecialFileFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no fifo")
	}
	f := newFixture(t)
	baseRepo(f)
	f.remove("src/b.go")
	if err := syscall.Mkfifo(f.path("src/b.go"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := TreeS(f.opts(false), f.repo, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, model.ErrFallback) {
			t.Errorf("err = %v, want ErrFallback", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hung on a FIFO")
	}
	// Exempt entries are not examined at all.
	f.remove("src/b.go")
	f.write("src/b.go", "package b\n")
	f.remove("README.md")
	if err := syscall.Mkfifo(f.path("README.md"), 0o644); err != nil {
		t.Skip(err)
	}
	// (git itself refuses to add a FIFO; only the native result matters here.)
	if _, err := TreeS(f.opts(false), f.repo, mustFilter(t, "**/*.md")); err != nil {
		t.Errorf("exempt FIFO must be skipped: %v", err)
	}
}
