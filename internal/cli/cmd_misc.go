package cli

import (
	"errors"
	"fmt"

	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/version"
)

func runVersion(a *app, c *command, args []string) int {
	if len(args) > 0 {
		return a.usageErr(c, "unexpected argument %q", args[0])
	}
	fmt.Fprintln(a.out, version.String())
	return ExitOK
}

// runGC removes expired session state, audit files, evidence and stale rules cache files.
// It is internal (spawned detached by SessionStart), so failures only
// go to stderr. It never touches gc.stamp: the hook alone claims the daily slot
// before spawning it (architecture.md §3.1).
func runGC(a *app, c *command, args []string) int {
	if len(args) > 0 {
		return a.usageErr(c, "unexpected argument %q", args[0])
	}
	var errs []error
	if _, err := a.stateStore().GC(0); err != nil {
		errs = append(errs, fmt.Errorf("state: %w", err))
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
	if err := errors.Join(errs...); err != nil {
		return a.errorf(ExitInfra, "gc: %v", err)
	}
	return ExitOK
}
