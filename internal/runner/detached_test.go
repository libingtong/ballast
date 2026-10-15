package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []int
	for _, s := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// Start returns at once; the detached child (this test binary acting as
// `ballast verify --run-job`) runs the job and records the result.
func TestStartDetachedEndToEnd(t *testing.T) {
	f := newFx(t)
	gate := filepath.Join(f.sb.Root, "go")
	f.setProject(helperSpec("test", "waitfile", "BALLAST_HELPER_FILE="+gate))

	begin := time.Now()
	st, err := Start(f.opt, f.root, nil)
	if err != nil || len(st) != 1 {
		t.Fatalf("Start = %+v, %v", st, err)
	}
	if time.Since(begin) > 5*time.Second {
		t.Errorf("Start took %v; it must return immediately", time.Since(begin))
	}
	id := st[0].JobID

	// The child registers its pid, and the job counts as running.
	waitFor(t, "the child to register its pid", 20*time.Second, func() bool {
		js, _ := f.store.ReadJob(f.inst(), "test", id)
		return js != nil && js.PID > 0 && js.PID != os.Getpid()
	})
	if running, _ := f.store.RunningJobs(f.inst(), "test"); len(running) != 1 || running[0].JobID != id {
		t.Errorf("running = %+v", running)
	}
	if f.latest("test") != nil {
		t.Error("a record exists while the job still runs")
	}

	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the record", 20*time.Second, func() bool { return f.latest("test") != nil })
	rec := f.latest("test")
	if rec.JobID != id || rec.Outcome != model.OutcomePass || !rec.ValidPass() {
		t.Fatalf("record = %+v", rec)
	}
	waitFor(t, "job state done", 10*time.Second, func() bool {
		js, _ := f.store.ReadJob(f.inst(), "test", id)
		return js != nil && js.State == model.JobDone
	})
	if running, _ := f.store.RunningJobs(f.inst(), "test"); len(running) != 0 {
		t.Errorf("still running: %+v", running)
	}
	log, _ := os.ReadFile(f.store.LogPath(f.inst(), "test", id))
	if !strings.Contains(string(log), "# ballast verify test job="+id) {
		t.Errorf("log = %q", log)
	}
}

// A second verify of the same name stops the first one and records it as
// superseded; only the new job can pass.
func TestSupersedeStopsRunningJob(t *testing.T) {
	f := newFx(t)
	gate := filepath.Join(f.sb.Root, "go")
	pidfile := filepath.Join(f.sb.Root, "pids")
	f.setProject(helperSpec("test", "waitfile", "BALLAST_HELPER_FILE="+gate, "BALLAST_HELPER_PIDFILE="+pidfile))

	a, err := Start(f.opt, f.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "A's command to start", 20*time.Second, func() bool {
		js, _ := f.store.ReadJob(f.inst(), "test", a[0].JobID)
		return js != nil && js.PID > 0 && len(readPIDs(t, pidfile)) == 1
	})
	aCmd := readPIDs(t, pidfile)[0]
	jsA, _ := f.store.ReadJob(f.inst(), "test", a[0].JobID)
	// The live detached runner must hold its job lock, so supersede signals
	// it (not only the once-a-second marker poll; review u8b minor).
	if jsA == nil || !f.opt.withDefaults().runnerHoldsLock(jsA) {
		t.Fatal("live runner does not hold its job lock")
	}

	b, err := Start(f.opt, f.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].JobID == a[0].JobID {
		t.Fatal("same job id")
	}

	// A is marked at once.
	js, recA, err := f.store.FindJob(a[0].JobID)
	if err != nil || js == nil || js.State != model.JobDone || js.Outcome != model.OutcomeSuperseded {
		t.Fatalf("A job = %+v, %v", js, err)
	}
	if recA == nil || recA.Outcome != model.OutcomeSuperseded {
		t.Fatalf("A record = %+v", recA)
	}
	// ... its command dies, and the runner exits without writing a result.
	if runtime.GOOS != "windows" {
		waitFor(t, "A's command to be killed", 15*time.Second, func() bool { return processGone(aCmd) })
	}
	waitFor(t, "A's runner to finish", 15*time.Second, func() bool {
		log, _ := os.ReadFile(f.store.LogPath(f.inst(), "test", a[0].JobID))
		return strings.Contains(string(log), "# ballast outcome=")
	})

	// B passes once released; A's record stays superseded.
	waitFor(t, "B's command to start", 20*time.Second, func() bool {
		js, _ := f.store.ReadJob(f.inst(), "test", b[0].JobID)
		pids := readPIDs(t, pidfile)
		return js != nil && js.PID > 0 && len(pids) == 1 && pids[0] != aCmd
	})
	if err := os.WriteFile(gate, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B's record", 20*time.Second, func() bool {
		r := f.latest("test")
		return r != nil && r.JobID == b[0].JobID
	})
	if r := f.latest("test"); r.Outcome != model.OutcomePass {
		t.Errorf("B = %+v", r)
	}
	_, recA, _ = f.store.FindJob(a[0].JobID)
	if recA == nil || recA.Outcome != model.OutcomeSuperseded {
		t.Errorf("A's record was overwritten: %+v", recA)
	}
}

// A job superseded before its child got going never runs.
func TestSupersededBeforeStart(t *testing.T) {
	f := newFx(t)
	marker := filepath.Join(f.sb.Root, "ran")
	f.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
	f.opt.Spawner = &fakeSpawner{} // nothing runs
	a, err := Start(f.opt, f.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(f.opt, f.root, nil); err != nil { // supersedes A (pid unknown, in start grace)
		t.Fatal(err)
	}
	rec, err := RunJob(f.opt, a[0].JobID) // A's child finally starts
	if err != nil || rec == nil || rec.Outcome != model.OutcomeSuperseded {
		t.Fatalf("RunJob(A) = %+v, %v", rec, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the superseded job ran its command")
	}
}

// Spawner children survive the parent's exit and live in their own session.
func TestSpawnerChildSurvivesParent(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "BALLAST_SPAWN_PARENT="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parent: %v\n%s", err, out)
	}
	// The parent has exited; the child is still waiting for its release.
	if _, err := os.Stat(filepath.Join(dir, "child.done")); err == nil {
		t.Fatal("child finished before the parent returned; the test proves nothing")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the detached child to finish", 15*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, "child.done"))
		return err == nil
	})
	pids := strings.Fields(string(mustRead(t, filepath.Join(dir, "parent.pid"))))
	info := strings.Fields(string(mustRead(t, filepath.Join(dir, "child.done"))))
	if len(pids) != 2 || len(info) != 2 {
		t.Fatalf("pids %v info %v", pids, info)
	}
	if pids[1] != info[0] {
		t.Errorf("Spawner returned pid %s, child is %s", pids[1], info[0])
	}
	if runtime.GOOS != "windows" && info[1] != info[0] {
		t.Errorf("child session id %s, pid %s: not a session leader (Setsid)", info[1], info[0])
	}
	log := mustRead(t, filepath.Join(dir, "child.log"))
	if !strings.Contains(string(log), "hello from the detached child") {
		t.Errorf("stdout was not redirected to the log: %q", log)
	}
	if fi, err := os.Stat(filepath.Join(dir, "child.log")); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v, want 0600", fi.Mode().Perm())
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSpawnerErrors(t *testing.T) {
	var s Spawner
	if _, err := s.SpawnDetached(nil, nil, ""); err == nil {
		t.Error("empty argv accepted")
	}
	if _, err := s.SpawnDetached([]string{filepath.Join(t.TempDir(), "no-such-binary")}, nil, ""); err == nil {
		t.Error("missing binary accepted")
	}
	// An unwritable log location fails before anything is started.
	file := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(file, nil, 0o644)
	exe, _ := os.Executable()
	if _, err := s.SpawnDetached([]string{exe}, []string{"BALLAST_HELPER=exit"}, filepath.Join(file, "sub", "x.log")); err == nil {
		t.Error("log under a file accepted")
	}
}
