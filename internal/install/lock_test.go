package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/sys"
)

// swapOnGuard replaces the lock with a fresh one when the stale-lock guard is
// first taken, like another process winning the race after our Stat.
type swapOnGuard struct {
	sys.FS
	lock string
	once sync.Once
}

func (s *swapOnGuard) OpenFile(n string, flag int, perm fs.FileMode) (sys.File, error) {
	if n == s.lock+".break" {
		s.once.Do(func() {
			_ = os.Remove(s.lock)
			_ = os.WriteFile(s.lock, []byte("live\n"), 0o600)
		})
	}
	return s.FS.OpenFile(n, flag, perm)
}

func ageLock(t *testing.T, lock string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestBreakStaleLockNeverRemovesALiveLock(t *testing.T) {
	e := newTenv(t)
	lock := filepath.Join(e.lay.Home, "install.lock")
	e.write(lock, "99999\n")
	ageLock(t, lock)
	defer func(w time.Duration) { lockWait = w }(lockWait)
	lockWait = 150 * time.Millisecond
	opt := e.opts()
	opt.FS = &swapOnGuard{FS: e.fs, lock: lock}
	if _, err := lockInstall(opt); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked: the replacement lock is live", err)
	}
	if got := e.read(lock); got != "live\n" {
		t.Errorf("live lock content = %q", got)
	}
	ents, _ := os.ReadDir(e.lay.Home)
	for _, en := range ents {
		if en.Name() != "install.lock" {
			t.Errorf("leftover file %s", en.Name())
		}
	}
}

func TestStaleLockBreakingIsMutuallyExclusive(t *testing.T) {
	e := newTenv(t)
	lock := filepath.Join(e.lay.Home, "install.lock")
	e.write(lock, "99999\n")
	ageLock(t, lock)
	defer func(w time.Duration) { lockWait = w }(lockWait)
	lockWait = 20 * time.Second
	var holders, maxHolders atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := lockInstall(e.opts())
			if err != nil {
				t.Error(err)
				return
			}
			n := holders.Add(1)
			for {
				m := maxHolders.Load()
				if n <= m || maxHolders.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			holders.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if maxHolders.Load() != 1 {
		t.Errorf("up to %d holders at once", maxHolders.Load())
	}
}
