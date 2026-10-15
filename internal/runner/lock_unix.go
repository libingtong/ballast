//go:build unix

package runner

import (
	"errors"
	"syscall"

	"github.com/libingtong/ballast/internal/sys"
)

// tryLock takes a non-blocking exclusive flock on f. ok is false (and err
// nil) when another open file description holds it.
func tryLock(f sys.File) (ok bool, err error) {
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return false, err
		}
	}
}

// unlock releases a lock taken by tryLock.
func unlock(f sys.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
