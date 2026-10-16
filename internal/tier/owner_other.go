//go:build !unix

package tier

import "io/fs"

// defaultOwner: the platform cannot tell who owns a file, so nothing is ever
// "root-owned" (native Windows is T0 only).
func defaultOwner(fi fs.FileInfo) (uint32, bool) { return 0, false }
