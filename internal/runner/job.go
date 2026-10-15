package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/version"
)

// Environment variables naming the invoking session / agent (unverified
// whether clients export them to tool subprocesses; see the open questions).
const (
	EnvSessionID       = "BALLAST_SESSION_ID"
	EnvSessionIDClaude = "CLAUDE_SESSION_ID"
	EnvAgentID         = "BALLAST_AGENT_ID"
)

// target is the resolved repo and the verify entries to run.
type target struct {
	repo     *model.RepoInfo
	cfg      *model.ProjectConfig
	specs    []model.VerifySpec
	hash     string // project_json_hash
	session  string
	agent    string
	toplevel string
}

// jobSpec is the sidecar next to a job file (evidence.Store.SpecPath): what
// RunJob needs although the detached child only gets the job id. It pins the
// entry as resolved by Start, so a project.json edited in between does not
// change what a started job runs.
type jobSpec struct {
	V               int              `json:"v"`
	Toplevel        string           `json:"toplevel"`
	Spec            model.VerifySpec `json:"spec"`
	Exempt          []string         `json:"exempt,omitempty"`
	ProjectJSONHash string           `json:"project_json_hash"`
	SessionID       string           `json:"session_id,omitempty"`
	AgentID         string           `json:"agent_id,omitempty"`
}

func timeoutOf(spec model.VerifySpec) time.Duration {
	s := spec.TimeoutS
	if s <= 0 {
		s = project.DefaultTimeoutS
	}
	return time.Duration(s) * time.Second
}

// bytesLoader is implemented by project.Loader.
type bytesLoader interface {
	LoadProjectBytes(root string) (*model.ProjectConfig, []byte, error)
}

// resolve locates the repo, loads project.json and selects the entries.
func (o Options) resolve(cwd string, names []string) (*target, error) {
	if cwd == "" {
		return nil, fmt.Errorf("%w: no working directory", ErrConfig)
	}
	repo, err := o.Repo.Locate(cwd)
	if err != nil {
		if errors.Is(err, model.ErrNotRepo) {
			return nil, fmt.Errorf("%w: %s is not inside a git repository", ErrConfig, cwd)
		}
		return nil, fmt.Errorf("%w: locate repository: %w", ErrConfig, err)
	}
	var (
		cfg  *model.ProjectConfig
		data []byte
	)
	if bl, ok := o.Project.(bytesLoader); ok {
		cfg, data, err = bl.LoadProjectBytes(repo.Toplevel)
	} else {
		cfg, err = o.Project.LoadProject(repo.Toplevel)
		if err == nil && cfg != nil {
			data, _ = o.FS.ReadFile(layout.ProjectJSON(repo.Toplevel))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: %s not found (run `ballast init`)", ErrConfig, layout.ProjectJSON(repo.Toplevel))
	}
	t := &target{repo: repo, cfg: cfg, hash: project.Hash(data), toplevel: repo.Toplevel}
	t.session = o.firstEnv(EnvSessionID, EnvSessionIDClaude)
	t.agent = o.firstEnv(EnvAgentID)
	if names == nil {
		names = cfg.Required()
		if len(names) == 0 {
			return nil, fmt.Errorf("%w: project.json has no required verify entries", ErrConfig)
		}
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		var found *model.VerifySpec
		for i := range cfg.Verify {
			if cfg.Verify[i].Name == n {
				found = &cfg.Verify[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("%w: no verify entry named %q in project.json", ErrConfig, n)
		}
		t.specs = append(t.specs, *found)
	}
	if len(t.specs) == 0 {
		return nil, fmt.Errorf("%w: no verify names given", ErrConfig)
	}
	return t, nil
}

func (o Options) firstEnv(keys ...string) string {
	for _, k := range keys {
		if v, ok := o.Env.Getenv(k); ok && v != "" {
			return v
		}
	}
	return ""
}

func (o Options) newJobID() string {
	return o.Clock.Now().UTC().Format("20060102T150405") + "-" + o.IDs.NewID(3)
}

func argvHash(argv []string) string {
	b, _ := json.Marshal(argv)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// createJob supersedes running jobs of the same (repo_inst, name), then
// writes the sidecar and a running JobStatus for a new job.
func (o Options) createJob(t *target, spec model.VerifySpec) (*model.JobStatus, error) {
	if err := o.supersedeRunning(t.repo.Inst, spec.Name); err != nil {
		return nil, fmt.Errorf("%w: supersede %s: %w", ErrInfra, spec.Name, err)
	}
	now := o.Clock.Now()
	js := &model.JobStatus{
		V: 1, JobID: o.newJobID(), Name: spec.Name, RepoInst: t.repo.Inst,
		State: model.JobRunning, StartedAt: now, Deadline: now.Add(timeoutOf(spec)),
	}
	side := jobSpec{V: 1, Toplevel: t.toplevel, Spec: spec, Exempt: t.cfg.Exempt, ProjectJSONHash: t.hash, SessionID: t.session, AgentID: t.agent}
	data, err := json.Marshal(&side)
	if err == nil {
		err = sys.WriteFileAtomic(o.FS, o.IDs, o.Evidence.SpecPath(js.RepoInst, js.Name, js.JobID), append(data, '\n'), 0o600, false)
	}
	if err == nil {
		err = o.Evidence.WriteJob(js)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: create job: %w", ErrInfra, err)
	}
	return js, nil
}

// supersedeRunning ends every running job of (repoInst, name): the job file
// becomes done/superseded first (so the child, if it still writes, sees it),
// then a superseded record is written, then the process is told to stop.
func (o Options) supersedeRunning(repoInst, name string) error {
	old, err := o.Evidence.RunningJobs(repoInst, name)
	if err != nil {
		return err
	}
	var errs []error
	for i := range old {
		js := old[i]
		side, _ := o.readSpec(&js)
		now := o.Clock.Now()
		pid := js.PID
		js.State, js.Outcome = model.JobDone, model.OutcomeSuperseded
		if err := o.Evidence.WriteJob(&js); err != nil {
			errs = append(errs, err)
			continue
		}
		rec := &model.VerifyRecord{
			V: 1, JobID: js.JobID, Name: js.Name, RepoInst: js.RepoInst,
			ArgvHash: argvHash(side.Spec.Argv), ProjectJSONHash: side.ProjectJSONHash,
			BallastVersion: version.Version, Tier: model.TierT0,
			TStart: js.StartedAt, TEnd: now, Outcome: model.OutcomeSuperseded,
			SessionID: side.SessionID, AgentID: side.AgentID,
			LogPath: o.Evidence.LogPath(js.RepoInst, js.Name, js.JobID),
		}
		if err := o.Evidence.WriteRecord(rec); err != nil {
			errs = append(errs, err)
		}
		o.audit(rec)
		if pid > 0 && pid != os.Getpid() {
			_ = terminateProcess(pid)
		}
	}
	return errors.Join(errs...)
}

func (o Options) readSpec(js *model.JobStatus) (jobSpec, error) {
	var s jobSpec
	data, err := o.FS.ReadFile(o.Evidence.SpecPath(js.RepoInst, js.Name, js.JobID))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return jobSpec{}, err
	}
	return s, nil
}

// audit appends the "verify" line; a failure is noted in the job log, not
// fatal (the record is already durable).
func (o Options) audit(rec *model.VerifyRecord) {
	if o.Audit == nil {
		return
	}
	r := *rec
	err := o.Audit.Append([]model.AuditRecord{{
		TS: o.Clock.Now().UTC(), V: model.AuditVersion, Type: model.AuditVerify,
		SessionID: r.SessionID, AgentID: r.AgentID, RepoInst: r.RepoInst,
		Tier: r.Tier, BallastVersion: r.BallastVersion, Verify: &r,
	}})
	if err != nil && r.LogPath != "" {
		appendLog(o.FS, r.LogPath, fmt.Sprintf("# ballast: audit append failed: %v\n", err))
	}
}

// appendLog best-effort appends a line to a log file.
func appendLog(fsys sys.FS, path, line string) {
	f, err := fsys.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write([]byte(line))
	_ = f.Close()
}

// failJob finishes a job that could not be started with an infra record.
func (o Options) failJob(js *model.JobStatus, t *target, spec model.VerifySpec, cause error) {
	now := o.Clock.Now()
	rec := &model.VerifyRecord{
		V: 1, JobID: js.JobID, Name: js.Name, RepoInst: js.RepoInst,
		ArgvHash: argvHash(spec.Argv), ProjectJSONHash: t.hash,
		BallastVersion: version.Version, Tier: model.TierT0,
		TStart: js.StartedAt, TEnd: now, Outcome: model.OutcomeInfra, Error: cause.Error(),
		SessionID: t.session, AgentID: t.agent,
		LogPath: o.Evidence.LogPath(js.RepoInst, js.Name, js.JobID),
	}
	_ = o.Evidence.WriteRecord(rec)
	o.audit(rec)
	js.State, js.Outcome = model.JobDone, model.OutcomeInfra
	_ = o.Evidence.WriteJob(js)
}

// jobCwd is the directory the entry runs in.
func jobCwd(toplevel string, spec model.VerifySpec) string {
	if spec.Cwd == "" || spec.Cwd == "." {
		return toplevel
	}
	return filepath.Join(toplevel, filepath.FromSlash(spec.Cwd))
}

var errNotRunning = errors.New("job is no longer running")

// markPID records the runner's own pid in the job file while it is running.
func (o Options) markPID(js *model.JobStatus) error {
	cur, err := o.Evidence.ReadJob(js.RepoInst, js.Name, js.JobID)
	if err != nil {
		return err
	}
	if cur == nil || cur.State != model.JobRunning {
		return errNotRunning
	}
	cur.PID = os.Getpid()
	*js = *cur
	return o.Evidence.WriteJob(cur)
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
