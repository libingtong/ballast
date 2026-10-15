package runner

import (
	"errors"
	"fmt"
	"io"

	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
)

// Options configures the runner.
type Options struct {
	Layout   layout.Layout
	FS       sys.FS
	Clock    sys.Clock
	IDs      sys.IDGen
	Env      sys.Env
	Self     string // absolute path of the ballast binary
	Evidence *evidence.Store
	Audit    ports.AuditSink
	Repo     ports.RepoLocator
	Project  ports.ProjectLoader
	Spawner  ports.Spawner

	// Output receives the verify command's combined output in Foreground
	// (nil ⇒ os.Stdout). The output always goes to the job log as well.
	Output io.Writer
}

// Started describes one started job.
type Started struct {
	JobID string
	Name  string
}

// String renders the line `ballast verify` prints for a started job.
func (s Started) String() string { return fmt.Sprintf("job=%s name=%s", s.JobID, s.Name) }

// Start launches background jobs for names (nil ⇒ required). Errors:
// configuration problems (no repo, no project.json, unknown name) are
// returned wrapped with ErrConfig; spawn failures with ErrInfra. Jobs started
// before a failure are returned together with the error.
func Start(opt Options, cwd string, names []string) ([]Started, error) {
	opt = opt.withDefaults()
	if opt.Self == "" {
		return nil, fmt.Errorf("%w: path of the ballast binary not set", ErrConfig)
	}
	tgt, err := opt.resolve(cwd, names)
	if err != nil {
		return nil, err
	}
	var started []Started
	for _, spec := range tgt.specs {
		js, err := opt.createJob(tgt, spec)
		if err != nil {
			return started, err
		}
		env := opt.Env.Environ()
		pid, err := opt.Spawner.SpawnDetached([]string{opt.Self, "verify", "--run-job", js.JobID}, env, opt.Evidence.LogPath(js.RepoInst, js.Name, js.JobID))
		if err != nil {
			err = fmt.Errorf("start %s: %w", spec.Name, err)
			opt.failJob(js, tgt, spec, err)
			return started, fmt.Errorf("%w: %w", ErrInfra, err)
		}
		_ = pid // the child records its own pid (RunJob), see job.go
		started = append(started, Started{JobID: js.JobID, Name: spec.Name})
	}
	return started, nil
}

// RunJob executes a job created by Start (the detached child). The returned
// record is the one written to the evidence store; for a job that was
// superseded before or while it ran it is the superseded record, and a job
// that is already finished yields its existing record. Errors are wrapped
// with ErrConfig (unknown job) or ErrInfra (evidence store failures).
func RunJob(opt Options, jobID string) (*model.VerifyRecord, error) {
	opt = opt.withDefaults()
	js, rec, err := opt.Evidence.FindJob(jobID)
	if err != nil {
		return nil, fmt.Errorf("%w: find job %s: %w", ErrInfra, jobID, err)
	}
	if js == nil {
		return nil, fmt.Errorf("%w: unknown job %q", ErrConfig, jobID)
	}
	if js.State != model.JobRunning {
		return rec, nil
	}
	return opt.runExisting(js, nil)
}

// Foreground runs one verify synchronously (CI / debugging): the command's
// output is streamed to Options.Output and the job is recorded like a
// background one.
func Foreground(opt Options, cwd, name string) (*model.VerifyRecord, error) {
	opt = opt.withDefaults()
	tgt, err := opt.resolve(cwd, []string{name})
	if err != nil {
		return nil, err
	}
	js, err := opt.createJob(tgt, tgt.specs[0])
	if err != nil {
		return nil, err
	}
	out := opt.Output
	if out == nil {
		out = stdout()
	}
	return opt.runExisting(js, out)
}

// Error classes for CLI exit-code mapping (verify: 2 config, 5 infra).
var (
	ErrConfig = errors.New("runner: configuration error")
	ErrInfra  = errors.New("runner: infrastructure error")
)

// StatusExitCode maps a record to the `verify --status` / `--foreground` /
// `--run-job` exit code (architecture §7): 0 pass, 1 fail, 3 timeout,
// 4 signal, 5 infra, 7 superseded; a nil record is 11 (no record).
func StatusExitCode(rec *model.VerifyRecord) int {
	if rec == nil {
		return 11
	}
	switch rec.Outcome {
	case model.OutcomePass:
		return 0
	case model.OutcomeFail:
		return 1
	case model.OutcomeTimeout:
		return 3
	case model.OutcomeSignal:
		return 4
	case model.OutcomeSuperseded:
		return 7
	}
	return 5
}

// Spawner implements ports.Spawner with os/exec.
type Spawner struct{}

var _ ports.Spawner = Spawner{}

// SpawnDetached implements ports.Spawner.
func (Spawner) SpawnDetached(argv []string, env []string, logPath string) (int, error) {
	return spawnDetached(argv, env, logPath)
}

func (o Options) withDefaults() Options {
	if o.FS == nil {
		o.FS = sys.OSFS{}
	}
	if o.Clock == nil {
		o.Clock = sys.RealClock{}
	}
	if o.IDs == nil {
		o.IDs = sys.RandomID{}
	}
	if o.Env == nil {
		o.Env = sys.OSEnv{}
	}
	if o.Repo == nil {
		o.Repo = &snapshot.Locator{FS: o.FS}
	}
	if o.Project == nil {
		o.Project = &project.Loader{FS: o.FS}
	}
	if o.Spawner == nil {
		o.Spawner = Spawner{}
	}
	if o.Evidence == nil {
		o.Evidence = evidence.NewStore(evidence.Options{Layout: o.Layout, FS: o.FS, Clock: o.Clock, IDs: o.IDs})
	}
	return o
}
