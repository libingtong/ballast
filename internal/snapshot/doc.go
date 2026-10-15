// Package snapshot (unit U8) locates git work trees and computes the
// content snapshots S (work tree) and C (to-be-committed) natively, without
// spawning git (plan B3, v0.4 §2; M0c-PERF). Prototype:
// proto/m0c/nativetree (C only, v2/v3).
//
// Hot path: yes, EXCEPT gitfallback.go — the only file in the hot-path
// packages allowed to import os/exec; it runs only under a deadline.
// Imports: model, ports, sys, doublestar, stdlib (crypto/sha1, crypto/sha256).
//
// Repo discovery (Locator, no exec): walk up from cwd to the first ".git"
// (dir ⇒ GitDir; file "gitdir: <p>" ⇒ worktree/submodule GitDir);
// CommonDir from "<GitDir>/commondir" when present; realpath every
// component; Inst = RepoInst(Toplevel, CommonDir); HeadRef from HEAD;
// ObjectFormat from config extensions.objectformat and RemoteHash from the
// first `url` under `[remote "origin"]` (one cheap line scan of
// <CommonDir>/config; includes are not followed). Locate never reads
// GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE from the environment; IndexPath
// is always <GitDir>/index (only evidence.Check substitutes it).
//
// Exempt filter: Filter compiled ONCE from project.json "exempt" (doublestar,
// repo-relative, '/'-separated, case-sensitive like git pathspecs) and used
// by BOTH S and C. An entry is removed when Filter.Exempt(path) is true.
//
// C (TreeC) by CommitForm.Kind:
//
//	plain        F(index)
//	all          F(index with every tracked entry refreshed from the work tree, i.e. add -u)
//	paths        F(index with entries under the given pathspecs refreshed)  [--only/-i approximated]
//	unparseable  ⇒ error (caller ⇒ unknown)
//
// S (TreeS) = F(index with every tracked entry refreshed) — identical to C(all).
// Refresh of one entry: lstat; missing ⇒ entry removed (deletion); stat
// (mtime, ctime, size, ino, dev, mode bits) equal to the index entry AND not
// racy (entry mtime < index file mtime) ⇒ keep the index sha; otherwise read
// the content (symlink ⇒ link target bytes, mode 120000) and hash
// "blob <len>\0<content>"; executable bit honoured only when core.filemode
// is true (default true; false on Windows/when configured).
// F builds tree objects in memory (sorted as git: by name with '/' suffix
// for trees) and returns the root tree id; nothing is written to .git.
//
// Fallback to git (ErrFallback ⇒ gitfallback.go) when: index v4 with
// unsupported features, split index ("link"), sparse index ("sdir" or
// mode 040000 entries), unmerged entries (stage ≠ 0), skip-worktree or
// intent-to-add flags, sha256 repos, any clean/smudge/eol conversion
// (.gitattributes lines with filter=, text, eol=, or core.autocrlf set to
// true/input, core.eol). Fallback = the v0.3 algorithm (copy index →
// GIT_INDEX_FILE=copy git add -u [-- paths] → git rm --cached -r -q
// --ignore-unmatch -- ':(glob)<exempt>'... → git write-tree), bounded by
// Options.Deadline (hook: remaining watchdog budget; runner: none).
// Every fallback git child runs with an environment whose inherited GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY,
// GIT_ALTERNATE_OBJECT_DIRECTORIES, GIT_COMMON_DIR and GIT_PREFIX are
// removed and then set explicitly (GIT_INDEX_FILE = the temporary copy),
// because `verify --check` runs inside git's pre-commit environment.
// Deadline exceeded ⇒ model.ErrDeadline (caller ⇒ unknown + audit diag).
//
// Acceptance (U8): Locate fills RemoteHash (fixture config with and without
// origin) and ignores GIT_INDEX_FILE in the environment; S/C consistency
// — for the same content S == C(all) == C(plain after `git add -u`);
// bit-for-bit equality with `git add -u && git write-tree` (with exempt
// removed) on fixture repos covering
// symlinks, exec bits, deletions, racy entries, nested dirs, v2/v3/v4
// indexes; native C on a 10k-file index < 5ms; fallback honours deadline.
package snapshot
