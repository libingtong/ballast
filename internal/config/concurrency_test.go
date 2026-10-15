package config_test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const childEnv = "BALLAST_U4_CHILD"

// TestMain doubles as the child process of the multi-process tests: with
// BALLAST_U4_CHILD set the test binary serves Load requests on stdin/stdout
// instead of running tests (the Go equivalent of scripts/hotreload.sh).
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(childMain())
	}
	os.Exit(m.Run())
}

// childMain reads one command per line: "load" prints
// "ok <generation> <rule ids>" (or "err <msg>"); "quit" exits.
func childMain() int {
	lay, err := layout.Resolve(sys.OSEnv{})
	if err != nil {
		fmt.Println("err", err)
		return 2
	}
	repo := os.Getenv("BALLAST_U4_REPO")
	l := config.NewLoader(config.Options{
		Layout: lay, FS: sys.OSFS{}, Compiler: &fakeCompiler{}, Version: "child",
		Stderr: io.Discard,
	})
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		switch sc.Text() {
		case "load":
			rs, err := l.Load(model.LoadRequest{Cwd: repo, Repo: &model.RepoInfo{Toplevel: repo}})
			if err != nil {
				fmt.Println("err", strings.ReplaceAll(err.Error(), "\n", " "))
				continue
			}
			fmt.Println("ok", rs.Generation, strings.Join(ids(rs), ","))
		case "quit":
			return 0
		}
	}
	return 0
}

type child struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Scanner
}

func startChild(t *testing.T, e *env) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=1", "BALLAST_U4_REPO="+e.repo)
	for k, v := range e.sb.Vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, in: in, out: bufio.NewScanner(out)}
	t.Cleanup(func() { c.stop() })
	return c
}

// ask requests one Load and returns (generation, ids). It is safe to call
// from a goroutine other than the test's.
func (c *child) ask() (string, string, error) {
	if _, err := fmt.Fprintln(c.in, "load"); err != nil {
		return "", "", fmt.Errorf("child stdin: %w", err)
	}
	if !c.out.Scan() {
		return "", "", fmt.Errorf("child died: %v", c.out.Err())
	}
	f := strings.SplitN(c.out.Text(), " ", 3)
	if f[0] != "ok" {
		return "", "", fmt.Errorf("child load failed: %s", c.out.Text())
	}
	ids := ""
	if len(f) == 3 {
		ids = f[2]
	}
	return f[1], ids, nil
}

func (c *child) load(t testing.TB) (string, string) {
	t.Helper()
	g, ids, err := c.ask()
	if err != nil {
		t.Fatal(err)
	}
	return g, ids
}

func (c *child) stop() {
	if c.cmd.Process == nil {
		return
	}
	fmt.Fprintln(c.in, "quit")
	c.in.Close()
	c.cmd.Wait()
	c.cmd.Process = nil
}

// replace writes content next to the target and renames it into place, the
// way an editor or installer does.
func replace(t testing.TB, path, content string) {
	t.Helper()
	if err := tryReplace(path, content); err != nil {
		t.Fatal(err)
	}
}

func tryReplace(path, content string) error {
	tmp := path + ".swap"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Hot reload across a long-lived process: editing any layer changes the
// next Load's generation without restarting anything.
func TestHotReloadLiveProcess(t *testing.T) {
	e := newEnv(t)
	c := startChild(t, e)
	g0, ids0 := c.load(t)
	if ids0 != "" {
		t.Fatalf("ids = %q", ids0)
	}
	seen := map[string]bool{g0: true}
	for _, layer := range []model.Layer{model.LayerOrg, model.LayerRepo, model.LayerUser} {
		for i := 1; i <= 3; i++ {
			rules := []spec{on(string(layer) + "-first")}
			for j := 1; j < i; j++ {
				rules = append(rules, on(fmt.Sprintf("%s-more-%d", layer, j)))
			}
			replace(t, e.path(layer), rulesJSON("", rules...))
			g, ids := c.load(t)
			if seen[g] {
				t.Fatalf("layer %s step %d: generation %s did not change", layer, i, g)
			}
			seen[g] = true
			if !strings.Contains(ids, string(layer)+"-first") {
				t.Errorf("layer %s step %d: ids = %q", layer, i, ids)
			}
			if g2, _ := c.load(t); g2 != g {
				t.Errorf("unchanged file changed generation")
			}
		}
	}
	// Settings-only edits change the generation too.
	replace(t, e.path(model.LayerUser), rulesJSON(`{"inject":{"anchor_every":5}}`, on("user-first")))
	if g, _ := c.load(t); seen[g] {
		t.Errorf("settings edit did not change the generation")
	}
}

// Several processes load while the files are replaced underneath them: no
// Load may fail, and every generation seen must be one that was published.
func TestConcurrentProcessesWithRewrites(t *testing.T) {
	e := newEnv(t)
	versions := []string{
		rulesJSON("", on("aaa-rule")),
		rulesJSON("", on("aaa-rule"), on("bbb-rule")),
		rulesJSON(`{"inject":{"batch_after_tools":7}}`, on("ccc-rule")),
	}
	valid := map[string]bool{config.Generation(nil, model.Settings{}): true}
	for _, v := range versions {
		f, _, err := (&fakeCompiler{}).ParseFile(model.LayerRepo, "x", []byte(v))
		if err != nil {
			t.Fatal(err)
		}
		var rules []model.Rule
		for _, r := range f.Rules {
			rules = append(rules, r)
		}
		var s model.Settings
		if f.Settings != nil {
			s = *f.Settings
		}
		valid[config.Generation(rules, s)] = true
	}
	replace(t, e.path(model.LayerRepo), versions[0])

	procs, loads := 6, 150
	if raceEnabled {
		procs, loads = 4, 40
	}
	var wg, writer sync.WaitGroup
	errs := make(chan string, procs+1)
	stop := make(chan struct{})
	for i := 0; i < procs; i++ {
		c := startChild(t, e)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < loads; n++ {
				g, _, err := c.ask()
				if err != nil {
					errs <- err.Error()
					return
				}
				if !valid[g] {
					errs <- "unexpected generation " + g
					return
				}
			}
		}()
	}
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := tryReplace(e.path(model.LayerRepo), versions[i%len(versions)]); err != nil {
				errs <- err.Error()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()
	close(stop)
	writer.Wait()
	select {
	case msg := <-errs:
		t.Fatal(msg)
	default:
	}
	// Quiesce on the last version: every process converges on it.
	time.Sleep(20 * time.Millisecond)
	replace(t, e.path(model.LayerRepo), versions[2])
	want, _ := startChild(t, e).load(t)
	if want == "" || !valid[want] {
		t.Fatalf("final generation %q", want)
	}
}

// In-process: many goroutines (own Loaders and a shared one) load while a
// writer flips between a valid file and garbage. Run with -race.
func TestConcurrentLoadsInProcess(t *testing.T) {
	e := newEnv(t)
	good := rulesJSON("", on("aaa-rule"), on("bbb-rule"))
	replace(t, e.path(model.LayerUser), good)
	shared := e.loader()
	e.load() // seed LKG and cache
	stop := make(chan struct{})
	var wg, writer sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			l := shared
			if g%2 == 1 {
				l = e.loader()
			}
			for i := 0; i < 100; i++ {
				rs, err := l.Load(e.req())
				if err != nil {
					t.Errorf("Load: %v", err)
					return
				}
				// Bad writes always fall back to the LKG, so both rules stay.
				if len(rs.Rules) != 2 {
					t.Errorf("rules = %v notices = %v", ids(rs), noticeKinds(rs))
					return
				}
			}
		}(g)
	}
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			content := good
			if i%2 == 1 {
				content = `{"version":1,"rules":[{"id":"BAD ID","event":"PreToolUse"}]}`
			}
			if err := tryReplace(e.path(model.LayerUser), content); err != nil {
				t.Errorf("replace: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	writer.Wait()
}
