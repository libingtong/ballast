//go:build windows

package sys

import (
	"io"
	"io/fs"
	"os"
	"syscall"
)

const (
	fileShareAll       = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE
	fileFlagBackupSems = 0x02000000 // FILE_FLAG_BACKUP_SEMANTICS: lets a directory open (the read then fails like os.ReadFile's)
	maxPathWithoutAfix = 240        // longer paths need the \\?\ prefix, which only package os adds
)

// readFile is os.ReadFile with FILE_SHARE_DELETE in the share mode. Go's own
// open leaves it out, which makes every reader block a rename over the file
// it is reading ("access denied" for the renamer); Ballast replaces rule,
// state and review files by rename while hooks read them.
func readFile(name string) ([]byte, error) {
	if len(name) >= maxPathWithoutAfix {
		return os.ReadFile(name)
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, fileShareAll, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL|fileFlagBackupSems, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(h), name)
	defer f.Close()
	return io.ReadAll(f)
}
