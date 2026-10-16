package sys

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLookupHomeCurrentEuid(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("passwd lookup is darwin/linux only")
	}
	// The environment must not influence the result: compare with and
	// without a redirected $HOME.
	before, _ := LookupHome(Euid())
	t.Setenv("HOME", t.TempDir())
	home, err := LookupHome(Euid())
	if err != nil {
		if errors.Is(err, ErrNoPasswdEntry) {
			t.Skipf("current uid has no passwd entry in this environment: %v", err)
		}
		t.Fatalf("LookupHome(%d): %v", Euid(), err)
	}
	if home == "" || !filepath.IsAbs(home) {
		t.Fatalf("LookupHome returned %q, want a non-empty absolute path", home)
	}
	if before != home {
		t.Fatalf("LookupHome changed with $HOME: %q vs %q", before, home)
	}
	if home != filepath.Clean(home) {
		t.Fatalf("home %q is not clean", home)
	}
}

func TestLookupHomeUnknownUID(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("passwd lookup is darwin/linux only")
	}
	for _, uid := range []int{2147483000, -5} {
		_, err := LookupHome(uid)
		if !errors.Is(err, ErrNoPasswdEntry) {
			t.Fatalf("LookupHome(%d) err = %v, want ErrNoPasswdEntry", uid, err)
		}
	}
}

func TestLookupHomeWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	if _, err := LookupHome(0); !errors.Is(err, ErrPasswdUnsupported) {
		t.Fatalf("err = %v, want ErrPasswdUnsupported", err)
	}
	if Euid() != -1 {
		t.Fatalf("Euid() = %d on windows, want -1", Euid())
	}
}

func BenchmarkLookupHome(b *testing.B) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		b.Skip("darwin/linux only")
	}
	uid := Euid()
	for i := 0; i < b.N; i++ {
		_, _ = LookupHome(uid)
	}
}
