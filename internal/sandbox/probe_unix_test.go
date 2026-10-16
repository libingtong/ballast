//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/sys"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProbeUnsupportedHosts(t *testing.T) {
	for _, goos := range []string{"windows", "freebsd", "plan9"} {
		k, err := Probe(Options{GOOS: goos})
		if k != KindNone || err == nil {
			t.Errorf("%s: (%q, %v)", goos, k, err)
		}
	}
}

func TestProbeRejectsBackendsThatDoNotSandbox(t *testing.T) {
	dir := realTemp(t)
	cases := []struct {
		name, script, want string
	}{
		// "sandbox-exec" that just runs the command: the denied write succeeds.
		{"no enforcement", `shift; shift; shift; exec "$@"`, "succeeded"},
		// A backend that cannot even start the command.
		{"broken", `echo broken >&2; exit 3`, "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exe := writeScript(t, dir, "fake-"+strings.ReplaceAll(c.name, " ", "-"), c.script)
			k, err := Probe(Options{GOOS: "darwin", SandboxExec: exe})
			if k != KindNone || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("(%q, %v), want KindNone and an error containing %q", k, err, c.want)
			}
			// The result is cached per executable: the second call does not run it again.
			if err := os.Remove(exe); err != nil {
				t.Fatal(err)
			}
			k2, err2 := Probe(Options{GOOS: "darwin", SandboxExec: exe})
			if k2 != k || err2 == nil || err2.Error() != err.Error() {
				t.Errorf("second call: (%q, %v), want the cached (%q, %v)", k2, err2, k, err)
			}
		})
	}
	if k, err := Probe(Options{GOOS: "darwin", SandboxExec: filepath.Join(dir, "absent")}); k != KindNone || err == nil {
		t.Errorf("missing executable: (%q, %v)", k, err)
	}
}

func TestProbeHost(t *testing.T) {
	k, err := Probe(Options{})
	switch runtime.GOOS {
	case "darwin":
		if _, serr := os.Stat("/usr/bin/sandbox-exec"); serr == nil && (k != KindSandboxExec || err != nil) {
			t.Fatalf("(%q, %v)", k, err)
		}
	case "linux":
		if k != KindBwrap && k != KindNone {
			t.Fatalf("kind %q", k)
		}
	}
	// Cached: the second call is quick and identical.
	start := time.Now()
	k2, err2 := Probe(Options{})
	if k2 != k || (err2 == nil) != (err == nil) || time.Since(start) > 50*time.Millisecond {
		t.Errorf("second call (%q, %v) after %v", k2, err2, time.Since(start))
	}
}

// A bwrap earlier on PATH that is not root-owned (an agent can plant one) is skipped.
func TestBwrapPathMustBeRootOwned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("files created by root are root-owned")
	}
	dir := realTemp(t)
	writeScript(t, dir, "bwrap", "exit 0")
	opt := Options{Env: sys.MapEnv{Vars: map[string]string{"PATH": dir}}}
	if p, err := opt.bwrapPath(); err == nil {
		t.Fatalf("a user-owned bwrap was trusted: %s", p)
	}
	// An explicit Options.Bwrap (tests, doctor) is used as given.
	opt.Bwrap = "/opt/x/bwrap"
	if p, err := opt.bwrapPath(); err != nil || p != "/opt/x/bwrap" {
		t.Errorf("(%q, %v)", p, err)
	}
	k, err := Probe(Options{GOOS: "linux", Env: sys.MapEnv{Vars: map[string]string{"PATH": dir}}})
	if k != KindNone || err == nil {
		t.Errorf("Probe trusted a planted bwrap: (%q, %v)", k, err)
	}
}

// The harness itself must be able to see an escape: the same attempts without
// a sandbox change the host.
func TestHarnessDetectsEscapes(t *testing.T) {
	f := fix(t, "deny")
	f.guard(f.home+"/.bashrc", f.home+"/.claude.json", rp(f, ".ballast/project.json"))
	res := f.sh(`echo pwn >> "$H/.bashrc"; echo pwn > "$H/.claude.json"; echo pwn > "$R/.ballast/project.json"; mkdir -p "$R/.claude" && echo '{}' > "$R/.claude/settings.local.json"`, runOpts{plain: true})
	if res.ExitErr != nil {
		t.Fatalf("plain run: %v %s", res.ExitErr, res.Out)
	}
	changed := 0
	for p, want := range f.guards {
		if b, err := os.ReadFile(p); err != nil || "content:"+string(b) != want {
			changed++
		}
	}
	if changed != 3 {
		t.Errorf("without a sandbox %d of 3 guarded files changed; the guards would not notice an escape", changed)
	}
	if _, err := os.Stat(rp(f, ".claude/settings.local.json")); err != nil {
		t.Errorf("the absent-path check would not notice an escape: %v", err)
	}
}

// A root-owned bwrap in a group/other-writable directory is not trusted
// (the entry could be swapped or replaced by a symlink). Needs root to create
// root-owned files, so it runs in the Docker/CI root jobs only.
func TestBwrapPathDirectoryMustBeTrusted(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create root-owned files")
	}
	dir := realTemp(t)
	writeScript(t, dir, "bwrap", "exit 0")
	opt := Options{Env: sys.MapEnv{Vars: map[string]string{"PATH": dir}}}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := opt.bwrapPath(); err != nil || p != filepath.Join(dir, "bwrap") {
		t.Fatalf("trusted directory: (%q, %v)", p, err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if p, err := opt.bwrapPath(); err == nil {
		t.Fatalf("a bwrap in a world-writable directory was trusted: %s", p)
	}
}
