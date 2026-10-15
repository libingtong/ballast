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
// racy ⇒ keep the index sha; otherwise read
// the content (symlink ⇒ link target bytes, mode 120000) and hash
// "blob <len>\0<content>"; executable bit honoured only when core.filemode
// is true (default true; false on Windows/when configured).
// Racy (contract v1.3, both halves of git's protection): the entry mtime is
// not older than the index file mtime (same second or later), OR the
// entry is "smudged" (size 0 in the index while its id is not the empty
// blob; git's ce_match_stat_basic rule).
// F builds tree objects in memory (sorted as git: by name with '/' suffix
// for trees) and returns the root tree id; nothing is written to .git.
//
// Fallback to git (ErrFallback ⇒ gitfallback.go) when: index v4 with
// unsupported features, split index ("link"), sparse index ("sdir" or
// mode 040000 entries), unmerged entries (stage ≠ 0), skip-worktree or
// intent-to-add flags, sha256 repos, any clean/smudge/eol conversion
// (.gitattributes lines with filter=, text, eol=, or core.autocrlf set to
// true/input, core.eol; refined in the implementation notes), and for
// gitlinks (contract v1.3): a checked-out submodule whose HEAD cannot be
// resolved natively (unborn, reftable, sha256, "commondir" layout, unusual
// ref names) or whose git directory lacks objects/ or refs/ (not a
// repository for git, which then fails itself ⇒ caller unknown), a gitlink
// replaced by a non-directory, and a tracked file or symlink replaced by a
// directory holding ".git". Fallback = the v0.3 algorithm (copy index →
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
//
// Implementation notes (U8a; additive, the contract above stays in force):
//
//   - Exempt matching is a port of git's wildmatch.c plus git's pathspec
//     rules (literal name or leading directory, then wildmatch on the part
//     after the literal head), i.e. exactly the semantics of the pathspec
//     ':(glob)<glob>' that the fallback hands to `git rm --cached`; it is
//     differentially tested against `git ls-files`. doublestar is not used
//     because its "a/**" and directory semantics differ from git's.
//   - The fallback runs `git rm --cached -r -q -f --ignore-unmatch`: without
//     -f git refuses to drop an exempt file whose staged content differs
//     from both HEAD and the work tree and the whole snapshot would fail.
//   - Entries are filtered (exempt) before they are refreshed, so exempt
//     files are never read; the result is identical because add -u followed
//     by rm --cached commutes with filtering.
//   - Racy test, both halves of git's protection: an index entry whose
//     mtime is in the same second as (or later than) the index file mtime is
//     rehashed, and so is a size-0 entry whose id is not the empty blob
//     (git's smudged-entry rule in ce_match_stat_basic; it is the only
//     protection left once the index has been rewritten).
//     Sub-second fields are compared when the index carries them; dev is
//     compared as the contract says; uid/gid are not (chown changes ctime).
//   - assume-unchanged entries are left alone (git add -u does so, even for
//     deleted files); a path below a symlinked or missing directory counts
//     as removed, like git's has_symlink_leading_path.
//   - Conversions: only content that contains CRLF can change under
//     core.autocrlf, core.eol and the text/eol/crlf attributes, so a dirty
//     file without CRLF is hashed natively even in such repositories; a
//     dirty file that has CRLF, any filter/ident/working-tree-encoding
//     attribute on the way to it, core.safecrlf=true, core.symlinks=false
//     with symlinks involved, or a configuration the scanner cannot
//     evaluate (conditional include that sets a relevant key, `git -c`
//     style environment overrides) falls back. NeedsFallback keeps the
//     strict contract meaning ("any such setting present"); the plain C
//     form never reads the work tree and ignores all of this.
//   - Options.Env (default sys.OSEnv; accepted as a contract addition in
//     v1.3) locates the global/system git config and attributes (HOME,
//     XDG_CONFIG_HOME, GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM,
//     GIT_CONFIG_NOSYSTEM) and the fallback's TMPDIR.
//   - Gitlinks (submodules; mode 160000) are handled like git's add -u: a
//     missing directory is a removal, a directory without ".git"
//     (uninitialised submodule) keeps the recorded commit, and a checked-out
//     submodule records its HEAD, resolved natively (".git" directory or
//     gitfile; detached id, or a loose ref or packed-refs entry). A HEAD
//     that cannot be resolved that way (unborn, reftable, sha256, a
//     "commondir" layout, unusual ref names), a git directory without
//     objects/ and refs/ (git's is_git_directory), a gitlink replaced by a
//     non-directory and a tracked file or symlink replaced by a directory
//     holding ".git" (git records the nested repository's HEAD as a gitlink)
//     fall back; v1.3 lifted these into the contract list above.
//   - TreeC paths form: pathspecs are relative to CommitForm.Dir when set
//     (else the repo root), absolute paths inside the repo are accepted
//     (symlinks in them and in Dir are resolved), "--only" is approximated
//     by "-i" as in the contract, and magic pathspecs (":(glob)...",
//     ":!...") are left to the fallback. The fallback runs `git add -u` in
//     Dir, so magic pathspecs mean what they would to `git commit` there.
//   - Killing a timed-out fallback git takes down its whole process tree:
//     the process group on unix and a Job Object on Windows. On Windows this
//     is best effort: the job is created right after the child starts (a
//     helper spawned in between escapes), and when it cannot be created only
//     the git process itself is killed.
package snapshot
