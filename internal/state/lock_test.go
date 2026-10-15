package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func openLockFile(t *testing.T, p string) *os.File {
	t.Helper()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestTryLockExclusiveAcrossDescriptors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, b := openLockFile(t, p), openLockFile(t, p)

	unlockA, waited, err := TryLock(a, LockBudget, sys.RealClock{})
	if err != nil || waited > LockBudget {
		t.Fatalf("first lock: waited=%v err=%v", waited, err)
	}
	start := time.Now()
	_, waited, err = TryLock(b, LockBudget, sys.RealClock{})
	if !errors.Is(err, model.ErrLockTimeout) {
		t.Fatalf("second lock err = %v, want ErrLockTimeout", err)
	}
	if waited < LockBudget || time.Since(start) > time.Second {
		t.Errorf("waited %v (wall %v)", waited, time.Since(start))
	}

	if err := unlockA(); err != nil {
		t.Fatal(err)
	}
	unlockB, _, err := TryLock(b, LockBudget, sys.RealClock{})
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := unlockB(); err != nil {
		t.Fatal(err)
	}
}

func TestTryLockReleasedOnClose(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, b := openLockFile(t, p), openLockFile(t, p)
	if _, _, err := TryLock(a, LockBudget, sys.RealClock{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TryLock(b, LockBudget, sys.RealClock{}); err != nil {
		t.Fatalf("lock after holder closed: %v", err)
	}
}

func TestTryLockWaitsForRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, b := openLockFile(t, p), openLockFile(t, p)
	unlockA, _, err := TryLock(a, LockBudget, sys.RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(3 * time.Millisecond)
		_ = unlockA()
	}()
	_, waited, err := TryLock(b, 500*time.Millisecond, sys.RealClock{})
	if err != nil {
		t.Fatalf("expected the retry loop to win once released: %v", err)
	}
	if waited <= 0 {
		t.Errorf("waited = %v", waited)
	}
}

func TestTryLockZeroBudgetAndFrozenClock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	a, b := openLockFile(t, p), openLockFile(t, p)
	if _, _, err := TryLock(a, LockBudget, nil); err != nil { // nil clock ⇒ real
		t.Fatal(err)
	}
	if _, _, err := TryLock(b, 0, sys.RealClock{}); !errors.Is(err, model.ErrLockTimeout) {
		t.Errorf("zero budget err = %v", err)
	}
	frozen := sys.NewFakeClock(t0)
	done := make(chan error, 1)
	go func() { _, _, err := TryLock(b, LockBudget, frozen); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, model.ErrLockTimeout) {
			t.Errorf("frozen clock err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TryLock spun forever on a frozen clock")
	}
}

func TestTryLockOnClosedFile(t *testing.T) {
	f := openLockFile(t, filepath.Join(t.TempDir(), "x.lock"))
	f.Close()
	_, _, err := TryLock(f, LockBudget, sys.RealClock{})
	if err == nil || errors.Is(err, model.ErrLockTimeout) {
		t.Errorf("err = %v, want a hard error that is not a timeout", err)
	}
}
