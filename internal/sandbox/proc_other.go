//go:build !unix

package sandbox

import (
	"io/fs"
	"os/exec"
)

func setsid(cmd *exec.Cmd) {}

func fileUID(fi fs.FileInfo) (uint32, bool) { return 0, false }

type fileKey struct{ dev, ino uint64 }

func keyOf(fi fs.FileInfo) (fileKey, bool) { return fileKey{}, false }
