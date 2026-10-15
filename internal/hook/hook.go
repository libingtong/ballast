package hook

import (
	"io"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
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
	panic("unimplemented: hook.ParseArgs")
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
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer, deps *Deps) int {
	panic("unimplemented: hook.Main")
}

// Run executes the pipeline up to (not including) the stdout write. Any
// returned error must lead to the exception path in Main.
func Run(args Args, stdin []byte, deps *Deps) (*Result, error) {
	panic("unimplemented: hook.Run")
}

// BuildDelta derives the StateDelta for an event, verdict and injection.
// readPaths are the already-normalized read-set additions (file-tool
// targets and shell Reads, see package doc). Exported for tests and U12
// integration only; replay (U9) does not import hook and keeps its own
// in-memory state model (internal/replay/doc.go).
func BuildDelta(ev *model.Event, rs *model.CompiledRuleSet, v *model.Verdict, inj *model.Injection, readPaths []string, now time.Time) *model.StateDelta {
	panic("unimplemented: hook.BuildDelta")
}

// ScopeFor returns the state scope key for an event (repo may be nil ⇒
// RepoInst "norepo-" + hex(sha256(normalized cwd))[:16]).
func ScopeFor(ev *model.Event, repo *model.RepoInfo, normCwd string) model.ScopeKey {
	panic("unimplemented: hook.ScopeFor")
}
