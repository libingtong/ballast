package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Tunables (variables so tests can shorten them).
var (
	lockWait  = 10 * time.Second
	lockStale = 60 * time.Second
	lockPoll  = 20 * time.Millisecond
)

// lockInstall serializes install/uninstall across processes with an
// exclusive-create lock file. A lock older than lockStale is considered
// abandoned. Real (not injected) time is used: the age is that of a file
// modification time.
func lockInstall(opt Options) (release func(), err error) {
	fsys := opt.FS
	path := filepath.Join(opt.Layout.Home, "install.lock")
	if err := fsys.MkdirAll(opt.Layout.Home, 0o700); err != nil {
		return nil, fmt.Errorf("install lock: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		f, err := fsys.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = fsys.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("install lock: %w", err)
		}
		if fi, serr := fsys.Stat(path); serr == nil && time.Since(fi.ModTime()) > lockStale {
			breakStaleLock(opt, path, fi)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w (lock file %s)", ErrLocked, path)
		}
		time.Sleep(lockPoll)
	}
}

// breakStaleLock removes an abandoned lock without deleting a live one.
// observed is the stale file info the caller saw; between that Stat and a
// plain Remove another process could have broken the lock and taken a fresh
// one. Breakers are therefore serialized by a second exclusive-create file and
// re-check, while holding it, that the lock is still the same stale file.
func breakStaleLock(opt Options, path string, observed fs.FileInfo) {
	fsys := opt.FS
	guard := path + ".break"
	f, err := fsys.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if fi, serr := fsys.Stat(guard); serr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = fsys.Remove(guard) // its owner died while breaking a lock
		} else {
			time.Sleep(lockPoll)
		}
		return
	}
	_ = f.Close()
	defer func() { _ = fsys.Remove(guard) }()
	if fi, err := fsys.Stat(path); err == nil && fi.ModTime().Equal(observed.ModTime()) {
		_ = fsys.Remove(path)
	}
}
