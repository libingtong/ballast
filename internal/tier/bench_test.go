package tier

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// guard fails the benchmark when the per-op time of fn exceeds limit. The
// race detector slows everything down by an order of magnitude, so the limits
// are only enforced in normal builds.
//
// The figure compared with the limit is the best of several short rounds, not
// the b.N average: the budgets are about what one call costs on a warm page
// cache, and a single scheduler hiccup or a busy CI neighbour must not turn a
// 25 µs operation into a failure. (The best of k rounds is a lower bound on
// the real cost, but every round runs the full real-fs call sequence, so a
// genuinely slow implementation still fails.) The b.N average is reported as
// us/op for the record.
func guard(b *testing.B, limit time.Duration, fn func()) {
	b.Helper()
	b.StopTimer()
	avg := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(float64(avg.Nanoseconds())/1000, "us/op")
	if raceEnabled {
		return
	}
	const rounds, iters = 15, 300
	for i := 0; i < 20; i++ { // warm up
		fn()
	}
	best := time.Duration(1 << 62)
	for r := 0; r < rounds; r++ {
		start := time.Now()
		for i := 0; i < iters; i++ {
			fn()
		}
		if per := time.Since(start) / iters; per < best {
			best = per
		}
	}
	b.ReportMetric(float64(best.Nanoseconds())/1000, "best-us/op")
	if best > limit {
		b.Fatalf("best-of-%d %v per op exceeds the %v budget (b.N average %v)", rounds, best, limit, avg)
	}
	// The best-of figure is a lower bound, so the budget margin it shows is
	// optimistic (realfs-faked-root: ~110 µs best vs ~190 µs average on a busy
	// darwin host against a 200 µs budget). The b.N average must therefore
	// stay within 1.5x the budget as well; short runs (-benchtime=1x style)
	// are dominated by cold caches and are not judged on the average.
	if b.N >= 100 && avg > limit+limit/2 {
		b.Fatalf("b.N average %v per op exceeds 1.5x the %v budget (best-of-%d %v)", avg, limit, rounds, best)
	}
}

// rebaseFS serves the virtual absolute paths of a production host from a real
// directory tree below root: Lstat("/etc/claude-code/ballast/org.json")
// becomes a real lstat of <root>/etc/claude-code/ballast/org.json. The
// detection code therefore issues exactly the system calls it would issue on a
// real host (same path depth, same number of Lstat per component) while the
// test never needs root to create the files. The kernel walks the longer
// <root>/... path, which makes the measurement slightly pessimistic.
type rebaseFS struct{ root string }

func (r rebaseFS) real(name string) string { return filepath.Join(r.root, filepath.FromSlash(name)) }

func (r rebaseFS) virtual(real string) string {
	if rel, err := filepath.Rel(r.root, real); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return "/" + filepath.ToSlash(rel)
	}
	return real
}

func (r rebaseFS) ReadFile(n string) ([]byte, error)       { return os.ReadFile(r.real(n)) }
func (r rebaseFS) Stat(n string) (fs.FileInfo, error)      { return os.Stat(r.real(n)) }
func (r rebaseFS) Lstat(n string) (fs.FileInfo, error)     { return os.Lstat(r.real(n)) }
func (r rebaseFS) ReadDir(n string) ([]fs.DirEntry, error) { return os.ReadDir(r.real(n)) }
func (r rebaseFS) Readlink(n string) (string, error)       { return os.Readlink(r.real(n)) }
func (r rebaseFS) EvalSymlinks(n string) (string, error) {
	p, err := filepath.EvalSymlinks(r.real(n))
	return r.virtual(p), err
}
func (r rebaseFS) OpenFile(n string, flag int, perm fs.FileMode) (sys.File, error) {
	f, err := os.OpenFile(r.real(n), flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}
func (r rebaseFS) MkdirAll(n string, perm fs.FileMode) error { return os.MkdirAll(r.real(n), perm) }
func (r rebaseFS) Rename(o, n string) error                  { return os.Rename(r.real(o), r.real(n)) }
func (r rebaseFS) Remove(n string) error                     { return os.Remove(r.real(n)) }
func (r rebaseFS) RemoveAll(n string) error                  { return os.RemoveAll(r.real(n)) }

// realFixture builds the baseline host on the real file system, rebased so
// that its paths have the production depth (/etc/claude-code/ballast on
// linux, /Library/Application Support/ClaudeCode/ballast on darwin; the
// budgets of §3.1 and §3.6 are stated for those, not for a deep t.TempDir()
// chain), with ownership faked as root (an unprivileged test cannot create
// root-owned files). Returns the Options for Detect, the FS, the virtual
// managed dir and the owner func.
func realFixture(b *testing.B) (Options, sys.FS, string, ownerFunc) {
	b.Helper()
	root, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	fsys := rebaseFS{root: root}
	managed := layout.ManagedDirFor(runtime.GOOS)
	self := "/usr/local/libexec/ballast/ballast"
	home := "/home/u"
	if runtime.GOOS == "darwin" {
		home = "/Users/u"
	}
	write := func(p, c string, perm os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(fsys.real(p)), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(fsys.real(p), []byte(c), perm); err != nil {
			b.Fatal(err)
		}
	}
	write(self, "#!", 0o755)
	write(managed+"/ballast/org.json", baseOrgJSON(), 0o644)
	write(managed+"/managed-settings.d/ballast.json", mustJSON(b, settingsDocAt(managed, self, baseExtra)), 0o644)
	write(home+"/.claude/settings.json", `{"model":"x"}`, 0o644)
	owner := func(os.FileInfo) (uint32, bool) { return 0, true }
	return Options{
		FS:         fsys,
		Env:        sys.MapEnv{OS: runtime.GOOS, Vars: map[string]string{"HOME": home}},
		Layout:     layout.Layout{GOOS: runtime.GOOS, InstallRoot: home, ClaudeDir: home + "/.claude"},
		Self:       self,
		Owner:      owner,
		ManagedDir: managed,
		GOOS:       runtime.GOOS,
	}, fsys, managed, owner
}

// BenchmarkProbe: one full detection (budget 200 µs on a warm page cache).
func BenchmarkProbe(b *testing.B) {
	b.Run("memfs", func(b *testing.B) {
		_, opt := baseline(b)
		if got := Detect(opt); got.Tier != model.TierT1 {
			b.Fatalf("fixture is %s: %v", got.Tier, got.Missing)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			Detect(opt)
		}
		guard(b, 200*time.Microsecond, func() { Detect(opt) })
	})
	b.Run("realfs-faked-root", func(b *testing.B) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			b.Skip("T1 detection is darwin/linux only")
		}
		opt, _, _, _ := realFixture(b)
		got := Detect(opt)
		if got.Tier != model.TierT1 {
			b.Fatalf("fixture is %s: %v", got.Tier, got.Missing)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			Detect(opt)
		}
		guard(b, 200*time.Microsecond, func() { Detect(opt) })
	})
	b.Run("realfs-unmanaged-host", func(b *testing.B) {
		// What an ordinary developer machine pays: nothing is managed.
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			b.Skip("darwin/linux only")
		}
		home := b.TempDir()
		opt := Options{FS: sys.OSFS{}, Env: sys.MapEnv{OS: runtime.GOOS, Vars: map[string]string{"HOME": home}},
			Layout: layout.Layout{GOOS: runtime.GOOS, InstallRoot: home}, Self: "/usr/bin/true", GOOS: runtime.GOOS,
			ManagedDir: filepath.Join(home, "no-such-managed-dir")}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			Detect(opt)
		}
		guard(b, 200*time.Microsecond, func() { Detect(opt) })
	})
}

// BenchmarkLocked: the redirect-lock check every hook invocation pays
// (budget 30 µs).
func BenchmarkLocked(b *testing.B) {
	b.Run("absent-real-fs", func(b *testing.B) {
		// The common case: no org.json, one Lstat of the real compiled-in
		// managed dir.
		goos := runtime.GOOS
		if goos != "darwin" && goos != "linux" {
			b.Skip("darwin/linux only")
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Locked(sys.OSFS{}, goos, nil)
		}
		guard(b, 30*time.Microsecond, func() { Locked(sys.OSFS{}, goos, nil) })
	})
	b.Run("locked-memfs", func(b *testing.B) {
		m, _ := baseline(b)
		if !Locked(m, "linux", memOwner) {
			b.Fatal("fixture not locked")
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			Locked(m, "linux", memOwner)
		}
		guard(b, 30*time.Microsecond, func() { Locked(m, "linux", memOwner) })
	})
	b.Run("locked-real-fs-faked-root", func(b *testing.B) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			b.Skip("darwin/linux only")
		}
		_, fsys, managed, owner := realFixture(b)
		if st := readOrg(fsys, owner, managed); !lockedFrom(st) {
			b.Fatalf("fixture not locked: file trusted %v, chain ok %v, tier %s", st.fileTrusted, st.chainOK, st.org.Tier)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lockedFrom(readOrg(fsys, owner, managed))
		}
		guard(b, 30*time.Microsecond, func() { lockedFrom(readOrg(fsys, owner, managed)) })
	})
}

// BenchmarkLockedHome: the extra cost on a locked host. The first sub-benchmark
// uses the real sys.LookupHome for the current user (result discarded); the
// second runs the home_root rule on a fake FS.
func BenchmarkLockedHome(b *testing.B) {
	b.Run("passwd-real", func(b *testing.B) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			b.Skip("darwin/linux only")
		}
		opt := HomeOptions{FS: sys.OSFS{}, Env: sys.OSEnv{}, Euid: sys.Euid()}
		if _, _, err := LockedHome(opt); err != nil && !errors.Is(err, sys.ErrNoPasswdEntry) {
			b.Skipf("no passwd answer for the current uid: %v", err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _ = LockedHome(opt)
		}
		b.ReportMetric(float64((b.Elapsed()/time.Duration(b.N)).Nanoseconds())/1000, "us/op")
	})
	b.Run("home-root-rule-fake-fs", func(b *testing.B) {
		m := newMemFS()
		m.mkdirAll("/home", 0, 0o755)
		m.mkdirAll("/home/u", tUser, 0o755)
		opt := HomeOptions{FS: m, Env: sys.MapEnv{Vars: map[string]string{"HOME": "/home/u"}}, Owner: memOwner, Euid: tUser,
			HomeRoot: []string{"/home"}, Lookup: func(int) (string, error) { return "", sys.ErrNoPasswdEntry }}
		if h, src, err := LockedHome(opt); err != nil || h != "/home/u" || src != HomeFromHomeRoot {
			b.Fatalf("fixture: %q %q %v", h, src, err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_, _, _ = LockedHome(opt)
		}
		b.ReportMetric(float64((b.Elapsed()/time.Duration(b.N)).Nanoseconds())/1000, "us/op")
	})
}
