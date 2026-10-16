//go:build unix

package sandbox

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// fix extends newFixture with the shell variables the scripts use.
func fix(t *testing.T, network model.NetworkPolicy) *fixture {
	t.Helper()
	return fixIn(t, network, "")
}

func fixIn(t *testing.T, network model.NetworkPolicy, sub string) *fixture {
	t.Helper()
	f := newFixtureIn(t, network, sub)
	f.extra = []string{"R=" + f.repo, "H=" + f.home, "O=" + f.outside, "W2=" + f.wt2, "T=" + f.tmp, "SUB=" + f.sub,
		"GD=" + filepath.Join(f.repo, ".git")}
	// A nested protected entry under a directory that is not pinned.
	mkfiles(t, f.repo, "a/b/.vscode/settings.json", "a/b/src.go")
	return f
}

// attempt is one escape attempt of docs/p2-design.md §3.4 "沙箱集成用例".
type attempt struct {
	name   string
	script string
	// guard: files that must be byte-identical afterwards.
	guard func(f *fixture) []string
	// absent: paths that must not exist on the host afterwards.
	absent func(f *fixture) []string
	// created: absolute paths the child manages to create INSIDE the sandbox
	// on linux (the bind mounts cannot cover what does not exist yet); the
	// post-check must find and quarantine every one of them. On darwin the
	// profile refuses the creation, so nothing may be found.
	created func(f *fixture) []string
	// createdDarwin: what the darwin post-check finds (nil ⇒ nothing). Only
	// the originals that the child MOVED to a new path show up there: the
	// profile refuses creating anything protected, but renaming a
	// non-protected ancestor is allowed.
	createdDarwin func(f *fixture) []string
}

func rp(f *fixture, rel ...string) string { return filepath.Join(append([]string{f.repo}, rel...)...) }

func TestEscapeAttempts(t *testing.T) {
	attempts := []attempt{
		{
			name:   "shell start-up files in the home directory",
			script: `echo pwn >> "$H/.bashrc"; echo pwn >> "$H/.zshrc"; echo pwn > "$H/.profile"; echo pwn > "$H/.newrc"; echo pwn > "$H/.zprofile"`,
			guard: func(f *fixture) []string {
				return []string{f.home + "/.bashrc", f.home + "/.zshrc", f.home + "/.profile"}
			},
			absent: func(f *fixture) []string { return []string{f.home + "/.newrc", f.home + "/.zprofile"} },
		},
		{
			name:   "claude.json and the user settings",
			script: `echo x > "$H/.claude.json"; echo x > "$H/.claude/settings.json"; echo x > "$H/.claude/settings.local.json"; echo x > "$H/.claude/settings.new.json"`,
			guard: func(f *fixture) []string {
				return []string{f.home + "/.claude.json", f.home + "/.claude/settings.json", f.home + "/.claude/settings.local.json"}
			},
			absent: func(f *fixture) []string { return []string{f.home + "/.claude/settings.new.json"} },
		},
		{
			name:   "ballast state",
			script: `echo x > "$H/.ballast/evidence/x"; echo x > "$H/.ballast/evidence/e1.json"; echo x > "$H/.ballast/rules.json"; mkdir "$H/.ballast/q"; echo x > "$H/.ballast/work/w"`,
			guard: func(f *fixture) []string {
				return []string{f.home + "/.ballast/evidence/e1.json", f.home + "/.ballast/rules.json"}
			},
			absent: func(f *fixture) []string {
				return []string{f.home + "/.ballast/evidence/x", f.home + "/.ballast/q", f.home + "/.ballast/work/w"}
			},
		},
		{
			name:   "write project.json",
			script: `echo x > "$R/.ballast/project.json"; echo x >> "$R/.ballast/project.json"`,
			guard:  func(f *fixture) []string { return []string{rp(f, ".ballast/project.json")} },
		},
		{
			name:   "new file in an existing .ballast",
			script: `echo x > "$R/.ballast/x"`,
			absent: func(f *fixture) []string { return []string{rp(f, ".ballast/x")} },
		},
		{
			name:    "new nested .ballast",
			script:  `mkdir -p "$R/pkg/.ballast" && echo x > "$R/pkg/.ballast/x"`,
			absent:  func(f *fixture) []string { return []string{rp(f, "pkg/.ballast")} },
			created: func(f *fixture) []string { return []string{rp(f, "pkg/.ballast")} },
		},
		{
			name:   "rename the repository and write",
			script: `cd "$R/.." && mv repo repo2 && echo x > repo2/.ballast/project.json; mv repo2 repo; true`,
			guard:  func(f *fixture) []string { return []string{rp(f, ".ballast/project.json"), rp(f, "src/main.go")} },
			absent: func(f *fixture) []string { return []string{f.repo + "2"} },
		},
		{
			name:   "replace .git by a directory",
			script: `cd "$R" && mv .git .git2 && mkdir .git && echo x > .git/config`,
			guard:  func(f *fixture) []string { return []string{rp(f, ".git/config"), rp(f, ".git/HEAD")} },
			absent: func(f *fixture) []string { return []string{rp(f, ".git2")} },
		},
		{
			name:   "hard link a protected file and write through it",
			script: `ln "$H/.ballast/rules.json" "$R/hl" && echo x > "$R/hl"; ln "$H/.claude.json" "$R/hl2" && echo x > "$R/hl2"; true`,
			guard:  func(f *fixture) []string { return []string{f.home + "/.ballast/rules.json", f.home + "/.claude.json"} },
		},
		{
			name: "symlink to a protected file or directory, then write through it",
			script: `ln -s "$H/.ballast/rules.json" "$R/lnk" && echo x > "$R/lnk"; ln -s "$H/.ballast" "$R/bl" && echo x > "$R/bl/evidence/x" && mkdir "$R/bl/q";
ln -s .git/config "$R/cfg" && echo x > "$R/cfg"; ln -s ../.git/hooks "$R/src/hk" && echo x > "$R/src/hk/pre-commit"; true`,
			guard: func(f *fixture) []string { return []string{f.home + "/.ballast/rules.json", rp(f, ".git/config")} },
			absent: func(f *fixture) []string {
				return []string{f.home + "/.ballast/evidence/x", f.home + "/.ballast/q", rp(f, ".git/hooks/pre-commit")}
			},
		},
		{
			name:   "write git hooks",
			script: `echo '#!/bin/sh' > "$R/.git/hooks/pre-commit"; mkdir "$R/.git/hooks/sub"; echo x > "$R/.git/hooks/post-merge"`,
			absent: func(f *fixture) []string {
				return []string{rp(f, ".git/hooks/pre-commit"), rp(f, ".git/hooks/sub"), rp(f, ".git/hooks/post-merge")}
			},
		},
		{
			name:   "write git config",
			script: `echo '[core]' >> "$R/.git/config"; echo 'fsmonitor = /bin/false' >> "$R/.git/config"; git -C "$R" config core.hooksPath /evil; git -C "$R" config --local core.fsmonitor evil; true`,
			guard:  func(f *fixture) []string { return []string{rp(f, ".git/config"), rp(f, ".git/config.worktree")} },
			absent: func(f *fixture) []string { return []string{rp(f, ".git/config.lock")} },
		},
		{
			name:   "nested submodule: replace its gitfile by a directory",
			script: `rm -rf "$SUB/.git" 2>/dev/null; mkdir "$SUB/.git" 2>/dev/null && echo x > "$SUB/.git/config"; true`,
			guard:  func(f *fixture) []string { return []string{rp(f, "sub/.git"), rp(f, ".git/modules/sub/config")} },
		},
		{
			name:   "nested submodule: redirect its gitfile",
			script: `echo 'gitdir: ../evil' > "$SUB/.git"; true`,
			guard:  func(f *fixture) []string { return []string{rp(f, "sub/.git")} },
		},
		{
			name:   "modules/sub/config and hooks in the common dir",
			script: `echo x >> "$GD/modules/sub/config"; echo '#!/bin/sh' > "$GD/modules/sub/hooks/post-checkout"; echo x > "$GD/modules/sub/config.worktree"; echo x > "$GD/modules/sub/commondir"; true`,
			guard:  func(f *fixture) []string { return []string{rp(f, ".git/modules/sub/config")} },
			absent: func(f *fixture) []string {
				return []string{rp(f, ".git/modules/sub/hooks/post-checkout"), rp(f, ".git/modules/sub/config.worktree"), rp(f, ".git/modules/sub/commondir")}
			},
			created: func(f *fixture) []string {
				return []string{rp(f, ".git/modules/sub/config.worktree"), rp(f, ".git/modules/sub/commondir")}
			},
		},
		{
			name:   "another worktree's config.worktree, gitdir and commondir",
			script: `echo x > "$GD/worktrees/w2/config.worktree"; echo x > "$GD/worktrees/w2/gitdir"; echo x > "$GD/worktrees/w2/commondir"; true`,
			guard: func(f *fixture) []string {
				return []string{rp(f, ".git/worktrees/w2/gitdir"), rp(f, ".git/worktrees/w2/commondir")}
			},
			absent: func(f *fixture) []string { return []string{rp(f, ".git/worktrees/w2/config.worktree")} },
			created: func(f *fixture) []string {
				return []string{rp(f, ".git/worktrees/w2/config.worktree")}
			},
		},
		{
			name:   "gitlink in the index plus a nested .git",
			script: `cd "$R" && git update-index --add --cacheinfo "160000,$(git rev-parse HEAD),evil" && mkdir -p evil/.git && echo x > evil/.git/config; true`,
			absent: func(f *fixture) []string { return []string{rp(f, "evil/.git")} },
			created: func(f *fixture) []string {
				return []string{rp(f, "evil/.git"), rp(f, "evil/.git/config")}
			},
		},
		{
			name:   "editor and agent configuration",
			script: `mkdir -p "$R/.claude" && echo '{}' > "$R/.claude/settings.local.json"; mkdir -p "$R/.vscode" && echo '{}' > "$R/.vscode/tasks.json"; echo '{}' > "$R/.mcp.json"; echo x > "$R/.bashrc"; echo x > "$R/.gitconfig"; mkdir -p "$R/.idea"; true`,
			absent: func(f *fixture) []string {
				return []string{rp(f, ".claude"), rp(f, ".vscode"), rp(f, ".mcp.json"), rp(f, ".bashrc"), rp(f, ".gitconfig"), rp(f, ".idea")}
			},
			created: func(f *fixture) []string {
				return []string{rp(f, ".claude"), rp(f, ".vscode"), rp(f, ".mcp.json"), rp(f, ".bashrc"), rp(f, ".gitconfig"), rp(f, ".idea")}
			},
		},
		{
			name:   "rename a non-pinned ancestor and recreate the protected directory",
			script: `cd "$R" && mv a a2 && mkdir -p a/b/.vscode && echo '{}' > a/b/.vscode/tasks.json; true`,
			absent: func(f *fixture) []string { return []string{rp(f, "a/b/.vscode"), rp(f, "a2/b/.vscode/tasks.json")} },
			created: func(f *fixture) []string {
				// linux: the recreated entry has the Baseline path of the
				// original but another inode, and the moved original sits at
				// a new path; both are found and quarantined. darwin refuses
				// the mkdir (any-depth regex); only the moved original is new.
				return []string{rp(f, "a/b/.vscode"), rp(f, "a2/b/.vscode")}
			},
			createdDarwin: func(f *fixture) []string { return []string{rp(f, "a2/b/.vscode")} },
		},
		{
			name:   "protected file under a renamed ancestor",
			script: `cd "$R" && mv sub sub2 2>/dev/null; mkdir -p sub 2>/dev/null; mkdir sub/.git 2>/dev/null && echo x > sub/.git/config; echo x > sub/.gitmodules; true`,
			absent: func(f *fixture) []string { return []string{rp(f, "sub/.git/config"), rp(f, "sub/.gitmodules")} },
			created: func(f *fixture) []string {
				return []string{rp(f, "sub/.git"), rp(f, "sub/.git/config"), rp(f, "sub/.gitmodules"), rp(f, "sub2/.git")}
			},
			createdDarwin: func(f *fixture) []string { return []string{rp(f, "sub2/.git")} },
		},
		{
			// PostCheck must not fail open: 4097 further matches exceed
			// MaxMandatoryMatches, which only bounds the bwrap mounts.
			name: "protected file followed by more matches than the mount limit",
			script: `mkdir -p "$R/.claude" && echo '{}' > "$R/.claude/settings.local.json"
i=0; while [ $i -le 4096 ]; do echo "$R/many/$i/.idea"; i=$((i+1)); done | xargs mkdir -p; true`,
			absent: func(f *fixture) []string { return []string{rp(f, ".claude/settings.local.json")} },
			created: func(f *fixture) []string {
				return []string{rp(f, ".claude"), rp(f, "many/0/.idea"), rp(f, "many/4096/.idea")}
			},
		},
		{
			// ... and not on a directory the scan cannot enter.
			name: "protected file followed by a directory without any permission",
			script: `mkdir -p "$R/.claude" && echo '{}' > "$R/.claude/settings.local.json"
mkdir -p "$R/locked/.vscode" "$R/locked/in" && echo '{}' > "$R/locked/.vscode/tasks.json" && chmod 000 "$R/locked"; true`,
			absent: func(f *fixture) []string {
				return []string{rp(f, ".claude/settings.local.json"), rp(f, "locked/.vscode/tasks.json")}
			},
			created: func(f *fixture) []string { return []string{rp(f, ".claude"), rp(f, "locked/.vscode")} },
		},
	}
	be := requireBackend(t)
	for _, a := range attempts {
		t.Run(a.name, func(t *testing.T) {
			if testing.Short() && strings.Contains(a.name, "mount limit") {
				t.Skip("creates thousands of directories")
			}
			f := fix(t, model.NetworkDeny)
			if a.guard != nil {
				f.guard(a.guard(f)...)
			}
			// Sanity: the attempt must not be a no-op script.
			res := f.sh(a.script, runOpts{})
			t.Logf("exit=%v found=%v out=%s", res.ExitErr, relFound(f.root, res.Found), strings.TrimSpace(res.Out))
			f.assertGuards()
			if a.absent != nil {
				f.assertAbsent(a.absent(f)...)
			}
			var want []string
			if be == KindBwrap && a.created != nil {
				want = a.created(f)
			} else if be == KindSandboxExec && a.createdDarwin != nil {
				want = a.createdDarwin(f)
			}
			got := map[string]bool{}
			for _, fp := range res.Found {
				got[fp] = true
			}
			for _, w := range want {
				if !got[w] {
					t.Errorf("post-check did not find %s (found %v)", w, res.Found)
				}
			}
			if be == KindSandboxExec && len(res.Found) != len(want) {
				t.Errorf("darwin: post-check found %v, want exactly %v", res.Found, want)
			}
			if len(res.Found) > 0 {
				if _, err := os.Stat(filepath.Join(f.quar, t.Name())); err != nil {
					t.Errorf("found paths but no quarantine directory: %v", err)
				}
			}
		})
	}
}

// What must keep working inside W.
func TestAllowedWrites(t *testing.T) {
	f := fix(t, model.NetworkDeny)
	script := `set -e
echo ok > "$R/src/ok.txt"
mkdir -p "$R/build/out" && echo built > "$R/build/out/a.bin"
echo tmp > "$TMPDIR/scratch" && test -f "$TMPDIR/scratch"
echo devnull > /dev/null
echo viafd > /dev/fd/1 >/dev/null
cd "$R"
git add src/ok.txt build/out/a.bin
git commit -q -m "from the sandbox"
git status --porcelain > "$R/status.txt"
git branch topic
git tag v1
mkdir -p "$R/node_modules/x" && echo m > "$R/node_modules/x/index.js"
echo pass`
	res := f.sh(script, runOpts{})
	if res.ExitErr != nil || !strings.Contains(res.Out, "pass") {
		t.Fatalf("writes inside W failed: %v\n%s", res.ExitErr, res.Out)
	}
	if len(res.Found) != 0 {
		t.Errorf("post-check found %v", res.Found)
	}
	for _, p := range []string{"src/ok.txt", "build/out/a.bin", "status.txt", "node_modules/x/index.js"} {
		if _, err := os.Stat(rp(f, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	out := git(t, f.home, f.repo, "log", "--oneline", "-1")
	if !strings.Contains(out, "from the sandbox") {
		t.Errorf("commit not visible on the host: %s", out)
	}
	git(t, f.home, f.repo, "rev-parse", "refs/tags/v1")
	// The fixture is a valid repo afterwards: hooks and config untouched.
	for _, p := range []string{".git/config"} {
		if b, _ := os.ReadFile(rp(f, p)); strings.Contains(string(b), "fsmonitor") {
			t.Errorf("%s was modified", p)
		}
	}
}

// A linked worktree: its private git dir and the common dir are outside the
// work tree and writable except for the mandatory entries.
func TestLinkedWorktree(t *testing.T) {
	f := fix(t, model.NetworkDeny)
	spec := f.specFor(f.wt2, model.NetworkDeny)
	if len(spec.GitDirs) != 2 {
		t.Fatalf("expected a private git dir and a common dir, got %v", spec.GitDirs)
	}
	gd := filepath.Join(f.repo, ".git")
	priv := filepath.Join(gd, "worktrees", "w2")
	f.guard(filepath.Join(gd, "config"), filepath.Join(priv, "gitdir"), filepath.Join(priv, "commondir"), filepath.Join(f.wt2, ".git"),
		filepath.Join(gd, "modules/sub/config"), filepath.Join(f.repo, "src/main.go"))
	script := `set -e
cd "$W2"
echo ok > ok.txt
git add ok.txt
git commit -q -m "from the worktree"
echo pass
# attempts below are expected to fail or be undone
echo x >> "$GD/config" 2>/dev/null || true
echo x > "$GD/worktrees/w2/gitdir" 2>/dev/null || true
echo x > "$GD/worktrees/w2/commondir" 2>/dev/null || true
echo x > "$GD/worktrees/w2/config.worktree" 2>/dev/null || true
echo '#!/bin/sh' > "$GD/hooks/post-commit" 2>/dev/null || true
echo x > "$W2/.git" 2>/dev/null || true
echo x > "$R/src/main.go" 2>/dev/null || true
exit 0`
	res := f.sh(script, runOpts{spec: &spec})
	if !strings.Contains(res.Out, "pass") {
		t.Fatalf("worktree commit failed: %v\n%s", res.ExitErr, res.Out)
	}
	f.assertGuards()
	f.assertAbsent(filepath.Join(priv, "config.worktree"), filepath.Join(gd, "hooks", "post-commit"))
	if out := git(t, f.home, f.wt2, "log", "--oneline", "-1"); !strings.Contains(out, "from the worktree") {
		t.Errorf("commit lost: %s", out)
	}
	if runtime.GOOS == "linux" {
		for _, fp := range res.Found {
			t.Logf("found %s", fp)
		}
	}
}

// ---- network ----

type tcpListener struct {
	ln   net.Listener
	hits atomic.Int64
}

func listenTCP(t *testing.T) *tcpListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &tcpListener{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.hits.Add(1)
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return l
}

func (l *tcpListener) port() int { return l.ln.Addr().(*net.TCPAddr).Port }

// shortSock returns a unix socket path outside every W that fits sun_path.
func shortSock(t *testing.T, f *fixture) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		d, err := os.MkdirTemp("/tmp", "bsi")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		return filepath.Join(d, "s.sock")
	}
	p := filepath.Join(f.outside, "s.sock")
	if len(p) > 100 {
		t.Skipf("socket path too long: %s", p)
	}
	return p
}

func listenUnix(t *testing.T, path string) *atomic.Int64 {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return &hits
}

func TestNetworkPolicies(t *testing.T) {
	needTool(t, "python3")
	for _, network := range []model.NetworkPolicy{model.NetworkDeny, model.NetworkLoopback, model.NetworkAllow} {
		t.Run(string(network), func(t *testing.T) {
			f := fix(t, network)
			l := listenTCP(t)
			unixPath := shortSock(t, f)
			uhits := listenUnix(t, unixPath)
			f.extra = append(f.extra, fmt.Sprintf("PORT=%d", l.port()), "SOCK="+unixPath)
			py := filepath.Join(f.root, "tcp.py")
			os.WriteFile(py, []byte(pyTCP), 0o644)
			pyu := filepath.Join(f.root, "unix.py")
			os.WriteFile(pyu, []byte(pyUnix), 0o644)

			// TCP to a listener the TEST started on 127.0.0.1 (outside the sandbox).
			res := f.sh(`python3 "`+py+`" 127.0.0.1 "$PORT"`, runOpts{})
			connected := strings.Contains(res.Out, "CONNECTED")
			time.Sleep(50 * time.Millisecond)
			wantConnect := network == model.NetworkAllow || (network == model.NetworkLoopback && runtime.GOOS == "darwin")
			if connected != wantConnect {
				t.Errorf("network %s on %s: connect to the host listener = %v, want %v\n%s", network, runtime.GOOS, connected, wantConnect, res.Out)
			}
			if !wantConnect && l.hits.Load() != 0 {
				t.Errorf("the host listener was reached %d times", l.hits.Load())
			}

			// A non-loopback address (TEST-NET-1 and a public address): under
			// deny nothing may leave. darwin: the profile refuses the
			// connect(2) itself (EPERM), so it is not a mere timeout; linux:
			// the empty network namespace, recorded only.
			if network == model.NetworkDeny {
				for _, addr := range []string{"192.0.2.1", "1.1.1.1"} {
					res = f.sh(`python3 "`+py+`" `+addr+` 443`, runOpts{})
					if strings.Contains(res.Out, "CONNECTED") {
						t.Errorf("deny: connected to %s\n%s", addr, res.Out)
					}
					if runtime.GOOS == "darwin" && !strings.Contains(res.Out, "PermissionError") {
						t.Errorf("darwin deny: connect to %s should be refused by the profile (EPERM), got\n%s", addr, res.Out)
					}
					t.Logf("%s deny: connect %s: %s", runtime.GOOS, addr, strings.TrimSpace(res.Out))
				}
			}

			// Unix sockets outside W are unreachable under every policy
			// (darwin: path rules; linux: only /run and /var/run are masked,
			// a socket elsewhere stays reachable and is a registered residual
			// risk, so it is only logged there).
			res = f.sh(`python3 "`+pyu+`" "$SOCK"`, runOpts{})
			if strings.Contains(res.Out, "CONNECTED") {
				if runtime.GOOS == "darwin" {
					t.Errorf("connected to a unix socket outside W under %s", network)
				} else {
					t.Logf("linux residual risk (registered): a unix socket outside /run is reachable (%d hits)", uhits.Load())
				}
			}

			// Environment: the socket-bearing variables are gone.
			res = f.sh(`env`, runOpts{env: []string{"SSH_AUTH_SOCK=" + unixPath, "DOCKER_HOST=unix://" + unixPath, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + unixPath, "DBUS_SYSTEM_BUS_ADDRESS=unix:path=" + unixPath}})
			for _, v := range []string{"SSH_AUTH_SOCK", "DOCKER_HOST", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS"} {
				if strings.Contains(res.Out, v+"=") {
					t.Errorf("%s reached the child under %s", v, network)
				}
			}

			// The test's own listener inside the sandbox.
			local := `import socket,sys
s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1)
c=socket.socket(); c.settimeout(3)
try:
    c.connect(s.getsockname()); print("LOCAL-OK")
except Exception as e:
    print("LOCAL-FAIL", e)
`
			res = f.runArgv([]string{"python3", "-c", local}, runOpts{})
			ok := strings.Contains(res.Out, "LOCAL-OK")
			switch {
			case network == model.NetworkDeny && runtime.GOOS == "darwin":
				if ok {
					t.Errorf("darwin deny: binding a loopback listener must fail\n%s", res.Out)
				}
			default:
				if !ok {
					t.Errorf("%s/%s: a listener the test starts itself on loopback should work\n%s", runtime.GOOS, network, res.Out)
				}
			}
		})
	}
}

// Under deny, darwin refuses every IP bind, including the wildcard.
func TestDarwinBindUnderDeny(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin profile rule")
	}
	needTool(t, "python3")
	f := fix(t, model.NetworkDeny)
	script := `import socket
res = {}
for name, fam, addr in (("0.0.0.0", socket.AF_INET, "0.0.0.0"), ("127.0.0.1", socket.AF_INET, "127.0.0.1"), ("::1", socket.AF_INET6, "::1")):
    s = socket.socket(fam, socket.SOCK_STREAM)
    try:
        s.bind((addr, 0)); s.listen(1); res[name] = "BOUND"
    except Exception as e:
        res[name] = "REFUSED"
    s.close()
print(res)
`
	res := f.runArgv([]string{"python3", "-c", script}, runOpts{})
	for _, a := range []string{"0.0.0.0", "127.0.0.1", "::1"} {
		if !strings.Contains(res.Out, "'"+a+"': 'REFUSED'") {
			t.Errorf("deny: bind %s not refused: %s", a, res.Out)
		}
	}
}

// Under loopback and allow, wildcard / non-loopback listeners are only
// recorded (registered residual risk: the `local ip` filter cannot express it).
func TestDarwinWildcardBindRecorded(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin profile rule")
	}
	needTool(t, "python3")
	for _, network := range []model.NetworkPolicy{model.NetworkLoopback, model.NetworkAllow} {
		f := fix(t, network)
		script := `import socket
for addr in ("127.0.0.1", "0.0.0.0"):
    s = socket.socket(); 
    try:
        s.bind((addr, 0)); s.listen(1); print(addr, "BOUND")
    except Exception as e:
        print(addr, "REFUSED", e)
    s.close()
`
		res := f.runArgv([]string{"python3", "-c", script}, runOpts{})
		if !strings.Contains(res.Out, "127.0.0.1 BOUND") {
			t.Errorf("%s: a localhost listener must work: %s", network, res.Out)
		}
		t.Logf("residual-risk record (%s): %s", network, strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
	}
}

// ---- environment ----

func TestChildEnvironment(t *testing.T) {
	f := fix(t, model.NetworkDeny)
	f.spec.LoaderEnv = []string{"LD_LIBRARY_PATH=/opt/ballast-lib"}
	res := f.sh(`env | sort`, runOpts{env: []string{
		"LD_PRELOAD=/nonexistent/ballast-preload.so", "LD_LIBRARY_PATH=/opt/ballast-lib", "DYLD_INSERT_LIBRARIES=/nonexistent/x.dylib", "DYLD_LIBRARY_PATH=/x",
		"GOCACHE=" + filepath.Join(f.home, ".cache", "go-build"), "XDG_CACHE_HOME=" + filepath.Join(f.home, ".cache"), "npm_config_cache=" + filepath.Join(f.home, ".npm"),
		"PIP_CACHE_DIR=" + filepath.Join(f.home, "pip"), "YARN_CACHE_FOLDER=" + filepath.Join(f.home, "yarn"),
		"GIT_TRACE=" + filepath.Join(f.home, "trace.log"),
	}})
	if res.ExitErr != nil {
		t.Fatalf("env failed: %v\n%s", res.ExitErr, res.Out)
	}
	if strings.Contains(strings.ToLower(res.Out), "preload") || strings.Contains(res.Out, "ld.so") {
		t.Errorf("a loader complaint reached the output (LD_PRELOAD leaked into the backend):\n%s", res.Out)
	}
	kv := envMap(strings.Split(strings.TrimSpace(res.Out), "\n"))
	tmp := f.tmp
	if runtime.GOOS == "linux" {
		tmp = "/tmp"
	}
	if kv["TMPDIR"] != tmp {
		t.Errorf("TMPDIR = %q, want %q", kv["TMPDIR"], tmp)
	}
	for _, k := range CacheVars {
		if v := kv[k]; !strings.HasPrefix(v, tmp+"/cache/") {
			t.Errorf("%s = %q, want a directory below %s/cache", k, v, tmp)
		}
	}
	for _, k := range []string{"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH"} {
		if _, ok := kv[k]; ok {
			t.Errorf("%s reached the child", k)
		}
	}
	if runtime.GOOS == "linux" {
		if kv["LD_LIBRARY_PATH"] != "/opt/ballast-lib" {
			t.Errorf("LoaderEnv did not reach the child: LD_LIBRARY_PATH=%q", kv["LD_LIBRARY_PATH"])
		}
	} else if _, ok := kv["LD_LIBRARY_PATH"]; ok {
		t.Errorf("darwin: LD_LIBRARY_PATH reached the child")
	}
	// The sandbox does not strip other GIT_* variables from the child.
	if kv["GIT_TRACE"] == "" {
		t.Errorf("GIT_TRACE was dropped from the child environment")
	}
	// ...but the file it names is outside W, so the trace cannot be written.
	if _, err := os.Stat(filepath.Join(f.home, "trace.log")); err == nil {
		t.Error("GIT_TRACE wrote outside W")
	}
	// A cache variable inside runner_allow_write is left alone.
	shared := filepath.Join(f.root, "sharedcache")
	mkfiles(t, shared, "x")
	spec := f.spec
	spec.AllowWrite = append(append([]string(nil), spec.AllowWrite...), shared)
	res = f.sh(`env | sort`, runOpts{spec: &spec, env: []string{"GOCACHE=" + shared + "/go"}})
	kv = envMap(strings.Split(strings.TrimSpace(res.Out), "\n"))
	if kv["GOCACHE"] != shared+"/go" {
		t.Errorf("GOCACHE inside runner_allow_write = %q, want it kept", kv["GOCACHE"])
	}
	if !strings.HasPrefix(kv["XDG_CACHE_HOME"], tmp+"/cache/") {
		t.Errorf("XDG_CACHE_HOME = %q", kv["XDG_CACHE_HOME"])
	}
}

// go in a sandbox: the build cache goes to the job temp dir, the module cache
// is only read.
func TestGoToolchainInSandbox(t *testing.T) {
	goBin := needTool(t, "go")
	f := fix(t, model.NetworkDeny)
	mkfiles(t, f.repo, "gomod/go.mod", "gomod/a_test.go")
	os.WriteFile(rp(f, "gomod/go.mod"), []byte("module example.invalid/m\n\ngo 1.21\n"), 0o644)
	os.WriteFile(rp(f, "gomod/a_test.go"), []byte("package m\nimport \"testing\"\nfunc TestA(t *testing.T) { t.Log(\"ok\") }\n"), 0o644)
	mcache := filepath.Join(f.root, "gomodcache")
	mkfiles(t, mcache, "x")
	cmd := exec.Command(goBin, "env", "GOROOT")
	goroot, err := cmd.Output()
	if err != nil {
		t.Skipf("go env: %v", err)
	}
	res := f.sh(`cd "$R/gomod" && go test -count=1 ./... 2>&1`, runOpts{env: []string{"GOROOT=" + strings.TrimSpace(string(goroot)), "GOFLAGS=-mod=mod", "GOMODCACHE=" + mcache, "GOPROXY=off", "GOTOOLCHAIN=local"}})
	if res.ExitErr != nil || !strings.Contains(res.Out, "ok") {
		t.Fatalf("go test in the sandbox failed: %v\n%s", res.ExitErr, res.Out)
	}
}

// ---- terminal ----

const ptyRunner = `
import os, pty, sys, json, subprocess
cfg = json.loads(sys.argv[1])
pid, fd = pty.fork()
if pid == 0:
    r = subprocess.run(cfg["argv"], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                       env=cfg["env"], cwd=cfg["cwd"], start_new_session=cfg["new_session"])
    os.write(1, b"CHILD-RESULT-BEGIN\n" + r.stdout + b"CHILD-RESULT-END\n")
    os._exit(0)
buf = b""
while True:
    try:
        d = os.read(fd, 65536)
    except OSError:
        break
    if not d:
        break
    buf += d
os.waitpid(pid, 0)
sys.stdout.write(buf.decode("utf-8", "replace"))
`

const ttyProbe = `
import os, fcntl, termios, errno, sys
def name(e):
    return errno.errorcode.get(e.errno, str(e.errno))
out = {}
try:
    fd = os.open("/dev/tty", os.O_RDWR)
    out["open_dev_tty"] = "OPENED"
    try:
        fcntl.ioctl(fd, termios.TIOCSTI, b"x"); out["tiocsti_dev_tty"] = "INJECTED"
    except OSError as e:
        out["tiocsti_dev_tty"] = name(e)
except OSError as e:
    out["open_dev_tty"] = name(e)
for n in (0, 1, 2):
    try:
        fcntl.ioctl(n, termios.TIOCSTI, b"x"); out["tiocsti_fd%d" % n] = "INJECTED"
    except OSError as e:
        out["tiocsti_fd%d" % n] = name(e)
print("TTYRESULT " + repr(sorted(out.items())))
`

// The wrapped command never reaches the controlling terminal of its runner.
func TestTerminalIsolation(t *testing.T) {
	needTool(t, "python3")
	f := fix(t, model.NetworkDeny)
	// The runner under test has a pty as controlling terminal (python pty.fork);
	// the wrapped argv is started in a NEW SESSION with stdin /dev/null and a pipe.
	run := func(plain, newSession bool) string {
		w := Wrapped{Kind: KindNone, Argv: []string{"python3", "-c", ttyProbe}}
		env := f.baseEnv()
		if !plain {
			var err error
			w, err = Wrap(Options{}, f.kind, f.spec, []string{"python3", "-c", ttyProbe})
			if err != nil {
				t.Fatal(err)
			}
			if !w.NewSession {
				t.Fatal("Wrapped.NewSession must be true for a wrapped command")
			}
			env = applyEnv(env, w.Env)
		}
		cfg, _ := json.Marshal(map[string]any{"argv": w.Argv, "env": envMap(env), "cwd": f.repo, "new_session": newSession})
		cmd := exec.Command("python3", "-c", ptyRunner, string(cfg))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pty runner: %v\n%s", err, out)
		}
		return string(out)
	}
	// Control: unwrapped, same session as the runner's controlling terminal:
	// the escape primitive exists on this host (proves the probe can detect it).
	control := run(true, false)
	t.Logf("control (same session, no sandbox): %s", lineWith(control, "TTYRESULT"))
	if !strings.Contains(control, "'open_dev_tty', 'OPENED'") {
		t.Skipf("this host's pty setup gives the control no controlling terminal; the probe would prove nothing:\n%s", control)
	}
	// The contract: a new session alone cuts the terminal off (no sandbox).
	plainNew := run(true, true)
	t.Logf("control (new session, no sandbox): %s", lineWith(plainNew, "TTYRESULT"))
	if strings.Contains(plainNew, "OPENED") || strings.Contains(plainNew, "INJECTED") || !strings.Contains(plainNew, "'open_dev_tty', 'ENXIO'") {
		t.Errorf("a new session must leave no controlling terminal:\n%s", plainNew)
	}
	got := run(false, true)
	t.Logf("sandboxed, new session: %s", lineWith(got, "TTYRESULT"))
	if !strings.Contains(got, "TTYRESULT") {
		t.Fatalf("no result:\n%s", got)
	}
	if strings.Contains(got, "OPENED") || strings.Contains(got, "INJECTED") {
		t.Errorf("the wrapped command reached the terminal:\n%s", got)
	}
	if !strings.Contains(got, "'open_dev_tty', 'ENXIO'") && !strings.Contains(got, "'open_dev_tty', 'EPERM'") && !strings.Contains(got, "'open_dev_tty', 'EACCES'") {
		t.Errorf("open(/dev/tty) should fail:\n%s", got)
	}
	// And a wrapped command in the SAME session is still kept off /dev/tty
	// for writing by the profile / by bwrap's own --new-session.
	same := run(false, false)
	t.Logf("sandboxed, same session (belt and braces): %s", lineWith(same, "TTYRESULT"))
	if strings.Contains(same, "INJECTED") {
		t.Errorf("TIOCSTI succeeded for a wrapped command:\n%s", same)
	}
}

func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// ---- Spec.Tree ----

func TestJobPrivateTree(t *testing.T) {
	needTool(t, "git")
	f := fix(t, model.NetworkDeny)
	work := filepath.Join(f.home, ".ballast", "work")
	job := filepath.Join(work, "job1")
	tree := filepath.Join(job, "tree")
	mkfiles(t, tree, "src/main.go", "README.md", "node_modules/.keep")
	mkfiles(t, f.home, ".gitignore")
	git(t, f.home, f.home, "init", "-q", "-b", "main") // a dotfiles repository above the copy
	spec := Spec{
		AllowWrite: []string{tree}, RepoRoots: []string{tree}, DenyWrite: []string{filepath.Join(f.home, ".ballast"), filepath.Join(f.home, ".claude.json")},
		DenyWriteGlobs: []string{filepath.Join(f.home, ".claude", "settings*.json")},
		TmpDir:         f.tmp, Network: model.NetworkDeny, Home: f.home, Cwd: tree, Tree: tree, WorkRoot: work,
	}
	f.extra = append(f.extra, "TREE="+tree, "JOB="+job)
	f.guard(rp(f, "src/main.go"), rp(f, ".git/config"), rp(f, ".ballast/project.json"), f.home+"/.ballast/rules.json", f.home+"/.ballast/evidence/e1.json", f.home+"/.claude.json")
	script := `
echo copy > "$TREE/out.txt" && echo in-tree-ok
echo more > "$TREE/src/new.go" && mkdir -p "$TREE/build" && echo b > "$TREE/build/o" && echo nested-ok
echo evil > "$R/src/main.go" 2>/dev/null; echo evil > "$R/src/planted.go" 2>/dev/null
echo evil >> "$R/.git/config" 2>/dev/null; echo evil > "$R/.ballast/project.json" 2>/dev/null
echo evil > "$H/.ballast/rules.json" 2>/dev/null; echo evil > "$H/.ballast/evidence/new" 2>/dev/null
mkdir "$H/.ballast/other" 2>/dev/null; echo x > "$H/.ballast/work/loose" 2>/dev/null
mv "$JOB" "$JOB-moved" 2>/dev/null
mv "$TREE" "$TREE-moved" 2>/dev/null
echo x > "$JOB/sibling" 2>/dev/null
mkdir "$TREE/.git" 2>/dev/null; echo x > "$TREE/.claude" 2>/dev/null
cd "$TREE" && git rev-parse --show-toplevel 2>&1 | head -1
echo "ceiling=$GIT_CEILING_DIRECTORIES"
true`
	res := f.sh(script, runOpts{spec: &spec})
	t.Logf("found=%v\n%s", res.Found, res.Out)
	for _, want := range []string{"in-tree-ok", "nested-ok", "ceiling=" + job} {
		if !strings.Contains(res.Out, want) {
			t.Errorf("missing %q in output", want)
		}
	}
	if strings.Contains(res.Out, f.home) && strings.Contains(res.Out, "toplevel") {
		t.Errorf("git discovered the dotfiles repository above the copy")
	}
	if !strings.Contains(res.Out, "not a git repository") {
		t.Errorf("git inside the copy should find no repository:\n%s", res.Out)
	}
	for _, p := range []string{"out.txt", "src/new.go", "build/o"} {
		if _, err := os.Stat(filepath.Join(tree, p)); err != nil {
			t.Errorf("write into the copy lost: %v", err)
		}
	}
	f.assertGuards()
	f.assertAbsent(rp(f, "src/planted.go"), f.home+"/.ballast/evidence/new", f.home+"/.ballast/other", f.home+"/.ballast/work/loose",
		job+"-moved", tree+"-moved", job+"/sibling", tree+"/.git", tree+"/.claude")
	if _, err := os.Stat(tree); err != nil {
		t.Errorf("the copy moved: %v", err)
	}
}

// ---- network allow: /run and /var/run (linux) ----

func TestLinuxRunDirectoriesMasked(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux bwrap masks /run and /var/run")
	}
	needTool(t, "python3")
	dir := os.Getenv("BALLAST_IT_RUNDIR") // a directory under /run the docker script makes writable
	if dir == "" {
		if ciTrue() && os.Getenv("BALLAST_REQUIRE_BWRAP") == "1" {
			t.Fatal("BALLAST_IT_RUNDIR is required in CI")
		}
		t.Skip("BALLAST_IT_RUNDIR not set")
	}
	sock := filepath.Join(dir, fmt.Sprintf("it-%d.sock", os.Getpid()))
	hits := listenUnix(t, sock)
	pyu := filepath.Join(t.TempDir(), "unix.py")
	os.WriteFile(pyu, []byte(pyUnix), 0o644)
	for _, network := range []model.NetworkPolicy{model.NetworkDeny, model.NetworkLoopback, model.NetworkAllow} {
		f := fix(t, network)
		res := f.sh(`ls /run/ 2>&1; ls /var/run/ 2>&1; python3 "`+pyu+`" "`+sock+`"; python3 "`+pyu+`" /var/run/`+filepath.Base(dir)+`/`+filepath.Base(sock), runOpts{})
		if strings.Contains(res.Out, "CONNECTED") || hits.Load() != 0 {
			t.Errorf("%s: reached a socket in /run (%d hits)\n%s", network, hits.Load(), res.Out)
		}
	}
}

// Paths with spaces, quotes, parentheses, regex metacharacters and non-ASCII
// are rendered correctly (darwin: SBPL string and regex escaping; linux:
// argv elements) and the protection still holds for them.
func TestAwkwardPaths(t *testing.T) {
	f := fixIn(t, model.NetworkDeny, `it "q" (x) [1] é+ü.dir`)
	f.guard(rp(f, ".ballast/project.json"), rp(f, ".git/config"), f.home+"/.bashrc")
	res := f.sh(`set -e
echo ok > "$R/src/ok.txt"
cd "$R" && git add src/ok.txt && git commit -q -m awkward
echo pass
set +e
echo x > "$R/.ballast/project.json"; echo x >> "$R/.git/config"; echo x >> "$H/.bashrc"
mkdir -p "$R/pkg/.claude" && echo x > "$R/pkg/.claude/s.json"
echo x > "$R/pkg/.mcp.json"
true`, runOpts{})
	if !strings.Contains(res.Out, "pass") {
		t.Fatalf("writes inside W failed:\n%s", res.Out)
	}
	f.assertGuards()
	f.assertAbsent(rp(f, "pkg/.claude"), rp(f, "pkg/.mcp.json"))
}

// docs/p2-design.md §3.4 "缓存变量" 【未核验】: with the default network deny the
// module cache is only read. Resolved here: a module that imports a
// dependency builds and tests under the sandbox while GOMODCACHE (outside W)
// is read-only, and nothing is written to it.
func TestGoReadsReadOnlyModuleCache(t *testing.T) {
	goBin := needTool(t, "go")
	out, err := exec.Command(goBin, "env", "GOMODCACHE", "GOROOT", "GOMOD").Output()
	if err != nil {
		t.Skipf("go env: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 || lines[2] == "" || lines[2] == os.DevNull {
		t.Skip("not inside a module")
	}
	modcache, goroot, gomod := lines[0], lines[1], lines[2]
	const dep = "github.com/bmatcuk/doublestar/v4"
	if _, err := os.Stat(filepath.Join(modcache, dep+"@v4.10.3")); err != nil {
		if ciTrue() {
			t.Fatalf("the module cache lacks %s: %v", dep, err)
		}
		t.Skipf("the module cache lacks %s", dep)
	}
	f := fix(t, model.NetworkDeny)
	proj := rp(f, "withdep")
	mkfiles(t, f.repo, "withdep/x")
	gomodBytes, _ := os.ReadFile(gomod)
	gosumBytes, _ := os.ReadFile(filepath.Join(filepath.Dir(gomod), "go.sum"))
	// The repository's own requirements, a different module path, one test.
	mod := strings.Replace(string(gomodBytes), "module github.com/libingtong/ballast", "module example.invalid/withdep", 1)
	os.WriteFile(filepath.Join(proj, "go.mod"), []byte(mod), 0o644)
	os.WriteFile(filepath.Join(proj, "go.sum"), gosumBytes, 0o644)
	os.WriteFile(filepath.Join(proj, "d_test.go"), []byte("package d\nimport (\"testing\"; \""+dep+"\")\nfunc TestD(t *testing.T){ ok, err := doublestar.Match(\"a/**/c\", \"a/b/c\"); if err != nil || !ok { t.Fatal(err) } }\n"), 0o644)
	snapshot := func() string {
		var b strings.Builder
		// Only the dependencies' own directories: the cache is shared with
		// other go processes on the machine.
		for _, sub := range []string{"github.com/bmatcuk", "cache/download/github.com/bmatcuk", "mvdan.cc", "cache/download/mvdan.cc"} {
			filepath.Walk(filepath.Join(modcache, sub), func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
				}
				return nil
			})
		}
		return b.String()
	}
	before := snapshot()
	res := f.sh(`cd "$R/withdep" && go test -count=1 ./... 2>&1`, runOpts{env: []string{
		"GOROOT=" + goroot, "GOMODCACHE=" + modcache, "GOPROXY=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local",
	}})
	if res.ExitErr != nil || !strings.Contains(res.Out, "ok") {
		t.Fatalf("go test with a dependency under a read-only GOMODCACHE failed: %v\n%s", res.ExitErr, res.Out)
	}
	if after := snapshot(); after != before {
		bl, al := strings.Split(before, "\n"), strings.Split(after, "\n")
		seen := map[string]bool{}
		for _, l := range bl {
			seen[l] = true
		}
		var diff []string
		for _, l := range al {
			if !seen[l] {
				diff = append(diff, l)
			}
		}
		t.Errorf("the sandboxed go command changed the module cache: %v", diff)
	}
}

// A minimal test project per runtime that is installed on this host runs
// under the profile (docs/p2-design.md §3.4 DarwinMachAllow: a service is
// only added with this kind of evidence). go is covered by
// TestGoToolchainInSandbox and TestGoReadsReadOnlyModuleCache; runtimes that
// are not installed here (pytest, mvn, dotnet) are reported as not run.
func TestRuntimeMinimalProjects(t *testing.T) {
	f := fix(t, model.NetworkDeny)
	ran := 0
	// node: node --test and npm test (cache redirected by CacheEnv).
	if node, err := exec.LookPath("node"); err == nil {
		mkfiles(t, f.repo, "rt/node/x")
		os.WriteFile(rp(f, "rt/node/package.json"), []byte(`{"name":"rt","version":"1.0.0","scripts":{"test":"node --test"}}`), 0o644)
		os.WriteFile(rp(f, "rt/node/a.test.js"), []byte(`const test = require("node:test"); const assert = require("node:assert"); const os = require("os");
test("userInfo", () => { assert.ok(os.userInfo().username.length > 0); require("fs").writeFileSync(require("path").join(os.tmpdir(), "x"), "y"); });`), 0o644)
		res := f.sh(`cd "$R/rt/node" && `+node+` --test 2>&1 | tail -12`, runOpts{timeout: 120 * time.Second})
		if !strings.Contains(res.Out, "pass 1") && !strings.Contains(res.Out, "# pass 1") {
			t.Errorf("node --test under the sandbox:\n%s", res.Out)
		}
		if npm, err := exec.LookPath("npm"); err == nil {
			res = f.sh(`cd "$R/rt/node" && `+npm+` test --offline --no-update-notifier --no-fund --no-audit 2>&1 | tail -14`, runOpts{timeout: 120 * time.Second})
			if !strings.Contains(res.Out, "pass 1") && !strings.Contains(res.Out, "# pass 1") {
				t.Errorf("npm test under the sandbox:\n%s", res.Out)
			}
			ran++
		}
		ran++
	} else {
		t.Log("node: not installed, not run")
	}
	// python3: unittest (pwd, ssl, tempfile, sqlite3).
	if py, err := exec.LookPath("python3"); err == nil {
		mkfiles(t, f.repo, "rt/py/x")
		os.WriteFile(rp(f, "rt/py/test_a.py"), []byte(`import unittest, pwd, os, ssl, tempfile, sqlite3, urllib.request
class T(unittest.TestCase):
    def test_env(self):
        self.assertTrue(pwd.getpwuid(os.getuid()).pw_name)
        ssl.create_default_context()
        with tempfile.NamedTemporaryFile() as f:
            f.write(b"x")
        db = sqlite3.connect(":memory:"); db.execute("create table t(a)")
        urllib.request.getproxies()
if __name__ == "__main__":
    unittest.main()
`), 0o644)
		res := f.sh(`cd "$R/rt/py" && `+py+` -m unittest -v 2>&1 | tail -5`, runOpts{timeout: 120 * time.Second})
		if !strings.Contains(res.Out, "OK") {
			t.Errorf("python3 -m unittest under the sandbox:\n%s", res.Out)
		}
		ran++
	}
	for _, tool := range []string{"pytest", "mvn", "dotnet"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Logf("%s: not installed on this host, not run", tool)
		}
	}
	if ran == 0 {
		t.Skip("no runtime installed")
	}
}
