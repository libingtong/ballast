//go:build windows

package audit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// TestWindowsAppendHandleCanBeLocked guards the open flags: a handle opened
// write-only with O_APPEND carries neither GENERIC_READ nor GENERIC_WRITE and
// LockFileEx fails with ERROR_ACCESS_DENIED, which would fail every Append.
func TestWindowsAppendHandleCanBeLocked(t *testing.T) {
	name := filepath.Join(t.TempDir(), "probe.jsonl")
	f, err := os.OpenFile(name, appendFlags|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	unlock, err := lockAppend(sys.File(f))
	if err != nil {
		t.Fatalf("lockAppend on the audit file handle: %v", err)
	}
	unlock()

	w, dir := newWriter(t, 0)
	for i := 0; i < 2; i++ {
		if err := w.Append([]model.AuditRecord{hitRec(i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := len(readLines(t, filepath.Join(dir, "2026-10.jsonl"))); got != 2 {
		t.Errorf("lines = %d, want 2", got)
	}
}
