//go:build !windows

package audit

import "github.com/libingtong/ballast/internal/sys"

// lockAppend is a no-op: a single write(2) on an O_APPEND descriptor is
// atomic with respect to other appenders.
func lockAppend(sys.File) (func(), error) { return func() {}, nil }
