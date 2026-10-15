package model

// ShellReadPaths returns the raw (not normalized) paths that a successfully
// executed shell command adds to the agent's read set. It is the single
// definition shared by internal/hook (live PostToolUse) and internal/replay
// (offline reconstruction); neither may re-implement the rule.
//
// Rule (frozen, docs/dsl-semantics.md §6/§14, internal/hook/doc.go):
//   - nil cl ⇒ nil.
//   - Every r in node.Reads of every node with !r.Dynamic and r.Path != ""
//     is a candidate, in node order then Reads order; duplicates removed
//     keeping the first occurrence.
//   - If any node's Argv[0] (non-dynamic; compared case-sensitively for bash,
//     case-insensitively for PowerShell) is cd/pushd/popd (bash) or
//     set-location/push-location/pop-location/cd/sl/chdir/pushd/popd
//     (PowerShell), only candidates for which IsAbsShellPath is true are
//     returned.
//
// Implemented by U2 (owner of the read-extraction tables); this file is the
// only file under internal/model U2 may edit, plus shellreads_test.go.
func ShellReadPaths(cl *CommandList) []string {
	panic("unimplemented: model.ShellReadPaths (U2)")
}

// IsAbsShellPath reports whether a literal shell path is absolute on any
// supported platform, without touching the filesystem: it starts with "/"
// (POSIX, Git Bash "/c/..."), "~/" or "~\" or equals "~", starts with "\\"
// or "//" (UNC), or matches ^[A-Za-z]:[\\/] (drive). "C:foo" (drive-relative)
// is NOT absolute.
//
// Implemented by U2.
func IsAbsShellPath(p string) bool {
	panic("unimplemented: model.IsAbsShellPath (U2)")
}
