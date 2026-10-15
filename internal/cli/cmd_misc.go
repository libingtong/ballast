package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/version"
)

func runVersion(a *app, c *command, args []string) int {
	if len(args) > 0 {
		return a.usageErr(c, "unexpected argument %q", args[0])
	}
	fmt.Fprintln(a.out, version.String())
	return ExitOK
}

// runGC removes expired session state, audit files, evidence and stale rules cache files, then
// refreshes gc.stamp. It is internal (spawned detached by SessionStart), so
// failures only go to stderr.
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
	if err := touchStamp(a.fs, a.lay.GCStamp, a.clock.Now()); err != nil {
		errs = append(errs, fmt.Errorf("gc.stamp: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return a.errorf(ExitInfra, "gc: %v", err)
	}
	return ExitOK
}

func touchStamp(fsys sys.FS, path string, now time.Time) error {
	if err := fsys.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := fsys.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(path, now, now)
}
