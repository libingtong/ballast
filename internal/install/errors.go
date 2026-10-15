package install

import "errors"

// Sentinel errors specific to the installer. Callers map them to the exit
// codes of docs/architecture.md §7 with errors.Is. §10 wants sentinels in
// model/errors.go; that package is frozen for this unit, so they live here
// until a §12 contract change moves them (the error values stay the same).
var (
	// ErrUsage: invalid Options (no client given, unknown client, relative
	// --binary). Exit 2.
	ErrUsage = errors.New("ballast install: usage error")
	// ErrClientNotDetected: an explicitly requested client is not present and
	// Options.Force is false. Exit 2.
	ErrClientNotDetected = errors.New("ballast install: client not detected")
	// ErrBinaryUnstable: the auto-resolved executable lives under a temporary
	// or build directory, so hook commands would break. Exit 2.
	ErrBinaryUnstable = errors.New("ballast install: binary path is not stable")
	// ErrOrgWriteDenied: `org render --write` without --i-am-admin or without
	// root. Exit 4.
	ErrOrgWriteDenied = errors.New("ballast org render: write not permitted")
	// ErrLocked: another install/uninstall holds the install lock. Exit 5.
	ErrLocked = errors.New("ballast install: another install is in progress")
)
