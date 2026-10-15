package cli

import (
	"io"

	"github.com/libingtong/ballast/internal/hook"
	"github.com/libingtong/ballast/internal/sys"
)

// Main dispatches argv (without the program name) and returns the exit code.
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	panic("unimplemented: cli.Main")
}

// WireHook builds hook.Deps from the real implementations of every unit:
// adapter.Default, config.NewLoader(dsl.NewEngine(...)), bashparse.Parser,
// psparse.Parser, paths.NewService, state.New, audit.NewWriter,
// snapshot.Locator, project.Loader, evidence.NewProvider, inject.Builder,
// runner.Spawner, hook.FaultsFromEnv. Must be cheap (no I/O).
func WireHook(env sys.Env) (*hook.Deps, error) {
	panic("unimplemented: cli.WireHook")
}
