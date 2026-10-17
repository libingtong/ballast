//go:build windows

package sys

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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

// A genuine ACL denial is indistinguishable from delete-pending at the
// error-code level, so ReadFile waits out the read budget (30 ms) before it
// reports it. This pins both halves of that documented trade-off: the denial
// is reported as a permission error, and the wait is bounded (nowhere near
// the 150 ms watchdog).
func TestReadFileACLDenialIsBoundedAndReported(t *testing.T) {
	if _, err := exec.LookPath("icacls"); err != nil {
		t.Skip("icacls not available: cannot create an ACL denial")
	}
	p := filepath.Join(t.TempDir(), "denied.txt")
	if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	const everyone = "*S-1-1-0"
	if out, err := exec.Command("icacls", p, "/deny", everyone+":(R)").CombinedOutput(); err != nil {
		t.Skipf("cannot deny read (%v): %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("icacls", p, "/remove:d", everyone).Run() })
	if _, err := os.ReadFile(p); err == nil {
		t.Skip("the ACL denial does not apply to this account")
	}
	start := time.Now()
	_, err := OSFS{}.ReadFile(p)
	took := time.Since(start)
	if err == nil {
		t.Fatal("ReadFile succeeded on a read-denied file")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want a permission error", err)
	}
	if took < readRetryBudget {
		t.Errorf("denial reported after %v, before the %v read budget: access denied is no longer retried", took, readRetryBudget)
	}
	if took > 10*readRetryBudget {
		t.Errorf("denial took %v, far beyond the %v read budget", took, readRetryBudget)
	}
}
