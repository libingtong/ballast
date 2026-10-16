package runner

import "github.com/libingtong/ballast/internal/model"

// P2 contract (architecture v2.0, docs/p2-design.md §3.3–§3.4): starting
// jobs on behalf of the hook (T1 path). Body owned by unit P2-RUNNER.
//
// The hook cannot import this package; it spawns
// model.HookStartArgv(self, req) through ports.Spawner, and the CLI
// (unit P2-CLI) turns the child's argv back into a request with
// model.ParseHookStartArgv and calls StartRequest.

// StartRequest resolves repo and project.json from req.Cwd, writes the
// model.VerifyRequestFile, supersedes running jobs of the same names, and
// starts one job per name exactly like Start, except that the first job's id
// is req.RequestID and every job is recorded with Origin "hook". On a config
// error it still writes the request file with Error set (so
// `verify --status <request_id>` can explain) and returns ErrConfig.
func StartRequest(opt Options, req model.HookStartRequest) ([]Started, error) {
	panic("unimplemented: runner.StartRequest (unit P2-RUNNER)")
}
