package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

type gitCall struct {
	dir     string
	env     []string
	args    []string
	indexAt []byte // contents of GIT_INDEX_FILE when the call was made
}

func (c gitCall) envVal(name string) (string, int) {
	var val string
	n := 0
	for _, kv := range c.env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			val, n = v, n+1
		}
	}
	return val, n
}

// fakeGit is a GitRunner that records calls and answers from handler.
type fakeGit struct {
	mu      sync.Mutex
	calls   []gitCall
	handler func(ctx context.Context, c gitCall) ([]byte, error)
}

func (g *fakeGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	c := gitCall{dir: dir, env: append([]string(nil), env...), args: append([]string(nil), args...)}
	if v, n := c.envVal("GIT_INDEX_FILE"); n == 1 {
		c.indexAt, _ = os.ReadFile(v)
	}
	g.mu.Lock()
	g.calls = append(g.calls, c)
	g.mu.Unlock()
	if g.handler != nil {
		return g.handler(ctx, c)
	}
	if len(args) > 0 && args[len(args)-1] == "write-tree" {
		return []byte(strings.Repeat("ab", 20) + "\n"), nil
	}
	return nil, nil
}

func (g *fakeGit) subcommands() []string {
	var out []string
	for _, c := range g.calls {
		out = append(out, strings.Join(c.args, " "))
	}
	return out
}

// polluted is an environment as seen inside git's own pre-commit hook.
func pollutedEnv(base sys.MapEnv, tmp string) sys.MapEnv {
	vars := map[string]string{}
	for k, v := range base.Vars {
		vars[k] = v
	}
	for _, k := range inheritedGitVars {
		vars[k] = filepath.Join("/definitely/not/here", k)
	}
	vars["TMPDIR"] = tmp
	vars["GIT_CONFIG_PARAMETERS"] = "'user.name=keep'"
	return sys.MapEnv{Vars: vars, OS: base.OS}
}

func TestFallbackEnvironmentAndSteps(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.write("src/a.go", "package changed\n")
	tmp := filepath.Join(f.sb.Root, "snaptmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	realIndex, err := os.ReadFile(f.repo.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{}
	opt := f.opts(true)
	opt.Env = pollutedEnv(f.env, tmp)
	opt.Git = fg
	filt := mustFilter(t, "**/*.md", "docs/**")

	id, err := fallbackTree(opt, f.repo, true, []string{"src", "bin/run.sh"}, filt)
	if err != nil {
		t.Fatal(err)
	}
	if id != model.TreeID(strings.Repeat("ab", 20)) {
		t.Errorf("tree id = %s", id)
	}
	wantArgs := []string{
		"-c core.fsmonitor=false add -u -- src bin/run.sh",
		"-c core.fsmonitor=false rm --cached -r -q -f --ignore-unmatch -- :(glob)**/*.md :(glob)docs/**",
		"-c core.fsmonitor=false write-tree",
	}
	if got := fg.subcommands(); strings.Join(got, "\n") != strings.Join(wantArgs, "\n") {
		t.Errorf("git calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantArgs, "\n"))
	}
	var idxPath string
	for i, c := range fg.calls {
		if c.dir != f.repo.Toplevel {
			t.Errorf("call %d dir = %s", i, c.dir)
		}
		for _, k := range inheritedGitVars {
			want := map[string]string{"GIT_DIR": f.repo.GitDir, "GIT_WORK_TREE": f.repo.Toplevel}[k]
			v, n := c.envVal(k)
			switch k {
			case "GIT_DIR", "GIT_WORK_TREE":
				if n != 1 || v != want {
					t.Errorf("call %d: %s = %q (%d times), want %q once", i, k, v, n, want)
				}
			case "GIT_INDEX_FILE":
				if n != 1 {
					t.Errorf("call %d: GIT_INDEX_FILE set %d times", i, n)
				}
				idxPath = v
			default:
				if n != 0 {
					t.Errorf("call %d: inherited %s leaked through (%q)", i, k, v)
				}
			}
		}
		if v, n := c.envVal("GIT_OPTIONAL_LOCKS"); n != 1 || v != "0" {
			t.Errorf("call %d: GIT_OPTIONAL_LOCKS = %q", i, v)
		}
		if v, n := c.envVal("GIT_CONFIG_PARAMETERS"); n != 1 || v != "'user.name=keep'" {
			t.Errorf("call %d: unrelated variables must be kept, got %q", i, v)
		}
		if !bytes.Equal(c.indexAt, realIndex) {
			t.Errorf("call %d: GIT_INDEX_FILE is not a copy of the real index", i)
		}
	}
	// The temporary index lives in TMPDIR, never under the git directory,
	// and is gone afterwards.
	if !strings.HasPrefix(idxPath, tmp+string(filepath.Separator)) {
		t.Errorf("temporary index %s is not under %s", idxPath, tmp)
	}
	if strings.HasPrefix(idxPath, f.repo.GitDir) || strings.HasPrefix(idxPath, f.repo.CommonDir) {
		t.Errorf("temporary index %s is inside the git directory", idxPath)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if after, _ := os.ReadFile(f.repo.IndexPath); !bytes.Equal(after, realIndex) {
		t.Error("the real index was modified")
	}
}

func TestFallbackStepSelection(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	for _, tc := range []struct {
		name    string
		refresh bool
		paths   []string
		filter  *Filter
		want    []string
	}{
		{"plain no exempt", false, nil, nil, []string{"write-tree"}},
		{"plain exempt", false, nil, mustFilter(t, "docs/**"), []string{"rm --cached -r -q -f --ignore-unmatch -- :(glob)docs/**", "write-tree"}},
		{"all", true, nil, nil, []string{"add -u", "write-tree"}},
		{"paths", true, []string{"a", "b"}, nil, []string{"add -u -- a b", "write-tree"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fg := &fakeGit{}
			opt := f.opts(true)
			opt.Git = fg
			if _, err := fallbackTree(opt, f.repo, tc.refresh, tc.paths, tc.filter); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range fg.subcommands() {
				got = append(got, strings.TrimPrefix(s, "-c core.fsmonitor=false "))
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("calls = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFallbackErrors(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	t.Run("git failure is returned and cleaned up", func(t *testing.T) {
		tmp := t.TempDir()
		opt := f.opts(true)
		opt.Env = pollutedEnv(f.env, tmp)
		boom := errors.New("boom")
		opt.Git = &fakeGit{handler: func(context.Context, gitCall) ([]byte, error) { return nil, boom }}
		_, err := fallbackTree(opt, f.repo, true, nil, nil)
		if !errors.Is(err, boom) || errors.Is(err, model.ErrDeadline) {
			t.Errorf("err = %v", err)
		}
		if left, _ := os.ReadDir(tmp); len(left) != 0 {
			t.Errorf("left behind: %v", left)
		}
	})
	t.Run("garbage from write-tree", func(t *testing.T) {
		opt := f.opts(true)
		opt.Git = &fakeGit{handler: func(context.Context, gitCall) ([]byte, error) { return []byte("not a tree\n"), nil }}
		if _, err := fallbackTree(opt, f.repo, false, nil, nil); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("missing index means empty tree for git", func(t *testing.T) {
		fg := &fakeGit{}
		opt := f.opts(true)
		opt.Git = fg
		repo := *f.repo
		repo.IndexPath = filepath.Join(t.TempDir(), "nonexistent")
		if _, err := fallbackTree(opt, &repo, false, nil, nil); err != nil {
			t.Fatal(err)
		}
		if v, _ := fg.calls[0].envVal("GIT_INDEX_FILE"); v == "" {
			t.Error("GIT_INDEX_FILE must still be set")
		}
		if len(fg.calls[0].indexAt) != 0 {
			t.Error("no index copy expected")
		}
	})
}

func TestFallbackDeadline(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	blocking := func(ctx context.Context, c gitCall) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Run("expires while git runs", func(t *testing.T) {
		tmp := t.TempDir()
		opt := f.opts(true)
		opt.Env = pollutedEnv(f.env, tmp)
		opt.Git = &fakeGit{handler: blocking}
		opt.Deadline = time.Now().Add(80 * time.Millisecond)
		start := time.Now()
		_, err := fallbackTree(opt, f.repo, true, nil, nil)
		if !errors.Is(err, model.ErrDeadline) {
			t.Fatalf("err = %v, want ErrDeadline", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("returned after %v", d)
		}
		if left, _ := os.ReadDir(tmp); len(left) != 0 {
			t.Errorf("temporary files left behind: %v", left)
		}
	})
	t.Run("already expired never starts git", func(t *testing.T) {
		fg := &fakeGit{}
		opt := f.opts(true)
		opt.Git = fg
		opt.Deadline = time.Now().Add(-time.Second)
		if _, err := fallbackTree(opt, f.repo, true, nil, nil); !errors.Is(err, model.ErrDeadline) {
			t.Fatalf("err = %v", err)
		}
		if len(fg.calls) != 0 {
			t.Errorf("git was started %d times", len(fg.calls))
		}
	})
	t.Run("fake clock decides", func(t *testing.T) {
		clock := sys.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		fg := &fakeGit{}
		opt := f.opts(true)
		opt.Clock = clock
		opt.Git = fg
		opt.Deadline = clock.Now().Add(time.Hour)
		if _, err := fallbackTree(opt, f.repo, false, nil, nil); err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Hour)
		if _, err := fallbackTree(opt, f.repo, false, nil, nil); !errors.Is(err, model.ErrDeadline) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("through TreeS and TreeC", func(t *testing.T) {
		f2 := newFixture(t)
		baseRepo(f2)
		f2.git("update-index", "--split-index") // forces the fallback
		f2.relocate()
		opt := f2.opts(true)
		opt.Git = &fakeGit{handler: blocking}
		opt.Deadline = time.Now().Add(60 * time.Millisecond)
		if _, err := TreeS(opt, f2.repo, nil); !errors.Is(err, model.ErrDeadline) {
			t.Errorf("TreeS: %v", err)
		}
		opt.Deadline = time.Now().Add(60 * time.Millisecond)
		if _, err := TreeC(opt, f2.repo, model.CommitForm{Kind: model.CommitPlain}, nil); !errors.Is(err, model.ErrDeadline) {
			t.Errorf("TreeC: %v", err)
		}
	})
}

func TestFallbackNotAllowed(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.git("update-index", "--split-index")
	f.relocate()
	fg := &fakeGit{}
	opt := f.opts(false)
	opt.Git = fg
	if _, err := TreeS(opt, f.repo, nil); !errors.Is(err, model.ErrFallback) {
		t.Errorf("TreeS: %v", err)
	}
	if _, err := TreeC(opt, f.repo, model.CommitForm{Kind: model.CommitAll}, nil); !errors.Is(err, model.ErrFallback) {
		t.Errorf("TreeC: %v", err)
	}
	if len(fg.calls) != 0 {
		t.Errorf("git ran although fallback is not allowed: %v", fg.subcommands())
	}
}

// TestNativeErrorsSkipFallback: errors that are not ErrFallback must not
// start git.
func TestNativeErrorsSkipFallback(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	fg := &fakeGit{}
	opt := f.opts(true)
	opt.Git = fg
	opt.Deadline = time.Now().Add(-time.Second)
	if _, err := TreeS(opt, f.repo, nil); !errors.Is(err, model.ErrDeadline) {
		t.Errorf("TreeS: %v", err)
	}
	if _, err := TreeC(opt, f.repo, model.CommitForm{Kind: model.CommitUnparseable, Reason: "dynamic"}, nil); err == nil || errors.Is(err, model.ErrFallback) {
		t.Errorf("unparseable: %v", err)
	}
	opt.Deadline = time.Time{}
	if _, err := TreeC(opt, f.repo, model.CommitForm{Kind: model.CommitPaths}, nil); err == nil || errors.Is(err, model.ErrFallback) {
		t.Errorf("paths without paths: %v", err)
	}
	if len(fg.calls) != 0 {
		t.Errorf("git ran: %v", fg.subcommands())
	}
}

// TestExecGitKillsProcessGroup runs the real ExecGit against a fake `git`
// script that leaves a background child behind; when the context ends both
// must be dead.
func TestExecGitKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix-only")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := "#!/bin/sh\nsleep 60 &\necho $! > \"" + pidFile + "\"\nwait\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ExecGit{}.Run(ctx, dir, []string{"PATH=" + os.Getenv("PATH")}, "add", "-u")
		done <- err
	}()
	// Cancel only once the script has started its background child.
	var data []byte
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		var err error
		if data, err = os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(data), "\n") {
			break
		}
		if time.Since(start) > 20*time.Second {
			t.Fatal("script did not start")
		}
	}
	cancelled := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error")
		}
		if d := time.Since(cancelled); d > 5*time.Second {
			t.Errorf("Run blocked for %v after cancellation", d)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil { // ESRCH: gone
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestExecGitRunsGit(t *testing.T) {
	requireGit(t)
	out, err := ExecGit{}.Run(context.Background(), t.TempDir(), os.Environ(), "--version")
	if err != nil || !strings.HasPrefix(string(out), "git version") {
		t.Errorf("out=%q err=%v", out, err)
	}
	_, err = ExecGit{}.Run(context.Background(), t.TempDir(), os.Environ(), "no-such-subcommand")
	if err == nil || !strings.Contains(err.Error(), "no-such-subcommand") {
		t.Errorf("err = %v", err)
	}
}

func TestGitEnvWindowsCaseInsensitive(t *testing.T) {
	repo := &model.RepoInfo{Toplevel: `C:\r`, GitDir: `C:\r\.git`}
	env := gitEnv([]string{"Git_Dir=x", "git_index_file=y", "PATH=p", "GIT_PREFIX=z"}, repo, `C:\tmp\index`, true)
	var keys []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		keys = append(keys, k)
	}
	if strings.Join(keys, ",") != "PATH,GIT_DIR,GIT_WORK_TREE,GIT_INDEX_FILE,GIT_OPTIONAL_LOCKS" {
		t.Errorf("keys = %v", keys)
	}
}

// TestFallbackInLinkedWorktree runs the real git fallback inside a linked
// worktree (GIT_DIR then lives under .git/worktrees).
func TestFallbackInLinkedWorktree(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	wt := filepath.Join(f.sb.Root, "wt")
	f.git("worktree", "add", "-q", "-b", "feature", wt)
	g := &fixture{t: t, sb: f.sb, env: f.env, dir: realpath(t, wt)}
	g.relocate()
	g.write("src/a.go", "package changed in worktree\n")
	g.write("src/new.go", "package untracked\n")
	g.git("add", "-N", "src/new.go") // intent-to-add forces the fallback
	filt := mustFilter(t, "**/*.md")
	want := g.reference(true, nil, []string{"**/*.md"})
	if _, err := TreeS(g.opts(false), g.repo, filt); !errors.Is(err, model.ErrFallback) {
		t.Fatalf("native: %v", err)
	}
	got, err := TreeS(g.opts(true), g.repo, filt)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("%s", describe(got, want))
	}
}
