// Package sandbox (P2 unit P2-SANDBOX; architecture v2.0, docs/p2-design.md
// §3.4) wraps the verify runner's TEST COMMAND in an OS sandbox. The runner
// process itself stays outside the sandbox (it writes evidence and job
// files); only the child argv is wrapped.
//
// Layer L2 (imports model, sys). NOT hot path: it may import os/exec (to
// probe availability). Only internal/runner and internal/cli may import it
// (archtest allowlist: runner lists it, cli is "*"); the hook never reaches
// it (the hook only spawns Ballast itself).
//
// Frozen model: DEFAULT-DENY WRITES. The child may write only
//
//	W = Spec.AllowWrite (repo work tree, its git dir / common dir when
//	    outside the work tree, org.json runner_allow_write)
//	  ∪ Spec.TmpDir (per-job, created 0700 by the runner, removed after)
//	  ∪ a fixed device set (/dev/null, /dev/zero, /dev/tty*, /dev/fd/*,
//	    /dev/dtracehelper on darwin)
//
// and, inside W, never the protected set
//
//	D = Spec.DenyWrite ∪ Spec.DenyWriteGlobs (tier.ProtectedPaths, raw and
//	    resolved) ∪ RepoMandatoryDeny under every Spec.RepoRoots entry
//	    ∪ GitDirMandatoryDeny under every Spec.GitDirs entry.
//
// Every ancestor A of a protected path P with A inside W (from the W root
// down to dir(P)) is pinned so that renaming or deleting A cannot replace
// P (AncestorPins): darwin denies file-write* on (literal A); linux makes A
// a mount point (--bind A A), which rename(2)/rmdir(2) refuse with EBUSY.
// Network follows Spec.Network (model.NetworkPolicy, default deny).
//
// Backends:
//   - darwin: /usr/bin/sandbox-exec -p <DarwinProfile(spec)> -- argv.
//     Profile, in this order (SBPL: the last matching rule wins):
//     (version 1)(allow default)
//     (deny file-write*)
//     (allow file-write* <W as (subpath …), devices as (literal …)/(regex …)>)
//     (deny file-write* <D as (subpath …)/(literal …)/(regex …)>)
//     (deny file-write* <AncestorPins as (literal …)>)
//     network deny ⇒ (deny network-outbound)
//     (allow network-outbound (remote ip "localhost:*"))
//     (allow network-outbound (remote unix-socket (subpath <each W>)))
//     plus the escape-channel denials in DarwinEscapeDenials (LaunchServices,
//     AppleEvents and other "ask a process outside the sandbox to run
//     something" services). SBPL matches paths, so rules also hold for
//     paths that do not exist yet (verified on macOS 26 at design time:
//     writes outside W, writes/creates/renames/hard links into D, renaming
//     W or an ancestor of D, symlink-then-write, and outbound TCP are all
//     refused; `git add` inside W still works).
//   - linux: bwrap --die-with-parent --unshare-pid [--unshare-net]
//     --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp
//     [--tmpfs /run --tmpfs /var/run when network deny]
//     then --bind A A for every W entry (existing), then --bind A A for
//     every AncestorPins entry (existing), then --ro-bind P P for every
//     existing D entry (globs expanded at wrap time; a glob whose directory
//     lies inside W ro-binds that whole directory), then --chdir <cwd>
//     --setenv TMPDIR /tmp -- argv. Later mounts override earlier ones.
//     Protected paths inside W that do not exist at wrap time cannot be
//     bound without creating a mount point on the host;
//     Wrapped.MissingProtected lists them and PostCheck reports any that
//     appeared (runner ⇒ outcome infra, error "protected_path_created").
//     Paths outside W need no entry: the root bind is read-only, and hard
//     links across the bind mounts fail with EXDEV (verified in a Debian
//     12 container at design time, with renames of non-pinned ancestors
//     shown to succeed and pinned ones to fail with EBUSY).
//   - windows / anything else: unavailable (native Windows is T0-only).
//
// Probe runs the backend once with a trivial command and a throw-away
// denied directory, verifying that the denied write actually fails; it
// caches the result per process.
//
// Acceptance (P2-SANDBOX): DarwinProfile/BwrapArgs/AncestorPins golden
// tests (testdata/profiles.json; paths with spaces, quotes, parens and
// non-ASCII); ValidateSpec table; integration tests (darwin with
// sandbox-exec, linux with bwrap; SKIP locally when missing, FAIL when
// CI=true and BALLAST_REQUIRE_BWRAP=1 / BALLAST_REQUIRE_SANDBOX_EXEC=1)
// that run each of the escape attempts in docs/p2-design.md §3.4 table
// "沙箱集成用例" and assert every one fails while a write inside W and
// `git add` succeed.
package sandbox

import (
	"errors"

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

// ErrUnavailable is returned by Decide when the sandbox is mandatory and no
// backend is usable; the runner records outcome infra, error
// "sandbox_unavailable", and does NOT run the test command.
var ErrUnavailable = errors.New("sandbox_unavailable")

// ErrInvalidSpec is returned by ValidateSpec; the runner records outcome
// infra, error "sandbox_spec_invalid", and does NOT run the test command.
var ErrInvalidSpec = errors.New("sandbox_spec_invalid")

// ErrProtectedCreated is returned by PostCheck.
var ErrProtectedCreated = errors.New("protected_path_created")

// RepoMandatoryDeny are paths, relative to every repo work tree in
// Spec.RepoRoots, that the test command must never write even though
// they lie inside W. Ballast's own entries come first; the rest mirror the
// "dangerous files" class of Claude Code's own Bash sandbox (code that runs
// later outside any sandbox: git hooks, git config such as core.fsmonitor /
// core.hooksPath, which Ballast's own snapshot would trigger, editor and
// MCP configs) [static basis, unverified: P2-SANDBOX checks the list
// against the sandbox-runtime source and may only ADD entries]. A
// trailing "/" marks a directory subtree.
var RepoMandatoryDeny = []string{
	".ballast/",
	".claude/",
	".git/hooks/",
	".git/config",
	".gitmodules",
	".mcp.json",
	".vscode/",
	".idea/",
}

// GitDirMandatoryDeny are paths, relative to every git dir / common dir in
// Spec.GitDirs (a worktree's private git dir and the shared common dir,
// whether inside or outside the work tree), that the test command must
// never write. Index, objects and refs stay writable (`git add`/`commit`
// inside tests are visible through the content snapshot, not a bypass).
var GitDirMandatoryDeny = []string{
	"hooks/",
	"config",
	"config.worktree",
}

// Spec describes one wrap. All paths are absolute OS paths.
type Spec struct {
	// AllowWrite are the writable roots W (besides TmpDir and devices):
	// the repo work tree, its git dir and common dir when they are outside
	// the work tree, and the ~-expanded org.json runner_allow_write.
	AllowWrite []string
	// RepoRoots are the AllowWrite entries that are repo work trees;
	// RepoMandatoryDeny is applied under each.
	RepoRoots []string
	// GitDirs are the repo's git dir and common dir (resolved through a
	// ".git" file for worktrees); GitDirMandatoryDeny is applied under each.
	// Those outside every RepoRoots entry are also added to the writable
	// set by the runner.
	GitDirs []string
	// DenyWrite are protected files or directory subtrees (raw and
	// symlink-resolved forms, tier.ProtectedPath.Path/Resolved).
	DenyWrite []string
	// DenyWriteGlobs are protected paths whose last element contains '*'
	// (e.g. ~/.claude/settings*.json).
	DenyWriteGlobs []string
	// TmpDir is the per-job temp dir (darwin: under os.TempDir(), created
	// 0700 by the runner; linux: ignored, the child gets a private tmpfs
	// /tmp). TMPDIR is set to it.
	TmpDir string
	// Network is the child's network policy ("" ⇒ NetworkDeny).
	Network model.NetworkPolicy
	// Home is the real home directory (passwd), used by ValidateSpec.
	Home string
	// Cwd is the child's working directory (must lie inside AllowWrite).
	Cwd string
}

// Wrapped is the result of Wrap.
type Wrapped struct {
	Kind Kind
	Argv []string
	// Env are environment changes for the child: "K=V" sets K, a bare "K"
	// (no '=') unsets it. Always sets TMPDIR; with network deny it unsets
	// SSH_AUTH_SOCK and DOCKER_HOST.
	Env []string
	// MissingProtected are protected paths inside W that did not exist at
	// wrap time and are only post-checked (linux).
	MissingProtected []string
}

// Options configures probing.
type Options struct {
	FS   sys.FS
	Env  sys.Env
	GOOS string
	// SandboxExec / Bwrap override the backend executable paths (tests);
	// defaults "/usr/bin/sandbox-exec" and the first "bwrap" on PATH that
	// is root-owned and not group/other-writable.
	SandboxExec string
	Bwrap       string
}

// Probe returns the available backend for this host, or KindNone with the
// reason. Cached per process.
func Probe(opt Options) (Kind, error) {
	panic("unimplemented: sandbox.Probe (unit P2-SANDBOX)")
}

// Decide applies the runner policy (docs/p2-design.md §3.4 table):
//   - origin == model.OriginHook: the sandbox is mandatory whatever policy
//     says; no backend ⇒ ErrUnavailable;
//   - origin == model.OriginCLI: SandboxRequire and no backend ⇒
//     ErrUnavailable; SandboxPrefer ⇒ avail (possibly KindNone);
//     SandboxOff ⇒ KindNone.
func Decide(policy model.SandboxPolicy, origin model.VerifyOrigin, avail Kind, availErr error) (Kind, error) {
	panic("unimplemented: sandbox.Decide (unit P2-SANDBOX)")
}

// ValidateSpec rejects specs that would make the sandbox meaningless:
// an AllowWrite entry equal to "/", to Home or to an ancestor of Home, or
// equal to / inside a DenyWrite directory; a Cwd outside AllowWrite; a
// relative path anywhere. Errors wrap ErrInvalidSpec.
func ValidateSpec(spec Spec) error {
	panic("unimplemented: sandbox.ValidateSpec (unit P2-SANDBOX)")
}

// Wrap returns the argv that runs argv inside backend kind (KindNone ⇒
// argv unchanged). It calls ValidateSpec first.
func Wrap(opt Options, kind Kind, spec Spec, argv []string) (Wrapped, error) {
	panic("unimplemented: sandbox.Wrap (unit P2-SANDBOX)")
}

// PostCheck reports ErrProtectedCreated (wrapping the first offending path)
// when any of w.MissingProtected exists after the child exited.
func PostCheck(fsys sys.FS, w Wrapped) error {
	panic("unimplemented: sandbox.PostCheck (unit P2-SANDBOX)")
}

// AncestorPins returns, sorted and deduplicated, every directory A such
// that A is inside some allow root (A == root or below it) and A is a
// proper ancestor of some protected path of spec (DenyWrite, glob
// directories, RepoMandatoryDeny under RepoRoots, GitDirMandatoryDeny under
// GitDirs) — i.e. the directories whose
// rename would let the child swap a protected path (pure; golden-tested).
func AncestorPins(spec Spec) []string {
	panic("unimplemented: sandbox.AncestorPins (unit P2-SANDBOX)")
}

// DarwinEscapeDenials is the SBPL fragment that closes "ask something
// outside the sandbox to run code" channels left open by (allow default):
// mach-lookup of com.apple.coreservices.launchservicesd,
// com.apple.coreservices.appleevents, com.apple.lsd.* and the launchd job
// submission path, plus process-exec of /usr/bin/open, /usr/bin/osascript
// and /bin/launchctl [unverified: P2-SANDBOX must prove with integration
// tests that `open -a`, `osascript -e 'do shell script …'` and
// `launchctl submit` cannot write outside W; if any channel cannot be
// closed this way the darwin profile switches to (deny default) plus an
// explicit allowlist, which is an internal change of DarwinProfile only].
func DarwinEscapeDenials() string {
	panic("unimplemented: sandbox.DarwinEscapeDenials (unit P2-SANDBOX)")
}

// DarwinProfile renders the SBPL profile for spec (pure; golden-tested).
func DarwinProfile(spec Spec) string {
	panic("unimplemented: sandbox.DarwinProfile (unit P2-SANDBOX)")
}

// BwrapArgs renders the bwrap arguments before "--" for spec, given the
// subset of paths that exist (pure; golden-tested).
func BwrapArgs(spec Spec, existing map[string]bool) []string {
	panic("unimplemented: sandbox.BwrapArgs (unit P2-SANDBOX)")
}
