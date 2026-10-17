package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Frozen limits (plan B6).
const (
	LockBudget        = 20 * time.Millisecond
	LockRetryInterval = 200 * time.Microsecond
	SeenCap           = 512
	RingCap           = 8
	ReadCap           = 4096
	ReportedJobsCap   = 64
	MaxAssistantRunes = 4000
	MaxPromptTailRune = 200
	TTL               = 7 * 24 * time.Hour
	RecordVersion     = 1
)

// Options configures a Store. Nil FS, Clock and IDs default to the real
// implementations.
type Options struct {
	Layout layout.Layout
	FS     sys.FS
	Clock  sys.Clock
	IDs    sys.IDGen
}

// Store implements ports.StateStore.
type Store struct {
	opt Options
}

var _ ports.StateStore = (*Store)(nil)

// New returns a Store.
func New(opt Options) *Store {
	if opt.FS == nil {
		opt.FS = sys.OSFS{}
	}
	if opt.Clock == nil {
		opt.Clock = sys.RealClock{}
	}
	if opt.IDs == nil {
		opt.IDs = sys.RandomID{}
	}
	return &Store{opt: opt}
}

// Load implements ports.StateStore.
func (s *Store) Load(key model.ScopeKey) (model.SessionState, model.StateLoadInfo, error) {
	rec, info, err := s.read(s.Path(key))
	return rec, info, err
}

// read loads and decodes the state file at path. It never returns a nil
// record: absent, corrupt and unreadable files all yield an empty one.
func (s *Store) read(path string) (*Record, model.StateLoadInfo, error) {
	data, err := s.opt.FS.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewRecord(), model.StateLoadInfo{Readable: true}, nil
		}
		return NewRecord(), model.StateLoadInfo{}, err
	}
	info := model.StateLoadInfo{Exists: true, Readable: true, Bytes: len(data)}
	rec, ok := decodeRecord(data)
	if !ok {
		info.Corrupt = true
		rec = NewRecord()
	}
	return rec, info, nil
}

// Commit implements ports.StateStore.
func (s *Store) Commit(key model.ScopeKey, d *model.StateDelta) (model.StateCommitInfo, error) {
	var info model.StateCommitInfo
	if d == nil {
		return info, nil
	}
	delta := *d
	if delta.Now.IsZero() {
		delta.Now = s.opt.Clock.Now()
	}
	path := s.Path(key)

	lf, unlock, waited, err := s.acquire(path, lockCreateDirs)
	info.LockWait = waited
	if err != nil {
		if errors.Is(err, model.ErrLockTimeout) {
			info.LockTimeout = true
			return info, nil
		}
		return info, err
	}
	defer lf.Close() // closing also drops the OS lock should unlock fail
	defer func() { _ = unlock() }()

	rec, load, err := s.read(path)
	if err != nil {
		return info, err
	}
	info.Corrupt = load.Corrupt

	info.Applied, info.Duplicate = rec.Merge(&delta)
	info.EventSeq = rec.Seq
	if !info.Applied {
		return info, nil
	}
	data, err := rec.encode()
	if err != nil {
		info.Applied = false
		return info, err
	}
	if err := s.writeAtomic(path, data); err != nil {
		info.Applied = false
		return info, err
	}
	return info, nil
}

// openLock opens (creating when needed) the lock sibling of path.
func (s *Store) openLock(path string) (sys.File, error) {
	lp := path + ".lock"
	var f sys.File
	var err error
	// A concurrent SessionEnd may remove the (emptied) directory between the
	// failed open and MkdirAll, so the pair is retried a few times.
	for i := 0; i < 3; i++ {
		f, err = s.opt.FS.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			return f, err
		}
		if err = s.opt.FS.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	return s.opt.FS.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
}

type lockMode int

const (
	lockExisting   lockMode = iota // the lock file must exist
	lockCreate                     // create the lock file; the directory must exist
	lockCreateDirs                 // create the lock file and its directories
)

// acquire opens the lock sibling of path (creating it per mode) and
// takes the OS lock within LockBudget. SessionEnd cleanup may unlink a lock
// file while holding it, so a process that opened the old file and then won
// the lock on an orphaned inode must not proceed: after locking, the open
// file has to still be the one at the lock path, otherwise it retries on the
// current one. (Windows cannot unlink an open file, so it skips the check.)
// The returned file is open and locked; the caller closes it.
func (s *Store) acquire(path string, mode lockMode) (lf sys.File, unlock func() error, waited time.Duration, err error) {
	lp := path + ".lock"
	for attempt := 0; ; attempt++ {
		switch mode {
		case lockCreateDirs:
			lf, err = s.openLock(path)
		case lockCreate:
			lf, err = s.opt.FS.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
		default:
			lf, err = s.opt.FS.OpenFile(lp, os.O_RDWR, 0o600)
		}
		if err != nil {
			return nil, nil, waited, err
		}
		var w time.Duration
		unlock, w, err = TryLock(lf, LockBudget, s.opt.Clock)
		waited += w
		if err != nil {
			lf.Close()
			return nil, nil, waited, err
		}
		if runtime.GOOS == "windows" || attempt >= 4 || s.lockIsCurrent(lf, lp) {
			return lf, unlock, waited, nil
		}
		_ = unlock()
		lf.Close()
	}
}

// lockIsCurrent reports whether the open lock file is still the file at lp.
func (s *Store) lockIsCurrent(lf sys.File, lp string) bool {
	a, err := lf.Stat()
	if err != nil {
		return true
	}
	b, err := s.opt.FS.Stat(lp)
	if err != nil {
		return false // unlinked
	}
	return os.SameFile(a, b)
}

// stateWriteBudget bounds how long the state write may keep retrying a
// transient Windows collision (see sys/retry.go) while the state lock is held.
// Waiters give up after LockBudget (20 ms); a holder that retries for much
// longer than that only turns every waiter into a lock timeout (a dropped
// state delta), so the retry is kept to about the waiters' own budget. A
// collision that outlasts it is reported like any I/O error: the delta is
// dropped, audited, and the hook fails open.
const stateWriteBudget = 20 * time.Millisecond

// writeAtomic replaces path via a temp file and rename, without fsync. On
// Windows the rename can fail while a lock-free reader still has the file
// open (Go opens without FILE_SHARE_DELETE), so it is retried briefly, within
// stateWriteBudget overall.
func (s *Store) writeAtomic(path string, data []byte) error {
	if runtime.GOOS != "windows" {
		return sys.WriteFileAtomic(s.opt.FS, s.opt.IDs, path, data, 0o600, false)
	}
	deadline := time.Now().Add(stateWriteBudget)
	var err error
	for i := 0; i < 8; i++ {
		remaining := time.Until(deadline)
		if remaining < time.Millisecond {
			remaining = time.Millisecond
		}
		if err = sys.WriteFileAtomicWithin(s.opt.FS, s.opt.IDs, path, data, 0o600, false, remaining); err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return err
}

// DropAgent implements ports.StateStore. The state file is removed while
// holding its lock, and "<file>.lock" itself is not unlinked here (the
// session may go on; only DropSession unlinks lock files, while holding
// them, and acquire detects an orphaned inode). Orphans go away with their
// session directory in DropSession or GC. Drop is best-effort cleanup: a lock
// timeout leaves the file for GC and returns nil (contract v1.3).
func (s *Store) DropAgent(key model.ScopeKey) error {
	return s.dropLocked(s.Path(key))
}

// DropSession implements ports.StateStore: every agent state file of the
// session is dropped as in DropAgent, then the now orphaned lock files and the
// empty session directory are removed best effort (see cleanupSessionDir);
// whatever survives is left to GC. Missing ⇒ nil.
func (s *Store) DropSession(key model.ScopeKey) error {
	dir := filepath.Dir(s.Path(key))
	ents, err := s.opt.FS.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var first error
	for _, de := range ents {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if err := s.dropLocked(filepath.Join(dir, name)); err != nil && first == nil {
			first = err
		}
	}
	s.cleanupSessionDir(dir)
	return first
}

// cleanupSessionDir is the best-effort tail of DropSession: it unlinks the
// lock files whose state file is gone (each only while holding its lock, see
// acquire) and then removes the session directory and its repo directory when
// empty. Every failure is ignored; the TTL GC is the backstop.
func (s *Store) cleanupSessionDir(dir string) {
	ents, err := s.opt.FS.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range ents {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json.lock") {
			continue
		}
		state := filepath.Join(dir, strings.TrimSuffix(name, ".lock"))
		if _, err := s.opt.FS.Lstat(state); !errors.Is(err, fs.ErrNotExist) {
			continue // state still there (lock timeout, error): leave both
		}
		lf, unlock, _, err := s.acquire(state, lockExisting)
		if err != nil {
			continue
		}
		_ = s.opt.FS.Remove(state + ".lock") // fails on Windows while open: left to GC
		_ = unlock()
		lf.Close()
	}
	if s.opt.FS.Remove(dir) == nil {
		_ = s.opt.FS.Remove(filepath.Dir(dir)) // the repo directory, only if now empty
	}
}

// dropLocked removes one state file under its lock (see DropAgent).
func (s *Store) dropLocked(path string) error {
	if _, err := s.opt.FS.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	lf, unlock, _, err := s.acquire(path, lockCreate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // the session directory vanished (GC)
		}
		if errors.Is(err, model.ErrLockTimeout) {
			return nil
		}
		return err
	}
	defer lf.Close()
	defer unlock()
	tries := 1
	if runtime.GOOS == "windows" {
		tries = 8 // a lock-free reader may hold the file open (no FILE_SHARE_DELETE)
	}
	for i := 0; ; i++ {
		err = s.opt.FS.Remove(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if i+1 >= tries {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

// GCStats reports a GC run.
type GCStats struct {
	ScannedSessions int
	RemovedSessions int
	FlushedEvals    int // agent state files whose residual eval counts were handed to the flush callback
	Errors          int
}

// EvalFlush receives the residual per-rule eval counts of one expired agent
// state (key components are the sanitized directory and file names). GC
// calls it only after the session directory was removed, so a failed removal
// never reports the same counts twice.
type EvalFlush func(key model.ScopeKey, evals map[string]model.EvalCount)

// GC removes state older than ttl (relative to Clock.Now). A session
// directory is removed when its newest file (the directory itself when it
// holds no file) was modified more than ttl ago; repo directories left empty
// are removed too. ttl <= 0 means TTL. A missing state directory is not an
// error; per-entry failures are counted in Errors and the run continues.
func (s *Store) GC(ttl time.Duration) (GCStats, error) {
	return s.GCFlush(ttl, nil)
}

// GCFlush is GC that also reports the residual eval counts of every expired
// agent state to flush (nil = discard), so the shadow-mode denominators of
// sessions that never reached a flushing event are not lost (plan B2).
func (s *Store) GCFlush(ttl time.Duration, flush EvalFlush) (GCStats, error) {
	var st GCStats
	if ttl <= 0 {
		ttl = TTL
	}
	cutoff := s.opt.Clock.Now().Add(-ttl)
	root := s.opt.Layout.StateDir
	repos, err := s.opt.FS.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	s.sweepClaims(root)
	for _, repo := range repos {
		if !repo.IsDir() || strings.HasPrefix(repo.Name(), ".") {
			continue // repo components are sanitized and never start with a dot
		}
		repoPath := filepath.Join(root, repo.Name())
		sessions, err := s.opt.FS.ReadDir(repoPath)
		if err != nil {
			st.Errors++
			continue
		}
		remaining := 0
		for _, sess := range sessions {
			if !sess.IsDir() {
				remaining++
				continue
			}
			st.ScannedSessions++
			sessPath := filepath.Join(repoPath, sess.Name())
			newest, err := s.newestMTime(sessPath, sess)
			if err != nil {
				st.Errors++
				remaining++
				continue
			}
			if !newest.Before(cutoff) {
				remaining++
				continue
			}
			// Claim the session directory atomically before reading its
			// counts: concurrent `ballast gc` processes (a hook-spawned one
			// overlapping a manual run) all list the same expired session, and
			// only the rename winner may flush it. Reading first and deleting
			// afterwards would let both flush (RemoveAll of an already removed
			// path succeeds).
			claimed, cerr := s.claimSession(root, sessPath)
			if cerr != nil {
				if errors.Is(cerr, fs.ErrNotExist) {
					continue // another process claimed (or removed) it
				}
				st.Errors++
				remaining++
				continue
			}
			var residual []residualEvals
			if flush != nil {
				residual = s.residualEvals(claimed, repo.Name(), sess.Name())
			}
			if err := retryWindows(func() error { return s.opt.FS.RemoveAll(claimed) }); err != nil {
				// Not reported. Put what is left back so a later run retries
				// (and reports once, after a successful removal); when that
				// fails too, the claim is swept and discarded by a later run.
				st.Errors++
				if s.opt.FS.Rename(claimed, sessPath) == nil {
					remaining++
				}
				continue
			}
			st.RemovedSessions++
			for _, r := range residual {
				st.FlushedEvals++
				flush(r.key, r.evals)
			}
		}
		if remaining == 0 {
			_ = s.opt.FS.Remove(repoPath) // fails harmlessly if a session appeared meanwhile
		}
	}
	return st, nil
}

// claimDirName is the directory (under the state root, outside every repo
// directory so a second GC never lists a claimed session as a session) that
// holds session directories a GC run has claimed and not yet deleted.
const claimDirName = ".gc"

// claimStale is the age after which a claim left behind by a crashed GC run
// is discarded without being reported.
const claimStale = time.Hour

// claimSession atomically moves sessPath to <root>/.gc/<unix-ns>-<pid>-<id>.
// The error wraps fs.ErrNotExist when the directory is already gone, i.e.
// another process claimed it first.
func (s *Store) claimSession(root, sessPath string) (string, error) {
	dir := filepath.Join(root, claimDirName)
	if err := s.opt.FS.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d-%d-%s", s.opt.Clock.Now().UnixNano(), os.Getpid(), s.opt.IDs.NewID(4))
	dest := filepath.Join(dir, name)
	err := retryWindows(func() error { return s.opt.FS.Rename(sessPath, dest) })
	if err != nil {
		return "", err
	}
	return dest, nil
}

// retryWindows runs op, and on native Windows retries a failure for up to
// about 2 s. A directory cannot be renamed or removed on Windows while any
// handle is open on it or inside it (Go opens files without FILE_SHARE_DELETE),
// and another GC worker listing the same session (ReadDir, or reading its
// counts) holds such a handle for a moment. Without the retry every worker
// can lose the claim on the same session at once, and nobody reports it in
// that run. A missing path (another worker claimed or removed it) is final
// and never retried. Elsewhere op runs exactly once.
func retryWindows(op func() error) error {
	tries := 1
	if runtime.GOOS == "windows" {
		tries = 400 // ~2s at 5ms: a loaded -race runner holds handles far longer than 50 ms
	}
	var err error
	for i := 0; i < tries; i++ {
		if err = op(); err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if i+1 < tries {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return err
}

// sweepClaims deletes claims older than claimStale (a GC run that died
// between claim and removal). Their counts are dropped, not reported:
// double counting is worse than a lost residual.
func (s *Store) sweepClaims(root string) {
	dir := filepath.Join(root, claimDirName)
	ents, err := s.opt.FS.ReadDir(dir)
	if err != nil {
		return
	}
	now := s.opt.Clock.Now()
	for _, e := range ents {
		ns, _, ok := strings.Cut(e.Name(), "-")
		n, perr := strconv.ParseInt(ns, 10, 64)
		if !ok || perr != nil {
			continue
		}
		if now.Sub(time.Unix(0, n)) > claimStale {
			_ = s.opt.FS.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

type residualEvals struct {
	key   model.ScopeKey
	evals map[string]model.EvalCount
}

// residualEvals reads the unflushed eval counts of every agent state file of
// one session directory (best effort: unreadable or corrupt files are
// skipped, files are visited in name order).
func (s *Store) residualEvals(sessPath, repoName, sessName string) []residualEvals {
	ents, err := s.opt.FS.ReadDir(sessPath)
	if err != nil {
		return nil
	}
	var out []residualEvals
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := s.opt.FS.ReadFile(filepath.Join(sessPath, name))
		if err != nil {
			continue
		}
		rec, ok := decodeRecord(data)
		if !ok || len(rec.EvalCounts) == 0 {
			continue
		}
		out = append(out, residualEvals{
			key:   model.ScopeKey{RepoInst: repoName, Session: sessName, Agent: strings.TrimSuffix(name, ".json")},
			evals: maps.Clone(rec.EvalCounts),
		})
	}
	return out
}

// newestMTime returns the newest modification time of the files directly
// inside dir, or of dir itself when it contains no file.
func (s *Store) newestMTime(dir string, self fs.DirEntry) (time.Time, error) {
	ents, err := s.opt.FS.ReadDir(dir)
	if err != nil {
		return time.Time{}, err
	}
	var newest time.Time
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return time.Time{}, err
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if newest.IsZero() {
		fi, err := self.Info()
		if err != nil {
			return time.Time{}, err
		}
		newest = fi.ModTime()
	}
	return newest, nil
}

// Path returns the state file path for key (components sanitized).
func (s *Store) Path(key model.ScopeKey) string {
	return s.opt.Layout.StatePath(SanitizeComponent(key.RepoInst), SanitizeComponent(key.Session), SanitizeComponent(key.Agent))
}

// SanitizeComponent returns c when it matches ^[A-Za-z0-9_-]{1,64}$,
// otherwise "h" + hex(sha256(c))[:16]. Empty ⇒ "h" + hash of "".
func SanitizeComponent(c string) string {
	if safeComponent(c) {
		return c
	}
	sum := sha256.Sum256([]byte(c))
	return "h" + hex.EncodeToString(sum[:])[:16]
}

func safeComponent(c string) bool {
	if c == "" || len(c) > 64 {
		return false
	}
	for i := 0; i < len(c); i++ {
		b := c[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_', b == '-':
		default:
			return false
		}
	}
	return true
}

// decodeRecord parses a state file. ok is false for unparsable data or a
// version other than RecordVersion.
func decodeRecord(data []byte) (*Record, bool) {
	r := new(Record) // V stays 0 unless the file says otherwise
	if err := json.Unmarshal(data, r); err != nil || r.V != RecordVersion {
		return nil, false
	}
	r.enforceCaps()
	return r, true
}
