package cli

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

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
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/psparse"
	"github.com/libingtong/ballast/internal/runner"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/state"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
	"github.com/libingtong/ballast/internal/version"
)

// core holds the process-wide collaborators shared by the hook wiring and
// the non-hot-path commands. Constructing it performs only the redirect-lock
// probe (one Lstat on an unlocked host) and, on a locked host, the passwd
// lookup of the home directory; everything else is path arithmetic and
// stateless values.
type core struct {
	env      sys.Env
	fs       sys.FS
	clock    sys.Clock
	ids      sys.IDGen
	lay      layout.Layout
	adapters *adapter.Registry
	pathSvc  *paths.Service
	engine   *dsl.Engine

	// Redirect lock (docs/p2-design.md §3.2 "重定向锁").
	//
	// locked is tier.LockState's verdict (or the ballast_fault test
	// override); org is the validated org.json it read (zero when not
	// locked); redirects lists, in the frozen order, the redirect variables
	// that are set but were ignored (nil when none or not locked).
	locked    bool
	org       tier.OrgConfig
	redirects []string
	// owner is the ownership probe handed to the tier package (nil ⇒
	// platform default; tests inject a fake).
	owner func(fi fs.FileInfo) (uint32, bool)
}

// wireSeams are the test seams of newCoreWith / wireHook. The zero value is
// production: the real file system, ownership probe, effective uid, passwd
// lookup and tier.DefaultLockedHomeTimeout.
type wireSeams struct {
	FS      sys.FS
	Owner   func(fi fs.FileInfo) (uint32, bool)
	Euid    func() int
	Lookup  func(uid int) (string, error)
	Timeout time.Duration
	// Diagnostic (doctor only): when the home of a locked host cannot be
	// determined, fall back to $HOME with the redirect variables still
	// ignored, only so that `ballast doctor` can run to its tier.locked_home
	// check and report the failure (docs/p2-design.md §3.5). The fallback
	// layout is for reading diagnostics; no command other than doctor uses it.
	Diagnostic bool
}

// redirectVars are the Ballast redirection variables the lock ignores, in
// the order hook.Deps.RedirectsIgnored reports them.
var redirectVars = [...]string{layout.EnvHome, layout.EnvManagedDir, layout.EnvInstallRoot}

// newCore resolves the layout from env and builds the shared services.
func newCore(env sys.Env) (*core, error) { return newCoreWith(env, wireSeams{}) }

// newCoreWith is newCore with seams. Order (frozen, §3.2): tier.LockState
// first, then layout.ResolveWith. On a locked host the redirection variables
// are ignored and <home> comes from tier.LockedHome (passwd, or the org.json
// home_root rule), never from $HOME; a failing or timed-out lookup is an
// error (the hook then fails open per B7, doctor reports tier.locked_home).
func newCoreWith(env sys.Env, sm wireSeams) (*core, error) {
	fsys := sm.FS
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	org, locked := tier.LockState(fsys, env.GOOS(), sm.Owner)
	forced := false
	if !locked {
		// Only a ballast_fault build can force the lock (bench scenarios
		// 13a/13b/14); production builds return forced=false.
		org, forced = testLock(env)
	}
	passwdHome := func() (string, error) {
		euid := sys.Euid()
		if sm.Euid != nil {
			euid = sm.Euid()
		}
		home, _, err := tier.LockedHome(tier.HomeOptions{
			FS: fsys, Env: env, Owner: sm.Owner, Euid: euid, Lookup: sm.Lookup,
			HomeRoot: org.HomeRoot, Timeout: sm.Timeout,
		})
		return home, err
	}
	var lay layout.Layout
	var err error
	switch {
	case locked:
		lay, err = layout.ResolveWith(env, layout.Options{IgnoreRedirects: true, PasswdHome: passwdHome})
		if err != nil && sm.Diagnostic {
			lay, err = layout.ResolveWith(env, layout.Options{IgnoreRedirects: true})
		}
	case forced:
		// Timing aid of bench scenarios 13a/13b: perform the real passwd
		// lookup (13a) or, with BALLAST_TEST_PASSWD=missing, the home_root
		// judgement (13b), discard the outcome and keep using the redirected
		// home so a real ~/.ballast is never touched.
		sm = testSeams(env, sm)
		_, _ = passwdHome()
		lay, err = layout.ResolveWith(env, layout.Options{})
	default:
		lay, err = layout.ResolveWith(env, layout.Options{})
	}
	if err != nil {
		return nil, err
	}
	adapters := adapter.Default()
	pathSvc := paths.NewService(fsys)
	engine := dsl.NewEngine(dsl.Options{
		Paths:    pathSvc,
		Bash:     bashparse.Parser{},
		PS:       psparse.Parser{},
		Adapters: adapters,
		GOOS:     env.GOOS(),
	})
	c := &core{
		env:      env,
		fs:       fsys,
		clock:    sys.RealClock{},
		ids:      sys.RandomID{},
		lay:      lay,
		adapters: adapters,
		pathSvc:  pathSvc,
		engine:   engine,
		locked:   locked || forced,
		org:      org,
		owner:    sm.Owner,
	}
	if c.locked {
		c.redirects = ignoredRedirects(env)
	}
	if locked && lay.InstallRoot != "" {
		// A locked host never trusts $HOME: every downstream collaborator
		// (hook path context, runner sandbox spec, tier detector, install)
		// sees the layout's home (audit r1). The redirect variables stay
		// visible in the Env so doctor and the audit can name them; the
		// layout already ignores them.
		c.env = layout.WithHome(env, lay.InstallRoot)
	}
	return c, nil
}

// ignoredRedirects lists the redirection variables that are set (non-empty)
// in env, in the frozen order; nil when there are none.
func ignoredRedirects(env sys.Env) []string {
	var out []string
	for _, k := range redirectVars {
		if v, ok := env.Getenv(k); ok && v != "" {
			out = append(out, k)
		}
	}
	return out
}

// tierProbe returns the lazily evaluated tier detector for this process
// (ports.TierProbe memoizes per process). self is the running binary.
func (c *core) tierProbe(self string) ports.TierProbe {
	if p := testTier(c.env); p != nil {
		return p
	}
	return tier.New(tier.Options{FS: c.fs, Env: c.env, Layout: c.lay, Self: self, Owner: c.owner})
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
// hook.FaultsFromEnv. It performs no I/O beyond locating the running binary
// and the redirect-lock probe (one Lstat on an unlocked host).
//
// P2 wiring (docs/p2-design.md §3.6):
//
//   - Tier is the lazy tier.Prober (consulted only for a canonical
//     `ballast verify`); IDs is always c.ids.
//   - On a redirect-locked host (tier.LockState) the layout comes from
//     layout.ResolveWith with the passwd/home_root home, RedirectsIgnored
//     lists the ignored variables that are set, ProjectSandbox is
//     tier.ProjectChecker and WeakenedProject is
//     tier.EffectiveWeakenedProject(org). On an unlocked host
//     RedirectsIgnored and ProjectSandbox stay nil and the passwd lookup is
//     never made.
//   - A locked host whose home cannot be determined (lookup error or
//     timeout) makes WireHook return the error: the caller fails open per
//     B7 (exit 1, empty stdout).
func WireHook(env sys.Env) (*hook.Deps, error) { return wireHook(env, wireSeams{}) }

func wireHook(env sys.Env, sm wireSeams) (*hook.Deps, error) {
	c, err := newCoreWith(env, sm)
	if err != nil {
		return nil, err
	}
	locator := &snapshot.Locator{FS: c.fs}
	evOpt := c.evidenceOptions()
	evOpt.Locator = locator
	self := selfPath()
	deps := &hook.Deps{
		Clock:    c.clock,
		Env:      c.env,
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
		Tier:     c.tierProbe(self),
		IDs:      c.ids,
		Faults:   hook.FaultsFromEnv(env.Getenv),
		Watchdog: testWatchdog(env),
		Self:     self,
		Exit:     os.Exit,
		Version:  version.Version,
	}
	if c.locked {
		deps.RedirectsIgnored = c.redirects
		deps.ProjectSandbox = tier.ProjectChecker{FS: c.fs}
		deps.WeakenedProject = tier.EffectiveWeakenedProject(c.org)
	}
	return deps, nil
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
