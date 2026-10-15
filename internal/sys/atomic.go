package sys

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// ErrShortWrite is returned when a write call wrote fewer bytes than requested.
var ErrShortWrite = errors.New("sys: short write")

// maxTempBase caps the part of the destination's file name that is copied
// into the temp file name, so "<name>.tmp.<pid>.<rand>" (about 20 bytes more)
// stays below NAME_MAX (255) even for a long destination name.
const maxTempBase = 200

func tempBase(base string) string {
	if len(base) <= maxTempBase {
		return base
	}
	cut := maxTempBase
	for cut > 0 && !utf8.RuneStart(base[cut]) {
		cut--
	}
	return base[:cut]
}

// WriteFileAtomic writes data to a sibling temp file
// ("<path>.tmp.<pid>.<rand>", the name part capped at 200 bytes) and renames it over path (os.Rename ⇒
// MoveFileEx(REPLACE_EXISTING) on Windows). Parent directories are created
// with 0700. When doFsync is true the temp file is fsynced before rename
// (evidence, manifest, backups); state and cache files do not fsync (plan
// v0.4 §3). The temp file is removed on any failure.
func WriteFileAtomic(fsys FS, ids IDGen, path string, data []byte, perm fs.FileMode, doFsync bool) error {
	if err := fsys.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), tempBase(filepath.Base(path))) +
		fmt.Sprintf(".tmp.%d.%s", os.Getpid(), ids.NewID(3))
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = ErrShortWrite
	}
	if err == nil && doFsync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fsys.Rename(tmp, path)
	}
	if err != nil {
		_ = fsys.Remove(tmp)
		return err
	}
	return nil
}
