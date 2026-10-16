//go:build windows

package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/libingtong/ballast/internal/sys"
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorLockViolation syscall.Errno = 33
	errorIOPending     syscall.Errno = 997

	// lockOffsetHigh puts the one-byte lock range far beyond any real file
	// size, so readers of the log are never blocked by it.
	lockOffsetHigh = 0x40000000
	lockAttempts   = 250
	lockInterval   = 200 * time.Microsecond

	// queueWait bounds the wait in the kernel's FIFO queue of the per-directory
	// mutex (see lockAppend). It must stay well under the hook's 150 ms
	// watchdog: after a timeout the LockFileEx fallback (about
	// lockAttempts*lockInterval = 50 ms) and the error path still have to run,
	// and a wait longer than the watchdog would let the watchdog fire first and
	// lose the record without any audit of why.
	queueWait = 60 * time.Millisecond

	waitObject0   = 0x00000000
	waitAbandoned = 0x00000080
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")

	procCreateMutex  = kernel32.NewProc("CreateMutexW")
	procWaitObject   = kernel32.NewProc("WaitForSingleObject")
	procReleaseMutex = kernel32.NewProc("ReleaseMutex")
)

var (
	mutexMu      sync.Mutex
	mutexHandles = map[string]syscall.Handle{}
)

// dirMutex returns the process-wide handle of the named mutex that queues the
// appenders of dir (0 when it cannot be created or opened). The handle lives
// as long as the process.
func dirMutex(dir string) syscall.Handle {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	name := `Local\ballast-audit-` + hex.EncodeToString(sum[:8])
	mutexMu.Lock()
	defer mutexMu.Unlock()
	if h, ok := mutexHandles[name]; ok {
		return h
	}
	var h syscall.Handle
	if p, err := syscall.UTF16PtrFromString(name); err == nil {
		r, _, _ := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(p)))
		h = syscall.Handle(r)
	}
	mutexHandles[name] = h
	return h
}

// appendFlags: Go drops GENERIC_WRITE from an O_APPEND handle (leaving
// FILE_APPEND_DATA), and LockFileEx needs GENERIC_READ or GENERIC_WRITE on the
// handle, so the file is opened read-write to get GENERIC_READ.
const appendFlags = os.O_RDWR

// lockAppend serializes appenders. Two layers:
//
//  1. A named mutex per audit directory. Waiting on it is a kernel wait with a
//     timeout, woken in FIFO order with direct hand-over, so many hot
//     appenders take turns instead of polling against each other (a polling
//     waiter starves behind a process that re-locks right after unlocking).
//     A dead owner's mutex is handed to the next waiter (WAIT_ABANDONED). The
//     mutex is owned by an OS thread, so the goroutine is pinned to its
//     thread until the returned function runs.
//  2. LockFileEx on a one-byte range at a far offset (the file is opened with
//     FILE_APPEND_DATA through O_APPEND). It protects against processes that
//     could not use the mutex (another user session, creation failure) and is
//     free when uncontended.
//
// The returned function must be called by the goroutine that called lockAppend.
func lockAppend(f sys.File) (func(), error) {
	release := func() {}
	if h := dirMutex(filepath.Dir(f.Name())); h != 0 {
		runtime.LockOSThread()
		r, _, _ := procWaitObject.Call(uintptr(h), uintptr(queueWait/time.Millisecond))
		if st := uint32(r); st == waitObject0 || st == waitAbandoned {
			release = func() {
				_, _, _ = procReleaseMutex.Call(uintptr(h))
				runtime.UnlockOSThread()
			}
		} else {
			// Timed out or failed: fall back to the byte-range lock alone.
			runtime.UnlockOSThread()
		}
	}
	unlock, err := lockRange(f)
	if err != nil {
		release()
		return nil, err
	}
	return func() {
		unlock()
		release()
	}, nil
}

// lockRange polls LockFileEx on the far one-byte range of f.
func lockRange(f sys.File) (func(), error) {
	fd := f.Fd()
	for i := 0; i < lockAttempts; i++ {
		ol := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
		r1, _, e1 := procLockFileEx.Call(fd, lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
		if r1 != 0 {
			return func() {
				ul := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
				_, _, _ = procUnlockFileEx.Call(fd, 0, 1, 0, uintptr(unsafe.Pointer(&ul)))
			}, nil
		}
		if errno, ok := e1.(syscall.Errno); !ok || (errno != errorLockViolation && errno != errorIOPending) {
			return nil, e1
		}
		time.Sleep(lockInterval)
	}
	return nil, errors.New("audit: timed out waiting for the append lock")
}
