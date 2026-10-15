package sys

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var goos = runtime.GOOS

// File is the subset of *os.File Ballast uses (locks need Fd).
type File interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	Sync() error
	Stat() (fs.FileInfo, error)
	Fd() uintptr
	Name() string
}

// FS abstracts the file system. Paths are OS paths (not the normalized
// internal form). OSFS is the production implementation; tests use OSFS on
// t.TempDir() or wrap it (FaultFS) to inject failures.
type FS interface {
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
	Lstat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Readlink(name string) (string, error)
	EvalSymlinks(name string) (string, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	MkdirAll(name string, perm fs.FileMode) error
	Rename(oldpath, newpath string) error
	Remove(name string) error
	RemoveAll(name string) error
}

// OSFS is the real file system.
type OSFS struct{}

// ReadFile implements FS.
func (OSFS) ReadFile(n string) ([]byte, error) { return os.ReadFile(n) }

// Stat implements FS.
func (OSFS) Stat(n string) (fs.FileInfo, error) { return os.Stat(n) }

// Lstat implements FS.
func (OSFS) Lstat(n string) (fs.FileInfo, error) { return os.Lstat(n) }

// ReadDir implements FS.
func (OSFS) ReadDir(n string) ([]fs.DirEntry, error) { return os.ReadDir(n) }

// Readlink implements FS.
func (OSFS) Readlink(n string) (string, error) { return os.Readlink(n) }

// EvalSymlinks implements FS.
func (OSFS) EvalSymlinks(n string) (string, error) { return filepath.EvalSymlinks(n) }

// OpenFile implements FS.
func (OSFS) OpenFile(n string, flag int, perm fs.FileMode) (File, error) {
	f, err := os.OpenFile(n, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// MkdirAll implements FS.
func (OSFS) MkdirAll(n string, perm fs.FileMode) error { return os.MkdirAll(n, perm) }

// Rename implements FS. On Windows os.Rename uses MoveFileEx(MOVEFILE_REPLACE_EXISTING).
func (OSFS) Rename(o, n string) error { return os.Rename(o, n) }

// Remove implements FS.
func (OSFS) Remove(n string) error { return os.Remove(n) }

// RemoveAll implements FS.
func (OSFS) RemoveAll(n string) error { return os.RemoveAll(n) }

// FaultFS wraps an FS and returns Err from the operations selected by Fail
// (keyed by op name: "ReadFile", "OpenFile", "Rename", ...; value = path
// substring, "" matches any path). Used by fault-injection tests (plan B7).
type FaultFS struct {
	FS
	Fail map[string]string
	Err  error
}

func (f FaultFS) hit(op, name string) bool {
	sub, ok := f.Fail[op]
	if !ok {
		return false
	}
	return sub == "" || strings.Contains(name, sub)
}

// ReadFile implements FS.
func (f FaultFS) ReadFile(n string) ([]byte, error) {
	if f.hit("ReadFile", n) {
		return nil, f.Err
	}
	return f.FS.ReadFile(n)
}

// OpenFile implements FS.
func (f FaultFS) OpenFile(n string, flag int, perm fs.FileMode) (File, error) {
	if f.hit("OpenFile", n) {
		return nil, f.Err
	}
	return f.FS.OpenFile(n, flag, perm)
}

// Rename implements FS.
func (f FaultFS) Rename(o, n string) error {
	if f.hit("Rename", n) {
		return f.Err
	}
	return f.FS.Rename(o, n)
}

// MkdirAll implements FS.
func (f FaultFS) MkdirAll(n string, perm fs.FileMode) error {
	if f.hit("MkdirAll", n) {
		return f.Err
	}
	return f.FS.MkdirAll(n, perm)
}
