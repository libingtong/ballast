package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// P2 contract (architecture v2.0, docs/p2-design.md §3.3): recognition of the
// canonical `ballast verify <name>...` command for hook-started verify under
// T1, and the request file that maps the hook's pre-allocated id to jobs.
//
// ParseVerifyRequest's body is owned by unit P2-HOOK (like shellreads.go is
// owned by U2); the signature and the rules below are frozen.

// MaxVerifyRequestNames caps the names in one canonical verify command.
const MaxVerifyRequestNames = 16

// VerifyRequest is a recognized canonical `ballast verify [name...]`.
type VerifyRequest struct {
	// Names in command order, de-duplicated; empty ⇒ every required entry
	// of project.json (resolved by the runner, never by the hook).
	Names []string `json:"names,omitempty"`
}

// ParseVerifyRequest reports whether cl is EXACTLY one canonical verify
// command. Frozen rules (all must hold, otherwise ok=false and the command
// is evaluated like any other Bash/PowerShell command):
//
//   - cl != nil, !cl.Unknown, len(cl.Nodes) == 1, len(cl.Pipes) == 0;
//   - the node: Depth 0, !Async, !InSubst, Loop == nil, no Redirects, no
//     Wrappers, !Sudo, Params empty (PowerShell), every Argv word literal
//     (not Dynamic);
//   - basename(Argv[0]) is "ballast" or "ballast.exe" (any directory: the
//     hook always starts its own Self, never the agent's argv[0]);
//   - Argv[1] == "verify";
//   - every further word matches ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ (so no
//     flags: `--status`, `--foreground`, `--check` stay ordinary commands),
//     at most MaxVerifyRequestNames of them.
//
// tool.RunInBackground does not matter (the hook denies either way).
func ParseVerifyRequest(cl *CommandList) (VerifyRequest, bool) {
	if cl == nil || cl.Unknown || len(cl.Nodes) != 1 || len(cl.Pipes) != 0 {
		return VerifyRequest{}, false
	}
	n := &cl.Nodes[0]
	if n.Depth != 0 || n.Async || n.InSubst || n.Loop != nil || len(n.Redirects) != 0 ||
		len(n.Wrappers) != 0 || n.Sudo || len(n.Params) != 0 {
		return VerifyRequest{}, false
	}
	if len(n.Argv) < 2 || len(n.Argv) > 2+MaxVerifyRequestNames {
		return VerifyRequest{}, false
	}
	for _, w := range n.Argv {
		if w.Dynamic {
			return VerifyRequest{}, false
		}
	}
	if b := lastPathElem(n.Argv[0].Lit); b != "ballast" && b != "ballast.exe" {
		return VerifyRequest{}, false
	}
	if n.Argv[1].Lit != "verify" {
		return VerifyRequest{}, false
	}
	var names []string
	seen := make(map[string]struct{}, len(n.Argv))
	for _, w := range n.Argv[2:] {
		if !validVerifyName(w.Lit) {
			return VerifyRequest{}, false
		}
		if _, dup := seen[w.Lit]; dup {
			continue
		}
		seen[w.Lit] = struct{}{}
		names = append(names, w.Lit)
	}
	return VerifyRequest{Names: names}, true
}

// lastPathElem is the basename of p for both '/' and '\\' separators (the
// command word is the agent's text, not necessarily in the host's syntax).
func lastPathElem(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// validVerifyName reports whether s matches ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$.
func validVerifyName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// validRequestID reports whether id has the job-id format: UTC
// "20060102T150405" + "-" + 6 lowercase hex digits.
func validRequestID(id string) bool {
	if len(id) != len("20060102T150405-")+6 || id[8] != 'T' || id[15] != '-' {
		return false
	}
	for i := 0; i < 15; i++ {
		if i == 8 {
			continue
		}
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	for i := 16; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// VerifyOrigin says who started a verify job (VerifyRecord.Origin).
type VerifyOrigin string

const (
	OriginCLI  VerifyOrigin = "cli"  // `ballast verify` run by the agent or a human (T0 path)
	OriginHook VerifyOrigin = "hook" // started by the hook on a denied canonical command (T1 path)
)

// RequestedJob is one job of a hook-started request.
type RequestedJob struct {
	Name  string `json:"name"`
	JobID string `json:"job_id"`
}

// VerifyRequestFile is <Layout.RequestsDir>/<repo_inst>/<request_id>.json
// (~/.ballast/requests/…; atomic write, 0600; repo_inst "norepo-…" when the
// cwd is not in a repo). It lives outside jobs/ so the evidence store's job
// listing and GC never see it; `ballast gc` removes request files older than
// 30 days. The request id has the job-id format and is pre-allocated by the
// hook, so the deny reason can name it before the runner starts. The first
// (or only) job reuses the request id as its job id; others get fresh ids.
// `ballast verify --status <request_id>` falls back to this file when no job
// has that id.
type VerifyRequestFile struct {
	V         int            `json:"v"` // 1
	RequestID string         `json:"request_id"`
	Origin    VerifyOrigin   `json:"origin"`
	Client    ClientID       `json:"client,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	Jobs      []RequestedJob `json:"jobs"`
	Error     string         `json:"error,omitempty"` // config/infra error that prevented any job
}

// HookStartRequest is what the hook hands to the detached
// `ballast verify --hook-start …` child when it denies a canonical verify
// command under T1 (docs/p2-design.md §3.3).
type HookStartRequest struct {
	// RequestID was pre-allocated by the hook (job-id format: UTC
	// "20060102T150405" + "-" + 6 hex) and already shown in the deny reason.
	RequestID string
	// Cwd is the event cwd (the detached child must not rely on its own cwd).
	Cwd       string
	Names     []string // empty ⇒ every required entry
	Client    ClientID
	SessionID string
	AgentID   string
	// ProjectDir (v1.4) is the session's project directory as Claude Code
	// itself sees it — the hook's $CLAUDE_PROJECT_DIR when absolute, "" when
	// unset — i.e. the directory whose .claude/settings*.json Claude loads
	// as the project and local layers. It can differ from the job repo
	// (Claude started in a monorepo subdirectory, or in a parent of the
	// repo). The runner applies tier.RepoSandboxWeakened to it IN ADDITION
	// to the job repo (docs/p2-design.md §3.4 "runner 自判 T1"); it never
	// widens the sandbox's writable set.
	ProjectDir string
}

// HookStartArgv renders the frozen child argv (self = absolute path of the
// running ballast binary):
//
//	<self> verify --hook-start --request <id> --cwd <cwd> --client <c>
//	       [--session <sid>] [--agent <aid>] [--project-dir <dir>] -- [names...]
//
// Pure. ParseHookStartArgv(HookStartArgv(s, r)[1:]) == r for every r with a
// valid RequestID (round-trip test, incl. cwd with spaces and leading '-').
func HookStartArgv(self string, req HookStartRequest) []string {
	argv := []string{self, "verify", "--hook-start",
		"--request", req.RequestID, "--cwd", req.Cwd, "--client", string(req.Client)}
	if req.SessionID != "" {
		argv = append(argv, "--session", req.SessionID)
	}
	if req.AgentID != "" {
		argv = append(argv, "--agent", req.AgentID)
	}
	if req.ProjectDir != "" {
		argv = append(argv, "--project-dir", req.ProjectDir)
	}
	argv = append(argv, "--")
	return append(argv, req.Names...)
}

// ParseHookStartArgv parses the arguments after the binary path (starting
// with "verify"). It rejects a RequestID that is not in job-id format, a
// relative Cwd or ProjectDir and names that ParseVerifyRequest would reject.
func ParseHookStartArgv(args []string) (HookStartRequest, error) {
	var req HookStartRequest
	if len(args) < 2 || args[0] != "verify" || args[1] != "--hook-start" {
		return req, errors.New("hook-start: want `verify --hook-start …`")
	}
	have := map[string]bool{}
	i := 2
	for ; i < len(args); i++ {
		flag := args[i]
		if flag == "--" {
			i++
			break
		}
		switch flag {
		case "--request", "--cwd", "--client", "--session", "--agent", "--project-dir":
		default:
			return HookStartRequest{}, fmt.Errorf("hook-start: unknown argument %q", flag)
		}
		// The value is the next token verbatim, even when it starts with '-'.
		if i+1 >= len(args) {
			return HookStartRequest{}, fmt.Errorf("hook-start: flag %s needs a value", flag)
		}
		if have[flag] {
			return HookStartRequest{}, fmt.Errorf("hook-start: flag %s given twice", flag)
		}
		have[flag] = true
		i++
		val := args[i]
		switch flag {
		case "--request":
			req.RequestID = val
		case "--cwd":
			req.Cwd = val
		case "--client":
			req.Client = ClientID(val)
		case "--session":
			req.SessionID = val
		case "--agent":
			req.AgentID = val
		case "--project-dir":
			req.ProjectDir = val
		}
	}
	for _, f := range []string{"--request", "--cwd", "--client"} {
		if !have[f] {
			return HookStartRequest{}, fmt.Errorf("hook-start: missing required flag %s", f)
		}
	}
	if !validRequestID(req.RequestID) {
		return HookStartRequest{}, fmt.Errorf("hook-start: request id %q is not in job-id format", req.RequestID)
	}
	if !filepath.IsAbs(req.Cwd) {
		return HookStartRequest{}, errors.New("hook-start: --cwd must be an absolute path")
	}
	if have["--project-dir"] && !filepath.IsAbs(req.ProjectDir) {
		return HookStartRequest{}, errors.New("hook-start: --project-dir must be an absolute path")
	}
	rest := args[i:]
	if len(rest) > MaxVerifyRequestNames {
		return HookStartRequest{}, fmt.Errorf("hook-start: more than %d names", MaxVerifyRequestNames)
	}
	for _, n := range rest {
		if !validVerifyName(n) {
			return HookStartRequest{}, fmt.Errorf("hook-start: invalid verify name %q", n)
		}
	}
	if len(rest) > 0 {
		req.Names = append([]string(nil), rest...)
	}
	return req, nil
}
