package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

func requireGit(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("git is required in CI: %v", err)
		}
		t.Skip("git not on PATH")
	}
}

// memAudit is a ports.AuditSink recording what it is given.
type memAudit struct {
	mu   sync.Mutex
	recs []model.AuditRecord
	err  error
}

func (m *memAudit) Append(recs []model.AuditRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.recs = append(m.recs, recs...)
	return nil
}

func (m *memAudit) all() []model.AuditRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.AuditRecord(nil), m.recs...)
}

// fakeSpawner records calls and optionally fails.
type fakeSpawner struct {
	mu    sync.Mutex
	calls []spawnCall
	err   error
}

type spawnCall struct {
	argv, env []string
	logPath   string
}

func (f *fakeSpawner) SpawnDetached(argv, env []string, logPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, spawnCall{argv, env, logPath})
	if f.err != nil {
		return 0, f.err
	}
	return 4242, nil
}

// fx is a fixture: sandbox home, a git repo and runner options.
type fx struct {
	t     testing.TB
	sb    *testenv.Sandbox
	vars  map[string]string
	root  string // repo work tree
	opt   Options
	audit *memAudit
	store *evidence.Store
}

func newFx(t testing.TB) *fx {
	t.Helper()
	requireGit(t)
	sb := testenv.New(t)
	vars := map[string]string{}
	for k, v := range sb.Vars {
		vars[k] = v
	}
	for _, k := range []string{"PATH", "SYSTEMROOT", "TMP", "TEMP", "ComSpec", "PATHEXT"} {
		if v, ok := os.LookupEnv(k); ok {
			vars[k] = v
		}
	}
	vars["TMPDIR"] = filepath.Join(sb.Root, "tmp")
	if err := os.MkdirAll(vars["TMPDIR"], 0o700); err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{t: t, sb: sb, vars: vars, root: filepath.Join(realRoot, "repo"), audit: &memAudit{}}
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git("init", "-q", "-b", "main")
	for _, kv := range [][2]string{{"user.email", "t@example.invalid"}, {"user.name", "t"}, {"core.autocrlf", "false"}, {"core.fsmonitor", "false"}, {"gc.auto", "0"}} {
		f.git("config", kv[0], kv[1])
	}
	f.write("src/main.txt", "hello\n")
	f.write("README.md", "# fixture\n")
	f.rebuild()
	return f
}

// rebuild (re)creates the runner options from f.vars.
func (f *fx) rebuild() {
	l, err := layout.Resolve(sys.MapEnv{Vars: f.vars})
	if err != nil {
		f.t.Fatal(err)
	}
	self, _ := os.Executable()
	clock := sys.RealClock{}
	ids := sys.RandomID{}
	f.store = evidence.NewStore(evidence.Options{Layout: l, FS: sys.OSFS{}, Clock: clock, IDs: ids})
	f.opt = Options{
		Layout: l, FS: sys.OSFS{}, Clock: clock, IDs: ids, Env: sys.MapEnv{Vars: f.vars},
		Self: self, Evidence: f.store, Audit: f.audit,
		Repo: &snapshot.Locator{FS: sys.OSFS{}}, Project: &project.Loader{FS: sys.OSFS{}},
		Spawner: Spawner{},
	}
}

func (f *fx) environ() []string { return sys.MapEnv{Vars: f.vars}.Environ() }

func (f *fx) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	cmd.Env = f.environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func (f *fx) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// specs is a compact way to write project.json.
type specJSON struct {
	Name     string   `json:"name"`
	Argv     []string `json:"argv"`
	TimeoutS int      `json:"timeout_s,omitempty"`
	Required bool     `json:"required,omitempty"`
	Env      []string `json:"env,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`

	InputsUntracked []string `json:"inputs_untracked,omitempty"`
}

// setProject writes .ballast/project.json and commits everything.
func (f *fx) setProject(specs ...specJSON) string {
	f.t.Helper()
	data, err := json.MarshalIndent(map[string]any{"version": 1, "verify": specs, "exempt": []string{"**/*.md", "docs/**"}}, "", " ")
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(".ballast/project.json", string(data)+"\n")
	f.commit()
	return string(data) + "\n"
}

func (f *fx) commit() {
	f.t.Helper()
	f.git("add", "-A")
	f.git("commit", "-q", "--allow-empty", "-m", "fixture")
	f.git("update-index", "--refresh", "-q")
}

// helper returns a spec that runs this test binary in the given mode.
func helperSpec(name, mode string, extraEnv ...string) specJSON {
	exe, _ := os.Executable()
	return specJSON{Name: name, Argv: []string{exe}, Required: true, Env: append([]string{"BALLAST_HELPER=" + mode}, extraEnv...)}
}

func (f *fx) latest(name string) *model.VerifyRecord {
	f.t.Helper()
	repo, err := f.opt.Repo.Locate(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	rec, err := f.store.Latest(repo.Inst, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return rec
}

func (f *fx) inst() string {
	f.t.Helper()
	repo, err := f.opt.Repo.Locate(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	return repo.Inst
}

func waitFor(t testing.TB, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// processGone reports whether pid no longer runs (a zombie counts as gone).
func processGone(pid int) bool {
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat"); err == nil {
			if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) {
				return b[i+2] == 'Z'
			}
		}
	}
	return !evidence.ProcessAlive(pid)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func isErr(err, target error) bool { return errors.Is(err, target) }
