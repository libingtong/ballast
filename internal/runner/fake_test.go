package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sandbox"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
)

// fakeTier is a ports.TierProbe returning a fixed TierInfo.
type fakeTier struct{ info model.TierInfo }

func (f fakeTier) Probe() model.TierInfo { return f.info }

func t1Info() model.TierInfo {
	return model.TierInfo{Tier: model.TierT1, Declared: model.TierT1, SandboxPolicy: model.SandboxRequire, Network: model.NetworkDeny}
}

// fakeBackend is a sandboxBackend that does not sandbox: Wrap returns the
// command unchanged (marked as a new session), everything is recorded.
type fakeBackend struct {
	mu          sync.Mutex
	kind        sandbox.Kind
	probeErr    error
	wrapErr     error
	postFound   []string
	postErr     error
	resolveErr  error
	calls       []string
	specs       []sandbox.Spec
	argvs       [][]string
	onWrap      func(spec sandbox.Spec)
	quarantined []quarantineCall
}

type quarantineCall struct {
	paths, roots []string
	dir          string
}

func newFakeBackend() *fakeBackend { return &fakeBackend{kind: sandbox.KindBwrap} }

func (b *fakeBackend) rec(s string) {
	b.mu.Lock()
	b.calls = append(b.calls, s)
	b.mu.Unlock()
}

func (b *fakeBackend) called(s string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if c == s {
			n++
		}
	}
	return n
}

func (b *fakeBackend) Probe(sandbox.Options) (sandbox.Kind, error) {
	b.rec("probe")
	return b.kind, b.probeErr
}

func (b *fakeBackend) Wrap(opt sandbox.Options, kind sandbox.Kind, spec sandbox.Spec, argv []string) (sandbox.Wrapped, error) {
	b.rec("wrap")
	b.mu.Lock()
	b.specs = append(b.specs, spec)
	b.argvs = append(b.argvs, append([]string(nil), argv...))
	b.mu.Unlock()
	if b.wrapErr != nil {
		return sandbox.Wrapped{}, b.wrapErr
	}
	if b.onWrap != nil {
		b.onWrap(spec)
	}
	return sandbox.Wrapped{Kind: kind, Argv: append([]string(nil), argv...), NewSession: true, Env: []string{"TMPDIR=" + spec.TmpDir}}, nil
}

func (b *fakeBackend) PostCheck(fsys sys.FS, spec sandbox.Spec, w sandbox.Wrapped) ([]string, error) {
	b.rec("postcheck")
	return b.postFound, b.postErr
}

func (b *fakeBackend) Quarantine(fsys sys.FS, paths, roots []string, dir string) []sandbox.QuarantineEntry {
	b.rec("quarantine")
	b.mu.Lock()
	b.quarantined = append(b.quarantined, quarantineCall{paths, roots, dir})
	b.mu.Unlock()
	out := make([]sandbox.QuarantineEntry, len(paths))
	for i, p := range paths {
		out[i] = sandbox.QuarantineEntry{Path: p, MovedTo: filepath.Join(dir, filepath.Base(p))}
	}
	return out
}

func (b *fakeBackend) ResolveGitDirs(fsys sys.FS, wt string) (string, string, error) {
	b.rec("resolve")
	if b.resolveErr != nil {
		return "", "", b.resolveErr
	}
	return sandbox.ResolveGitDirs(fsys, wt)
}

// recGit wraps snapshot.ExecGit, records every call and can run a hook
// before a call (to play the agent).
type recGit struct {
	mu     sync.Mutex
	calls  []gitCall
	before func(args []string)
	fail   func(args []string) error
}

type gitCall struct {
	dir  string
	env  []string
	args []string
}

func (g *recGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	g.mu.Lock()
	g.calls = append(g.calls, gitCall{dir, append([]string(nil), env...), append([]string(nil), args...)})
	before, fail := g.before, g.fail
	g.mu.Unlock()
	if before != nil {
		before(args)
	}
	if fail != nil {
		if err := fail(args); err != nil {
			return nil, err
		}
	}
	return snapshot.ExecGit{}.Run(ctx, dir, env, args...)
}

// verbs returns the git subcommands in call order (the first non-flag
// argument that follows the -c pairs).
func (g *recGit) verbs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, c := range g.calls {
		out = append(out, gitVerb(c.args))
	}
	return out
}

func gitVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func (g *recGit) has(verb string) bool {
	for _, v := range g.verbs() {
		if v == verb {
			return true
		}
	}
	return false
}

// hookFx adds the P2 plumbing to a fixture: fake T1, fake backend, recording
// git runner and trace.
type hookFx struct {
	*fx
	be    *fakeBackend
	rg    *recGit
	spawn *fakeSpawner
	steps []string
	mu    sync.Mutex
}

func newHookFx(t testing.TB) *hookFx {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("origin=hook jobs need a sandbox backend, which does not exist on native Windows")
	}
	h := &hookFx{fx: newFx(t), be: newFakeBackend(), rg: &recGit{}, spawn: &fakeSpawner{}}
	h.wire()
	return h
}

// wire (re)attaches the seams to f.opt (call after fx.rebuild()).
func (h *hookFx) wire() {
	h.opt.Tier = fakeTier{t1Info()}
	h.opt.backend = h.be
	h.opt.gitRun = h.rg
	h.opt.Spawner = h.spawn
	h.opt.trace = func(s string) {
		h.mu.Lock()
		h.steps = append(h.steps, s)
		h.mu.Unlock()
	}
}

func (h *hookFx) traced() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.steps...)
}

var reqCounter int

// startHook runs StartRequest for names and returns the started jobs.
func (h *hookFx) startHook(names ...string) ([]Started, model.HookStartRequest, error) {
	h.t.Helper()
	reqCounter++
	req := model.HookStartRequest{
		RequestID: h.opt.newJobID(), Cwd: h.root, Names: names, Client: model.ClientClaude,
		SessionID: "sess-1", AgentID: "agent-1",
	}
	started, err := StartRequest(h.opt, req)
	return started, req, err
}

// runHook starts a request for one name and runs its job in-process.
func (h *hookFx) runHook(name string) (*model.VerifyRecord, error) {
	h.t.Helper()
	started, _, err := h.startHook(name)
	if err != nil {
		return nil, err
	}
	return RunJob(h.opt, started[0].JobID)
}

// realGit is a plain git helper for tests that inspect repositories.
func gitOut(t testing.TB, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func readFileT(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fx) gitStdin(stdin string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	cmd.Env = f.environ()
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func jsonIndent(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", " ")
	return string(b), err
}

func after10s() <-chan time.Time { return time.After(10 * time.Second) }
