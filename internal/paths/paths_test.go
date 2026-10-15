package paths

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestIsCaseInsensitive(t *testing.T) {
	for goos, want := range map[string]bool{"windows": true, "darwin": true, "linux": false, "freebsd": false, "": false} {
		if got := IsCaseInsensitive(goos); got != want {
			t.Errorf("IsCaseInsensitive(%q) = %v, want %v", goos, got, want)
		}
	}
}

func TestToSlashForm(t *testing.T) {
	tests := []struct{ in, goos, want string }{
		{`C:\a\b`, "windows", "c:/a/b"},
		{`C:\a\b`, "linux", "c:/a/b"},
		{`C:\a\b`, "darwin", "c:/a/b"},
		{`\\srv\sh\x`, "windows", "//srv/sh/x"},
		{`\\srv\sh\x`, "linux", `\\srv\sh\x`},
		{"/c/Users", "windows", "c:/Users"},
		{"/c/Users", "linux", "/c/Users"},
		{"/C", "windows", "c:/"},
		{"/cygdrive/D/x", "windows", "d:/x"},
		{"/cygdrive/D/x", "linux", "/cygdrive/D/x"},
		{`\\?\C:\x`, "windows", "c:/x"},
		{`\\?\UNC\srv\sh\x`, "windows", "//srv/sh/x"},
		{`\\.\C:\x`, "windows", "c:/x"},
		{`\??\C:\x`, "windows", "c:/x"},
		{"/Users/x", "windows", "/Users/x"},
		{`a\b`, "windows", "a/b"},
		{`a\b`, "linux", `a\b`},
		{`D:rel`, "windows", "d:rel"},
		{`D:rel`, "linux", `D:rel`},
		{"", "windows", ""},
		{"~", "windows", "~"},
	}
	for _, tt := range tests {
		if got := ToSlashForm(tt.in, tt.goos); got != tt.want {
			t.Errorf("ToSlashForm(%q, %q) = %q, want %q", tt.in, tt.goos, got, tt.want)
		}
	}
}

func TestToOS(t *testing.T) {
	tests := []struct{ in, goos, want string }{
		{"c:/Users/x", "windows", `c:\Users\x`},
		{"c:/", "windows", `c:\`},
		{"//srv/share/x", "windows", `\\srv\share\x`},
		{"c:/Users/x", "linux", "c:/Users/x"},
		{"/home/u", "darwin", "/home/u"},
		{"", "windows", ""},
	}
	for _, tt := range tests {
		if got := ToOS(tt.in, tt.goos); got != tt.want {
			t.Errorf("ToOS(%q, %q) = %q, want %q", tt.in, tt.goos, got, tt.want)
		}
	}
	// ToOS inverts Normalize on windows.
	ctx := model.PathContext{GOOS: "windows"}
	for _, p := range []string{`C:\Users\x\a.go`, `\\srv\share\d`} {
		n := Normalize(p, "", ctx)
		if back := Normalize(ToOS(n, "windows"), "", ctx); back != n {
			t.Errorf("round trip %q: %q → %q", p, n, back)
		}
	}
}

func TestExpandHome(t *testing.T) {
	tests := []struct{ p, home, want string }{
		{"~", "/home/u", "/home/u"},
		{"~/x", "/home/u", "/home/u/x"},
		{"~/x", "/home/u/", "/home/u/x"},
		{"~/x", "/", "/x"},
		{"~/x", "c:/", "c:/x"},
		{"~", "c:/Users/x", "c:/Users/x"},
		{"~/", "/h", "/h/"},
		{"~user/x", "/h", "~user/x"},
		{"x/~/y", "/h", "x/~/y"},
		{"~/x", "", "~/x"},
		{"", "/h", ""},
	}
	for _, tt := range tests {
		if got := ExpandHome(tt.p, tt.home); got != tt.want {
			t.Errorf("ExpandHome(%q, %q) = %q, want %q", tt.p, tt.home, got, tt.want)
		}
	}
}

func TestNormalizeEmptyAndRelativeBase(t *testing.T) {
	ctx := model.PathContext{GOOS: "linux", Home: "/h"}
	if got := Normalize("", "/repo", ctx); got != "" {
		t.Errorf("Normalize(\"\") = %q", got)
	}
	if got := Normalize("a", "rel/base", ctx); got != "rel/base/a" {
		t.Errorf("relative base: %q", got)
	}
	if got := Normalize("a", "", ctx); got != "a" {
		t.Errorf("no base: %q", got)
	}
	// A relative home never expands (it would not be idempotent).
	ctx.Home = "rel"
	if got := Normalize("~/x", "/b", ctx); got != "/b/~/x" {
		t.Errorf("relative home: %q", got)
	}
}

func TestEqualWindowsSpec(t *testing.T) {
	ctx := model.PathContext{GOOS: "windows"}
	a := Normalize(`C:\Users\X\a.go`, "", ctx)
	b := Normalize("c:/users/x/A.GO", "", ctx)
	if !Equal(a, b, "windows") {
		t.Errorf("Equal(%q, %q, windows) = false", a, b)
	}
	if Equal("/Users/x/a.go", "/users/x/A.GO", "linux") {
		t.Error("Equal must be case-sensitive on linux")
	}
}

func TestWithinComponentWise(t *testing.T) {
	tests := []struct {
		p, dir, goos string
		want         bool
	}{
		{"/a/b", "/a", "linux", true},
		{"/a", "/a", "linux", true},
		{"/ab", "/a", "linux", false},
		{"/A/b", "/a", "linux", false},
		{"/A/b", "/a", "darwin", true},
		{"/a/b", "/", "linux", true},
		{"c:/Users/x/a", "C:/users", "windows", true},
		{"c:/Users2", "c:/Users", "windows", false},
		{"//srv/share/a", "//srv/share", "windows", true},
		{"/a", "/a/b", "linux", false},
		{"", "/a", "linux", false},
		{"/a", "", "linux", false},
	}
	for _, tt := range tests {
		if got := Within(tt.p, tt.dir, tt.goos); got != tt.want {
			t.Errorf("Within(%q, %q, %q) = %v, want %v", tt.p, tt.dir, tt.goos, got, tt.want)
		}
	}
}

func TestNormalizeLongInputs(t *testing.T) {
	ctx := model.PathContext{GOOS: "windows", Home: "c:/h"}
	p := strings.Repeat(`a\..\b\.\`, 5000) + "z"
	want := "c:/base" + strings.Repeat("/b", 5000) + "/z"
	if got := Normalize(p, "c:/base", ctx); got != want {
		t.Errorf("long input: got %d bytes, want %d", len(got), len(want))
	}
}
