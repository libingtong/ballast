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
//	W = Spec.AllowWrite — origin=hook (v1.5): the job's private
//	    materialized tree Spec.Tree (runner.Materialize; neither the real
//	    work tree nor its git dir / common dir is writable any more) and
//	    org.json runner_allow_write; origin=cli: the repo work tree, its
//	    git dir / common dir when outside the work tree — provenance-checked
//	    by ResolveGitDirs —, and org.json runner_allow_write
//	  ∪ Spec.TmpDir (per-job, created 0700 by the runner, removed after;
//	    origin=hook: <WorkRoot>/<job>/tmp, never under the inherited $TMPDIR)
//	  ∪ a fixed device set: /dev/null, /dev/zero, /dev/fd/* — and nothing
//	    else (in particular NO /dev/tty*, see "terminal" below)
//
// and, inside W, never the protected set
//
//	D = Spec.DenyWrite ∪ Spec.DenyWriteGlobs (tier.ProtectedPaths, raw and
//	    resolved) ∪ RepoMandatoryDeny at ANY DEPTH under every
//	    Spec.RepoRoots entry ∪ GitDirMandatoryDeny under every Spec.GitDirs
//	    entry (patterns, see the variables).
//
// Every ancestor A of a Spec.DenyWrite / DenyWriteGlobs path P with A inside
// W (from the W root down to dir(P)) is pinned so that renaming or deleting
// A cannot replace P (AncestorPins): darwin denies file-write* on (literal
// A); linux makes A a mount point (--bind A A), which rename(2)/rmdir(2)
// refuse with EBUSY. Mandatory patterns need no pins: darwin matches them by
// regex at any depth (a renamed ancestor does not help), linux re-scans
// after the run (PostCheck). IP network follows Spec.Network
// (model.NetworkPolicy, default deny). Local unix sockets are isolated under
// EVERY network policy (v1.4, docs/p2-design.md §3.4 "本地 socket"): a
// socket outside W — a container daemon, the user's session bus, an SSH
// agent — is a "run this for me outside the sandbox" service, so
// runner_network:"allow" only opens outbound IP, never local sockets.
//
// Terminal (frozen, docs/p2-design.md §3.4 "终端"): the wrapped child must
// never reach the human's terminal, or it could inject keystrokes with
// ioctl(TIOCSTI) and run commands outside every sandbox. The caller
// (internal/runner) MUST start the wrapped argv in a NEW SESSION
// (SysProcAttr.Setsid on unix, replacing Setpgid: the session leader is also
// its own process-group leader, so group signalling is unchanged) with stdin
// /dev/null and stdout/stderr a regular file or a pipe (foreground mode
// forwards output through a pipe only; it never hands the terminal fds to
// the child). bwrap additionally gets --new-session. Opening /dev/tty then
// fails (no controlling terminal) and TIOCSTI on any inherited fd fails.
//
// Backends:
//   - darwin: /usr/bin/sandbox-exec -p <DarwinProfile(spec)> -- argv.
//     Profile, in this order (SBPL: the last matching rule wins):
//     (version 1)(allow default)
//     (deny file-write*)
//     (allow file-write* <W except Spec.Tree and TreeTmp as (subpath …), devices as
//     (literal …)/(regex …)>)
//     (deny file-write* <D entries NOT inside Spec.Tree: DenyWrite as
//     (subpath …)/(literal …), globs as (regex …)>)
//     (allow file-write* (subpath <Spec.Tree>)) — v1.5, only when Tree is
//     set: the carve-out of W inside a D directory (~/.ballast)
//     (allow file-write* (subpath <TreeTmp>)) — the job temp dir next to
//     Tree, the only other such carve-out
//     (deny file-write* <D entries inside Spec.Tree or another W root, and
//     RepoMandatoryDeny / GitDirMandatoryDeny as anchored (regex …) that
//     match at any depth>)
//     (deny file-write* <AncestorPins as (literal …)>)
//     (deny network-outbound) — always
//     IP by policy: deny ⇒ nothing (NO loopback); loopback ⇒
//     (allow network-outbound (remote ip "localhost:*")); allow ⇒
//     (allow network-outbound (remote ip "*:*")) plus
//     (allow network-outbound (remote unix-socket (path-literal
//     "/private/var/run/mDNSResponder"))) for name resolution
//     (allow network-outbound (remote unix-socket (subpath <each W>))) —
//     always, the only other unix sockets reachable
//     (deny network-bind) (deny network-inbound) — always (v1.5), then
//     (allow network-bind (local unix-socket (subpath <each W>))) always,
//     and under loopback / allow only:
//     (allow network-bind (local ip "localhost:*"))
//     (allow network-inbound (local ip "localhost:*"))
//     — intended: listen on loopback only; measured at design time (v1.5,
//     macOS 26): under loopback / allow a bind to the wildcard address
//     0.0.0.0 is NOT refused and inbound from the host's LAN address is
//     accepted (the `local ip` filter cannot express it) — a registered
//     residual risk of loopback / allow (docs/p2-design.md §3.4, §11); under
//     deny every IP bind (0.0.0.0, 127.0.0.1, [::1]) is refused
//     plus DarwinEscapeDenials() (v1.5: IPC DEFAULT-DENY — (deny mach-lookup)
//     followed by the explicit DarwinMachAllow allowlist, explicit
//     (deny mach-register), (deny job-creation), (deny user-preference-write),
//     (deny lsopen), (deny appleevent-send), signals only within the sandbox,
//     and process-exec denials of open / osascript / launchctl). File READS
//     stay allowed. SBPL matches paths, so rules also hold for
//     paths that do not exist yet (verified on macOS 26 at design time:
//     writes outside W, writes/creates/renames/hard links into D, renaming
//     W or an ancestor of D, symlink-then-write, and outbound TCP are all
//     refused; `git add` inside W still works). Note: at design time the
//     profile still allowed localhost:* under deny; v1.4 removed that
//     (host loopback services are outside the sandbox).
//   - linux: bwrap --die-with-parent --new-session --unshare-pid
//     [--unshare-net unless network allow] --ro-bind / / --dev /dev
//     --proc /proc --tmpfs /tmp --tmpfs /run --tmpfs /var/run (the two
//     socket directories are masked under EVERY policy)
//     then --bind A A for every W entry except Spec.Tree (existing), then
//     --ro-bind P P for every existing D entry NOT inside Spec.Tree, then
//     (v1.5, only when Tree is set) --bind <Tree> <Tree> (the carve-out
//     inside the read-only ~/.ballast), then --bind A A for every
//     AncestorPins entry (existing), then --ro-bind P P for every
//     existing D entry inside a W root (globs expanded at wrap time; a glob
//     whose directory lies inside W ro-binds that whole directory) and for every path of
//     Wrapped.Baseline (the mandatory matches that already exist, found by
//     ScanMandatory), then --chdir <cwd> --setenv TMPDIR /tmp, one
//     --setenv K V per Spec.LoaderEnv entry, -- argv.
//     Later mounts override earlier ones. Protected paths inside W that do
//     not exist at wrap time cannot be bound without creating a mount point
//     on the host: Wrapped.MissingProtected lists the explicit ones, and
//     PostCheck re-scans every RepoRoots / GitDirs tree for mandatory
//     matches that are not in Wrapped.Baseline. Anything found is moved out
//     of the repo by Quarantine (the runner then records outcome infra,
//     error "protected_path_created", and audits diag "sandbox_quarantine")
//     so it does not stay on disk and take effect later.
//     Paths outside W need no entry: the root bind is read-only, and hard
//     links across the bind mounts fail with EXDEV (verified in a Debian
//     12 container at design time, with renames of non-pinned ancestors
//     shown to succeed and pinned ones to fail with EBUSY). With network
//     allow the host network namespace is shared, so ABSTRACT unix
//     sockets of the host (not files, not maskable by tmpfs) stay
//     reachable — registered residual risk of "allow" (docs/p2-design.md
//     §11); deny and loopback use a private namespace where they are not.
//   - windows / anything else: unavailable (native Windows is T0-only).
//
// Probe runs the backend once with a trivial command and a throw-away
// denied directory, verifying that the denied write actually fails; it
// caches the result per process.
//
// Wrap-up order (frozen, v1.5, docs/p2-design.md §3.4 "收尾顺序"): the
// runner calls PostCheck only after the child exited (darwin: after it
// SIGKILLed the child's session / process group; linux: bwrap's PID
// namespace is gone with its init), then Quarantine on what PostCheck found,
// and only THEN computes S_end (outside the sandbox) — so S_end never sees a
// protected path that is about to be moved. On linux no sandboxed process is
// left to write after S_end (bwrap's PID namespace is gone with its init). On
// darwin there is no PID namespace: a descendant that left the session with
// setsid / a double fork survives the runner's killpg and can keep writing
// inside W after PostCheck, Quarantine and S_end. Its writes are still bound
// by the profile (no protected path), so it cannot forge T1 evidence — under
// origin=cli it can only make the result stale later, under origin=hook the
// private copy is deleted afterwards — but this is a registered darwin
// residual (docs/p2-design.md §11), not a guarantee of this package.
//
// Matching of protected names is case-sensitive (MatchMandatory, PostCheck).
// On macOS's default case-insensitive APFS the variants (.CLAUDE, .Git,
// .MCP.json) are denied by sandbox-exec's own path matching, not by this
// package; TestDarwinCaseVariantsAreDenied pins that dependency.
//
// Acceptance (P2-SANDBOX): DarwinProfile/BwrapArgs/AncestorPins golden
// tests (testdata/profiles.json; paths with spaces, quotes, parens and
// non-ASCII; BwrapArgs contains --new-session; no /dev/tty* in the darwin
// device allowances); MatchMandatory table (testdata/mandatory.json);
// ResolveGitDirs / ValidateSpec tables (testdata/gitdirs.json); integration
// tests (darwin with sandbox-exec, linux with bwrap; SKIP locally when
// missing, FAIL when CI=true and BALLAST_REQUIRE_BWRAP=1 /
// BALLAST_REQUIRE_SANDBOX_EXEC=1) that run each of the escape attempts in
// docs/p2-design.md §3.4 table "沙箱集成用例" and assert every one fails —
// including opening /dev/tty, ioctl(TIOCSTI) on inherited fds under a pty
// controlling terminal, nested .git/config and modules/x/hooks writes, and
// no residue on the host after creating .claude/settings.local.json — while
// a write inside W and `git add` succeed. v1.4 additions: under deny, a TCP
// connect to a listener the TEST did not start on 127.0.0.1 fails on both
// platforms (darwin: any loopback connect fails; linux: the test's own
// loopback listener still works); under allow, connecting to a unix socket
// outside W, reaching /run and /var/run, and seeing SSH_AUTH_SOCK /
// DOCKER_HOST / DBUS_*_BUS_ADDRESS all fail or are absent, while outbound
// TCP to a local test listener succeeds; the darwin escape channels are
// proven closed by mach-lookup denial ALONE (see DarwinEscapeDenials); and
// a LoaderEnv variable reaches the child but not the bwrap process itself.
// v1.5 additions: the IPC section of every darwin golden (DarwinMachAllow in
// order, none of DarwinMachForbidden, the explicit denials); darwin
// integration cases (v1.5.1: aligned with docs/p2-design.md §3.4):
// `defaults write net.ballast.probe.<rand> k v` into a NAMED preference
// domain leaves ~/Library/Preferences/net.ballast.probe.<rand>.plist absent
// and `defaults read` (outside the sandbox) finds nothing — this case writes
// real user preferences, so it runs when CI=true (mandatory there) and SKIPs
// locally by default, and always ends with `defaults delete`; an
// absolute-path domain may be added as an auxiliary case only, never as the
// assertion (design-time control runs showed it fails even without the
// user-preference-write / mach denials, so it cannot tell whether cfprefsd
// wrote on the test's behalf); python3 ctypes CGEventPost of a key event and
// a pasteboard write fail to reach WindowServer / pboard (the pasteboard
// read back outside the sandbox is unchanged); `launchctl submit -l
// net.ballast.probe.<rand> -- /usr/bin/touch <outside W>` creates nothing
// and no such job is listed outside the sandbox; listeners: under deny,
// binding 0.0.0.0, 127.0.0.1 and [::1] all FAIL (asserted); under loopback
// and allow, a localhost listener works and the outcome of a bind on the
// wildcard / a non-loopback address is only RECORDED in the test log (the
// design-time result is "succeeds", registered as residual risk; NOT a
// failure condition); with Spec.Tree set, a
// write into the tree and into the job temp dir next to it succeeds while
// writes to the real work tree, its .git and the rest of ~/.ballast (the job
// directory itself included) fail. The linux half of every integration
// case runs in scripts/sandbox-docker.sh (Debian 12 container, the three
// --security-opt of docs/p2-design.md §3.4, BALLAST_REQUIRE_BWRAP=1) — a
// mandatory acceptance of the unit that needs Docker only, run as
// `BALLAST_REQUIRE_DOCKER=1 bash scripts/sandbox-docker.sh` (v1.5.2: that one
// variable turns "no Docker" into a failure, CI=true is not needed). The
// darwin IPC cases above that only run when CI=true (named preference
// domain, ctypes CGEventPost / pasteboard, the profile variant without the
// process-exec rules) count as passed for delivery only with the attached
// output of a local macOS run of `CI=true BALLAST_REQUIRE_SANDBOX_EXEC=1 go
// test ./internal/sandbox`, or once P2-CLI's macOS CI has run them (v1.5.2).
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

// RepoMandatoryDeny are patterns, relative to every repo work tree in
// Spec.RepoRoots, that the test command must never write even though they
// lie inside W. Each entry E matches <root>/E AND <root>/<any>/E at ANY
// DEPTH (nested repos, submodule work trees, a gitlink the test adds to the
// index itself): a trailing "/" marks a directory subtree (the entry and
// everything below it); an entry without "/" at the end matches exactly
// that path — for ".git" that is the entry itself, file (gitfile) or
// directory, so it cannot be created, replaced, renamed or removed, while
// the contents of an existing .git directory stay writable except for the
// ".git/…" entries listed here. Ballast's own entries come first; the rest
// mirror the "dangerous files" class of Claude Code's own Bash sandbox,
// which also matches at any depth (code that runs later outside any
// sandbox: git hooks, git config such as core.fsmonitor / core.hooksPath
// that Ballast's own snapshot or the human's next `git status` would
// trigger, a gitfile redirecting git to an attacker-written git dir, editor
// and MCP configs) [checked by P2-SANDBOX against the sandbox-runtime source
// (src/sandbox/sandbox-utils.ts DANGEROUS_FILES, DANGEROUS_DIRECTORIES); the
// entries after ".idea/" were added by that check; the list may only grow].
// MatchMandatory is the single matcher (darwin regex rendering, linux
// ScanMandatory and PostCheck all derive from it).
var RepoMandatoryDeny = []string{
	".ballast/",
	".claude/",
	".git",
	".git/hooks/",
	".git/config",
	".git/config.worktree",
	".gitmodules",
	".mcp.json",
	".vscode/",
	".idea/",
	// Added by P2-SANDBOX after checking the list against the sandbox-runtime
	// source (DANGEROUS_FILES / DANGEROUS_DIRECTORIES): its "dangerous files"
	// that the list above did not yet contain. Shell start-up files and
	// .gitconfig are read by the human's next shell or git run outside any
	// sandbox; .ripgreprc by the next ripgrep (it can name a preprocessor).
	// .claude/commands and .claude/agents are covered by ".claude/".
	".gitconfig",
	".bashrc",
	".bash_profile",
	".zshrc",
	".zprofile",
	".profile",
	".ripgreprc",
}

// GitDirMandatoryDeny are patterns, relative to every git dir / common dir
// in Spec.GitDirs (a worktree's private git dir and the shared common dir,
// whether inside or outside the work tree), that the test command must
// never write. "*" matches exactly one path element, "**" one or more; a
// trailing "/" marks a subtree. modules/** covers submodule git dirs at any
// nesting (their config and hooks run when the human works in the
// submodule or `git status` recurses into it); worktrees/*/… covers the
// other worktrees of the same repository (their config.worktree, and the
// commondir/gitdir links that ResolveGitDirs trusts). Index, objects and
// refs stay writable (`git add`/`commit` inside tests are visible through
// the content snapshot, not a bypass); a ref that happens to be named
// "config" or "hooks" under modules/** is refused too, which is a visible
// test failure, not a security issue.
var GitDirMandatoryDeny = []string{
	"hooks/",
	"config",
	"config.worktree",
	"commondir",
	"modules/**/hooks/",
	"modules/**/config",
	"modules/**/config.worktree",
	"modules/**/commondir",
	"worktrees/*/config.worktree",
	"worktrees/*/commondir",
	"worktrees/*/gitdir",
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
	// GitDirs are the repo's git dir and common dir, exactly as returned by
	// ResolveGitDirs (never derived any other way); GitDirMandatoryDeny is
	// applied under each. Those outside every RepoRoots entry are also
	// added to the writable set by the runner.
	GitDirs []string
	// DenyWrite are protected files or directory subtrees (raw and
	// symlink-resolved forms, tier.ProtectedPath.Path/Resolved).
	DenyWrite []string
	// DenyWriteGlobs are protected paths whose last element contains '*'
	// (e.g. ~/.claude/settings*.json).
	DenyWriteGlobs []string
	// TmpDir is the per-job temp dir (created 0700 by the runner, removed
	// after; linux: not bound, the child gets a private tmpfs /tmp). TMPDIR
	// and the cache variables point into it. For origin=hook (Tree set) the
	// runner puts it at <WorkRoot>/<job>/tmp, next to Tree, NOT under the
	// inherited $TMPDIR: that variable comes from the agent's client, and its
	// directory may be writable from the agent's own sandbox, which would let
	// the agent alter the build products of a running job. TreeTmp is the
	// second (and only other) writable root ValidateSpec accepts inside a
	// DenyWrite directory.
	TmpDir string
	// Network is the child's network policy ("" ⇒ NetworkDeny).
	Network model.NetworkPolicy
	// Home is the real home directory (passwd), used by ValidateSpec.
	Home string
	// Cwd is the child's working directory (must lie inside AllowWrite).
	Cwd string
	// Tree (v1.5) is the private materialized tree of an origin=hook job
	// (runner.Materialize: <WorkRoot>/<job_id>/tree). When non-empty:
	// RepoRoots == [Tree], GitDirs is empty, AllowWrite == [Tree] ∪ the
	// expanded runner_allow_write, Cwd lies inside Tree, and Tree is the
	// ONLY W entry allowed to lie inside a DenyWrite directory (the
	// ~/.ballast subtree): ValidateSpec checks Tree == WorkRoot/<one
	// element>/tree with no symlink on the way. Wrapped.Env then also sets
	// GIT_CEILING_DIRECTORIES=<dir(Tree)> so git inside the test never
	// discovers a repository above the copy (the copy has no .git; tests
	// that need git metadata fail visibly). Empty for origin=cli.
	Tree string
	// WorkRoot (v1.5) is layout.WorkDir; required when Tree is set.
	WorkRoot string
	// LoaderEnv (v1.4) are the runner environment's dynamic-loader
	// variables ("K=V", K matching LD_*) that the TEST may legitimately
	// need (e.g. LD_LIBRARY_PATH). They must never reach the backend
	// executable itself — a non-setuid bwrap would load LD_PRELOAD code
	// before the namespaces exist, i.e. outside the sandbox — so the
	// runner removes LD_* / DYLD_* from the backend environment (Wrapped.Env)
	// and linux re-sets these inside the sandbox with --setenv. darwin ignores LoaderEnv:
	// /usr/bin/sandbox-exec is a platform binary and dyld drops DYLD_*
	// for it, so DYLD_* never reach darwin tests.
	LoaderEnv []string
}

// Wrapped is the result of Wrap.
type Wrapped struct {
	Kind Kind
	Argv []string
	// Env are environment changes for the child: "K=V" sets K, a bare "K"
	// (no '=') unsets it. The runner forms the BACKEND process environment
	// (inherited by the child) as: its own environment minus every LD_* and
	// DYLD_* variable (v1.4; LD_* reach the linux child via
	// Spec.LoaderEnv), then Env applied. Under EVERY network policy (v1.4)
	// Env sets TMPDIR, unsets SSH_AUTH_SOCK, DOCKER_HOST,
	// DBUS_SESSION_BUS_ADDRESS and DBUS_SYSTEM_BUS_ADDRESS, and includes
	// CacheEnv(...). Other variables (GIT_* included) pass through: inside
	// the sandbox they are contained.
	Env []string
	// MissingProtected are explicit protected paths (Spec.DenyWrite and
	// glob directories) inside W that did not exist at wrap time and are
	// only post-checked (linux).
	MissingProtected []string
	// Baseline are the mandatory matches (ScanMandatory) that existed at
	// wrap time, absolute and sorted. linux ro-binds each of them; PostCheck
	// reports any match that is not in Baseline. Computed on darwin too
	// (PostCheck runs there as defence in depth).
	Baseline []string
	// NewSession is always true for a wrapped command: the caller must
	// start Argv with SysProcAttr.Setsid, stdin /dev/null and stdout/stderr
	// a file or pipe (package doc "Terminal"). KindNone leaves it false
	// (unwrapped origin=cli runs keep the current behaviour).
	NewSession bool

	// ids fingerprints (device, inode) every Baseline path at wrap time.
	// PostCheck uses it to notice a protected entry that was REPLACED at the
	// same path (the child renames a non-pinned ancestor away and recreates
	// the directory: the recreated entry has a Baseline path but another
	// inode). Unexported: callers pass the Wrapped value they got from Wrap
	// to PostCheck unchanged, in the same process; a hand-built or
	// deserialized Wrapped has no fingerprints and PostCheck then compares
	// paths only (that replacement attack is no longer detected).
	ids map[string]fileKey
}

// CacheVars are the build-cache variables CacheEnv redirects, in order.
// Append-only (P2-SANDBOX may add entries, never remove).
var CacheVars = []string{
	"GOCACHE",           // <TmpDir>/cache/go-build
	"XDG_CACHE_HOME",    // <TmpDir>/cache/xdg
	"npm_config_cache",  // <TmpDir>/cache/npm
	"YARN_CACHE_FOLDER", // <TmpDir>/cache/yarn
	"PIP_CACHE_DIR",     // <TmpDir>/cache/pip
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

// MaxMandatoryMatches bounds ScanMandatory (each match is one bwrap mount).
const MaxMandatoryMatches = 4096

// QuarantineEntry is one path moved (or removed) by Quarantine.
type QuarantineEntry struct {
	Path    string // original absolute path
	MovedTo string // path under the quarantine dir; "" when removed instead
	Err     string // non-empty when the path could be neither moved nor removed
}

// DarwinMachAllow (v1.5) is the mach-lookup allowlist of the darwin profile,
// rendered in this order after (deny mach-lookup). Minimal set for the test
// runners of go / node / python / java / dotnet, modelled on the macOS
// allowlist approach of Claude Code's sandbox-runtime (deny by default, name
// the services) and the Chromium / Codex seatbelt base policies 【静态依据待核:
// P2-SANDBOX checks every name against the sandbox-runtime source and runs a
// minimal test project per runtime (go test, npm test, pytest, mvn -q test,
// dotnet test) under the profile; a missing service is added only with that
// evidence recorded in docs/p2-design.md §3.4 and an argument that it is
// neither a code-execution nor a write proxy】. Append-only; never one of
// DarwinMachForbidden.
var DarwinMachAllow = []string{
	"com.apple.system.opendirectoryd.libinfo",    // getpwuid/getgrgid (every runtime: os/user, pwd, user.home)
	"com.apple.system.opendirectoryd.membership", // group membership (getgrouplist, mbr_*)
	"com.apple.system.notification_center",       // notify(3): libinfo cache invalidation, time zone changes
	"com.apple.system.logger",                    // asl (older runtimes)
	"com.apple.logd",                             // os_log (java, dotnet)
	"com.apple.diagnosticd",                      // os_log activity streaming
	"com.apple.cfprefsd.daemon",                  // CFPreferences READS; writes are refused by (deny user-preference-write)
	"com.apple.cfprefsd.agent",                   // idem (per-user agent)
	"com.apple.trustd.agent",                     // TLS chain evaluation (go crypto/x509, Security.framework) under allow
	"com.apple.SystemConfiguration.configd",      // proxy / reachability reads (java, dotnet, python urllib)
	"com.apple.FSEvents",                         // file watchers (node, dotnet)
}

// DarwinMachForbidden (v1.5) are mach service names (exact, or prefixes when
// ending in '.') that must never appear in DarwinMachAllow: each one lets a
// sandboxed process ask a process OUTSIDE the sandbox to run code, inject
// input or exfiltrate (golden-asserted). The list is final for each entry: no
// measurement moves an entry from here into DarwinMachAllow (that would be a
// contract change argued in docs/p2-design.md, not an implementation
// decision); a service that is merely "not needed yet" belongs in neither
// list (v1.5.2).
var DarwinMachForbidden = []string{
	"com.apple.coreservices.launchservicesd", // LaunchServices (open -a)
	"com.apple.lsd.",                         // LaunchServices daemons
	"com.apple.coreservices.appleevents",     // AppleEvents (osascript)
	"com.apple.windowserver.",                // CGEventPost / window server
	"com.apple.pasteboard.",                  // pboard
	"com.apple.tccd",                         // privacy database
	"com.apple.SecurityServer",               // securityd / keychain: a credential proxy, NEVER allowed (v1.5.2); TLS chain evaluation uses com.apple.trustd.agent, a runtime that needs securityd fails visibly
	"com.apple.dock.",                        // Dock / Spaces
	"com.apple.xpc.launchd.",                 // launchd domains
}
