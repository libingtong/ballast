package model

import "errors"

// Shared sentinel errors. Units wrap them with fmt.Errorf("...: %w", err) so
// callers can test with errors.Is.
var (
	// ErrUnimplemented marks contract skeleton functions not yet implemented.
	// Skeletons may also panic("unimplemented: <pkg>.<Func>").
	ErrUnimplemented = errors.New("ballast: unimplemented")
	// ErrNotRepo: cwd is not inside a git work tree.
	ErrNotRepo = errors.New("ballast: not a git repository")
	// ErrFallback: the native snapshot cannot guarantee git equivalence.
	ErrFallback = errors.New("ballast: native snapshot fallback required")
	// ErrDeadline: a deadline-bounded operation ran out of time (→ unknown).
	ErrDeadline = errors.New("ballast: deadline exceeded")
	// ErrUnknownEvent: (client, native event) not in NativeEventTable.
	ErrUnknownEvent = errors.New("ballast: unknown native event")
	// ErrUnknownClient: client id not supported.
	ErrUnknownClient = errors.New("ballast: unknown client")
	// ErrBadInput: hook stdin is not a JSON object.
	ErrBadInput = errors.New("ballast: malformed hook input")
	// ErrLockTimeout: the 20ms state lock budget was exceeded.
	ErrLockTimeout = errors.New("ballast: state lock timeout")
	// ErrSettingsParse: a client settings file exists but cannot be parsed;
	// installers must refuse to overwrite it.
	ErrSettingsParse = errors.New("ballast: client settings parse error")
	// ErrManagedPresent: Ballast is already deployed via managed settings.
	ErrManagedPresent = errors.New("ballast: managed deployment present")

	// Installer sentinels (contract v1.3; moved from internal/install, which
	// re-exports them under their old names, so the values are identical).
	// The CLI maps them to the exit codes of docs/architecture.md §7.

	// ErrInstallUsage: invalid install Options (no client, unknown client,
	// relative --binary). Exit 2.
	ErrInstallUsage = errors.New("ballast install: usage error")
	// ErrClientNotDetected: an explicitly requested client is not present
	// and --force was not given. Exit 2.
	ErrClientNotDetected = errors.New("ballast install: client not detected")
	// ErrBinaryUnstable: the auto-resolved executable lives under a
	// temporary or build directory. Exit 2.
	ErrBinaryUnstable = errors.New("ballast install: binary path is not stable")
	// ErrOrgWriteDenied: `org render --write` without --i-am-admin or root.
	// Exit 4.
	ErrOrgWriteDenied = errors.New("ballast org render: write not permitted")
	// ErrInstallLocked: another install/uninstall holds the install lock.
	// Exit 5.
	ErrInstallLocked = errors.New("ballast install: another install is in progress")
)
