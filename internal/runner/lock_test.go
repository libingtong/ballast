package runner

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// startBystander starts an unrelated live process (the test binary in sleep
// mode) and returns its pid.
func startBystander(t *testing.T, f *fx) int {
	t.Helper()
	exe, _ := os.Executable()
	cmd := exec.Command(exe)
	cmd.Env = append(f.environ(), "BALLAST_HELPER=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// A job file left behind by a dead runner may name a pid that now belongs to
// an unrelated process; superseding the job must not touch it.
func TestSupersedeSparesRecycledPID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lockFile bool // the dead runner's lock file is still on disk
	}{{"lock file left behind", true}, {"no lock file", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
			f.opt.Spawner = &fakeSpawner{}
			bystander := startBystander(t, f)

			tgt, err := f.opt.withDefaults().resolve(f.root, []string{"test"})
			if err != nil {
				t.Fatal(err)
			}
			old, err := f.opt.createJob(tgt, tgt.specs[0])
			if err != nil {
				t.Fatal(err)
			}
			old.PID = bystander
			if err := f.store.WriteJob(old); err != nil {
				t.Fatal(err)
			}
			if tc.lockFile {
				if err := os.WriteFile(f.store.LockPath(old.RepoInst, old.Name, old.JobID), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := Start(f.opt, f.root, nil); err != nil {
				t.Fatal(err)
			}
			js, rec, err := f.store.FindJob(old.JobID)
			if err != nil || js == nil || js.Outcome != model.OutcomeSuperseded || rec == nil || rec.Outcome != model.OutcomeSuperseded {
				t.Fatalf("old job = %+v, %+v, %v", js, rec, err)
			}
			time.Sleep(300 * time.Millisecond)
			if processGone(bystander) {
				t.Fatal("the unrelated process was terminated")
			}
		})
	}
}

func TestRunnerHoldsLock(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	opt := f.opt.withDefaults()
	js := &model.JobStatus{V: 1, JobID: "20261016T120000-abcdef", Name: "test", RepoInst: f.inst(), State: model.JobRunning}
	if err := os.MkdirAll(f.opt.Layout.JobsDir+"/"+js.RepoInst+"/test", 0o700); err != nil {
		t.Fatal(err)
	}
	if opt.runnerHoldsLock(js) {
		t.Error("no lock file: reported as held")
	}
	release := opt.holdJobLock(js)
	if !opt.runnerHoldsLock(js) {
		t.Error("held lock not seen")
	}
	// The probe must not disturb the holder.
	if !opt.runnerHoldsLock(js) {
		t.Error("lock lost after a probe")
	}
	release()
	if opt.runnerHoldsLock(js) {
		t.Error("released lock still reported as held")
	}
	// A second runner for the same job waits for a probe, then gets the lock.
	r1 := opt.holdJobLock(js)
	if !opt.runnerHoldsLock(js) {
		t.Error("lock not taken again")
	}
	r1()
}

// A live runner holds its job lock for its whole life, so superseding it
// still stops the command (TestSupersedeStopsRunningJob); the file stays for
// GC, which removes it with the job.
func TestRunLeavesLockFile(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v, %v", rec, err)
	}
	lock := f.store.LockPath(rec.RepoInst, rec.Name, rec.JobID)
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("lock file missing after a run: %v", err)
	}
}
