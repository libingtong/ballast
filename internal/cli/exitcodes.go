package cli

// Exit codes (frozen; docs/architecture.md §7). Generic codes apply to every
// command unless the command table overrides them.
const (
	ExitOK       = 0 // success
	ExitFindings = 1 // command ran; result negative (lint errors, test failures, doctor fail, gate refused, managed present)
	ExitUsage    = 2 // bad flags/arguments, unknown command, configuration error (verify)
	ExitParse    = 3 // a file Ballast must edit/read cannot be parsed (install/uninstall settings), refusal without writes
	ExitRefused  = 4 // operation not permitted (promote org rule / unsupported action; org render --write without root)
	ExitInfra    = 5 // I/O or infrastructure error

	// hook: only 0 and 1 are ever returned (exit 2 would BLOCK the tool call, plan A7).
	ExitHookOK    = 0
	ExitHookError = 1

	// verify --status / --foreground
	ExitVerifyPass       = 0
	ExitVerifyFail       = 1
	ExitVerifyTimeout    = 3
	ExitVerifySignal     = 4
	ExitVerifyInfra      = 5
	ExitVerifyRunning    = 6
	ExitVerifySuperseded = 7
	ExitVerifyNoRecord   = 11

	// verify --check
	ExitCheckFresh       = 0
	ExitCheckStale       = 10
	ExitCheckNoEvidence  = 11
	ExitCheckUndecidable = 12
	ExitCheckRunning     = 13
)
