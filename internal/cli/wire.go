package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/libingtong/ballast/internal/adapter"
	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/dsl"
	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/hook"
	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/paths"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/psparse"
	"github.com/libingtong/ballast/internal/runner"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/state"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/version"
)

// core holds the process-wide collaborators shared by the hook wiring and
// the non-hot-path commands. Constructing it performs no I/O: it only
// resolves the layout (pure path arithmetic) and allocates stateless values.
type core struct {
	env      sys.Env
	fs       sys.FS
	clock    sys.Clock
	ids      sys.IDGen
	lay      layout.Layout
	adapters *adapter.Registry
	pathSvc  *paths.Service
	engine   *dsl.Engine
}

// newCore resolves the layout from env and builds the shared services.
func newCore(env sys.Env) (*core, error) {
	lay, err := layout.Resolve(env)
	if err != nil {
		return nil, err
	}
	fsys := sys.OSFS{}
	adapters := adapter.Default()
	pathSvc := paths.NewService(fsys)
	engine := dsl.NewEngine(dsl.Options{
		Paths:    pathSvc,
		Bash:     bashparse.Parser{},
		PS:       psparse.Parser{},
		Adapters: adapters,
		GOOS:     env.GOOS(),
	})
	return &core{
		env:      env,
		fs:       fsys,
		clock:    sys.RealClock{},
		ids:      sys.RandomID{},
		lay:      lay,
		adapters: adapters,
		pathSvc:  pathSvc,
		engine:   engine,
	}, nil
}

// loader returns the rule loader (three layers, LKG, optional cache).
func (c *core) loader(noCache bool, stderr io.Writer) *config.Loader {
	return config.NewLoader(config.Options{
		Layout:   c.lay,
		FS:       c.fs,
		Clock:    c.clock,
		Compiler: c.engine,
		Version:  version.Version,
		NoCache:  noCache,
		IDs:      c.ids,
		Stderr:   stderr,
	})
}

// evidenceOptions returns the options shared by evidence.Provider/Store/Check.
func (c *core) evidenceOptions() evidence.Options {
	return evidence.Options{
		Layout:  c.lay,
		FS:      c.fs,
		Clock:   c.clock,
		IDs:     c.ids,
		Locator: &snapshot.Locator{FS: c.fs},
		Project: &project.Loader{FS: c.fs},
		Env:     c.env,
	}
}

// auditWriter returns the audit sink.
func (c *core) auditWriter() *audit.Writer {
	return audit.NewWriter(audit.Options{Dir: c.lay.AuditDir, FS: c.fs, Clock: c.clock})
}

// auditReader returns the audit source.
func (c *core) auditReader() *audit.Reader {
	return &audit.Reader{Dir: c.lay.AuditDir, FS: c.fs}
}

// stateStore returns the session state store.
func (c *core) stateStore() *state.Store {
	return state.New(state.Options{Layout: c.lay, FS: c.fs, Clock: c.clock, IDs: c.ids})
}

// WireHook builds hook.Deps from the real implementations of every unit:
// adapter.Default, config.NewLoader(dsl.NewEngine(...)), bashparse.Parser,
// psparse.Parser, paths.NewService, state.New, audit.NewWriter,
// snapshot.Locator, evidence.NewProvider (which loads project.json lazily
// through project.Loader), inject.Builder, runner.Spawner and
// hook.FaultsFromEnv. It performs no I/O beyond locating the running binary.
func WireHook(env sys.Env) (*hook.Deps, error) {
	c, err := newCore(env)
	if err != nil {
		return nil, err
	}
	locator := &snapshot.Locator{FS: c.fs}
	evOpt := c.evidenceOptions()
	evOpt.Locator = locator
	return &hook.Deps{
		Clock:    c.clock,
		Env:      env,
		FS:       c.fs,
		Layout:   c.lay,
		Adapters: c.adapters,
		Rules:    c.loader(false, nil),
		Engine:   c.engine,
		Bash:     bashparse.Parser{},
		PS:       psparse.Parser{},
		Paths:    c.pathSvc,
		State:    c.stateStore(),
		Audit:    c.auditWriter(),
		Repo:     locator,
		Evidence: evidence.NewProvider(evOpt),
		Injector: inject.Builder{},
		Spawner:  runner.Spawner{},
		Faults:   hook.FaultsFromEnv(env.Getenv),
		Self:     selfPath(),
		Exit:     os.Exit,
		Version:  version.Version,
	}, nil
}

// selfPath returns the path of the running binary for detached children
// (`ballast gc`). The kernel-provided path is cheap to obtain; symlinks are
// left alone here (the hook is invoked through the installed absolute path).
func selfPath() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		if len(os.Args) > 0 {
			if abs, aerr := filepath.Abs(os.Args[0]); aerr == nil {
				return abs
			}
		}
		return ""
	}
	return exe
}

// resolvedSelf returns the symlink-resolved absolute path of the running
// binary (verify's detached runner and install use it).
func resolvedSelf() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if rp, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = rp
	}
	if !filepath.IsAbs(exe) {
		return "", errors.New("the running executable path is not absolute: " + exe)
	}
	return exe, nil
}
