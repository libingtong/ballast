package cli

import (
	"github.com/libingtong/ballast/internal/adapter"
	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/dsl"
	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/hook"
	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/install"
	"github.com/libingtong/ballast/internal/paths"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/psparse"
	"github.com/libingtong/ballast/internal/replay"
	"github.com/libingtong/ballast/internal/runner"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/state"
	"github.com/libingtong/ballast/internal/stats"
)

// wiringSurface pins the constructors U12 wires together. It exists so the
// skeleton proves the full import graph is acyclic at contract-freeze time;
// U12 replaces it with real wiring in WireHook and the command handlers.
var wiringSurface = []any{
	adapter.Default,
	audit.NewWriter,
	bashparse.Parser{},
	config.NewLoader,
	dsl.NewEngine,
	evidence.NewProvider,
	hook.Main,
	inject.Builder{},
	install.Install,
	paths.NewService,
	project.Detect,
	psparse.Parser{},
	replay.Run,
	runner.Start,
	snapshot.RepoInst,
	state.New,
	stats.Compute,
}
