package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const (
	// JobStartGrace is how long a job whose PID is not yet known (the
	// detached child records it when it starts) counts as running.
	JobStartGrace = 30 * time.Second

	// maxRunSpan bounds t_end - t_start of a record: project.json caps
	// timeout_s at 86400, plus snapshot time.
	maxRunSpan = 25 * time.Hour

	recordSuffix = ".json"
	specSuffix   = ".spec.json" // runner sidecar next to a job file
	// supersededSuffix marks a job as superseded; created exclusively, never
	// rewritten, so it cannot be lost to a read-modify-write of the job file.
	supersededSuffix = ".superseded"
	logSuffix        = ".log"

	// tsDigits is the width of the zero-padded t_start in record file names.
	tsDigits = 20
)

// errCorrupt marks an evidence or job file that cannot be decoded.
var errCorrupt = errors.New("evidence: corrupt file")

func (o Options) withDefaults() Options {
	if o.FS == nil {
		o.FS = sys.OSFS{}
	}
	if o.Clock == nil {
		o.Clock = sys.RealClock{}
	}
	if o.IDs == nil {
		o.IDs = sys.RandomID{}
	}
	return o
}

// safeComponent reports whether s can be one path element.
func safeComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") && !strings.Contains(s, ":")
}

func checkComponents(parts ...string) error {
	for _, p := range parts {
		if !safeComponent(p) {
			return fmt.Errorf("evidence: unsafe path component %q", p)
		}
	}
	return nil
}

func recordFileName(tStart time.Time, jobID string) string {
	ns := tStart.UnixNano()
	if ns < 0 {
		ns = 0
	}
	return fmt.Sprintf("%0*d-%s%s", tsDigits, ns, jobID, recordSuffix)
}

// recordNS parses the t_start prefix of a record file name.
func recordNS(name string) (int64, bool) {
	if len(name) < tsDigits+1 || name[tsDigits] != '-' {
		return 0, false
	}
	for i := 0; i < tsDigits; i++ {
		if name[i] < '0' || name[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(name[:tsDigits], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func isRecordFile(name string) bool {
	return strings.HasSuffix(name, recordSuffix) && !strings.Contains(name, ".tmp.")
}

func isJobFile(name string) bool {
	return strings.HasSuffix(name, recordSuffix) && !strings.HasSuffix(name, specSuffix) && !strings.Contains(name, ".tmp.")
}

// RecordPath returns the evidence file path for rec.
func (s *Store) recordPath(rec *model.VerifyRecord) string {
	return filepath.Join(s.opt.Layout.EvidenceDir, rec.RepoInst, rec.Name, recordFileName(rec.TStart, rec.JobID))
}

// JobPath returns the JobStatus file path of a job.
func (s *Store) JobPath(repoInst, name, jobID string) string {
	return filepath.Join(s.opt.Layout.JobsDir, repoInst, name, jobID+recordSuffix)
}

// LogPath returns the runner log path of a job.
func (s *Store) LogPath(repoInst, name, jobID string) string {
	return filepath.Join(s.opt.Layout.JobsDir, repoInst, name, jobID+logSuffix)
}

// SpecPath returns the path of the runner's sidecar for a job (the resolved
// verify entry, repo root and exempt list captured when the job was
// created). The store only knows the name so that GC can remove it.
func (s *Store) SpecPath(repoInst, name, jobID string) string {
	return filepath.Join(s.opt.Layout.JobsDir, repoInst, name, jobID+specSuffix)
}

// SupersededPath returns the path of a job's supersede marker: the file whose
// existence tells the job's runner that a newer run replaced it.
func (s *Store) SupersededPath(repoInst, name, jobID string) string {
	return filepath.Join(s.opt.Layout.JobsDir, repoInst, name, jobID+supersededSuffix)
}

func (s *Store) writeRecord(rec *model.VerifyRecord) error {
	if err := checkComponents(rec.RepoInst, rec.Name, rec.JobID); err != nil {
		return err
	}
	out := *rec
	if out.V == 0 {
		out.V = 1
	}
	data, err := json.Marshal(&out)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return sys.WriteFileAtomic(s.opt.FS, s.opt.IDs, s.recordPath(&out), data, 0o600, true)
}

// latestName returns the lexicographically greatest record file name in dir
// ("" when none). A missing directory is not an error.
func (s *Store) latestName(dir string) (string, error) {
	ents, err := s.opt.FS.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	best := ""
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !isRecordFile(n) {
			continue
		}
		if n > best {
			best = n
		}
	}
	return best, nil
}

func (s *Store) readRecord(p string) (*model.VerifyRecord, error) {
	data, err := s.opt.FS.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var rec model.VerifyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errCorrupt, p, err)
	}
	return &rec, nil
}

func (s *Store) latest(repoInst, name string) (*model.VerifyRecord, error) {
	if err := checkComponents(repoInst, name); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.opt.Layout.EvidenceDir, repoInst, name)
	best, err := s.latestName(dir)
	if err != nil || best == "" {
		return nil, err
	}
	rec, err := s.readRecord(filepath.Join(dir, best))
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil, nil // removed by GC between ReadDir and ReadFile
	}
	return rec, err
}

func (s *Store) finishedSince(repoInst string, since time.Time) ([]model.VerifyRecord, error) {
	if err := checkComponents(repoInst); err != nil {
		return nil, err
	}
	root := filepath.Join(s.opt.Layout.EvidenceDir, repoInst)
	names, err := s.opt.FS.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	floor := since.Add(-maxRunSpan).UnixNano()
	var out []model.VerifyRecord
	for _, nd := range names {
		if !nd.IsDir() {
			continue
		}
		dir := filepath.Join(root, nd.Name())
		ents, err := s.opt.FS.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !isRecordFile(n) {
				continue
			}
			if ns, ok := recordNS(n); ok && ns < floor {
				continue
			}
			rec, err := s.readRecord(filepath.Join(dir, n))
			if err != nil || !rec.TEnd.After(since) {
				continue
			}
			out = append(out, *rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].TEnd.Equal(out[j].TEnd) {
			return out[i].TEnd.Before(out[j].TEnd)
		}
		return out[i].JobID < out[j].JobID
	})
	return out, nil
}

func (s *Store) readJob(p string) (*model.JobStatus, error) {
	data, err := s.opt.FS.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var js model.JobStatus
	if err := json.Unmarshal(data, &js); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", errCorrupt, p, err)
	}
	return &js, nil
}

// ReadJob reads the JobStatus of a known (repoInst, name, jobID); a missing
// file yields (nil, nil).
func (s *Store) ReadJob(repoInst, name, jobID string) (*model.JobStatus, error) {
	if err := checkComponents(repoInst, name, jobID); err != nil {
		return nil, err
	}
	js, err := s.readJob(s.JobPath(repoInst, name, jobID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return js, err
}

func (s *Store) findJob(jobID string) (*model.JobStatus, *model.VerifyRecord, error) {
	if err := checkComponents(jobID); err != nil {
		return nil, nil, err
	}
	var js *model.JobStatus
	jobRoot := s.opt.Layout.JobsDir
	insts, err := s.opt.FS.ReadDir(jobRoot)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
search:
	for _, in := range insts {
		if !in.IsDir() {
			continue
		}
		names, err := s.opt.FS.ReadDir(filepath.Join(jobRoot, in.Name()))
		if err != nil {
			continue
		}
		for _, nm := range names {
			if !nm.IsDir() {
				continue
			}
			j, err := s.readJob(filepath.Join(jobRoot, in.Name(), nm.Name(), jobID+recordSuffix))
			if err == nil {
				js = j
				break search
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, nil, err
			}
		}
	}
	rec, err := s.findRecordOf(jobID, js)
	return js, rec, err
}

// findRecordOf locates the record of a job: directly when the job file
// tells repo and name, else by scanning every name directory.
func (s *Store) findRecordOf(jobID string, js *model.JobStatus) (*model.VerifyRecord, error) {
	evRoot := s.opt.Layout.EvidenceDir
	suffix := "-" + jobID + recordSuffix
	scan := func(dir string) (*model.VerifyRecord, error) {
		ents, err := s.opt.FS.ReadDir(dir)
		if err != nil {
			return nil, nil
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) && isRecordFile(e.Name()) {
				rec, err := s.readRecord(filepath.Join(dir, e.Name()))
				if errors.Is(err, fs.ErrNotExist) {
					return nil, nil
				}
				return rec, err
			}
		}
		return nil, nil
	}
	if js != nil && checkComponents(js.RepoInst, js.Name) == nil {
		return scan(filepath.Join(evRoot, js.RepoInst, js.Name))
	}
	insts, err := s.opt.FS.ReadDir(evRoot)
	if err != nil {
		return nil, nil
	}
	for _, in := range insts {
		if !in.IsDir() {
			continue
		}
		names, err := s.opt.FS.ReadDir(filepath.Join(evRoot, in.Name()))
		if err != nil {
			continue
		}
		for _, nm := range names {
			if !nm.IsDir() {
				continue
			}
			if rec, err := scan(filepath.Join(evRoot, in.Name(), nm.Name())); rec != nil || err != nil {
				return rec, err
			}
		}
	}
	return nil, nil
}

func (s *Store) writeJob(js *model.JobStatus) error {
	if err := checkComponents(js.RepoInst, js.Name, js.JobID); err != nil {
		return err
	}
	out := *js
	if out.V == 0 {
		out.V = 1
	}
	data, err := json.Marshal(&out)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return sys.WriteFileAtomic(s.opt.FS, s.opt.IDs, s.JobPath(out.RepoInst, out.Name, out.JobID), data, 0o600, false)
}

// JobActive reports whether js is a running job: state running, before its
// deadline, and its process alive (a job whose PID is not recorded yet counts
// as running during JobStartGrace).
func (s *Store) JobActive(js *model.JobStatus) bool {
	if js == nil || js.State != model.JobRunning {
		return false
	}
	now := s.opt.Clock.Now()
	if !now.Before(js.Deadline) {
		return false
	}
	if js.PID > 0 {
		return ProcessAlive(js.PID)
	}
	return now.Sub(js.StartedAt) < JobStartGrace
}

func (s *Store) runningJobs(repoInst, name string) ([]model.JobStatus, error) {
	if err := checkComponents(repoInst, name); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.opt.Layout.JobsDir, repoInst, name)
	ents, err := s.opt.FS.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	// A running job started less than the longest timeout ago. Job ids start
	// with their UTC start time, so older files are skipped unread.
	floor := s.opt.Clock.Now().Add(-maxRunSpan)
	var out []model.JobStatus
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !isJobFile(n) {
			continue
		}
		if t, ok := jobIDTime(strings.TrimSuffix(n, recordSuffix)); ok && t.Before(floor) {
			continue
		}
		js, err := s.readJob(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		if s.JobActive(js) {
			out = append(out, *js)
		}
	}
	return out, nil
}

// jobIDTime parses the "20060102T150405" prefix of a job id.
func jobIDTime(id string) (time.Time, bool) {
	if len(id) < 15 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405", id[:15])
	return t, err == nil
}

// gc applies retention.
func (s *Store) gc() error {
	var errs []error
	now := s.opt.Clock.Now()
	fsys := s.opt.FS
	note := func(err error) {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	each := func(root string, fn func(dir, name string)) {
		ents, err := fsys.ReadDir(root)
		if err != nil {
			note(err)
			return
		}
		for _, e := range ents {
			if e.IsDir() {
				fn(filepath.Join(root, e.Name()), e.Name())
			}
		}
	}

	cutoff := now.Add(-RecordRetention).UnixNano()
	each(s.opt.Layout.EvidenceDir, func(instDir, _ string) {
		each(instDir, func(dir, _ string) {
			ents, err := fsys.ReadDir(dir)
			if err != nil {
				note(err)
				return
			}
			newest := ""
			for _, e := range ents {
				if !e.IsDir() && isRecordFile(e.Name()) && e.Name() > newest {
					newest = e.Name()
				}
			}
			for _, e := range ents {
				n := e.Name()
				if e.IsDir() || !isRecordFile(n) || n == newest {
					continue
				}
				if ns, ok := recordNS(n); ok && ns < cutoff {
					note(fsys.Remove(filepath.Join(dir, n)))
				}
			}
			_ = fsys.Remove(dir) // only succeeds when empty
		})
		_ = fsys.Remove(instDir)
	})

	jobCutoff := now.Add(-JobRetention)
	each(s.opt.Layout.JobsDir, func(instDir, _ string) {
		each(instDir, func(dir, _ string) {
			ents, err := fsys.ReadDir(dir)
			if err != nil {
				note(err)
				return
			}
			for _, e := range ents {
				n := e.Name()
				if e.IsDir() || !isJobFile(n) {
					continue
				}
				id := strings.TrimSuffix(n, recordSuffix)
				p := filepath.Join(dir, n)
				js, err := s.readJob(p)
				if err != nil {
					if errors.Is(err, errCorrupt) {
						if t, ok := jobIDTime(id); ok && t.Before(jobCutoff) {
							s.removeJobFiles(dir, id, note)
						}
					}
					continue
				}
				if js.State == model.JobRunning && !s.JobActive(js) {
					js.State, js.Outcome = model.JobDone, model.OutcomeInfra
					note(s.writeJob(js))
				}
				if js.State == model.JobDone && js.StartedAt.Before(jobCutoff) {
					s.removeJobFiles(dir, id, note)
				}
			}
			// Logs and sidecars whose job file is gone.
			ents, _ = fsys.ReadDir(dir)
			present := map[string]bool{}
			for _, e := range ents {
				if isJobFile(e.Name()) {
					present[strings.TrimSuffix(e.Name(), recordSuffix)] = true
				}
			}
			for _, e := range ents {
				n := e.Name()
				var id string
				switch {
				case strings.HasSuffix(n, specSuffix):
					id = strings.TrimSuffix(n, specSuffix)
				case strings.HasSuffix(n, supersededSuffix):
					id = strings.TrimSuffix(n, supersededSuffix)
				case strings.HasSuffix(n, logSuffix):
					id = strings.TrimSuffix(n, logSuffix)
				default:
					continue
				}
				if t, ok := jobIDTime(id); ok && !present[id] && t.Before(jobCutoff) {
					note(fsys.Remove(filepath.Join(dir, n)))
				}
			}
			_ = fsys.Remove(dir)
		})
		_ = fsys.Remove(instDir)
	})
	return errors.Join(errs...)
}

func (s *Store) removeJobFiles(dir, id string, note func(error)) {
	for _, suf := range []string{recordSuffix, specSuffix, logSuffix, supersededSuffix} {
		note(s.opt.FS.Remove(filepath.Join(dir, id+suf)))
	}
}
