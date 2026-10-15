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
)
