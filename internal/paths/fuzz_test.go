package paths

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

var fuzzGOOS = []string{"linux", "darwin", "windows", ""}

func FuzzNormalize(f *testing.F) {
	seeds := []struct{ p, base, home string }{
		{"src/a.go", "/repo", "/home/u"},
		{"/repo/./src/../b.go", "/repo", "/home/u"},
		{"~/.ballast/rules.json", "/repo", "/home/u"},
		{"~", "/repo", "/"},
		{`C:\Users\X\repo\a.go`, "c:/Users/x/repo", "c:/Users/x"},
		{`\\srv\share\d\e.txt`, "c:/Users/x/repo", "c:/Users/x"},
		{`\\?\UNC\srv\share\f`, "", ""},
		{`\\?\C:\x`, "", ""},
		{`//?///?/x`, "", ""},
		{`/a/../??/x`, "", ""},
		{`/foo/../c/x`, "c:/b", ""},
		{`/cygdrive/c/../d`, "", ""},
		{"/c/Users/x", "c:/b", ""},
		{"/c:/Users/x", "", ""},
		{"c:foo", "c:/b", ""},
		{"C:", "d:/b", ""},
		{"//srv/share/../..", "", ""},
		{"//./x", "", ""},
		{"//?/", "", ""},
		{"~/x", "", "~/y"},
		{"a/../..", "", ""},
		{"", "/b", "/h"},
		{"\x00/\xff", "\xfe", "\x00"},
		// Regressions found by fuzzing.
		{`\\srv\share\d`, "c:/Users/x/repo", ""},
		{`\`, "A:", "0"},
		{"0", "//.", "0"},
		{"0", "/:", "0"},
		{"../A:", "0", "0"},
		{"a/../~/x", "", "/h"},
		{`\\?\Volume{1234}\Users\x\.ballast`, "c:/repo", "c:/Users/x"},
		{`\\?\GLOBALROOT\Device\HarddiskVolume3\..\..\..\x`, "c:/repo", ""},
		{`\\.\pipe\x`, "c:/repo", ""},
		{`\??\Volume{1}\x`, "c:/repo", ""},
		{`//?//c:/x`, "", ""},
		{`//?/UNC`, "", ""},
		{`//?/c:foo/x`, "", ""},
		{"rel", "//?/Volume{1}/b", ""},
	}
	for _, s := range seeds {
		for i := range fuzzGOOS {
			f.Add(s.p, s.base, s.home, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, p, base, home string, g uint8) {
		goos := fuzzGOOS[int(g)%len(fuzzGOOS)]
		ctx := model.PathContext{GOOS: goos, Home: home, Cwd: base}
		n1 := Normalize(p, base, ctx)
		if n2 := Normalize(n1, "", ctx); n2 != n1 {
			t.Fatalf("goos=%q home=%q: Normalize(%q, %q) = %q; again = %q", goos, home, p, base, n1, n2)
		}
		if n1 != "" && strings.Contains(n1, "//") && !(goos == "windows" && strings.HasPrefix(n1, "//") && !strings.Contains(n1[2:], "//")) {
			t.Fatalf("goos=%q: unclean result %q for %q", goos, n1, p)
		}
		if goos == "windows" && strings.ContainsRune(n1, '\\') {
			t.Fatalf("backslash left in %q for %q", n1, p)
		}
		if n1 != "" && (n1[0] == '/' || isDriveAbs(n1, goos == "windows")) {
			if n3 := Normalize(n1, base, ctx); n3 != n1 {
				t.Fatalf("with base: %q → %q", n1, n3)
			}
			// ToOS/Normalize round trip.
			if back := Normalize(ToOS(n1, goos), "", ctx); back != n1 {
				t.Fatalf("round trip %q → %q", n1, back)
			}
			// An absolute path is within itself and Rel never panics.
			if !Within(n1, n1, goos) || !Equal(n1, n1, goos) {
				t.Fatalf("Within/Equal reflexivity failed for %q", n1)
			}
		}
		_ = ToSlashForm(p, goos)
		_ = ExpandHome(p, home)
		_, _ = Rel(base, p, goos)
	})
}

func FuzzGlob(f *testing.F) {
	for _, s := range []string{"**/*.go", "!vendor/**", "~/.ballast/**", "*.md", "./a", "{a,b}/**", "[a-z]*", "[", "{", "!", "\\", "a/**/b", "!!x", "**", ""} {
		f.Add(s, "!vendor/**", "src/vendor/x.go", "/home/u[1]", true)
		f.Add(s, s, s, "", false)
	}
	f.Fuzz(func(t *testing.T, p1, p2, cand, home string, ci bool) {
		opt := GlobOptions{Home: home, CaseInsensitive: ci}
		g, err := CompileGlobs([]string{p1, p2}, opt)
		if err != nil {
			if g != nil {
				t.Fatal("non-nil GlobSet with error")
			}
			return
		}
		a := g.Match(cand)
		if b := g.Match(cand, cand); b != a && a {
			t.Fatalf("duplicate candidate changed a positive result: %q %q %q", p1, p2, cand)
		}
		if ci != g.opt.CaseInsensitive {
			t.Fatal("options lost")
		}
		if ValidatePattern(p1) != nil || ValidatePattern(p2) != nil {
			t.Fatalf("compiled but invalid: %q %q", p1, p2)
		}
	})
}
