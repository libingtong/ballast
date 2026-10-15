package model

import "time"

// RepoInfo locates a git work tree WITHOUT running git (snapshot.Discover).
type RepoInfo struct {
	Toplevel  string `json:"toplevel"`   // realpath of the work tree root (OS form)
	GitDir    string `json:"git_dir"`    // per-worktree git dir (realpath)
	CommonDir string `json:"common_dir"` // shared git dir (realpath); == GitDir for the main worktree
	// IndexPath is <GitDir>/index. Locate ignores GIT_INDEX_FILE on purpose
	// (the hook must see the real index). The only override is
	// evidence.CheckOptions.IndexFile for `verify --check` under a git
	// pre-commit hook, which substitutes it on a copy of RepoInfo.
	IndexPath string `json:"index_path"`
	// Inst = hex(sha256(Toplevel + "\x00" + CommonDir))[:16] (plan B3 repo_inst).
	Inst string `json:"inst"`
	// HeadRef is the symbolic ref from HEAD ("refs/heads/main"), "" when detached.
	HeadRef string `json:"head_ref,omitempty"`
	// ObjectFormat is "sha1" or "sha256" (extensions.objectformat); sha256 ⇒ fallback.
	ObjectFormat string `json:"object_format"`
	// RemoteHash = hex(sha256(url))[:16] of the first `url` key in the
	// `[remote "origin"]` section of <CommonDir>/config (value trimmed, no
	// other normalization); "" when absent. Filled by Locate during the same
	// config line scan that reads extensions.objectformat. The hook copies
	// it into AuditRecord.RepoRemoteHash.
	RemoteHash string `json:"remote_hash,omitempty"`
}

// TreeID is a hex git tree object id.
type TreeID string

// Tier of the trust boundary that produced a record (plan B0).
type Tier string

const (
	TierT0 Tier = "T0"
	TierT1 Tier = "T1"
)

// Outcome of a verify job (plan B3).
type Outcome string

const (
	OutcomePass       Outcome = "pass"
	OutcomeFail       Outcome = "fail"
	OutcomeTimeout    Outcome = "timeout"
	OutcomeSignal     Outcome = "signal"
	OutcomeInfra      Outcome = "infra"
	OutcomeSuperseded Outcome = "superseded"
)

// VerifyRecord is one finished verify job (plan B3). Written once, atomically,
// with fsync, to ~/.ballast/evidence/<repo_inst>/<name>/<t_start_ns>-<job_id>.json.
type VerifyRecord struct {
	V               int       `json:"v"` // 1
	JobID           string    `json:"job_id"`
	Name            string    `json:"name"`
	RepoInst        string    `json:"repo_inst"`
	ArgvHash        string    `json:"argv_hash"`
	ProjectJSONHash string    `json:"project_json_hash"`
	BallastVersion  string    `json:"ballast_version"`
	Tier            Tier      `json:"tier"`
	SStart          TreeID    `json:"s_start"`
	SEnd            TreeID    `json:"s_end"`
	TStart          time.Time `json:"t_start"`
	TEnd            time.Time `json:"t_end"`
	Outcome         Outcome   `json:"outcome"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	Signal          string    `json:"signal,omitempty"`
	SessionID       string    `json:"session_id,omitempty"` // from BALLAST_SESSION_ID / CLAUDE_SESSION_ID when present
	AgentID         string    `json:"agent_id,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	Error           string    `json:"error,omitempty"` // infra errors
}

// ValidPass reports outcome=pass with S_start == S_end (plan B3).
func (r *VerifyRecord) ValidPass() bool {
	return r.Outcome == OutcomePass && r.SStart != "" && r.SStart == r.SEnd
}

// JobState of a background verify job.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
)

// JobStatus is ~/.ballast/jobs/<repo_inst>/<name>/<job_id>.json.
type JobStatus struct {
	V         int       `json:"v"`
	JobID     string    `json:"job_id"`
	Name      string    `json:"name"`
	RepoInst  string    `json:"repo_inst"`
	State     JobState  `json:"state"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Deadline  time.Time `json:"deadline"` // started_at + timeout_s
	Outcome   Outcome   `json:"outcome,omitempty"`
}

// CommitKind classifies a `git commit` invocation (plan B3 C table).
type CommitKind string

const (
	CommitPlain       CommitKind = "plain"       // C = F(index copy)
	CommitAll         CommitKind = "all"         // -a/--all: C = F(index copy + add -u)
	CommitPaths       CommitKind = "paths"       // <paths>/--only/-i: add -u -- <paths>
	CommitUnparseable CommitKind = "unparseable" // → verify_fresh unknown, --check 12
)

// CommitForm is the evidence-side parse of a git commit node.
type CommitForm struct {
	Kind    CommitKind `json:"kind"`
	Paths   []string   `json:"paths,omitempty"`
	Include bool       `json:"include,omitempty"` // -i/--include
	Amend   bool       `json:"amend,omitempty"`
	Dir     string     `json:"dir,omitempty"` // resolved -C directory (repo may differ from the event repo)
	Reason  string     `json:"reason,omitempty"`
}

// FreshTargetKind selects what C is for verify_fresh.
type FreshTargetKind string

const (
	FreshCommit   FreshTargetKind = "commit"   // C from the commit node
	FreshWorktree FreshTargetKind = "worktree" // C = S of the current work tree (Stop-family)
)

// FreshQuery asks whether required verifications are fresh for a target.
type FreshQuery struct {
	Names []string        // nil ⇒ every required entry in project.json
	Kind  FreshTargetKind //
	Git   *GitInvocation  // the witness node's git info (Kind == commit)
	Cwd   string          // event cwd (raw), used to resolve git -C
}

// FreshResult is the three-valued freshness verdict plus explanation.
type FreshResult struct {
	Value Tri `json:"value"`
	// Cause: "fresh", "stale", "no_record", "not_valid_pass", "job_running",
	// "not_a_repo", "no_project", "project_invalid", "unparseable_commit",
	// "fallback_timeout", "snapshot_error", "no_required".
	Cause   string   `json:"cause"`
	Names   []string `json:"names,omitempty"`   // names checked
	Stale   []string `json:"stale,omitempty"`   // names not fresh
	Running []string `json:"running,omitempty"` // names with a running job
	C       TreeID   `json:"c,omitempty"`
}

// EvidenceView answers evidence predicates for one repo instance. Built per
// hook invocation by the EvidenceProvider (U8); faked in dsl tests.
type EvidenceView interface {
	VerifyFresh(q FreshQuery) FreshResult
	// LastFailureAfter reports whether a verify record with outcome
	// fail|timeout|signal has t_end after `after`. ok=false when unknown.
	LastFailureAfter(after time.Time) (Tri, time.Time)
	// FinishedSince lists records with t_end > since (inject job results).
	FinishedSince(since time.Time) []VerifyRecord
}

// VerifySpec is one entry of project.json "verify".
type VerifySpec struct {
	Name     string   `json:"name"`
	Argv     []string `json:"argv"`
	TimeoutS int      `json:"timeout_s,omitempty"` // default 1800
	Required bool     `json:"required,omitempty"`
	Env      []string `json:"env,omitempty"` // extra KEY=VALUE
	Cwd      string   `json:"cwd,omitempty"` // relative to the repo root
}

// ProjectConfig is <repo>/.ballast/project.json (plan B3).
type ProjectConfig struct {
	Version int          `json:"version,omitempty"`
	Verify  []VerifySpec `json:"verify"`
	Exempt  []string     `json:"exempt,omitempty"` // globs; ONE filter shared by S and C
}

// Required returns the names of required verify entries.
func (p *ProjectConfig) Required() []string {
	var out []string
	for _, v := range p.Verify {
		if v.Required {
			out = append(out, v.Name)
		}
	}
	return out
}
