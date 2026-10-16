package tier

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// guard fails the benchmark when the measured per-op time exceeds limit.
// The race detector slows everything down by an order of magnitude, so the
// limits are only enforced in normal builds.
func guard(b *testing.B, limit time.Duration) {
	b.Helper()
	per := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(float64(per.Nanoseconds())/1000, "us/op")
	if !raceEnabled && b.N >= 100 && per > limit {
		b.Fatalf("%v per op exceeds the %v budget", per, limit)
	}
}

// realFixture builds the baseline host on the real file system below a temp
// dir, with ownership faked as root (an unprivileged test cannot create
// root-owned files). Returns the Options for Detect and the managed dir.
func realFixture(b *testing.B) (Options, string) {
	b.Helper()
	// Resolve symlinks (macOS /var → /private/var) so the Self chain is the
	// plain directory chain.
	root, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	managed := filepath.Join(root, "etc", "claude-code")
	self := filepath.Join(root, "usr", "libexec", "ballast", "ballast")
	home := filepath.Join(root, "home")
	write := func(p, c string, perm os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), perm); err != nil {
			b.Fatal(err)
		}
	}
	write(self, "#!", 0o755)
	write(filepath.Join(managed, "ballast", "org.json"), baseOrgJSON(), 0o644)
	write(filepath.Join(managed, "managed-settings.d", "ballast.json"), mustJSON(b, settingsDocAt(filepath.ToSlash(managed), self, baseExtra)), 0o644)
	write(filepath.Join(home, ".claude", "settings.json"), `{"model":"x"}`, 0o644)
	return Options{
		FS:         sys.OSFS{},
		Env:        sys.MapEnv{OS: runtime.GOOS, Vars: map[string]string{"HOME": home}},
		Layout:     layout.Layout{GOOS: runtime.GOOS, InstallRoot: home, ClaudeDir: filepath.Join(home, ".claude")},
		Self:       self,
		Owner:      func(os.FileInfo) (uint32, bool) { return 0, true },
		ManagedDir: managed,
		GOOS:       runtime.GOOS,
	}, managed
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
		b.StopTimer()
		guard(b, 200*time.Microsecond)
	})
	b.Run("realfs-faked-root", func(b *testing.B) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			b.Skip("T1 detection is darwin/linux only")
		}
		opt, _ := realFixture(b)
		got := Detect(opt)
		b.Logf("fixture tier %s missing %v (on linux a sticky /tmp ancestor makes the chain fail early)", got.Tier, got.Missing)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			Detect(opt)
		}
		b.StopTimer()
		guard(b, 200*time.Microsecond)
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
		b.StopTimer()
		guard(b, 200*time.Microsecond)
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
		guard(b, 30*time.Microsecond)
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
		guard(b, 30*time.Microsecond)
	})
	b.Run("locked-real-fs-faked-root", func(b *testing.B) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			b.Skip("darwin/linux only")
		}
		_, managed := realFixture(b)
		owner := func(os.FileInfo) (uint32, bool) { return 0, true }
		st := readOrg(sys.OSFS{}, owner, filepath.ToSlash(managed))
		b.Logf("fixture: file trusted %v, chain ok %v, tier %s", st.fileTrusted, st.chainOK, st.org.Tier)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			lockedFrom(readOrg(sys.OSFS{}, owner, filepath.ToSlash(managed)))
		}
		guard(b, 30*time.Microsecond)
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
