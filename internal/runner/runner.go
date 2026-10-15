package runner

import (
	"errors"

	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
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
}

// Started describes one started job.
type Started struct {
	JobID string
	Name  string
}

// Start launches background jobs for names (nil ⇒ required). Errors:
// configuration problems (no repo, no project.json, unknown name) are
// returned wrapped with ErrConfig; spawn failures with ErrInfra.
func Start(opt Options, cwd string, names []string) ([]Started, error) {
	panic("unimplemented: runner.Start")
}

// RunJob executes a job created by Start (the detached child).
func RunJob(opt Options, jobID string) (*model.VerifyRecord, error) {
	panic("unimplemented: runner.RunJob")
}

// Foreground runs one verify synchronously (CI / debugging).
func Foreground(opt Options, cwd, name string) (*model.VerifyRecord, error) {
	panic("unimplemented: runner.Foreground")
}

// Error classes for CLI exit-code mapping (verify: 2 config, 5 infra).
var (
	ErrConfig = errors.New("runner: configuration error")
	ErrInfra  = errors.New("runner: infrastructure error")
)

// Spawner implements ports.Spawner with os/exec.
type Spawner struct{}

var _ ports.Spawner = Spawner{}

// SpawnDetached implements ports.Spawner.
func (Spawner) SpawnDetached(argv []string, env []string, logPath string) (int, error) {
	return spawnDetached(argv, env, logPath)
}
