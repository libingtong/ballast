package layout

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/sys"
)

func TestResolveWithZeroOptionsEqualsResolve(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "h", "u")
	cases := []struct {
		name string
		env  sys.MapEnv
	}{
		{"home-only", sys.MapEnv{Vars: map[string]string{"HOME": home, "USERPROFILE": home}, OS: "linux"}},
		{"ballast-home", sys.MapEnv{Vars: map[string]string{"HOME": home, "USERPROFILE": home, EnvHome: "/x/b"}, OS: "linux"}},
		{"all-redirects", sys.MapEnv{Vars: map[string]string{"HOME": home, "USERPROFILE": home, EnvHome: "/x/b", EnvManagedDir: "/x/m", EnvInstallRoot: "/x/i", EnvClaudeDir: "/x/c", EnvCodexHome: "/x/x", EnvHermesHome: "/x/h"}, OS: "darwin"}},
		{"no-home-with-ballast-home", sys.MapEnv{Vars: map[string]string{EnvHome: "/x/b"}, OS: "linux"}},
		{"no-home-at-all", sys.MapEnv{Vars: map[string]string{}, OS: "linux"}},
		{"windows", sys.MapEnv{Vars: map[string]string{"USERPROFILE": `C:\Users\u`}, OS: "windows"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, werr := Resolve(tc.env)
			called := false
			got, gerr := ResolveWith(tc.env, Options{PasswdHome: func() (string, error) { called = true; return "/never", nil }})
			if called {
				t.Fatal("PasswdHome called although IgnoreRedirects is false")
			}
			if (werr == nil) != (gerr == nil) {
				t.Fatalf("error mismatch: Resolve %v, ResolveWith %v", werr, gerr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ResolveWith(Options{}) != Resolve:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestResolveWithIgnoreRedirects(t *testing.T) {
	env := sys.MapEnv{OS: "linux", Vars: map[string]string{
		"HOME":         "/agent/home",
		EnvHome:        "/agent/ballast",
		EnvManagedDir:  "/agent/managed",
		EnvInstallRoot: "/agent/root",
		EnvClaudeDir:   "/client/claude",
		EnvCodexHome:   "/client/codex",
		EnvHermesHome:  "/client/hermes",
	}}
	l, err := ResolveWith(env, Options{IgnoreRedirects: true, PasswdHome: func() (string, error) { return "/real/home", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if l.Home != filepath.Join("/real/home", ".ballast") {
		t.Errorf("Home = %q", l.Home)
	}
	if l.InstallRoot != "/real/home" {
		t.Errorf("InstallRoot = %q", l.InstallRoot)
	}
	if l.ManagedDir != ManagedDirFor("linux") || l.OrgConfig != filepath.Join(ManagedDirFor("linux"), "ballast", "org.json") {
		t.Errorf("ManagedDir = %q OrgConfig = %q", l.ManagedDir, l.OrgConfig)
	}
	// Client redirections keep their meaning.
	if l.ClaudeDir != "/client/claude" || l.CodexDir != "/client/codex" || l.HermesDir != "/client/hermes" {
		t.Errorf("client dirs = %q %q %q", l.ClaudeDir, l.CodexDir, l.HermesDir)
	}
	if l.CursorDir != filepath.Join("/real/home", ".cursor") {
		t.Errorf("CursorDir = %q", l.CursorDir)
	}
	for _, p := range []string{l.Home, l.UserRules, l.StateDir, l.EvidenceDir, l.AuditDir, l.OrgRules} {
		if strings.Contains(p, "agent") {
			t.Errorf("path %q still derives from an ignored variable", p)
		}
	}
}

func TestResolveWithPasswdHomeFallsBackToEnvHome(t *testing.T) {
	env := sys.MapEnv{OS: "linux", Vars: map[string]string{"HOME": "/env/home", EnvHome: "/x/b"}}
	l, err := ResolveWith(env, Options{IgnoreRedirects: true})
	if err != nil {
		t.Fatal(err)
	}
	if l.Home != filepath.Join("/env/home", ".ballast") {
		t.Errorf("Home = %q", l.Home)
	}
}

func TestResolveWithPasswdHomeFailure(t *testing.T) {
	sentinel := errors.New("boom")
	env := sys.MapEnv{OS: "linux", Vars: map[string]string{"HOME": "/agent/home", EnvHome: "/agent/ballast"}}
	_, err := ResolveWith(env, Options{IgnoreRedirects: true, PasswdHome: func() (string, error) { return "", sentinel }})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapped sentinel (must NOT fall back to $HOME or BALLAST_HOME)", err)
	}
	_, err = ResolveWith(env, Options{IgnoreRedirects: true, PasswdHome: func() (string, error) { return "", nil }})
	if err == nil {
		t.Fatal("empty home from PasswdHome must fail")
	}
}

func TestResolveWithIgnoredEnvironNotLeaked(t *testing.T) {
	env := sys.MapEnv{OS: "linux", Vars: map[string]string{"HOME": "/h", EnvHome: "/x", "KEEP": "1"}}
	e := lockedEnv{Env: env, home: "/real"}
	got := e.Environ()
	if !reflect.DeepEqual(got, []string{"HOME=/h", "KEEP=1"}) {
		t.Fatalf("Environ = %v", got)
	}
}
