package sys

import "errors"

// P2 contract (architecture v2.0, docs/p2-design.md §3.2 "重定向锁 · 家目录").
// Owner: unit P2-TIER (the passwd*.go files only; the rest of package sys
// is unchanged in P2). Wired by P2-CLI through tier.LockedHome.

// ErrNoPasswdEntry: the passwd database reachable by this build has no
// entry for the uid (typical for sssd/LDAP users on a CGO_ENABLED=0 linux
// build, which reads only /etc/passwd). Callers on a locked host may then
// fall back to the org.json home_root rule (tier.LockedHome); nothing else
// may fall back to $HOME.
var ErrNoPasswdEntry = errors.New("sys: no passwd entry for uid")

// ErrPasswdUnsupported: the platform has no passwd database (windows).
// tier.Locked is always false there, so production never calls LookupHome
// on such a platform.
var ErrPasswdUnsupported = errors.New("sys: passwd lookup unsupported on this platform")

// Euid returns the effective uid of the process (-1 on windows).
func Euid() int { return euid() }

// LookupHome returns the home directory the passwd database records for
// uid. It NEVER consults $HOME, $USER or any other environment variable.
//
// Frozen per-platform behaviour:
//   - darwin: os/user.LookupId(strconv.Itoa(uid)). With CGO_ENABLED=0 the
//     standard library still calls libSystem getpwuid_r through its
//     syscall trampolines, so Open Directory (local, mobile and directory
//     accounts) is consulted. Measured on a dev Mac (local account,
//     CGO_ENABLED=0, one fresh process per call): p50 ≈ 0.9 ms, p95 ≈ 1.3
//     ms; second call in the same process ≈ 0. Directory-bound accounts
//     whose lookup is not cached locally are 【未核验】.
//   - linux: os/user.LookupId as well. A CGO_ENABLED=0 build (the release
//     default) parses /etc/passwd only; a cgo build goes through NSS
//     (sssd/LDAP). "unknown user id" maps to ErrNoPasswdEntry.
//   - other: ErrPasswdUnsupported.
//
// os/user.Current is FORBIDDEN here: in pure-Go mode it falls back to
// $HOME/$USER when the lookup fails, which is exactly the agent-controlled
// input the redirect lock exists to ignore. A non-absolute or empty home
// field is reported as ErrNoPasswdEntry. LookupHome does not cache; the
// hook calls it at most once per process.
func LookupHome(uid int) (string, error) { return lookupHome(uid) }
