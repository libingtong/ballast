//go:build windows

package sys

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A reader must not make a rename over the file it reads fail: Ballast
// replaces files by rename while hooks read them.
func TestOSFSReadDoesNotBlockRenameOver(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("v0"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := (OSFS{}).ReadFile(target); err != nil {
				t.Errorf("read: %v", err)
				return
			}
		}
	}()
	// Plain os.Rename on purpose: the point is that readers never get in its way.
	for i := 0; i < 300; i++ {
		tmp := filepath.Join(dir, "swap")
		if err := os.WriteFile(tmp, []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, target); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("rename %d over a file being read: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestOSFSReadDirectoryAndMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := (OSFS{}).ReadFile(dir); err == nil {
		t.Error("reading a directory must fail")
	}
	if _, err := (OSFS{}).ReadFile(filepath.Join(dir, "nope")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v, want a not-exist error", err)
	}
}
