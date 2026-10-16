// Package sandbox (P2 unit P2-SANDBOX; architecture v2.0, docs/p2-design.md
// §3.4) wraps the verify runner's TEST COMMAND in an OS sandbox that denies
// writes to the protected paths. The runner process itself stays outside
// the sandbox (it writes evidence and job files); only the child argv is
// wrapped.
//
// Layer L2 (imports model, sys). NOT hot path: it may import os/exec (to
// probe availability); the hook never reaches it (the hook only spawns
// Ballast itself; archtest keeps sandbox out of every hot-path closure).
//
// Backends:
//   - darwin: /usr/bin/sandbox-exec -p <profile> -- argv. Profile =
//     "(version 1)(allow default)" plus one
//     "(deny file-write* (subpath P))" / "(literal P)" / "(regex …)" per
//     protected path, emitted for both Path and Resolved forms.
//     sandbox-exec is deprecated by Apple but present; its absence or a
//     failing probe ⇒ unavailable.
//   - linux: bwrap --die-with-parent --bind / / --dev /dev --proc /proc
//     then --ro-bind-try P P for every existing protected path (Path and
//     Resolved), then --chdir <cwd> -- argv. Protected paths that do not
//     exist at wrap time cannot be bind-mounted without creating a mount
//     point on the host; Spec.MissingProtected lists them and the runner
//     post-checks that they still do not exist (else outcome infra,
//     error "protected_path_created").
//   - windows / anything else: unavailable (native Windows is T0-only).
//
// Probe runs the backend once with a trivial command (`/usr/bin/true`
// equivalent) and a throw-away denied directory, verifying that the
// denied write actually fails; it caches the result per process.
//
// Acceptance (P2-SANDBOX): DarwinProfile/BwrapArgs golden tests
// (testdata/profiles.json); quoting of paths with spaces, quotes, parens and
// non-ASCII; on darwin with sandbox-exec, an integration test runs
// `sh -c 'echo x > $P/f'` under Wrap and asserts the write fails while a
// write to an allowed temp dir succeeds; on linux the same with bwrap,
// SKIP (not FAIL) when bwrap is missing locally, FAIL when CI=true and
// BALLAST_REQUIRE_BWRAP=1.
package sandbox

import (
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Kind is the sandbox backend that wrapped a command (VerifyRecord.Sandbox).
type Kind string

const (
	KindSandboxExec Kind = "sandbox-exec"
	KindBwrap       Kind = "bwrap"
	KindNone        Kind = "none"
)

// Spec describes one wrap.
type Spec struct {
	// DenyWrite are absolute paths (files or directory subtrees) the child
	// must not create, modify, rename or delete. Callers pass both the raw
	// and the symlink-resolved form (tier.ProtectedPath.Path/Resolved).
	DenyWrite []string
	// DenyWriteGlobs are absolute paths whose last element contains '*'
	// (e.g. ~/.claude/settings*.json); darwin uses (regex), linux expands
	// existing matches at wrap time and post-checks new ones.
	DenyWriteGlobs []string
	// Cwd is the child's working directory.
	Cwd string
}

// Wrapped is the result of Wrap.
type Wrapped struct {
	Kind Kind
	Argv []string
	// MissingProtected are DenyWrite entries that did not exist at wrap
	// time and are only post-checked (linux).
	MissingProtected []string
}

// Options configures probing.
type Options struct {
	FS   sys.FS
	Env  sys.Env
	GOOS string
	// SandboxExec / Bwrap override the backend executable paths (tests);
	// defaults "/usr/bin/sandbox-exec" and the first "bwrap" on PATH.
	SandboxExec string
	Bwrap       string
}

// Probe returns the available backend for this host, or KindNone with the
// reason. Cached per process.
func Probe(opt Options) (Kind, error) {
	panic("unimplemented: sandbox.Probe (unit P2-SANDBOX)")
}

// Decide applies the runner policy (docs/p2-design.md §3.4 table): it
// returns the backend to use, or an error that the runner records as
// outcome infra with error "sandbox_unavailable" (SandboxRequire and no
// backend). SandboxOff always yields KindNone.
func Decide(policy model.SandboxPolicy, avail Kind, availErr error) (Kind, error) {
	panic("unimplemented: sandbox.Decide (unit P2-SANDBOX)")
}

// Wrap returns the argv that runs argv inside backend kind (KindNone ⇒
// argv unchanged).
func Wrap(opt Options, kind Kind, spec Spec, argv []string) (Wrapped, error) {
	panic("unimplemented: sandbox.Wrap (unit P2-SANDBOX)")
}

// DarwinProfile renders the SBPL profile for spec (pure; golden-tested).
func DarwinProfile(spec Spec) string {
	panic("unimplemented: sandbox.DarwinProfile (unit P2-SANDBOX)")
}

// BwrapArgs renders the bwrap arguments before "--" for spec, given the
// subset of DenyWrite entries that exist (pure; golden-tested).
func BwrapArgs(spec Spec, existing map[string]bool) []string {
	panic("unimplemented: sandbox.BwrapArgs (unit P2-SANDBOX)")
}
