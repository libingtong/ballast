//go:build !windows

package audit

import (
	"os"

	"github.com/libingtong/ballast/internal/sys"
)

// appendFlags are the access flags of the audit file; O_APPEND is added by
// the caller.
const appendFlags = os.O_WRONLY

// lockAppend is a no-op: a single write(2) on an O_APPEND descriptor is
// atomic with respect to other appenders.
func lockAppend(sys.File) (func(), error) { return func() {}, nil }
