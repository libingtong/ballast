package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

type env struct {
	store *Store
	clk   *sys.FakeClock
	lay   layout.Layout
}

func newEnv(t *testing.T) env {
	t.Helper()
	lay := testenv.New(t).Layout(t)
	clk := sys.NewFakeClock(t0)
	return env{store: New(Options{Layout: lay, FS: sys.OSFS{}, Clock: clk, IDs: &sys.SeqID{}}), clk: clk, lay: lay}
}

// realClock variant for tests that exercise the lock budget in real time.
func newRealEnv(t *testing.T) env {
	t.Helper()
	lay := testenv.New(t).Layout(t)
	return env{store: New(Options{Layout: lay, Clock: sys.RealClock{}}), lay: lay}
}

var key = model.ScopeKey{RepoInst: "repo1", Session: "sess1", Agent: "main"}

func mustCommit(t *testing.T, s *Store, k model.ScopeKey, d model.StateDelta) model.StateCommitInfo {
	t.Helper()
	info, err := s.Commit(k, &d)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return info
}

func mustLoad(t *testing.T, s *Store, k model.ScopeKey) (*Record, model.StateLoadInfo) {
	t.Helper()
	st, info, err := s.Load(k)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec, ok := st.(*Record)
	if !ok || rec == nil {
		t.Fatalf("Load returned %T", st)
	}
	return rec, info
}

func TestSanitizeComponent(t *testing.T) {
	long := strings.Repeat("a", 64)
	tests := []struct {
		in   string
		same bool
	}{
		{"main", true},
		{"abc_DEF-123", true},
		{"0123456789", true},
		{long, true},
		{long + "a", false},
		{"", false},
		{"a b", false},
		{"a.b", false},
		{"../../etc/passwd", false},
		{"a/b", false},
		{`a\b`, false},
		{"café", false},
		{"nul\x00byte", false},
		{"-rf", true},
		{"..", false},
		{".", false},
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	for _, tc := range tests {
		got := SanitizeComponent(tc.in)
		if tc.same && got != tc.in {
			t.Errorf("Sanitize(%q) = %q, want unchanged", tc.in, got)
		}
		if !tc.same {
			if !strings.HasPrefix(got, "h") || len(got) != 17 {
				t.Errorf("Sanitize(%q) = %q, want h+16 hex", tc.in, got)
			}
		}
		if !valid.MatchString(got) {
			t.Errorf("Sanitize(%q) = %q is not a safe component", tc.in, got)
		}
		if again := SanitizeComponent(tc.in); again != got {
			t.Errorf("Sanitize(%q) is not deterministic", tc.in)
		}
	}
	// Known value: sha256("") = e3b0c44298fc1c14...
	if got := SanitizeComponent(""); got != "he3b0c44298fc1c14" {
		t.Errorf("Sanitize(\"\") = %q", got)
	}
	if SanitizeComponent("a b") == SanitizeComponent("a c") {
		t.Error("different inputs collided")
	}
}

func FuzzSanitizeComponent(f *testing.F) {
	for _, s := range []string{"", "main", "../x", "a b", strings.Repeat("z", 65), "\x00", "日本", "h0123456789abcdef"} {
		f.Add(s)
	}
	valid := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	f.Fuzz(func(t *testing.T, s string) {
		got := SanitizeComponent(s)
		if !valid.MatchString(got) {
			t.Fatalf("Sanitize(%q) = %q", s, got)
		}
		if valid.MatchString(s) && got != s {
			t.Fatalf("safe input %q changed to %q", s, got)
		}
		if SanitizeComponent(s) != got {
			t.Fatal("not deterministic")
		}
	})
}

func TestPathStaysUnderStateDir(t *testing.T) {
	e := newEnv(t)
	for _, k := range []model.ScopeKey{
		key,
		{RepoInst: "../../..", Session: "/etc", Agent: ".."},
		{},
		{RepoInst: "norepo-abc", Session: strings.Repeat("s", 200), Agent: "agent/1"},
	} {
		p := e.store.Path(k)
		rel, err := filepath.Rel(e.lay.StateDir, p)
		if err != nil || strings.HasPrefix(rel, "..") || strings.Count(filepath.ToSlash(rel), "/") != 2 || !strings.HasSuffix(p, ".json") {
			t.Errorf("Path(%+v) = %q (rel %q, err %v)", k, p, rel, err)
		}
	}
	want := filepath.Join(e.lay.StateDir, "repo1", "sess1", "main.json")
	if got := e.store.Path(key); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestLoadMissing(t *testing.T) {
	e := newEnv(t)
	rec, info := mustLoad(t, e.store, key)
	if info.Exists || info.Corrupt || !info.Readable || info.Bytes != 0 {
		t.Errorf("info = %+v", info)
	}
	if rec.V != RecordVersion || rec.EventSeq() != 0 {
		t.Errorf("record = %+v", rec)
	}
	if _, err := os.Stat(e.lay.StateDir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Load must not create directories")
	}
}

func TestLoadCorruptFixtures(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "decode"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		name := ent.Name()
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			data, err := os.ReadFile(filepath.Join("testdata", "decode", name))
			if err != nil {
				t.Fatal(err)
			}
			p := e.store.Path(key)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
			rec, info := mustLoad(t, e.store, key)
			wantCorrupt := strings.HasPrefix(name, "corrupt_")
			if info.Corrupt != wantCorrupt || !info.Exists || !info.Readable || info.Bytes != len(data) {
				t.Errorf("info = %+v (len %d)", info, len(data))
			}
			if wantCorrupt && (rec.Seq != 0 || rec.V != RecordVersion) {
				t.Errorf("corrupt file must yield an empty record, got %+v", rec)
			}
		})
	}
}

func TestLoadIOError(t *testing.T) {
	e := newEnv(t)
	// A directory where the state file should be: ReadFile fails, not ErrNotExist.
	p := e.store.Path(key)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	st, info, err := e.store.Load(key)
	if err == nil {
		t.Fatal("expected an I/O error")
	}
	if st == nil || info.Readable || info.Exists {
		t.Errorf("state=%v info=%+v", st, info)
	}
	if rec := st.(*Record); rec.V != RecordVersion {
		t.Error("returned state must still be a usable empty record")
	}

	boom := errors.New("boom")
	fe := newEnv(t)
	fe.store = New(Options{Layout: fe.lay, FS: sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"ReadFile": ""}, Err: boom}, Clock: fe.clk})
	st, info, err = fe.store.Load(key)
	if !errors.Is(err, boom) || st == nil || info.Readable {
		t.Errorf("fault load: st=%v info=%+v err=%v", st, info, err)
	}
}

func TestCommitCreatesFilesAndAccumulates(t *testing.T) {
	e := newEnv(t)
	info := mustCommit(t, e.store, key, model.StateDelta{DedupeKey: "PreToolUse:1", ToolCall: true, ReadPaths: []string{"/a"}})
	if !info.Applied || info.Duplicate || info.LockTimeout || info.Corrupt || info.EventSeq != 1 {
		t.Fatalf("info = %+v", info)
	}
	p := e.store.Path(key)
	for _, f := range []string{p, p + ".lock"} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f, fi.Mode().Perm())
		}
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{filepath.Dir(p), filepath.Dir(filepath.Dir(p))} {
			if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s: %v %v, want 0700", d, fi, err)
			}
		}
	}

	e.clk.Advance(time.Second)
	info = mustCommit(t, e.store, key, model.StateDelta{DedupeKey: "PostToolUse:1", ToolCall: true, HitRules: []string{"r"}, Summary: &model.EventSummary{Text: "x"}})
	if info.EventSeq != 2 {
		t.Errorf("second EventSeq = %d", info.EventSeq)
	}
	rec, li := mustLoad(t, e.store, key)
	if !li.Exists || li.Corrupt || !li.Readable || li.Bytes == 0 {
		t.Errorf("load info = %+v", li)
	}
	if rec.EventSeq() != 2 || rec.ToolCount() != 2 || !rec.HasRead("/a") || rec.PriorHits("r") != 1 {
		t.Errorf("record = %+v", rec)
	}
	// The zero Now of the delta was filled from the clock.
	if s, _ := rec.StartedAt(); !s.Equal(t0) {
		t.Errorf("StartedAt = %v, want %v", s, t0)
	}
	if ts, _ := rec.LastReadAt("/a"); !ts.Equal(t0) {
		t.Errorf("LastReadAt = %v", ts)
	}
	if got := rec.Recent(1); len(got) != 1 || !got[0].TS.Equal(t0.Add(time.Second)) {
		t.Errorf("ring = %+v", got)
	}
	assertNoTempFiles(t, filepath.Dir(p))
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestCommitNilAndCallerDeltaUntouched(t *testing.T) {
	e := newEnv(t)
	if info, err := e.store.Commit(key, nil); err != nil || info != (model.StateCommitInfo{}) {
		t.Errorf("nil delta: %+v %v", info, err)
	}
	if _, err := os.Stat(e.store.Path(key)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("nil delta created a file")
	}
	d := model.StateDelta{ToolCall: true}
	if _, err := e.store.Commit(key, &d); err != nil {
		t.Fatal(err)
	}
	if !d.Now.IsZero() {
		t.Error("Commit mutated the caller's delta")
	}
}

func TestCommitDuplicate(t *testing.T) {
	e := newEnv(t)
	mustCommit(t, e.store, key, model.StateDelta{DedupeKey: "PreToolUse:t", ToolCall: true})
	p := e.store.Path(key)
	before, _ := os.ReadFile(p)

	info := mustCommit(t, e.store, key, model.StateDelta{DedupeKey: "PreToolUse:t", ToolCall: true, HitRules: []string{"r"}})
	if !info.Duplicate || info.Applied || info.EventSeq != 1 {
		t.Errorf("duplicate info = %+v", info)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Error("a pure duplicate rewrote the file")
	}

	// A duplicate carrying a new generation is applied (idempotent fields) but still flagged.
	info = mustCommit(t, e.store, key, model.StateDelta{DedupeKey: "PreToolUse:t", ToolCall: true, Generation: "g2"})
	if !info.Duplicate || !info.Applied {
		t.Errorf("duplicate+generation info = %+v", info)
	}
	rec, _ := mustLoad(t, e.store, key)
	if rec.ToolCount() != 1 || rec.Generation() != "g2" || rec.EventSeq() != 1 {
		t.Errorf("record = %+v", rec)
	}
}

func TestCommitRecoversCorruptFile(t *testing.T) {
	e := newEnv(t)
	p := e.store.Path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"v":1,"event_seq":9,"seen":["a`), 0o600); err != nil {
		t.Fatal(err)
	}
	info := mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
	if !info.Applied || !info.Corrupt || info.EventSeq != 1 {
		t.Errorf("info = %+v", info)
	}
	rec, li := mustLoad(t, e.store, key)
	if li.Corrupt || rec.EventSeq() != 1 || rec.ToolCount() != 1 {
		t.Errorf("after recovery: info=%+v rec=%+v", li, rec)
	}
	info = mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
	if info.Corrupt {
		t.Error("healthy file reported corrupt")
	}
}

func TestCommitLockTimeout(t *testing.T) {
	e := newRealEnv(t)
	p := e.store.Path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	unlock, _, err := TryLock(holder, LockBudget, sys.RealClock{})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	info, err := e.store.Commit(key, &model.StateDelta{ToolCall: true})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("lock timeout must not be an error: %v", err)
	}
	if !info.LockTimeout || info.Applied || info.Duplicate {
		t.Fatalf("info = %+v", info)
	}
	if info.LockWait < LockBudget || elapsed < LockBudget/2 || elapsed > time.Second {
		t.Errorf("waited %v (reported %v), want about %v", elapsed, info.LockWait, LockBudget)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a timed-out commit must not write state")
	}

	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	info = mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
	if !info.Applied || info.LockTimeout || info.EventSeq != 1 {
		t.Errorf("after unlock: %+v", info)
	}
}

func TestCommitLockTimeoutWithFrozenClock(t *testing.T) {
	// A FakeClock never advances; the attempt bound must still end the wait.
	e := newEnv(t)
	p := e.store.Path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, _, err := TryLock(holder, LockBudget, e.clk); err != nil {
		t.Fatal(err)
	}
	done := make(chan model.StateCommitInfo, 1)
	go func() {
		info, _ := e.store.Commit(key, &model.StateDelta{ToolCall: true})
		done <- info
	}()
	select {
	case info := <-done:
		if !info.LockTimeout {
			t.Errorf("info = %+v", info)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Commit hung with a frozen clock")
	}
}

func TestCommitIOErrors(t *testing.T) {
	boom := errors.New("disk full")
	for _, op := range []string{"OpenFile", "MkdirAll", "Rename"} {
		t.Run(op, func(t *testing.T) {
			e := newEnv(t)
			mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
			p := e.store.Path(key)
			before, _ := os.ReadFile(p)

			sub := ""
			if op == "OpenFile" {
				sub = ".lock"
			}
			s := New(Options{Layout: e.lay, Clock: e.clk, IDs: &sys.SeqID{}, FS: sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{op: sub}, Err: boom}})
			info, err := s.Commit(key, &model.StateDelta{ToolCall: true})
			if !errors.Is(err, boom) || info.Applied || info.LockTimeout {
				t.Fatalf("info=%+v err=%v", info, err)
			}
			after, _ := os.ReadFile(p)
			if string(before) != string(after) {
				t.Error("failed commit changed the state file")
			}
			assertNoTempFiles(t, filepath.Dir(p))
		})
	}
}

func TestCommitFirstCreationMkdirError(t *testing.T) {
	e := newEnv(t)
	boom := errors.New("read-only fs")
	s := New(Options{Layout: e.lay, Clock: e.clk, FS: sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"MkdirAll": ""}, Err: boom}})
	if _, err := s.Commit(key, &model.StateDelta{ToolCall: true}); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestAgentAndSessionScoping(t *testing.T) {
	e := newEnv(t)
	main := key
	sub := model.ScopeKey{RepoInst: "repo1", Session: "sess1", Agent: "agent-7"}
	other := model.ScopeKey{RepoInst: "repo1", Session: "sess2", Agent: "main"}
	wt := model.ScopeKey{RepoInst: "repo2", Session: "sess1", Agent: "main"}

	mustCommit(t, e.store, main, model.StateDelta{ReadPaths: []string{"/main-only"}, HitRules: []string{"r"}, ToolCall: true})
	mustCommit(t, e.store, sub, model.StateDelta{ReadPaths: []string{"/sub-only"}})

	m, _ := mustLoad(t, e.store, main)
	s, _ := mustLoad(t, e.store, sub)
	if !m.HasRead("/main-only") || m.HasRead("/sub-only") || m.PriorHits("r") != 1 {
		t.Errorf("main = %+v", m)
	}
	if !s.HasRead("/sub-only") || s.HasRead("/main-only") || s.PriorHits("r") != 0 || s.ToolCount() != 0 {
		t.Errorf("sub = %+v", s)
	}
	for _, k := range []model.ScopeKey{other, wt} {
		r, li := mustLoad(t, e.store, k)
		if li.Exists || r.HasRead("/main-only") {
			t.Errorf("%+v leaked state", k)
		}
	}
}

func TestDropAgentAndSession(t *testing.T) {
	e := newEnv(t)
	sub := model.ScopeKey{RepoInst: "repo1", Session: "sess1", Agent: "agent-7"}
	other := model.ScopeKey{RepoInst: "repo1", Session: "sess2", Agent: "main"}
	for _, k := range []model.ScopeKey{key, sub, other} {
		mustCommit(t, e.store, k, model.StateDelta{ToolCall: true})
	}

	if err := e.store.DropAgent(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.store.Path(sub)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s still exists", e.store.Path(sub))
	}
	// DropAgent keeps the lock file (the session goes on); DropSession and GC
	// remove it.
	if _, err := os.Stat(e.store.Path(sub) + ".lock"); err != nil {
		t.Errorf("lock file must survive DropAgent: %v", err)
	}
	if _, li := mustLoad(t, e.store, key); !li.Exists {
		t.Error("DropAgent removed a sibling agent")
	}
	if err := e.store.DropAgent(sub); err != nil {
		t.Errorf("dropping a missing agent: %v", err)
	}

	mustCommit(t, e.store, sub, model.StateDelta{ToolCall: true})
	if err := e.store.DropSession(key); err != nil {
		t.Fatal(err)
	}
	for _, k := range []model.ScopeKey{key, sub} {
		if _, li := mustLoad(t, e.store, k); li.Exists {
			t.Errorf("DropSession left %+v", k)
		}
		// Windows cannot unlink a file it holds open: GC cleans up there.
		if _, err := os.Stat(e.store.Path(k) + ".lock"); runtime.GOOS != "windows" && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("DropSession should remove the orphaned lock file of %+v: %v", k, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(e.store.Path(key))); runtime.GOOS != "windows" && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the emptied session directory should be gone: %v", err)
	}
	if _, li := mustLoad(t, e.store, other); !li.Exists {
		t.Error("DropSession removed another session")
	}
	if err := e.store.DropSession(key); err != nil {
		t.Errorf("dropping a missing session: %v", err)
	}
	// An empty/odd key can never resolve to the state root.
	if err := e.store.DropSession(model.ScopeKey{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.lay.StateDir); err != nil {
		t.Errorf("state root lost: %v", err)
	}
	if _, li := mustLoad(t, e.store, other); !li.Exists {
		t.Error("zero-key DropSession removed unrelated state")
	}
}

// DropAgent/DropSession while another process holds the lock must not
// unlink the lock file: a later Commit still waits on the same lock
// (contract v1.3, review u5 minor).
func TestDropKeepsHeldLock(t *testing.T) {
	for _, drop := range []string{"agent", "session"} {
		t.Run(drop, func(t *testing.T) {
			e := newRealEnv(t)
			mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
			p := e.store.Path(key)
			holder, err := os.OpenFile(p+".lock", os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			unlock, _, err := TryLock(holder, LockBudget, sys.RealClock{})
			if err != nil {
				t.Fatal(err)
			}
			if drop == "agent" {
				err = e.store.DropAgent(key)
			} else {
				err = e.store.DropSession(key)
			}
			if err != nil {
				t.Fatalf("drop under a held lock must be a nil best-effort: %v", err)
			}
			if _, err := os.Stat(p + ".lock"); err != nil {
				t.Fatalf("lock file unlinked: %v", err)
			}
			if _, err := os.Stat(p); err != nil {
				t.Errorf("state removed without holding the lock: %v", err)
			}
			info, err := e.store.Commit(key, &model.StateDelta{ToolCall: true})
			if err != nil || !info.LockTimeout {
				t.Fatalf("second commit = %+v, %v; want LockTimeout", info, err)
			}
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
			if drop == "agent" {
				err = e.store.DropAgent(key)
			} else {
				err = e.store.DropSession(key)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("state not dropped after unlock: %v", err)
			}
		})
	}
}

func TestGC(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	keys := map[string]time.Duration{ // session -> age of its newest file
		"fresh":   time.Hour,
		"edge":    TTL - time.Minute,
		"stale":   TTL + time.Hour,
		"ancient": 30 * 24 * time.Hour,
	}
	for sess, age := range keys {
		k := model.ScopeKey{RepoInst: "repoA", Session: sess, Agent: "main"}
		mustCommit(t, e.store, k, model.StateDelta{ToolCall: true})
		sub := model.ScopeKey{RepoInst: "repoA", Session: sess, Agent: "agent-1"}
		mustCommit(t, e.store, sub, model.StateDelta{ToolCall: true})
		dir := filepath.Dir(e.store.Path(k))
		// The agent file is older than the main file; the newest decides.
		setAge(t, e.store.Path(sub), now, age+48*time.Hour)
		setAge(t, e.store.Path(sub)+".lock", now, age+48*time.Hour)
		setAge(t, e.store.Path(k), now, age)
		setAge(t, e.store.Path(k)+".lock", now, age)
		_ = dir
	}
	// A repo that holds only a stale session disappears entirely.
	lone := model.ScopeKey{RepoInst: "repoB", Session: "gone", Agent: "main"}
	mustCommit(t, e.store, lone, model.StateDelta{ToolCall: true})
	setAge(t, e.store.Path(lone), now, 10*24*time.Hour)
	setAge(t, e.store.Path(lone)+".lock", now, 10*24*time.Hour)
	// An empty session dir (left by DropAgent) ages by its own mtime.
	empty := filepath.Join(e.lay.StateDir, "repoC", "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	setAge(t, empty, now, 9*24*time.Hour)
	// A stray file at the repo level is left alone.
	stray := filepath.Join(e.lay.StateDir, "repoA", "README")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := e.store.GC(TTL)
	if err != nil {
		t.Fatal(err)
	}
	if st.ScannedSessions != 6 || st.RemovedSessions != 4 || st.Errors != 0 {
		t.Errorf("stats = %+v", st)
	}
	exists := func(parts ...string) bool {
		_, err := os.Stat(filepath.Join(append([]string{e.lay.StateDir}, parts...)...))
		return err == nil
	}
	if !exists("repoA", "fresh") || !exists("repoA", "edge") || !exists("repoA", "README") {
		t.Error("fresh state was removed")
	}
	if exists("repoA", "stale") || exists("repoA", "ancient") {
		t.Error("stale sessions survived")
	}
	if exists("repoB") || exists("repoC") {
		t.Error("empty repo directories were not cleaned up")
	}

	// Running again is a no-op; a later clock expires the rest.
	st, _ = e.store.GC(TTL)
	if st.RemovedSessions != 0 {
		t.Errorf("second run removed %d", st.RemovedSessions)
	}
	e.clk.Advance(TTL)
	st, _ = e.store.GC(0) // 0 ⇒ default TTL
	if st.RemovedSessions != 2 {
		t.Errorf("after advancing: %+v", st)
	}
}

func TestGCFlushResidualEvals(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	mk := func(sess, agent string, age time.Duration, evals map[string]model.EvalCount) {
		k := model.ScopeKey{RepoInst: "repoA", Session: sess, Agent: agent}
		mustCommit(t, e.store, k, model.StateDelta{ToolCall: true, Evals: evals})
		setAge(t, e.store.Path(k), now, age)
		setAge(t, e.store.Path(k)+".lock", now, age)
	}
	mk("stale", "main", 10*24*time.Hour, map[string]model.EvalCount{"r": {Evals: 4, Hits: 1}})
	mk("stale", "agent-1", 10*24*time.Hour, map[string]model.EvalCount{"q": {Evals: 2, Would: 2}})
	mk("stale", "agent-2", 10*24*time.Hour, nil) // nothing to flush
	mk("fresh", "main", time.Hour, map[string]model.EvalCount{"r": {Evals: 9}})

	got := map[model.ScopeKey]map[string]model.EvalCount{}
	st, err := e.store.GCFlush(TTL, func(k model.ScopeKey, ev map[string]model.EvalCount) { got[k] = ev })
	if err != nil {
		t.Fatal(err)
	}
	if st.RemovedSessions != 1 || st.FlushedEvals != 2 || len(got) != 2 {
		t.Fatalf("stats=%+v got=%v", st, got)
	}
	want := map[model.ScopeKey]map[string]model.EvalCount{
		{RepoInst: "repoA", Session: "stale", Agent: "main"}:    {"r": {Evals: 4, Hits: 1}},
		{RepoInst: "repoA", Session: "stale", Agent: "agent-1"}: {"q": {Evals: 2, Would: 2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flushed = %v, want %v", got, want)
	}
	// A nil callback just discards (plain GC) and never panics.
	e.clk.Advance(TTL)
	if _, err := e.store.GC(0); err != nil {
		t.Fatal(err)
	}
}

// TestGCFlushConcurrentGoroutines: GCFlush callers racing on the same expired
// sessions report each of them exactly once.
func TestGCFlushConcurrentGoroutines(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	const sessions, workers = 30, 6
	for i := 0; i < sessions; i++ {
		k := model.ScopeKey{RepoInst: "repoA", Session: fmt.Sprintf("s%d", i), Agent: "main"}
		mustCommit(t, e.store, k, model.StateDelta{ToolCall: true, Evals: map[string]model.EvalCount{"r": {Evals: 1}}})
		setAge(t, e.store.Path(k), now, 10*24*time.Hour)
		setAge(t, e.store.Path(k)+".lock", now, 10*24*time.Hour)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.store.GCFlush(TTL, func(k model.ScopeKey, _ map[string]model.EvalCount) {
				mu.Lock()
				seen[k.Session]++
				mu.Unlock()
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(seen) != sessions {
		var missing []string
		for i := 0; i < sessions; i++ {
			if n := fmt.Sprintf("s%d", i); seen[n] == 0 {
				missing = append(missing, n)
			}
		}
		t.Fatalf("flushed %d sessions, want %d (missing %v)", len(seen), sessions, missing)
	}
	for s, n := range seen {
		if n != 1 {
			t.Errorf("%s flushed %d times", s, n)
		}
	}
}

type removeAllFails struct {
	sys.FS
	err error
}

func (r removeAllFails) RemoveAll(string) error { return r.err }

// TestGCFlushFailedRemovalIsRetriedNotDoubled: a removal failure reports
// nothing and leaves the session for the next run, which reports it once.
func TestGCFlushFailedRemovalIsRetriedNotDoubled(t *testing.T) {
	e := newEnv(t)
	now := e.clk.Now()
	k := model.ScopeKey{RepoInst: "repoA", Session: "stale", Agent: "main"}
	mustCommit(t, e.store, k, model.StateDelta{ToolCall: true, Evals: map[string]model.EvalCount{"r": {Evals: 3}}})
	setAge(t, e.store.Path(k), now, 10*24*time.Hour)
	setAge(t, e.store.Path(k)+".lock", now, 10*24*time.Hour)

	boom := errors.New("boom")
	failing := New(Options{Layout: e.lay, Clock: e.clk, IDs: &sys.SeqID{}, FS: removeAllFails{FS: sys.OSFS{}, err: boom}})
	n := 0
	st, _ := failing.GCFlush(TTL, func(model.ScopeKey, map[string]model.EvalCount) { n++ })
	if n != 0 || st.Errors != 1 || st.RemovedSessions != 0 {
		t.Fatalf("failed run: n=%d stats=%+v", n, st)
	}
	st, _ = e.store.GCFlush(TTL, func(model.ScopeKey, map[string]model.EvalCount) { n++ })
	if n != 1 || st.RemovedSessions != 1 {
		t.Fatalf("retry: n=%d stats=%+v", n, st)
	}
	st, _ = e.store.GCFlush(TTL, func(model.ScopeKey, map[string]model.EvalCount) { n++ })
	if n != 1 || st.RemovedSessions != 0 {
		t.Fatalf("third run: n=%d stats=%+v", n, st)
	}
}

// TestGCFlushSweepsStaleClaims: a claim left by a crashed run is discarded
// without being reported once it is older than claimStale.
func TestGCFlushSweepsStaleClaims(t *testing.T) {
	e := newEnv(t)
	claims := filepath.Join(e.lay.StateDir, claimDirName)
	stale := filepath.Join(claims, fmt.Sprintf("%d-1-aa", e.clk.Now().Add(-2*claimStale).UnixNano()))
	fresh := filepath.Join(claims, fmt.Sprintf("%d-1-bb", e.clk.Now().UnixNano()))
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	if _, err := e.store.GCFlush(TTL, func(model.ScopeKey, map[string]model.EvalCount) { n++ }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale claim kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh claim removed: %v", err)
	}
	if n != 0 {
		t.Errorf("claims were reported: %d", n)
	}
}

func setAge(t *testing.T, path string, now time.Time, age time.Duration) {
	t.Helper()
	ts := now.Add(-age)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestGCMissingStateDir(t *testing.T) {
	e := newEnv(t)
	st, err := e.store.GC(TTL)
	if err != nil || st != (GCStats{}) {
		t.Errorf("stats=%+v err=%v", st, err)
	}
}

func TestGCCustomTTL(t *testing.T) {
	e := newEnv(t)
	mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
	setAge(t, e.store.Path(key), e.clk.Now(), 2*time.Hour)
	setAge(t, e.store.Path(key)+".lock", e.clk.Now(), 2*time.Hour)
	if st, _ := e.store.GC(3 * time.Hour); st.RemovedSessions != 0 {
		t.Errorf("removed within ttl: %+v", st)
	}
	if st, _ := e.store.GC(time.Hour); st.RemovedSessions != 1 {
		t.Errorf("not removed past ttl: %+v", st)
	}
}

func TestConcurrentGoroutinesLoseNoUpdate(t *testing.T) {
	e := newRealEnv(t)
	const workers, per = 8, 40
	var mu sync.Mutex
	applied, timeouts := 0, 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				d := model.StateDelta{
					DedupeKey: fmt.Sprintf("PreToolUse:w%d-%d", w, i), ToolCall: true,
					ReadPaths: []string{fmt.Sprintf("/w%d/%d", w, i)}, HitRules: []string{"r"},
					Evals: map[string]model.EvalCount{"r": {Evals: 1}},
				}
				info, err := e.store.Commit(key, &d)
				if err != nil {
					t.Errorf("commit: %v", err)
					return
				}
				mu.Lock()
				if info.LockTimeout {
					timeouts++
				} else if info.Applied && !info.Duplicate {
					applied++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if applied+timeouts != workers*per {
		t.Fatalf("applied %d + timeouts %d != %d", applied, timeouts, workers*per)
	}
	rec, li := mustLoad(t, e.store, key)
	if li.Corrupt {
		t.Fatal("state corrupt after concurrent commits")
	}
	if int(rec.Seq) != applied || rec.Tools != applied || rec.Hits["r"] != applied || rec.EvalCounts["r"].Evals != applied || len(rec.ReadAt) != applied {
		t.Errorf("applied=%d seq=%d tools=%d hits=%d evals=%d reads=%d", applied, rec.Seq, rec.Tools, rec.Hits["r"], rec.EvalCounts["r"].Evals, len(rec.ReadAt))
	}
	assertNoTempFiles(t, filepath.Dir(e.store.Path(key)))
}

func TestConcurrentLoadDuringCommits(t *testing.T) {
	// Lock-free readers must always see a complete, parsable file.
	e := newRealEnv(t)
	mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad, reads int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, li, err := e.store.Load(key)
			reads++
			if runtime.GOOS != "windows" && (err != nil || li.Corrupt) {
				bad++
			}
		}
	}()
	for i := 0; i < 200; i++ {
		d := model.StateDelta{ToolCall: true, ReadPaths: []string{fmt.Sprint("/p", i)}}
		if _, err := e.store.Commit(key, &d); err != nil && runtime.GOOS != "windows" {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if bad != 0 {
		t.Errorf("%d of %d lock-free loads saw a torn file", bad, reads)
	}
}

// SessionEnd leaves no residue: state files, their lock files and the
// session directory are gone, while a sibling session and the repo directory
// (still used by it) stay.
func TestDropSessionLeavesNoResidue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an open lock file cannot be unlinked on Windows; GC removes it")
	}
	e := newRealEnv(t)
	sub := model.ScopeKey{RepoInst: key.RepoInst, Session: key.Session, Agent: "agent-3"}
	other := model.ScopeKey{RepoInst: key.RepoInst, Session: "sess-other", Agent: "main"}
	for _, k := range []model.ScopeKey{key, sub, other} {
		mustCommit(t, e.store, k, model.StateDelta{ToolCall: true})
	}
	if err := e.store.DropSession(key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(e.store.Path(key))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("session directory left behind: %v", err)
	}
	if _, li := mustLoad(t, e.store, other); !li.Exists {
		t.Error("the other session lost its state")
	}
	if err := e.store.DropSession(other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(e.store.Path(other)))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("emptied repo directory left behind: %v", err)
	}
	if _, err := os.Stat(e.lay.StateDir); err != nil {
		t.Errorf("state root lost: %v", err)
	}
	// the session can start again afterwards
	mustCommit(t, e.store, key, model.StateDelta{ToolCall: true})
	if _, li := mustLoad(t, e.store, key); !li.Exists {
		t.Error("cannot commit after the session directory was removed")
	}
}

// A process that opened a lock file which was unlinked and recreated meanwhile
// must notice that it holds an orphaned inode.
func TestLockIsCurrentDetectsReplacedLockFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an open lock file cannot be unlinked on Windows")
	}
	e := newRealEnv(t)
	lp := e.store.Path(key) + ".lock"
	if err := os.MkdirAll(filepath.Dir(lp), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := e.store.openLock(e.store.Path(key))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !e.store.lockIsCurrent(f, lp) {
		t.Fatal("a fresh lock file is current")
	}
	if err := os.Remove(lp); err != nil {
		t.Fatal(err)
	}
	if e.store.lockIsCurrent(f, lp) {
		t.Error("an unlinked lock file is not current")
	}
	if err := os.WriteFile(lp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if e.store.lockIsCurrent(f, lp) {
		t.Error("a recreated lock file is a different file")
	}
}

func TestRetryTransientNonTransientNotRetried(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	err := retryTransient(50, time.Millisecond, func(e error) bool { return errors.Is(e, errTransientForTest) }, func() error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("non-transient error: err=%v calls=%d, want boom after 1 call", err, calls)
	}
}

func TestRetryTransientRetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	err := retryTransient(50, time.Millisecond, func(e error) bool { return errors.Is(e, errTransientForTest) }, func() error {
		calls++
		if calls < 3 {
			return errTransientForTest
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want nil after 3 calls", err, calls)
	}
}

func TestRetryTransientNotExistNotRetried(t *testing.T) {
	calls := 0
	err := retryTransient(50, time.Millisecond, func(error) bool { return true }, func() error {
		calls++
		return fs.ErrNotExist
	})
	if !errors.Is(err, fs.ErrNotExist) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want ErrNotExist after 1 call", err, calls)
	}
}

func TestRetryTransientGivesUpAfterTries(t *testing.T) {
	calls := 0
	err := retryTransient(4, time.Millisecond, func(error) bool { return true }, func() error {
		calls++
		return errTransientForTest
	})
	if !errors.Is(err, errTransientForTest) || calls != 4 {
		t.Fatalf("err=%v calls=%d, want transient error after 4 calls", err, calls)
	}
}

var errTransientForTest = errors.New("transient")
