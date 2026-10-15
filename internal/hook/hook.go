package hook

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/version"
)

// Frozen budgets (plan B7, B10).
const (
	DefaultWatchdog = 150 * time.Millisecond
	DeadlineMargin  = 30 * time.Millisecond
	MaxStdinBytes   = 16 << 20
	GCInterval      = 24 * time.Hour
)

// Args are the parsed command-line arguments of `ballast hook`.
type Args struct {
	Client model.ClientID // --client (required)
	Native string         // --event  (required, native event name)
}

// ParseArgs parses ["--client", X, "--event", E] (order-free; "--k=v" accepted).
func ParseArgs(argv []string) (Args, error) {
	var client, native string
	var haveClient, haveNative bool
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if !strings.HasPrefix(arg, "--") {
			return Args{}, fmt.Errorf("hook: unexpected argument %q", arg)
		}
		name, val, hasVal := strings.Cut(arg, "=")
		if name != "--client" && name != "--event" {
			return Args{}, fmt.Errorf("hook: unknown flag %q", name)
		}
		if !hasVal {
			i++
			if i >= len(argv) || strings.HasPrefix(argv[i], "--") {
				return Args{}, fmt.Errorf("hook: flag %s needs a value", name)
			}
			val = argv[i]
		}
		if val == "" {
			return Args{}, fmt.Errorf("hook: flag %s has an empty value", name)
		}
		switch name {
		case "--client":
			if haveClient {
				return Args{}, fmt.Errorf("hook: flag --client given twice")
			}
			client, haveClient = val, true
		case "--event":
			if haveNative {
				return Args{}, fmt.Errorf("hook: flag --event given twice")
			}
			native, haveNative = val, true
		}
	}
	if !haveClient {
		return Args{}, fmt.Errorf("hook: missing required flag --client")
	}
	if !haveNative {
		return Args{}, fmt.Errorf("hook: missing required flag --event")
	}
	return Args{Client: model.ClientID(client), Native: native}, nil
}

// Deps are the injected collaborators. All fields are required except
// Faults (nil in production builds) and Spawner (nil disables GC spawning).
type Deps struct {
	Clock    sys.Clock
	Env      sys.Env
	FS       sys.FS
	Layout   layout.Layout
	Adapters ports.AdapterRegistry
	Rules    ports.RulesLoader
	Engine   ports.RuleEvaluator
	Bash     ports.BashParser
	PS       ports.PowerShellParser
	Paths    ports.PathService
	State    ports.StateStore
	Audit    ports.AuditSink
	Repo     ports.RepoLocator
	// Evidence loads project.json lazily itself (evidence.Options.Project);
	// the hook never reads project.json.
	Evidence ports.EvidenceProvider
	Injector ports.Injector
	Spawner  ports.Spawner
	Faults   FaultInjector
	// Self is the absolute path of the running binary (for detached children).
	Self string
	// Watchdog defaults to DefaultWatchdog when zero.
	Watchdog time.Duration
	// Exit terminates the process (os.Exit in production; tests intercept).
	Exit func(code int)
	// Version is recorded in audit records.
	Version string
}

// Result is the outcome of Run (before the stdout commit).
type Result struct {
	Stdout   []byte
	Decision *model.Decision
	Audit    []model.AuditRecord
	Event    *model.Event
}

// Main is the entry point used by cmd/ballast (`ballast hook ...`). It
// returns the process exit code (0 or 1); it calls deps.Exit only from the
// watchdog. It never panics (recovers and fails open).
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer, deps *Deps) (code int) {
	if stderr == nil {
		stderr = io.Discard
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if deps == nil {
		fmt.Fprintln(stderr, "ballast: hook: nil dependencies")
		return 1
	}
	if err := deps.validate(); err != nil {
		fmt.Fprintf(stderr, "ballast: %s\n", oneLine(err.Error()))
		return 1
	}
	wd := deps.watchdog()
	g := newGuard(stderr, deps.Exit)
	g.arm(wd)
	defer g.disarm()
	defer func() {
		if r := recover(); r != nil {
			g.fail(fmt.Sprintf("panic: %v", r))
			code = 1
		}
	}()

	start := deps.Clock.Now()
	args, err := ParseArgs(argv)
	if err != nil {
		g.fail(err.Error())
		return 1
	}
	in, err := readStdin(stdin)
	if err != nil {
		g.fail(err.Error())
		return 1
	}
	inv := &invocation{deps: deps, guard: g, start: start, wd: wd}
	res, post, err := inv.run(args, in)
	if err != nil {
		g.fail(err.Error())
		return 1
	}
	if err := g.commit(stdout, res.Stdout); err != nil {
		return 1
	}
	post()
	return 0
}

// Run executes the pipeline up to (not including) the stdout write, then the
// post-audit lifecycle cleanup (DropAgent/DropSession, daily GC spawn; their
// failures are ignored). Any returned error must lead to the exception path
// in Main. Main itself performs the cleanup only after the stdout commit.
func Run(args Args, stdin []byte, deps *Deps) (*Result, error) {
	if deps == nil {
		return nil, fmt.Errorf("hook: nil dependencies")
	}
	if err := deps.validate(); err != nil {
		return nil, err
	}
	wd := deps.watchdog()
	inv := &invocation{deps: deps, guard: newGuard(io.Discard, nil), start: deps.Clock.Now(), wd: wd}
	res, post, err := inv.run(args, stdin)
	if err != nil {
		return nil, err
	}
	post()
	return res, nil
}

// BuildDelta derives the StateDelta for an event, verdict and injection.
// readPaths are the already-normalized read-set additions (file-tool
// targets and shell Reads, see package doc). Exported for tests and U12
// integration only; replay (U9) does not import hook and keeps its own
// in-memory state model (internal/replay/doc.go).
func BuildDelta(ev *model.Event, rs *model.CompiledRuleSet, v *model.Verdict, inj *model.Injection, readPaths []string, now time.Time) *model.StateDelta {
	return buildDelta(ev, rs, v, inj, readPaths, now)
}

// ScopeFor returns the state scope key for an event (repo may be nil ⇒
// RepoInst "norepo-" + hex(sha256(normalized cwd))[:16]).
func ScopeFor(ev *model.Event, repo *model.RepoInfo, normCwd string) model.ScopeKey {
	return scopeFor(ev, repo, normCwd)
}

func (d *Deps) validate() error {
	var missing []string
	add := func(isNil bool, name string) {
		if isNil {
			missing = append(missing, name)
		}
	}
	add(d.Clock == nil, "Clock")
	add(d.Env == nil, "Env")
	add(d.FS == nil, "FS")
	add(d.Adapters == nil, "Adapters")
	add(d.Rules == nil, "Rules")
	add(d.Engine == nil, "Engine")
	add(d.Paths == nil, "Paths")
	add(d.State == nil, "State")
	add(d.Audit == nil, "Audit")
	add(d.Repo == nil, "Repo")
	add(d.Evidence == nil, "Evidence")
	add(d.Injector == nil, "Injector")
	if len(missing) > 0 {
		return fmt.Errorf("hook: missing dependencies: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (d *Deps) watchdog() time.Duration {
	if d.Watchdog > 0 {
		return d.Watchdog
	}
	return DefaultWatchdog
}

func (d *Deps) version() string {
	if d.Version != "" {
		return d.Version
	}
	return version.Version
}

func readStdin(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r, MaxStdinBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(b) > MaxStdinBytes {
		return nil, fmt.Errorf("stdin exceeds %d bytes", MaxStdinBytes)
	}
	return b, nil
}
