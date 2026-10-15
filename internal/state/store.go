package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

	lf, err := s.openLock(path)
	if err != nil {
		return info, err
	}
	defer lf.Close() // closing also drops the OS lock should unlock fail

	unlock, waited, err := TryLock(lf, LockBudget, s.opt.Clock)
	info.LockWait = waited
	if err != nil {
		if errors.Is(err, model.ErrLockTimeout) {
			info.LockTimeout = true
			return info, nil
		}
		return info, err
	}
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
	f, err := s.opt.FS.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return f, err
	}
	if err := s.opt.FS.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return s.opt.FS.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o600)
}

// writeAtomic replaces path via a temp file and rename, without fsync. On
// Windows the rename can fail while a lock-free reader still has the file
// open (Go opens without FILE_SHARE_DELETE), so it is retried briefly.
func (s *Store) writeAtomic(path string, data []byte) error {
	tries := 1
	if runtime.GOOS == "windows" {
		tries = 8
	}
	var err error
	for i := 0; i < tries; i++ {
		if err = sys.WriteFileAtomic(s.opt.FS, s.opt.IDs, path, data, 0o600, false); err == nil {
			return nil
		}
		if i+1 < tries {
			time.Sleep(time.Millisecond)
		}
	}
	return err
}

// DropAgent implements ports.StateStore. The state file is removed while
// holding its lock, and "<file>.lock" itself is never unlinked: deleting a
// lock file that another process holds (or is about to open) would let two
// committers each own "the" lock and lose updates. Orphan lock files go away
// with their session directory in GC. Drop is best-effort cleanup: a lock
// timeout leaves the file for GC and returns nil (contract v1.3).
func (s *Store) DropAgent(key model.ScopeKey) error {
	return s.dropLocked(s.Path(key))
}

// DropSession implements ports.StateStore: every agent state file of the
// session is dropped as in DropAgent. The directory and its lock files stay
// until GC (same lock-file reasoning). Missing ⇒ nil.
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
	return first
}

// dropLocked removes one state file under its lock (see DropAgent).
func (s *Store) dropLocked(path string) error {
	if _, err := s.opt.FS.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	lf, err := s.opt.FS.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // the session directory vanished (GC)
		}
		return err
	}
	defer lf.Close()
	unlock, _, err := TryLock(lf, LockBudget, s.opt.Clock)
	if err != nil {
		if errors.Is(err, model.ErrLockTimeout) {
			return nil
		}
		return err
	}
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
	Errors          int
}

// GC removes state older than ttl (relative to Clock.Now). A session
// directory is removed when its newest file (the directory itself when it
// holds no file) was modified more than ttl ago; repo directories left empty
// are removed too. ttl <= 0 means TTL. A missing state directory is not an
// error; per-entry failures are counted in Errors and the run continues.
func (s *Store) GC(ttl time.Duration) (GCStats, error) {
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
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
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
			if err := s.opt.FS.RemoveAll(sessPath); err != nil {
				st.Errors++
				remaining++
				continue
			}
			st.RemovedSessions++
		}
		if remaining == 0 {
			_ = s.opt.FS.Remove(repoPath) // fails harmlessly if a session appeared meanwhile
		}
	}
	return st, nil
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
