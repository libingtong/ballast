package paths

import (
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// fakeFS is an in-memory sys.FS keyed by OS-form names (case-sensitive). It
// models what Go reports on Windows: a junction is a directory entry that
// carries only fs.ModeIrregular, and EvalSymlinks never follows it.
type fakeFS struct {
	sys.FS // unimplemented methods panic on the nil embedded interface

	mu      sync.Mutex
	entries map[string]fakeEntry
	calls   map[string]int
}

type fakeEntry struct {
	mode   fs.FileMode
	target string
}

type fakeInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (i fakeInfo) Mode() fs.FileMode { return i.mode }

func newFakeFS() *fakeFS {
	return &fakeFS{entries: map[string]fakeEntry{}, calls: map[string]int{}}
}

func (f *fakeFS) dir(names ...string) {
	for _, n := range names {
		f.entries[n] = fakeEntry{mode: fs.ModeDir}
	}
}

func (f *fakeFS) link(name string, mode fs.FileMode, target string) {
	f.entries[name] = fakeEntry{mode: mode, target: target}
}

func (f *fakeFS) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *fakeFS) Lstat(name string) (fs.FileInfo, error) {
	f.mu.Lock()
	f.calls["lstat"]++
	f.mu.Unlock()
	e, ok := f.entries[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeInfo{mode: e.mode}, nil
}

func (f *fakeFS) Readlink(name string) (string, error) {
	f.mu.Lock()
	f.calls["readlink"]++
	f.mu.Unlock()
	e, ok := f.entries[name]
	if !ok || e.target == "" {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}
	return e.target, nil
}

// EvalSymlinks follows fs.ModeSymlink entries only, like Go on Windows with
// winsymlink=1; a missing component (or a dangling link) is not-exist.
func (f *fakeFS) EvalSymlinks(name string) (string, error) {
	f.mu.Lock()
	f.calls["eval"]++
	f.mu.Unlock()
	sep := `\`
	if !strings.Contains(name, sep) {
		sep = "/"
	}
	parts := strings.Split(name, sep)
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], sep)
		if i == 1 && (prefix == "" || strings.HasSuffix(prefix, ":")) {
			continue // root
		}
		e, ok := f.entries[prefix]
		if !ok {
			return "", os.ErrNotExist
		}
		if e.mode&fs.ModeSymlink != 0 {
			return "", os.ErrNotExist // not needed by these tests: treat as dangling
		}
	}
	return name, nil
}

func winCtx() model.PathContext {
	return model.PathContext{GOOS: "windows", Cwd: "c:/repo", ProjectRoot: "c:/repo", Home: "c:/Users/x"}
}

func winFixture() *fakeFS {
	f := newFakeFS()
	f.dir(`c:\repo`, `c:\repo\src`, `c:\Users`, `c:\Users\x`, `c:\Users\x\.ballast`, `c:\Users\x\.ballast\state`)
	return f
}

func ballastGlob(t *testing.T, ctx model.PathContext, cands []string) bool {
	t.Helper()
	g, err := NewService(nil).CompileGlobs([]string{"~/.ballast/**"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	return g.Match(cands...)
}

func TestCandidatesWindowsJunctionFakeFS(t *testing.T) {
	ctx := winCtx()
	tests := []struct {
		name   string
		mode   fs.FileMode
		target string
	}{
		{"junction (irregular)", fs.ModeDir | fs.ModeIrregular, `c:\Users\x\.ballast`},
		{"junction with NT prefix", fs.ModeDir | fs.ModeIrregular, `\??\c:\Users\x\.ballast`},
		{"symlink", fs.ModeDir | fs.ModeSymlink, `C:\USERS\X\.BALLAST`},
		{"relative junction", fs.ModeDir | fs.ModeIrregular, `..\Users\x\.ballast`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := winFixture()
			f.link(`c:\repo\junc`, tc.mode, tc.target)
			got := NewService(f).Candidates(`c:\repo\junc\state\x.json`, ctx)
			if !ballastGlob(t, ctx, got) {
				t.Fatalf("junction into ~/.ballast not matched: %q", got)
			}
			want := "c:/Users/x/.ballast/state/x.json"
			if !slices.ContainsFunc(got, func(s string) bool { return strings.EqualFold(s, want) }) {
				t.Errorf("resolved form missing: %q", got)
			}
			if got[0] != "c:/repo/junc/state/x.json" || got[1] != "junc/state/x.json" {
				t.Errorf("unresolved forms first: %q", got)
			}
		})
	}
}

func TestCandidatesWindowsDanglingJunction(t *testing.T) {
	ctx := winCtx()
	f := winFixture()
	// The target does not exist yet: the write would create it.
	f.link(`c:\repo\junc`, fs.ModeDir|fs.ModeIrregular, `c:\Users\x\.ballast\later`)
	got := NewService(f).Candidates(`c:\repo\junc\new.json`, ctx)
	if !ballastGlob(t, ctx, got) {
		t.Errorf("dangling junction into ~/.ballast not matched: %q", got)
	}
}

func TestCandidatesWindowsIrregularWithoutReadlink(t *testing.T) {
	// A non-link reparse point (cloud placeholder, socket, ...) reads as
	// irregular but has no link target: it is an ordinary path component.
	ctx := winCtx()
	f := winFixture()
	f.entries[`c:\repo\cloud`] = fakeEntry{mode: fs.ModeDir | fs.ModeIrregular}
	f.dir(`c:\repo\cloud\a`)
	got := NewService(f).Candidates(`c:\repo\cloud\a\f.go`, ctx)
	if want := []string{"c:/repo/cloud/a/f.go", "cloud/a/f.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRealPathWindowsJunctionChainAndLoop(t *testing.T) {
	f := winFixture()
	f.link(`c:\repo\a`, fs.ModeDir|fs.ModeIrregular, `c:\repo\b`)
	f.link(`c:\repo\b`, fs.ModeDir|fs.ModeIrregular, `c:\Users\x\.ballast`)
	got, err := RealPath(f, "c:/repo/a/state/x", "windows")
	if err != nil || got != "c:/Users/x/.ballast/state/x" {
		t.Errorf("chain: (%q, %v)", got, err)
	}
	f.link(`c:\repo\p`, fs.ModeDir|fs.ModeIrregular, `c:\repo\q`)
	f.link(`c:\repo\q`, fs.ModeDir|fs.ModeIrregular, `c:\repo\p`)
	if _, err := RealPath(f, "c:/repo/p/x", "windows"); err == nil || !strings.Contains(err.Error(), "too many symbolic links") {
		t.Errorf("loop: %v", err)
	}
}

func TestRealPathWindowsVolumeTargetKeepsDeviceForm(t *testing.T) {
	f := winFixture()
	f.link(`c:\repo\vol`, fs.ModeDir|fs.ModeIrregular, `\\?\Volume{1234}\Users\x\.ballast`)
	got, err := RealPath(f, "c:/repo/vol/state/x", "windows")
	if err != nil || got != "//?/Volume{1234}/Users/x/.ballast/state/x" {
		t.Errorf("device target: (%q, %v)", got, err)
	}
}

func TestCandidatesWindowsDevicePathsStayAbsolute(t *testing.T) {
	ctx := winCtx()
	for _, raw := range []string{
		`\\?\Volume{1234}\Users\x\.ballast\rules.json`,
		`\\?\GLOBALROOT\Device\HarddiskVolume3\Users\x\.ballast\rules.json`,
		`\\.\pipe\x`,
		`\??\Volume{1234}\x`,
	} {
		for _, fsys := range []sys.FS{nil, winFixture()} {
			got := NewService(fsys).Candidates(raw, ctx)
			if len(got) == 0 {
				t.Fatalf("%q: no candidates", raw)
			}
			for _, c := range got {
				if strings.HasPrefix(c, "c:/repo") {
					t.Errorf("%q: candidate %q was joined to cwd", raw, c)
				}
				if !strings.HasPrefix(c, "//") {
					t.Errorf("%q: candidate %q lost its absolute device form", raw, c)
				}
			}
		}
	}
}

func TestCandidatesWindowsDevicePathMapsBackToDrive(t *testing.T) {
	// EvalSymlinks (GetFinalPathNameByHandle) maps a Volume path to a drive.
	ctx := winCtx()
	f := &mapFS{fakeFS: winFixture(), out: map[string]string{
		`\\?\Volume{1234}\Users\x\.ballast\rules.json`: `\\?\C:\Users\x\.ballast\rules.json`,
	}}
	got := NewService(f).Candidates(`\\?\Volume{1234}\Users\x\.ballast\rules.json`, ctx)
	if !ballastGlob(t, ctx, got) {
		t.Errorf("device path mapped to a drive not matched: %q", got)
	}
}

type mapFS struct {
	*fakeFS
	out map[string]string
}

func (m *mapFS) EvalSymlinks(name string) (string, error) {
	if o, ok := m.out[name]; ok {
		return o, nil
	}
	return m.fakeFS.EvalSymlinks(name)
}

func TestRealPathWindowsRoots(t *testing.T) {
	f := winFixture()
	for _, p := range []string{"c:/", "//srv/share", "//srv", "/", "rel/x", "//?/Volume{1}"} {
		if got, err := RealPath(f, p, "windows"); err != nil || got != p {
			t.Errorf("RealPath(%q) = (%q, %v)", p, got, err)
		}
	}
}

func TestRootLen(t *testing.T) {
	for p, want := range map[string]int{
		"c:/x": 3, "c:/": 3, "c:": 2, "//srv/share/x": 11, "//srv/share": 11, "//srv": 5,
		"//?/Volume{1}/x": 13, "//?/Volume{1}": 13, "/x": 1, "rel": 0, "": 0,
	} {
		if got := rootLen(p); got != want {
			t.Errorf("rootLen(%q) = %d, want %d", p, got, want)
		}
	}
}

func TestResolveLstatBudgetForMissingDeepPath(t *testing.T) {
	// One EvalSymlinks per probe and one Lstat per missing level: linear, not quadratic.
	f := newFakeFS()
	f.dir("/a", "/a/b")
	deep := "/a/b/c1/c2/c3/c4/c5/c6/c7/c8"
	got, err := RealPath(f, deep, "linux")
	if err != nil || got != deep {
		t.Fatalf("(%q, %v)", got, err)
	}
	if n := f.count("lstat"); n > 10 {
		t.Errorf("lstat calls = %d, want <= 10 for 8 missing levels", n)
	}
	if n := f.count("eval"); n != 2 {
		t.Errorf("EvalSymlinks calls = %d, want 2 (full path, then nearest ancestor)", n)
	}
}

func TestServiceCachesProjectRootResolution(t *testing.T) {
	f := winFixture()
	svc := NewService(f)
	ctx := winCtx()
	svc.Candidates(`c:\repo\src\a.go`, ctx)
	first := f.count("eval")
	svc.Candidates(`c:\repo\src\a.go`, ctx)
	second := f.count("eval") - first
	if second >= first {
		t.Errorf("root resolution not cached: first call %d evals, second %d", first, second)
	}
	// A different GOOS or root is a different key.
	ctx2 := ctx
	ctx2.ProjectRoot = "c:/repo/src"
	before := f.count("eval")
	svc.Candidates(`c:\repo\src\a.go`, ctx2)
	if f.count("eval")-before <= second {
		t.Error("distinct root must be resolved separately")
	}
}

func TestServiceCacheConcurrent(t *testing.T) {
	svc := NewService(winFixture())
	ctx := winCtx()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if got := svc.Candidates(`c:\repo\src\a.go`, ctx); len(got) != 2 {
					t.Errorf("got %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}
