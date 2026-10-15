package install

import "github.com/libingtong/ballast/internal/model"

// Sentinel errors of the installer. Since contract v1.3 the values live in
// internal/model/errors.go (architecture §10); these names are aliases of
// the SAME values, so errors.Is works with either spelling. Callers map
// them to the exit codes of docs/architecture.md §7.
var (
	// ErrUsage = model.ErrInstallUsage. Exit 2.
	ErrUsage = model.ErrInstallUsage
	// ErrClientNotDetected = model.ErrClientNotDetected. Exit 2.
	ErrClientNotDetected = model.ErrClientNotDetected
	// ErrBinaryUnstable = model.ErrBinaryUnstable. Exit 2.
	ErrBinaryUnstable = model.ErrBinaryUnstable
	// ErrOrgWriteDenied = model.ErrOrgWriteDenied. Exit 4.
	ErrOrgWriteDenied = model.ErrOrgWriteDenied
	// ErrLocked = model.ErrInstallLocked. Exit 5.
	ErrLocked = model.ErrInstallLocked
)
