package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

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
	opt = opt.withDefaults()
	if !safeName(req.RequestID) {
		return nil, fmt.Errorf("%w: bad request id %q", ErrConfig, req.RequestID)
	}
	file := model.VerifyRequestFile{
		V: 1, RequestID: req.RequestID, Origin: model.OriginHook, Client: req.Client,
		SessionID: req.SessionID, AgentID: req.AgentID, Jobs: []model.RequestedJob{},
	}
	repoInst := norepoScope(req.Cwd)
	if req.Cwd != "" {
		if repo, err := opt.Repo.Locate(req.Cwd); err == nil {
			repoInst = repo.Inst
		}
	}
	write := func() error { return opt.writeRequestFile(repoInst, &file) }
	configErr := func(err error) ([]Started, error) {
		file.Error = trimRunner(err)
		if werr := write(); werr != nil {
			return nil, fmt.Errorf("%w: %w (and the request file: %v)", ErrConfig, err, werr)
		}
		return nil, err
	}
	if opt.Self == "" {
		return configErr(fmt.Errorf("%w: path of the ballast binary not set", ErrConfig))
	}
	names := req.Names
	if len(names) == 0 {
		names = nil
	}
	tgt, err := opt.resolve(req.Cwd, names)
	if err != nil {
		return configErr(err)
	}
	repoInst = tgt.repo.Inst
	// Attribution comes from the hook's event, not from this process.
	tgt.session, tgt.agent = req.SessionID, req.AgentID

	type created struct {
		js   *model.JobStatus
		spec model.VerifySpec
	}
	var jobs []created
	abandon := func(cause error) {
		for _, c := range jobs {
			opt.failJob(c.js, tgt, c.spec, cause)
		}
	}
	for i, spec := range tgt.specs {
		meta := jobMeta{Origin: model.OriginHook, RequestID: req.RequestID, Client: req.Client, ProjectDir: req.ProjectDir}
		if i == 0 {
			meta.ID = req.RequestID
		}
		js, err := opt.createJobWith(tgt, spec, meta)
		if err != nil {
			abandon(err)
			file.Error = trimRunner(err)
			for _, c := range jobs {
				file.Jobs = append(file.Jobs, model.RequestedJob{Name: c.spec.Name, JobID: c.js.JobID})
			}
			_ = write()
			return nil, err
		}
		jobs = append(jobs, created{js, spec})
		file.Jobs = append(file.Jobs, model.RequestedJob{Name: spec.Name, JobID: js.JobID})
	}
	if err := write(); err != nil {
		cause := fmt.Errorf("%w: write request file: %w", ErrInfra, err)
		abandon(cause)
		return nil, cause
	}

	var started []Started
	for i, c := range jobs {
		// The detached child is Ballast itself: no loader variables.
		env := stripLoaderEnv(opt.Env.Environ())
		_, err := opt.Spawner.SpawnDetached([]string{opt.Self, "verify", "--run-job", c.js.JobID}, env, opt.Evidence.LogPath(c.js.RepoInst, c.js.Name, c.js.JobID))
		if err != nil {
			err = fmt.Errorf("start %s: %w", c.spec.Name, err)
			for _, rest := range jobs[i:] {
				opt.failJob(rest.js, tgt, rest.spec, err)
			}
			file.Error = err.Error()
			_ = write()
			return started, fmt.Errorf("%w: %w", ErrInfra, err)
		}
		started = append(started, Started{JobID: c.js.JobID, Name: c.spec.Name})
	}
	return started, nil
}

// norepoScope is the request-file scope outside a repository.
func norepoScope(cwd string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return "norepo-" + hex.EncodeToString(sum[:])[:16]
}

// safeName accepts a single path element made of [A-Za-z0-9._-].
func safeName(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func (o Options) requestPath(repoInst, id string) string {
	return filepath.Join(o.Layout.RequestsDir, repoInst, id+".json")
}

// writeRequestFile writes <RequestsDir>/<repo_inst>/<request_id>.json
// atomically (0600).
func (o Options) writeRequestFile(repoInst string, f *model.VerifyRequestFile) error {
	if o.Layout.RequestsDir == "" {
		return errors.New("layout has no requests directory")
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return sys.WriteFileAtomic(o.FS, o.IDs, o.requestPath(repoInst, f.RequestID), append(data, '\n'), 0o600, true)
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
	opt = opt.withDefaults()
	if !safeName(requestID) || (repoInst != "" && !safeName(repoInst)) {
		return RequestStatusResult{}, fmt.Errorf("%w: bad request id or scope", ErrRequestNotFound)
	}
	file, err := opt.readRequestFile(repoInst, requestID)
	if err != nil {
		return RequestStatusResult{}, err
	}
	res := RequestStatusResult{RequestID: file.RequestID, Error: file.Error}
	for _, j := range file.Jobs {
		line := RequestJobStatus{Name: j.Name, JobID: j.JobID, Outcome: string(model.OutcomeInfra)}
		js, rec, ferr := opt.Evidence.FindJob(j.JobID)
		switch {
		case ferr == nil && rec != nil:
			line.Record, line.Outcome = rec, string(rec.Outcome)
		case ferr == nil && js != nil && js.State == model.JobRunning && opt.Evidence.JobActive(js):
			line.Outcome = outcomeRunning
		}
		res.Jobs = append(res.Jobs, line)
	}
	res.Worst = outcomeInfra
	if len(res.Jobs) > 0 {
		res.Worst = outcomePass
		for _, l := range res.Jobs {
			if w := aggregateOutcome(l.Outcome); outcomeRank[w] > outcomeRank[res.Worst] {
				res.Worst = w
			}
		}
	}
	if len(res.Jobs) == 1 {
		res.ExitCode = jobExitCode(res.Jobs[0])
	} else {
		res.ExitCode = outcomeExit[res.Worst]
	}
	return res, nil
}

// ErrRequestNotFound: no request file has the id (the CLI maps it to the
// "no record" exit code of `verify --status`).
var ErrRequestNotFound = errors.New("runner: request not found")

const (
	outcomeRunning = "running"
	outcomePass    = string(model.OutcomePass)
	outcomeInfra   = string(model.OutcomeInfra)
)

// outcomeRank orders the aggregate: fail > timeout > signal > infra >
// running > pass (superseded is aggregated as infra).
var outcomeRank = map[string]int{
	outcomePass: 0, outcomeRunning: 1, outcomeInfra: 2,
	string(model.OutcomeSignal): 3, string(model.OutcomeTimeout): 4, string(model.OutcomeFail): 5,
}

// outcomeExit is StatusExitCode applied to an aggregate outcome.
var outcomeExit = map[string]int{
	outcomePass: 0, string(model.OutcomeFail): 1, string(model.OutcomeTimeout): 3,
	string(model.OutcomeSignal): 4, outcomeInfra: 5, outcomeRunning: 6,
}

func aggregateOutcome(o string) string {
	if o == string(model.OutcomeSuperseded) {
		return outcomeInfra
	}
	return o
}

// jobExitCode is the exit code `verify --status <job>` gives for one line.
func jobExitCode(l RequestJobStatus) int {
	if l.Record != nil {
		return StatusExitCode(l.Record)
	}
	if l.Outcome == outcomeRunning {
		return outcomeExit[outcomeRunning]
	}
	return outcomeExit[outcomeInfra]
}

// readRequestFile finds and decodes the request file (every repo_inst
// directory is searched when repoInst is "").
func (o Options) readRequestFile(repoInst, id string) (*model.VerifyRequestFile, error) {
	insts := []string{repoInst}
	if repoInst == "" {
		insts = nil
		ents, err := o.FS.ReadDir(o.Layout.RequestsDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %v", ErrInfra, err)
		}
		for _, e := range ents {
			if e.IsDir() {
				insts = append(insts, e.Name())
			}
		}
	}
	for _, inst := range insts {
		data, err := o.FS.ReadFile(o.requestPath(inst, id))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("%w: read request file: %v", ErrInfra, err)
		}
		var f model.VerifyRequestFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%w: request file %s: %v", ErrInfra, id, err)
		}
		if f.RequestID != id {
			return nil, fmt.Errorf("%w: request file %s names %q", ErrInfra, id, f.RequestID)
		}
		return &f, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrRequestNotFound, strings.TrimSpace(id))
}
