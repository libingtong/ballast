package runner

import "github.com/libingtong/ballast/internal/model"

// P2 contract (architecture v2.0, docs/p2-design.md §3.3–§3.4): starting
// jobs on behalf of the hook (T1 path). Bodies owned by unit P2-RUNNER.
//
// The hook cannot import this package; it spawns
// model.HookStartArgv(self, req) through ports.Spawner, and the CLI
// (unit P2-CLI) turns the child's argv back into a request with
// model.ParseHookStartArgv and calls StartRequest.

// StartRequest resolves repo and project.json from req.Cwd, writes the
// model.VerifyRequestFile, supersedes running jobs of the same names, and
// starts one job per name exactly like Start, except that the first job's id
// is req.RequestID and every job is recorded with Origin "hook" (which makes
// the OS sandbox mandatory, docs/p2-design.md §3.4). Before deciding each
// record's tier it applies tier.RepoSandboxWeakened to the job repo AND to
// req.ProjectDir when non-empty and different (v1.4; the session's project
// layer can live in a monorepo subdirectory or a parent of the repo); any
// entry ⇒ record tier T0, tier_missing ["lower_sandbox_weakened"], audit diag
// "tier_mismatch". v1.5: every job it starts runs in a private materialized
// tree (Materialize, tree.go), never in the real work tree: S_start is
// Tree.SStart, the sandbox Spec has Tree = Tree.Dir as its only repo root (W =
// the copy ∪ TmpDir ∪ runner_allow_write; neither the real work tree nor its
// git dir / common dir is writable), S_end is TreeSEnd, and the record carries
// Workspace "copy" and InputsHash. v1.5.2: what StartRequest reads from the
// work tree's project.json (entry, exempt, sha256 of the bytes) is only a
// PIN written to the sidecar; the --run-job child passes it to Materialize
// as a PinnedEntry, which refuses to run (infra project_json_changed /
// project_json_untracked) unless the .ballast/project.json blob of S_start
// hashes to the pinned value and decodes to the same entry — the read here
// and S_start are two different reads of an agent-writable file. Every git child it runs outside the sandbox (snapshots)
// gets a sys.Env filtered by sandbox.HostEnv; the detached --run-job child
// and the sandbox backend get every LD_* / DYLD_* variable removed (v1.4,
// docs/p2-design.md §3.4 "沙箱外进程的环境"). On a config error it
// still writes the request file with Error set (so
// `verify --status <request_id>` can explain) and returns ErrConfig.
func StartRequest(opt Options, req model.HookStartRequest) ([]Started, error) {
	panic("unimplemented: runner.StartRequest (unit P2-RUNNER)")
}

// RequestJobStatus is one job line of RequestStatus.
type RequestJobStatus struct {
	Name   string
	JobID  string
	Record *model.VerifyRecord // nil while running or when the job file is gone
	// Outcome is Record.Outcome, or "running" while the job is live, or
	// "infra" when neither a record nor a live job exists.
	Outcome string
}

// RequestStatusResult is the aggregate of a hook-started request.
type RequestStatusResult struct {
	RequestID string
	Error     string // VerifyRequestFile.Error
	Jobs      []RequestJobStatus
	// Worst is the worst outcome over Jobs in the frozen order
	// fail > timeout > signal > infra > running > pass (superseded counts
	// as infra); a request file with Error and no jobs ⇒ "infra".
	Worst string
	// ExitCode is StatusExitCode semantics applied to Worst (single job:
	// identical to that job's `verify --status` exit code).
	ExitCode int
}

// RequestStatus implements the `ballast verify --status <id>` fallback when
// no job has that id: it locates <RequestsDir>/<repo_inst>/<id>.json
// (searching every repo_inst directory when repoInst is ""), reads each
// job's record or live state, and aggregates them. Owned by P2-RUNNER; the
// CLI (P2-CLI) only prints the result line by line and exits with
// ExitCode. Not found ⇒ ErrNotFound-style error chosen by P2-RUNNER; the
// exit code is documented in architecture §7 by P2-CLI (v2.0.4: P2-CLI owns
// the §7 and §3.1 rows it adds, docs/p2-design.md §9).
func RequestStatus(opt Options, repoInst, requestID string) (RequestStatusResult, error) {
	panic("unimplemented: runner.RequestStatus (unit P2-RUNNER)")
}
