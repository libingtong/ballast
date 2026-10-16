package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const (
	envChild   = "BALLAST_INSTALL_TEST_CHILD"
	envClients = "BALLAST_INSTALL_TEST_CLIENTS"
)

// TestMain doubles as the worker of the multi-process tests: when the
// marker variable is set the test binary acts as a tiny installer CLI.
func TestMain(m *testing.M) {
	if mode := os.Getenv(envChild); mode != "" {
		os.Exit(childMain(mode))
	}
	os.Exit(m.Run())
}

func childMain(mode string) int {
	env := sys.OSEnv{}
	lay, err := layout.Resolve(env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var clients []model.ClientID
	for _, c := range strings.Split(os.Getenv(envClients), ",") {
		if c != "" {
			clients = append(clients, model.ClientID(c))
		}
	}
	opt := Options{
		Layout: lay, FS: sys.OSFS{}, Env: env, Clock: sys.RealClock{}, IDs: sys.RandomID{},
		Clients: clients, Binary: fakeBinary(), MDC: []byte(testMDC), Force: true,
	}
	switch mode {
	case "install":
		_, err = Install(opt)
	case "uninstall":
		_, err = Uninstall(opt)
	default:
		err = fmt.Errorf("unknown mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "child:", err)
		return 1
	}
	return 0
}

func allClientList() string {
	var ids []string
	for _, c := range model.AllClients {
		ids = append(ids, string(c))
	}
	return strings.Join(ids, ",")
}

func runChild(e *tenv, mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	for k, v := range e.sb.Vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if sr := os.Getenv("SystemRoot"); sr != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+sr)
	}
	cmd.Env = append(cmd.Env, envChild+"="+mode, envClients+"="+allClientList())
	return cmd
}

// ballastLines counts hook commands for a client in a file.
func countOccurrences(path, needle string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), needle)
}

// assertConsistent checks the invariants that must hold after any mix of
// concurrent installs/uninstalls: either everything is installed exactly
// once or nothing is, and the manifest agrees with the files.
func assertConsistent(t *testing.T, e *tenv) (installed bool) {
	t.Helper()
	m, err := LoadManifest(e.fs, e.lay.Manifest)
	if err != nil {
		t.Fatalf("manifest unreadable after concurrent runs: %v", err)
	}
	total := 0
	for _, f := range []string{e.claudeSettings(), e.codexHooks(), e.cursorHooks(), e.hermesConfig()} {
		total += countOccurrences(f, "--client")
	}
	if manifestEmpty(m) {
		if total != 0 || e.exists(e.cursorMDC()) {
			t.Errorf("manifest says uninstalled but %d hook commands remain (mdc exists: %v)", total, e.exists(e.cursorMDC()))
		}
	} else {
		if len(m.Entries) != 10+6+10+6 {
			t.Errorf("manifest has %d entries, want 32", len(m.Entries))
		}
		// claude commands are command+args: count by event arg instead
		for _, c := range model.AllClients {
			for _, ne := range defaultEvents(c) {
				var n int
				switch c {
				case model.ClientClaude:
					n = strings.Count(e.read(e.claudeSettings()), fmt.Sprintf("--event\",\n              \"%s\"", ne.Native))
				case model.ClientCodex:
					n = countOccurrences(e.codexHooks(), "--client codex --event "+ne.Native+"\"")
				case model.ClientCursor:
					n = countOccurrences(e.cursorHooks(), "--client cursor --event "+ne.Native+"\"")
				case model.ClientHermes:
					n = countOccurrences(e.hermesConfig(), "--client hermes --event "+ne.Native+"\"")
				}
				if n != 1 {
					t.Errorf("%s %s appears %d times, want exactly 1", c, ne.Native, n)
				}
			}
		}
		if len(m.Backups) > MaxBackups {
			t.Errorf("%d backups recorded", len(m.Backups))
		}
	}
	// no leftovers
	_ = filepath.Walk(e.sb.Home, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && (strings.Contains(fi.Name(), ".tmp.") || fi.Name() == "install.lock") {
			t.Errorf("leftover file %s", p)
		}
		return nil
	})
	if ents, _ := os.ReadDir(e.lay.BackupsDir); len(ents) > MaxBackups {
		t.Errorf("%d backup dirs on disk", len(ents))
	}
	return !manifestEmpty(m)
}

func TestConcurrentInstallGoroutines(t *testing.T) {
	e := newTenv(t)
	for _, files := range userFiles(e) {
		for p, c := range files {
			e.write(p, c)
		}
	}
	e.mkClientDirs()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			opt := e.opts(model.AllClients...)
			opt.IDs = sys.RandomID{}
			if _, err := Install(opt); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent install failed: %v", err)
	}
	if !assertConsistent(t, e) {
		t.Fatal("expected an installed state")
	}
	// user hooks survived the race
	for _, p := range []string{e.claudeSettings(), e.codexHooks(), e.cursorHooks(), e.hermesConfig()} {
		if !strings.Contains(e.read(p), "/usr/local/bin/audit") {
			t.Errorf("%s lost the user's hook", p)
		}
	}
}

func TestConcurrentProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	e := newTenv(t)
	for _, files := range userFiles(e) {
		for p, c := range files {
			e.write(p, c)
		}
	}

	t.Run("installs", func(t *testing.T) {
		runAll(t, e, []string{"install", "install", "install", "install", "install", "install"})
		if !assertConsistent(t, e) {
			t.Fatal("expected an installed state")
		}
	})
	t.Run("mixed", func(t *testing.T) {
		runAll(t, e, []string{"uninstall", "install", "uninstall", "install", "install", "uninstall", "install", "uninstall"})
		assertConsistent(t, e)
	})
	t.Run("uninstalls", func(t *testing.T) {
		runAll(t, e, []string{"uninstall", "uninstall", "uninstall", "uninstall"})
		if assertConsistent(t, e) {
			t.Fatal("expected everything to be uninstalled")
		}
		for p, orig := range userFiles(e)[model.ClientClaude] {
			if e.read(p) != orig {
				t.Errorf("%s not restored", p)
			}
		}
		for p, orig := range userFiles(e)[model.ClientHermes] {
			if e.read(p) != orig {
				t.Errorf("%s not restored", p)
			}
		}
	})
}

func runAll(t *testing.T, e *tenv, modes []string) {
	t.Helper()
	cmds := make([]*exec.Cmd, len(modes))
	outs := make([]*strings.Builder, len(modes))
	for i, m := range modes {
		cmds[i] = runChild(e, m)
		outs[i] = &strings.Builder{}
		cmds[i].Stderr = outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Errorf("child %d (%s) failed: %v\n%s", i, modes[i], err, outs[i])
		}
	}
}

func TestChildModeNeedsNoRealHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	e := newTenv(t)
	cmd := runChild(e, "install")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !e.exists(e.claudeSettings()) || !strings.HasPrefix(e.claudeSettings(), e.sb.Root) {
		t.Fatal("child did not honour the redirected environment")
	}
}

// slowFS widens race windows: every read yields the CPU for a moment.
type slowFS struct{ sys.FS }

func (s slowFS) ReadFile(n string) ([]byte, error) {
	b, err := s.FS.ReadFile(n)
	time.Sleep(300 * time.Microsecond)
	return b, err
}

func TestConcurrentMixedGoroutines(t *testing.T) {
	e := newTenv(t)
	for _, files := range userFiles(e) {
		for p, c := range files {
			e.write(p, c)
		}
	}
	e.mkClientDirs()
	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				opt := e.opts(model.AllClients...)
				opt.IDs = sys.RandomID{}
				opt.FS = slowFS{e.fs}
				var err error
				if i%2 == 0 {
					_, err = Install(opt)
				} else {
					opt.Clients = nil
					_, err = Uninstall(opt)
				}
				if err != nil {
					t.Errorf("goroutine %d: %v", i, err)
				}
			}(i)
		}
		wg.Wait()
		assertConsistent(t, e)
	}
}
