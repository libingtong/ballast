package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/sys"
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
		if staleLock(fsys, path) {
			_ = fsys.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w (lock file %s)", ErrLocked, path)
		}
		time.Sleep(lockPoll)
	}
}

func staleLock(fsys sys.FS, path string) bool {
	fi, err := fsys.Stat(path)
	return err == nil && time.Since(fi.ModTime()) > lockStale
}
