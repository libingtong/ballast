package model

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
	panic("unimplemented: model.ParseVerifyRequest (unit P2-HOOK)")
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
}

// HookStartArgv renders the frozen child argv (self = absolute path of the
// running ballast binary):
//
//	<self> verify --hook-start --request <id> --cwd <cwd> --client <c>
//	       [--session <sid>] [--agent <aid>] -- [names...]
//
// Pure. ParseHookStartArgv(HookStartArgv(s, r)[1:]) == r for every r with a
// valid RequestID (round-trip test, incl. cwd with spaces and leading '-').
func HookStartArgv(self string, req HookStartRequest) []string {
	panic("unimplemented: model.HookStartArgv (unit P2-HOOK)")
}

// ParseHookStartArgv parses the arguments after the binary path (starting
// with "verify"). It rejects a RequestID that is not in job-id format, a
// relative Cwd and names that ParseVerifyRequest would reject.
func ParseHookStartArgv(args []string) (HookStartRequest, error) {
	panic("unimplemented: model.ParseHookStartArgv (unit P2-HOOK)")
}
