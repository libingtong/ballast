//go:build windows

package sys

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsTransientClassification(t *testing.T) {
	for _, e := range []syscall.Errno{errorAccessDenied, errorSharingViolation, errorLockViolation} {
		if !isTransientRead(e) {
			t.Errorf("read error %d must be retried (delete-pending / replace in flight)", e)
		}
	}
	if isTransientRead(syscall.ERROR_FILE_NOT_FOUND) {
		t.Error("a missing file is not transient")
	}

	dir := t.TempDir()
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(dst, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Access denied on an existing destination: an open handle or delete-pending.
	if !isTransientRename(dst, errorAccessDenied) {
		t.Error("access denied with an existing destination must be retried")
	}
	// ... but with no destination it is a real permission problem.
	if isTransientRename(filepath.Join(dir, "absent"), errorAccessDenied) {
		t.Error("access denied with no destination must not be waited out")
	}
	if !isTransientRename(filepath.Join(dir, "absent"), errorSharingViolation) {
		t.Error("a sharing violation is always retried")
	}
}
