package install

import (
	"os"
	"testing"

	"github.com/libingtong/ballast/internal/sys"
)

func TestUnstableBinary(t *testing.T) {
	env := sys.MapEnv{Vars: map[string]string{"TMPDIR": "/custom/tmp/", "TEMP": `D:\scratch`}}
	for p, want := range map[string]bool{
		"/tmp/x/ballast":                                 true,
		"/var/tmp/ballast":                               true,
		"/private/tmp/ballast":                           true,
		"/var/folders/ab/cd/T/ballast":                   true,
		"/private/var/folders/ab/cd/T/ballast":           true,
		"/home/u/.cache/go-build/ab/ballast":             true,
		"/x/go-build123/b001/exe/ballast":                true,
		"/custom/tmp/ballast":                            true,
		`D:\scratch\ballast.exe`:                         true,
		`C:\Users\u\AppData\Local\Temp\x\ballast.exe`:    true,
		"/usr/local/bin/ballast":                         false,
		"/opt/ballast/bin/ballast":                       false,
		"/home/u/go/bin/ballast":                         false,
		"/home/u/tmpfiles/ballast":                       false,
		"/tmpfoo/ballast":                                false,
		`C:\Program Files\Ballast\ballast.exe`:           false,
		"/Users/u/Library/Application Support/b/ballast": false,
	} {
		if got := unstableBinary(p, env); got != want {
			t.Errorf("unstableBinary(%q) = %v, want %v", p, got, want)
		}
	}
	if !unstableBinary(os.TempDir()+"/x/ballast", nil) {
		t.Error("os.TempDir() must count as unstable even without an env")
	}
}

func TestIsAbsPath(t *testing.T) {
	for _, c := range []struct {
		p, goos string
		want    bool
	}{
		{"/a/b", "linux", true}, {"a/b", "linux", false}, {"", "linux", false}, {`C:\a`, "linux", false},
		{`C:\a`, "windows", true}, {"c:/a", "windows", true}, {`\\srv\share\a`, "windows", true},
		{`\a`, "windows", false}, {`C:a`, "windows", false}, {"/a", "windows", false}, {"a", "windows", false},
	} {
		if got := isAbsPath(c.p, c.goos); got != c.want {
			t.Errorf("isAbsPath(%q,%s) = %v", c.p, c.goos, got)
		}
	}
}

func TestLoneCarriageReturnIsRefused(t *testing.T) {
	if _, err := parseTOML([]byte("a = 1\rb = 2\r")); err == nil {
		t.Error("TOML with bare CR must be refused")
	}
	if _, err := parseYAMLDoc([]byte("a: 1\rhooks:\r  x: 1\r")).section(); err == nil {
		t.Error("YAML with bare CR must be refused")
	}
	if _, err := parseTOML([]byte("a = 1\r\nb = 2\r\n")); err != nil {
		t.Errorf("CRLF is fine: %v", err)
	}
}
