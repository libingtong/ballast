package paths

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// fixture is a sandbox whose paths are already symlink-free (macOS temp dirs
// live under /var → /private/var), so expectations can be written literally.
type fixture struct {
	home    string // internal form
	ballast string // internal form
	proj    string // internal form
	ctx     model.PathContext
	svc     *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	sb := testenv.New(t)
	root, err := filepath.EvalSymlinks(sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	goos := runtime.GOOS
	n := func(p string) string { return Normalize(p, "", model.PathContext{GOOS: goos}) }
	f := &fixture{
		home:    n(filepath.Join(root, "home")),
		ballast: n(filepath.Join(root, "home", ".ballast")),
		proj:    n(filepath.Join(root, "home", "proj")),
		svc:     NewService(sys.OSFS{}),
	}
	for _, d := range []string{f.ballast + "/state", f.proj + "/src"} {
		if err := os.MkdirAll(ToOS(d, goos), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.ctx = model.PathContext{Cwd: f.proj, ProjectRoot: f.proj, Home: f.home, GOOS: goos}
	return f
}

func (f *fixture) osPath(p string) string { return ToOS(p, f.ctx.GOOS) }

func (f *fixture) symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(f.osPath(target), f.osPath(link)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation not permitted: %v", err)
		}
		t.Fatal(err)
	}
}

func (f *fixture) write(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(f.osPath(p), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) matches(t *testing.T, patterns []string, cands []string) bool {
	t.Helper()
	g, err := f.svc.CompileGlobs(patterns, f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return g.Match(cands...)
}

func TestCandidatesPlain(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.proj+"/src/a.go")
	want := []string{f.proj + "/src/a.go", "src/a.go"}
	for _, raw := range []string{f.proj + "/src/a.go", "src/a.go", "./src/../src/a.go", f.proj + "//src/./a.go"} {
		if got := f.svc.Candidates(raw, f.ctx); !reflect.DeepEqual(got, want) {
			t.Errorf("Candidates(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCandidatesOutsideProjectAndEdges(t *testing.T) {
	f := newFixture(t)
	outside := f.home + "/other.txt"
	if got := f.svc.Candidates(outside, f.ctx); !reflect.DeepEqual(got, []string{outside}) {
		t.Errorf("outside project: %q", got)
	}
	if got := f.svc.Candidates("", f.ctx); got != nil {
		t.Errorf("empty raw: %q", got)
	}
	// The project root itself has no useful relative form.
	if got := f.svc.Candidates(f.proj, f.ctx); !reflect.DeepEqual(got, []string{f.proj}) {
		t.Errorf("project root: %q", got)
	}
	// An empty ProjectRoot falls back to Cwd.
	ctx := f.ctx
	ctx.ProjectRoot = ""
	if got := f.svc.Candidates("src/b.go", ctx); !reflect.DeepEqual(got, []string{f.proj + "/src/b.go", "src/b.go"}) {
		t.Errorf("empty ProjectRoot: %q", got)
	}
	// "~" is expanded.
	if got := f.svc.Candidates("~/other.txt", f.ctx); !reflect.DeepEqual(got, []string{outside}) {
		t.Errorf("tilde: %q", got)
	}
	// nil FS: unresolved forms only.
	if got := NewService(nil).Candidates("src/a.go", f.ctx); !reflect.DeepEqual(got, []string{f.proj + "/src/a.go", "src/a.go"}) {
		t.Errorf("nil fs: %q", got)
	}
}

func TestCandidatesSymlinkIntoBallast(t *testing.T) {
	f := newFixture(t)
	f.symlink(t, f.ballast, f.proj+"/link")
	patterns := []string{"~/.ballast/**"}

	// Existing target behind the link.
	f.write(t, f.ballast+"/state/rules.json")
	raw := f.proj + "/link/state/rules.json"
	got := f.svc.Candidates(raw, f.ctx)
	for _, w := range []string{raw, "link/state/rules.json", f.ballast + "/state/rules.json"} {
		if !slices.Contains(got, w) {
			t.Errorf("Candidates(%q) = %q, missing %q", raw, got, w)
		}
	}
	if !f.matches(t, patterns, got) {
		t.Errorf("symlink to ~/.ballast not matched by %q: %q", patterns, got)
	}
	// Without resolution the raw forms alone do not match (what the double check fixes).
	if f.matches(t, patterns, []string{raw, "link/state/rules.json"}) {
		t.Error("raw forms unexpectedly match ~/.ballast/**")
	}

	// Non-existent file: resolved through the nearest existing parent.
	raw = f.proj + "/link/state/new/deeper/file.json"
	got = f.svc.Candidates(raw, f.ctx)
	if !slices.Contains(got, f.ballast+"/state/new/deeper/file.json") || !f.matches(t, patterns, got) {
		t.Errorf("non-existent file behind link: %q", got)
	}
}

func TestCandidatesRelativeSymlinkAndDangling(t *testing.T) {
	f := newFixture(t)
	// Dangling link to a not-yet-existing file in ~/.ballast (absolute and relative targets).
	f.symlink(t, f.ballast+"/state/new.json", f.proj+"/dangling")
	if err := os.Symlink(filepath.FromSlash("../.ballast/state/rel.json"), f.osPath(f.proj+"/rel")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	for raw, want := range map[string]string{
		f.proj + "/dangling": f.ballast + "/state/new.json",
		f.proj + "/rel":      f.ballast + "/state/rel.json",
	} {
		got := f.svc.Candidates(raw, f.ctx)
		if !slices.Contains(got, want) {
			t.Errorf("Candidates(%q) = %q, missing %q", raw, got, want)
		}
		if !f.matches(t, []string{"~/.ballast/**"}, got) {
			t.Errorf("dangling link %q not matched", raw)
		}
	}

	// Dangling link under a symlinked directory.
	f.symlink(t, f.ballast+"/state", f.proj+"/dir")
	f.symlink(t, f.ballast+"/state/later", f.ballast+"/state/ptr")
	got := f.svc.Candidates(f.proj+"/dir/ptr", f.ctx)
	if !slices.Contains(got, f.ballast+"/state/later") {
		t.Errorf("dangling under symlinked dir: %q", got)
	}
}

func TestCandidatesSymlinkLoopAndNotDir(t *testing.T) {
	f := newFixture(t)
	f.symlink(t, f.proj+"/b", f.proj+"/a")
	f.symlink(t, f.proj+"/a", f.proj+"/b")
	if _, err := RealPath(f.svc.FS, f.proj+"/a", f.ctx.GOOS); err == nil {
		t.Error("RealPath of a symlink loop must fail")
	}
	got := f.svc.Candidates(f.proj+"/a", f.ctx)
	if !reflect.DeepEqual(got, []string{f.proj + "/a", "a"}) {
		t.Errorf("loop candidates = %q", got)
	}

	// A path below a regular file resolves through the file.
	f.write(t, f.proj+"/file")
	if runtime.GOOS != "windows" { // windows reports a different error class here
		want := f.proj + "/file/x/y"
		got = f.svc.Candidates(want, f.ctx)
		if !slices.Contains(got, want) {
			t.Errorf("below file: %q", got)
		}
		r, err := RealPath(f.svc.FS, want, f.ctx.GOOS)
		if err != nil || r != want {
			t.Errorf("RealPath below file = (%q, %v)", r, err)
		}
	}
}

// loopFS makes every link a self-reference to exercise the hop bound.
type loopFS struct{ sys.FS }

func (loopFS) EvalSymlinks(string) (string, error) { return "", os.ErrNotExist }
func (loopFS) Lstat(n string) (os.FileInfo, error) {
	return fakeLink{}, nil
}
func (loopFS) Readlink(n string) (string, error) { return n + "x", nil }

type fakeLink struct{ os.FileInfo }

func (fakeLink) Mode() os.FileMode { return os.ModeSymlink }

func TestRealPathHopBound(t *testing.T) {
	_, err := RealPath(loopFS{sys.OSFS{}}, "/a/b", "linux")
	if err == nil || !strings.Contains(err.Error(), "too many symbolic links") {
		t.Errorf("RealPath on endless chain: %v", err)
	}
}

// errFS fails EvalSymlinks with a configurable error.
type errFS struct {
	sys.FS
	err error
}

func (e errFS) EvalSymlinks(string) (string, error) { return "", e.err }

func TestCandidatesFSErrors(t *testing.T) {
	f := newFixture(t)
	for _, e := range []error{syscall.EACCES, errors.New("boom")} {
		svc := NewService(errFS{sys.OSFS{}, e})
		got := svc.Candidates("src/a.go", f.ctx)
		if !reflect.DeepEqual(got, []string{f.proj + "/src/a.go", "src/a.go"}) {
			t.Errorf("EvalSymlinks error %v: %q", e, got)
		}
		if _, err := RealPath(errFS{sys.OSFS{}, e}, f.proj+"/src", f.ctx.GOOS); !errors.Is(err, e) {
			t.Errorf("RealPath error = %v, want %v", err, e)
		}
	}
	// Everything is "not exist" (e.g. foreign root): the path comes back unchanged.
	r, err := RealPath(errFS{sys.OSFS{}, os.ErrNotExist}, "c:/a/b", "windows")
	if err != nil || r != "c:/a/b" {
		t.Errorf("all-missing RealPath = (%q, %v)", r, err)
	}
	if r, err := RealPath(nil, "/a", "linux"); err != nil || r != "/a" {
		t.Errorf("nil FS RealPath = (%q, %v)", r, err)
	}
}

func TestCandidatesSymlinkedProjectRoot(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.proj+"/src/a.go")
	link := f.home + "/linkproj"
	f.symlink(t, f.proj, link)
	ctx := f.ctx
	ctx.ProjectRoot = link
	ctx.Cwd = link

	got := f.svc.Candidates(link+"/src/a.go", ctx)
	want := []string{link + "/src/a.go", "src/a.go", f.proj + "/src/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("via link: %q, want %q", got, want)
	}
	// The real spelling is still project-relative through the resolved root.
	got = f.svc.Candidates(f.proj+"/src/a.go", ctx)
	want = []string{f.proj + "/src/a.go", "src/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("via real path: %q, want %q", got, want)
	}
}

func TestCandidatesRelativeSymlinkInsideProject(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.proj+"/src/real.go")
	f.symlink(t, f.proj+"/src/real.go", f.proj+"/alias.go")
	got := f.svc.Candidates("alias.go", f.ctx)
	want := []string{f.proj + "/alias.go", "alias.go", f.proj + "/src/real.go", "src/real.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// A negation on the real target applies even when the alias is outside it.
	if f.matches(t, []string{"**/*.go", "!src/**"}, got) {
		t.Error("negated resolved target must veto the alias")
	}
	if !f.matches(t, []string{"**/*.go", "!vendor/**"}, got) {
		t.Error("unrelated negation must not veto")
	}
}

func TestCandidatesWindowsContextOnAnyHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("drive q: may exist on a real Windows host")
	}
	ctx := model.PathContext{GOOS: "windows", Cwd: "q:/Users/x/repo", ProjectRoot: "q:/Users/x/repo", Home: "q:/Users/x"}
	svc := NewService(sys.OSFS{})
	got := svc.Candidates(`Q:\Users\X\Repo\Src\A.go`, ctx)
	want := []string{"q:/Users/X/Repo/Src/A.go", "Src/A.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	g, err := svc.CompileGlobs([]string{"**/src/*.GO", "!**/vendor/**"}, ctx)
	if err != nil || !g.Match(got...) {
		t.Errorf("windows glob match: %v %v", g, err)
	}
	g, err = svc.CompileGlobs([]string{"~/.ballast/**"}, ctx)
	if err != nil || !g.Match(svc.Candidates(`Q:\USERS\x\.BALLAST\state\x`, ctx)...) {
		t.Errorf("windows ~ glob: %v %v", g, err)
	}
}

func TestCandidatesWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows feature")
	}
	f := newFixture(t)
	junction := f.osPath(f.proj + "/junc")
	out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, f.osPath(f.ballast)).CombinedOutput()
	if err != nil {
		t.Skipf("mklink /J failed: %v: %s", err, out)
	}
	got := f.svc.Candidates(f.proj+"/junc/state/x.json", f.ctx)
	if !f.matches(t, []string{"~/.ballast/**"}, got) {
		t.Errorf("junction into ~/.ballast not matched: %q", got)
	}
}

func TestCandidatesDanglingRelativeLinkUnderSymlinkedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows resolves component by component; covered by the fakeFS tests")
	}
	f := newFixture(t)
	f.symlink(t, f.ballast+"/state", f.proj+"/dir")
	// The target is relative to the physical parent (~/.ballast/state), not to proj/dir's spelling.
	if err := os.Symlink("../later.json", f.osPath(f.ballast+"/state/ptr")); err != nil {
		t.Fatal(err)
	}
	got := f.svc.Candidates(f.proj+"/dir/ptr", f.ctx)
	if !slices.Contains(got, f.ballast+"/later.json") {
		t.Errorf("Candidates = %q, missing %q", got, f.ballast+"/later.json")
	}
	if !f.matches(t, []string{"~/.ballast/**"}, got) {
		t.Errorf("dangling relative link not matched: %q", got)
	}
	// What the OS really does.
	if err := os.WriteFile(f.osPath(f.proj+"/dir/ptr"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.osPath(f.ballast + "/later.json")); err != nil {
		t.Fatalf("OS did not write through the link: %v", err)
	}
}

func TestCandidatesDotDotAfterSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Win32 folds .. lexically")
	}
	f := newFixture(t)
	f.symlink(t, f.ballast+"/state", f.proj+"/d")
	patterns := []string{"~/.ballast/**"}

	// Target exists / does not exist; absolute, relative and ~-based spellings.
	f.write(t, f.ballast+"/rules.json")
	for _, c := range []struct{ raw, want string }{
		{"d/../rules.json", f.ballast + "/rules.json"},
		{f.proj + "/d/../rules.json", f.ballast + "/rules.json"},
		{"d/../new.json", f.ballast + "/new.json"},
		{"d/../new/deeper/x.json", f.ballast + "/new/deeper/x.json"},
		{"src/../d/../new.json", f.ballast + "/new.json"},
		{"./d/./../new.json", f.ballast + "/new.json"},
	} {
		got := f.svc.Candidates(c.raw, f.ctx)
		if !slices.Contains(got, c.want) {
			t.Errorf("Candidates(%q) = %q, missing %q", c.raw, got, c.want)
		}
		if !f.matches(t, patterns, got) {
			t.Errorf("Candidates(%q) = %q not matched by %q", c.raw, got, patterns)
		}
	}

	// The OS agrees.
	if err := os.WriteFile(f.osPath(f.proj+"/d/../w.json"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.osPath(f.ballast + "/w.json")); err != nil {
		t.Fatalf("OS did not apply .. physically: %v", err)
	}

	// Plain .. keeps its lexical meaning and adds no extra candidates.
	want := []string{f.proj + "/src/a.go", "src/a.go"}
	if got := f.svc.Candidates("src/../src/a.go", f.ctx); !reflect.DeepEqual(got, want) {
		t.Errorf("plain ..: %q, want %q", got, want)
	}
	// ".." above a missing directory and past the root stay harmless.
	if got := f.svc.Candidates("nope/../x.go", f.ctx); !slices.Contains(got, f.proj+"/x.go") {
		t.Errorf("missing dir ..: %q", got)
	}
	if got := f.svc.Candidates("/../../x.go", f.ctx); !slices.Contains(got, "/x.go") {
		t.Errorf("above root: %q", got)
	}
}

func TestPhysicalDotDotEdges(t *testing.T) {
	ctx := model.PathContext{Cwd: "/w", Home: "/h", GOOS: "linux"}
	fsys := sys.OSFS{}
	for _, c := range []struct {
		raw  string
		ctx  model.PathContext
		want bool
	}{
		{"a/b", ctx, false},
		{"..foo/x", ctx, false},
		{"a/..b/c", ctx, false},
		{"~/../x", model.PathContext{Cwd: "/w", GOOS: "linux"}, false},
		{"../x", model.PathContext{GOOS: "linux"}, false},
		{"../x", model.PathContext{Cwd: "rel", GOOS: "linux"}, false},
		{"a/../x", model.PathContext{Cwd: "/w", GOOS: "windows"}, false},
		{"/nonexistent-ballast-root/a/../x", ctx, true},
		{"~/a/../x", ctx, true},
	} {
		if _, ok := physicalDotDot(fsys, c.raw, c.ctx); ok != c.want {
			t.Errorf("physicalDotDot(%q, %+v) ok = %v, want %v", c.raw, c.ctx, ok, c.want)
		}
	}
	if _, ok := physicalDotDot(errFS{sys.OSFS{}, errors.New("boom")}, "/a/../x", ctx); ok {
		t.Error("resolution error must yield ok=false")
	}
}

func TestCompileGlobsSymlinkedHome(t *testing.T) {
	sb := testenv.New(t)
	root, err := filepath.EvalSymlinks(sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	goos := runtime.GOOS
	n := func(p string) string { return Normalize(p, "", model.PathContext{GOOS: goos}) }
	realHome := n(filepath.Join(root, "data", "u"))
	home := n(filepath.Join(root, "linkedhome"))
	for _, d := range []string{realHome + "/.ballast/state", realHome + "/proj"} {
		if err := os.MkdirAll(ToOS(d, goos), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ToOS(realHome, goos), ToOS(home, goos)); err != nil {
		if goos == "windows" {
			t.Skipf("symlink creation not permitted: %v", err)
		}
		t.Fatal(err)
	}
	proj := home + "/proj"
	ctx := model.PathContext{Cwd: proj, ProjectRoot: proj, Home: home, GOOS: goos}
	if err := os.Symlink(ToOS(realHome+"/.ballast", goos), ToOS(proj+"/l", goos)); err != nil {
		t.Fatal(err)
	}
	svc := NewService(sys.OSFS{})
	cands := svc.Candidates("l/state/x.json", ctx)
	if !slices.Contains(cands, realHome+"/.ballast/state/x.json") {
		t.Fatalf("candidates = %q", cands)
	}

	g, err := svc.CompileGlobs([]string{"~/.ballast/**"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Match(cands...) {
		t.Errorf("link into the real home not matched: %q", cands)
	}
	if !g.Match(home + "/.ballast/x") {
		t.Error("literal home spelling must still match")
	}
	if got := g.Patterns(); !reflect.DeepEqual(got, []string{"~/.ballast/**"}) {
		t.Errorf("Patterns() = %q, want the source list", got)
	}
	// Negations get both homes, too.
	g, err = svc.CompileGlobs([]string{"**/*.json", "!~/.ballast/**"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.Match(cands...) {
		t.Errorf("negated ~ pattern must veto the resolved-home spelling: %q", cands)
	}
	if !g.Match(proj + "/other.json") {
		t.Error("unrelated json must match")
	}
	// Bare "~" and a list without "~" patterns.
	if g, err = svc.CompileGlobs([]string{"~"}, ctx); err != nil || !g.Match(realHome) || !g.Match(home) {
		t.Errorf("bare ~: %v %v", g, err)
	}
	if _, err = svc.CompileGlobs([]string{"!~/x"}, ctx); !errors.Is(err, ErrOnlyNegation) {
		t.Errorf("only negation: %v", err)
	}
	if _, err = svc.CompileGlobs([]string{"~/["}, ctx); !errors.Is(err, ErrBadPattern) {
		t.Errorf("bad pattern: %v", err)
	}
}
