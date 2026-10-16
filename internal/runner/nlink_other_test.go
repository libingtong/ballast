//go:build !unix

package runner

import "io/fs"

func fileNlink(fi fs.FileInfo) uint64 { return 1 }
