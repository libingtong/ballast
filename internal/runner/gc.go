package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// Retention of the P2 directories (docs/p2-design.md §8 E2).
const (
	// RequestRetention: requests/ files and quarantine/ entries older than
	// this are removed.
	RequestRetention = 30 * 24 * time.Hour
	// WorkRetention: a work/<job_id>/ leftover whose job is no longer
	// running is removed once it is older than this.
	WorkRetention = 24 * time.Hour
)

// GCCounts is what GCRequests removed.
type GCCounts struct {
	Requests   int // files under requests/ (request files and spawn logs)
	Quarantine int // entries under quarantine/
	Work       int // directories under work/
}

// GCRequests (P2 v1.5, §8 E2; wired into `ballast gc` by P2-CLI) removes
//
//   - files under Layout.RequestsDir older than RequestRetention (and the
//     repo_inst directories that end up empty),
//   - entries under Layout.QuarantineDir older than RequestRetention,
//   - directories under Layout.WorkDir older than WorkRetention whose job is
//     finished, unknown (no job file) or no longer alive.
//
// Symlinks are removed as links, never followed. A directory that cannot be
// removed is reported in the returned error (joined) and does not stop the
// pass. Missing directories are fine.
func GCRequests(opt Options) (GCCounts, error) {
	opt = opt.withDefaults()
	var (
		n    GCCounts
		errs []error
		now  = opt.Clock.Now()
	)
	old := func(fi fs.FileInfo, d time.Duration) bool { return now.Sub(fi.ModTime()) > d }

	if dir := opt.Layout.RequestsDir; dir != "" {
		insts, err := opt.FS.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, inst := range insts {
			ip := filepath.Join(dir, inst.Name())
			fi, err := opt.FS.Lstat(ip)
			if err != nil {
				continue
			}
			if !fi.IsDir() {
				if old(fi, RequestRetention) {
					if err := opt.FS.Remove(ip); err == nil {
						n.Requests++
					} else {
						errs = append(errs, err)
					}
				}
				continue
			}
			files, err := opt.FS.ReadDir(ip)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			left := len(files)
			for _, f := range files {
				fp := filepath.Join(ip, f.Name())
				ffi, err := opt.FS.Lstat(fp)
				if err != nil || !old(ffi, RequestRetention) {
					continue
				}
				if err := opt.FS.RemoveAll(fp); err != nil {
					errs = append(errs, err)
					continue
				}
				n.Requests++
				left--
			}
			if left == 0 {
				_ = opt.FS.Remove(ip) // only succeeds when empty
			}
		}
	}

	if dir := opt.Layout.QuarantineDir; dir != "" {
		ents, err := opt.FS.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			fi, err := opt.FS.Lstat(p)
			if err != nil || !old(fi, RequestRetention) {
				continue
			}
			if err := removeTreeRoot(opt, p); err != nil {
				errs = append(errs, err)
				continue
			}
			n.Quarantine++
		}
	}

	if dir := opt.Layout.WorkDir; dir != "" {
		ents, err := opt.FS.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			fi, err := opt.FS.Lstat(p)
			if err != nil || !old(fi, WorkRetention) {
				continue
			}
			if js, _, ferr := opt.Evidence.FindJob(e.Name()); ferr == nil && js != nil &&
				js.State == model.JobRunning && opt.Evidence.JobActive(js) {
				continue // still in use
			}
			if err := removeTreeRoot(opt, p); err != nil {
				errs = append(errs, fmt.Errorf("work/%s: %w", e.Name(), err))
				continue
			}
			n.Work++
		}
	}
	return n, errors.Join(errs...)
}
