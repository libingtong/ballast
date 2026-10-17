package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/runner"
	"github.com/libingtong/ballast/internal/state"
	"github.com/libingtong/ballast/internal/version"
)

func runVersion(a *app, c *command, args []string) int {
	if len(args) > 0 {
		return a.usageErr(c, "unexpected argument %q", args[0])
	}
	fmt.Fprintln(a.out, version.String())
	return ExitOK
}

// tryGCLock takes the non-blocking exclusive lock on gc.lock. ok is false only
// when another gc holds it; any other failure (no writable home, ...) proceeds
// without the lock, since the state claim in state.GCFlush keeps the counts
// exactly-once regardless.
func (a *app) tryGCLock() (unlock func(), ok bool) {
	if a.lay.GCLock == "" {
		return nil, true
	}
	if err := a.fs.MkdirAll(filepath.Dir(a.lay.GCLock), 0o700); err != nil {
		return nil, true
	}
	f, err := a.fs.OpenFile(a.lay.GCLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, true
	}
	release, _, err := state.TryLock(f, 0, a.clock)
	if err != nil {
		f.Close()
		return nil, !errors.Is(err, model.ErrLockTimeout)
	}
	return func() { _ = release(); f.Close() }, true
}

// runGC removes expired session state, audit files, evidence, stale rules cache files and (P2) old hook-start request files, quarantine entries and work-tree leftovers.
// It is internal (spawned detached by SessionStart), so failures only
// go to stderr. It never touches gc.stamp: the hook alone claims the daily slot
// before spawning it (architecture.md §3.1).
func runGC(a *app, c *command, args []string) int {
	if len(args) > 0 {
		return a.usageErr(c, "unexpected argument %q", args[0])
	}
	// One gc at a time: a manual run overlapping a hook-spawned one (or two
	// hook-spawned ones) would otherwise scan the same expired data twice.
	// Losing the race is not an error.
	if unlock, ok := a.tryGCLock(); !ok {
		return ExitOK
	} else if unlock != nil {
		defer unlock()
	}
	var errs []error
	// Expired state may still hold eval counts that no Stop/SessionEnd ever
	// flushed; they are written as eval_summary(cause=gc) so the stats
	// denominators stay whole. State GC runs before audit GC on purpose.
	// Tier is left unset: this process never probes it, and the audit contract
	// (architecture §14 v2.0.9) forbids claiming T0 for a host it never looked at.
	var flushed []model.AuditRecord
	now := a.clock.Now()
	_, err := a.stateStore().GCFlush(0, func(k model.ScopeKey, evals map[string]model.EvalCount) {
		rec := model.AuditRecord{
			TS:             now.UTC(),
			V:              model.AuditVersion,
			Type:           model.AuditEvalSummary,
			SessionID:      k.Session,
			RepoInst:       k.RepoInst,
			BallastVersion: version.Version,
			Evals:          evals,
			Detail:         json.RawMessage(`{"cause":"gc"}`),
		}
		if k.Agent != "main" {
			rec.AgentID = k.Agent
		}
		flushed = append(flushed, rec)
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("state: %w", err))
	}
	if len(flushed) > 0 {
		if err := a.auditWriter().Append(flushed); err != nil {
			errs = append(errs, fmt.Errorf("audit flush: %w", err))
		}
	}
	if _, err := audit.GC(a.fs, a.lay.AuditDir, a.clock.Now(), 0); err != nil {
		errs = append(errs, fmt.Errorf("audit: %w", err))
	}
	if err := evidence.NewStore(a.evidenceOptions()).GC(); err != nil {
		errs = append(errs, fmt.Errorf("evidence: %w", err))
	}
	if _, err := config.GCCache(a.fs, a.lay.CacheDir, a.clock.Now(), 0); err != nil {
		errs = append(errs, fmt.Errorf("cache: %w", err))
	}
	// P2: requests/ and quarantine/ older than 30 days and work/<job>
	// leftovers of finished jobs older than 24 hours (runner.GCRequests).
	if _, err := runner.GCRequests(runner.Options{Layout: a.lay, FS: a.fs, Clock: a.clock, IDs: a.ids, Env: a.env}); err != nil {
		errs = append(errs, fmt.Errorf("requests: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return a.errorf(ExitInfra, "gc: %v", err)
	}
	return ExitOK
}
