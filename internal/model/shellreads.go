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
	if cl == nil {
		return nil
	}
	var out []string
	var seen map[string]struct{}
	changesDir := false
	for i := range cl.Nodes {
		n := &cl.Nodes[i]
		if !changesDir && len(n.Argv) > 0 && !n.Argv[0].Dynamic {
			cmd := n.Argv[0].Lit
			if cl.Shell == ShellPowerShell {
				lower := []byte(cmd)
				for j, c := range lower {
					if c >= 'A' && c <= 'Z' {
						lower[j] = c + 'a' - 'A'
					}
				}
				switch string(lower) {
				case "set-location", "push-location", "pop-location", "cd", "sl", "chdir", "pushd", "popd":
					changesDir = true
				}
			} else if cmd == "cd" || cmd == "pushd" || cmd == "popd" {
				changesDir = true
			}
		}
		for _, r := range n.Reads {
			if r.Dynamic || r.Path == "" {
				continue
			}
			if _, dup := seen[r.Path]; dup {
				continue
			}
			if seen == nil {
				seen = make(map[string]struct{})
			}
			seen[r.Path] = struct{}{}
			out = append(out, r.Path)
		}
	}
	if !changesDir {
		return out
	}
	abs := out[:0]
	for _, p := range out {
		if IsAbsShellPath(p) {
			abs = append(abs, p)
		}
	}
	if len(abs) == 0 {
		return nil
	}
	return abs
}

// IsAbsShellPath reports whether a literal shell path is absolute on any
// supported platform, without touching the filesystem: it starts with "/"
// (POSIX, Git Bash "/c/..."), "~/" or "~\" or equals "~", starts with "\\"
// or "//" (UNC), or matches ^[A-Za-z]:[\\/] (drive). "C:foo" (drive-relative)
// is NOT absolute.
//
// Implemented by U2.
func IsAbsShellPath(p string) bool {
	switch {
	case p == "":
		return false
	case p[0] == '/' || p[0] == '~' && (len(p) == 1 || p[1] == '/' || p[1] == '\\'):
		return true
	case len(p) >= 2 && p[0] == '\\' && p[1] == '\\':
		return true
	case len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'):
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return false
}
