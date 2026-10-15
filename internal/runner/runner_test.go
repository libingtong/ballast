package runner

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/version"
)

func TestForegroundOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		spec    specJSON
		outcome model.Outcome
		exit    *int
		sig     string
		exitMap int
	}{
		{"pass", helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"), model.OutcomePass, intp(0), "", 0},
		{"fail", helperSpec("test", "exit", "BALLAST_HELPER_CODE=3"), model.OutcomeFail, intp(3), "", 1},
		{"command not found", specJSON{Name: "test", Argv: []string{"definitely-not-a-command-xyz"}, Required: true}, model.OutcomeInfra, nil, "", 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			data := f.setProject(tc.spec)
			rec, err := Foreground(f.opt, f.root, "test")
			if err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != tc.outcome {
				t.Fatalf("outcome = %s (%s), want %s", rec.Outcome, rec.Error, tc.outcome)
			}
			if (rec.ExitCode == nil) != (tc.exit == nil) || (tc.exit != nil && *rec.ExitCode != *tc.exit) {
				t.Errorf("exit code = %v, want %v", rec.ExitCode, tc.exit)
			}
			if rec.Signal != tc.sig {
				t.Errorf("signal = %q", rec.Signal)
			}
			if StatusExitCode(rec) != tc.exitMap {
				t.Errorf("StatusExitCode = %d", StatusExitCode(rec))
			}
			if tc.outcome == model.OutcomeInfra {
				if !strings.Contains(rec.Error, "not found") {
					t.Errorf("error = %q", rec.Error)
				}
			}
			// The record is complete and stored.
			if rec.V != 1 || rec.Tier != model.TierT0 || rec.BallastVersion != version.Version {
				t.Errorf("bad header fields: %+v", rec)
			}
			if rec.ArgvHash != argvHash(tc.spec.Argv) || len(rec.ArgvHash) != 64 {
				t.Errorf("argv_hash = %s", rec.ArgvHash)
			}
			if rec.ProjectJSONHash != project.Hash([]byte(data)) {
				t.Errorf("project_json_hash = %s", rec.ProjectJSONHash)
			}
			if rec.SStart == "" || rec.SStart != rec.SEnd {
				t.Errorf("s_start=%q s_end=%q: the repo did not change", rec.SStart, rec.SEnd)
			}
			if rec.TStart.IsZero() || rec.TEnd.Before(rec.TStart) {
				t.Errorf("times: %v %v", rec.TStart, rec.TEnd)
			}
			if rec.ValidPass() != (tc.outcome == model.OutcomePass) {
				t.Errorf("ValidPass = %v", rec.ValidPass())
			}
			stored := f.latest("test")
			if stored == nil || stored.JobID != rec.JobID || stored.Outcome != tc.outcome {
				t.Errorf("stored = %+v", stored)
			}
			// Job status is done with the same outcome, and the id format holds.
			js, _, err := f.store.FindJob(rec.JobID)
			if err != nil || js == nil || js.State != model.JobDone || js.Outcome != tc.outcome || js.PID != os.Getpid() {
				t.Errorf("job status = %+v, %v", js, err)
			}
			if len(rec.JobID) != len("20060102T150405-")+6 || rec.JobID[8] != 'T' || rec.JobID[15] != '-' {
				t.Errorf("job id %q", rec.JobID)
			}
			// One audit line of type verify with the record.
			au := f.audit.all()
			if len(au) != 1 || au[0].Type != model.AuditVerify || au[0].Verify == nil || au[0].Verify.JobID != rec.JobID ||
				au[0].RepoInst != rec.RepoInst || au[0].V != model.AuditVersion || au[0].Tier != model.TierT0 {
				t.Errorf("audit = %+v", au)
			}
			// Log header and footer.
			log, err := os.ReadFile(rec.LogPath)
			if err != nil || !strings.Contains(string(log), "# ballast verify test job="+rec.JobID) || !strings.Contains(string(log), "# ballast outcome="+string(tc.outcome)) {
				t.Errorf("log = %q, %v", log, err)
			}
		})
	}
}

func intp(n int) *int { return &n }

func TestForegroundStreamsOutput(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "echo", "BALLAST_HELPER_CODE=0"))
	var buf bytes.Buffer
	f.opt.Output = &buf
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v, %v", rec, err)
	}
	for _, want := range []string{"stdout-line", "stderr-line"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("streamed output lacks %q: %q", want, buf.String())
		}
	}
	log, _ := os.ReadFile(rec.LogPath)
	if !strings.Contains(string(log), "stdout-line") || !strings.Contains(string(log), "stderr-line") {
		t.Errorf("log lacks the output: %q", log)
	}
}

func TestCommandEnvironmentAndCwd(t *testing.T) {
	f := newFx(t)
	if err := os.MkdirAll(filepath.Join(f.root, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(f.sb.Root, "probe.txt")
	f.vars["BALLAST_PROBE"] = "from-options-env"
	f.vars["BALLAST_PROBE2"] = "options"
	f.rebuild()
	spec := helperSpec("test", "probe", "BALLAST_HELPER_FILE="+probe, "BALLAST_PROBE2=from-spec")
	spec.Cwd = "sub/dir"
	f.write("sub/dir/keep.txt", "x")
	f.setProject(spec)
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v, %v", rec, err)
	}
	b, _ := os.ReadFile(probe)
	lines := strings.Split(string(b), "\n")
	wantCwd, _ := filepath.EvalSymlinks(filepath.Join(f.root, "sub", "dir"))
	gotCwd, _ := filepath.EvalSymlinks(lines[0])
	if gotCwd != wantCwd {
		t.Errorf("cwd = %q, want %q", gotCwd, wantCwd)
	}
	if lines[1] != "from-options-env" {
		t.Errorf("env from Options.Env not passed: %q", lines[1])
	}
	if lines[2] != "from-spec" {
		t.Errorf("spec env must override: %q", lines[2])
	}
}

func TestPathLookupUsesChildEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	f := newFx(t)
	bin := filepath.Join(f.sb.Root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"stub says: $@\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "mytool"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.vars["PATH"] = bin + string(os.PathListSeparator) + f.vars["PATH"]
	f.rebuild()
	f.setProject(specJSON{Name: "test", Argv: []string{"mytool", "a", "b"}, Required: true})
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v, %v", rec, err)
	}
	log, _ := os.ReadFile(rec.LogPath)
	if !strings.Contains(string(log), "stub says: a b") {
		t.Errorf("log = %q", log)
	}
}

func TestSessionAttribution(t *testing.T) {
	for _, tc := range []struct {
		name         string
		vars         map[string]string
		session, agt string
	}{
		{"none", nil, "", ""},
		{"ballast ids", map[string]string{"BALLAST_SESSION_ID": "s1", "BALLAST_AGENT_ID": "a1"}, "s1", "a1"},
		{"claude session", map[string]string{"CLAUDE_SESSION_ID": "c1"}, "c1", ""},
		{"ballast wins", map[string]string{"BALLAST_SESSION_ID": "s2", "CLAUDE_SESSION_ID": "c2"}, "s2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t)
			for k, v := range tc.vars {
				f.vars[k] = v
			}
			f.rebuild()
			f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
			rec, err := Foreground(f.opt, f.root, "test")
			if err != nil {
				t.Fatal(err)
			}
			if rec.SessionID != tc.session || rec.AgentID != tc.agt {
				t.Errorf("session/agent = %q/%q", rec.SessionID, rec.AgentID)
			}
			if au := f.audit.all(); len(au) != 1 || au[0].SessionID != tc.session || au[0].AgentID != tc.agt {
				t.Errorf("audit attribution: %+v", au)
			}
		})
	}
}

func TestSnapshotsDetectMutationDuringVerify(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE=src/main.txt"))
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomePass {
		t.Fatalf("outcome %s", rec.Outcome)
	}
	if rec.SStart == rec.SEnd || rec.ValidPass() {
		t.Errorf("the command modified a tracked file: s_start=%s s_end=%s validpass=%v", rec.SStart, rec.SEnd, rec.ValidPass())
	}
}

func TestExemptFilesDoNotChangeSnapshots(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE=README.md")) // **/*.md is exempt
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.ValidPass() {
		t.Errorf("exempt file change broke the pass: %+v", rec)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix; Windows uses a Job Object (not exercised here)")
	}
	for _, mode := range []string{"tree", "ignoreterm"} {
		t.Run(mode, func(t *testing.T) {
			old := killGrace
			killGrace = 300 * time.Millisecond
			defer func() { killGrace = old }()
			f := newFx(t)
			pidfile := filepath.Join(f.sb.Root, "pids")
			spec := helperSpec("test", mode, "BALLAST_HELPER_PIDFILE="+pidfile)
			spec.TimeoutS = 1
			f.setProject(spec)
			start := time.Now()
			rec, err := Foreground(f.opt, f.root, "test")
			if err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != model.OutcomeTimeout || rec.ExitCode != nil {
				t.Fatalf("record = %+v", rec)
			}
			if el := time.Since(start); el < 900*time.Millisecond || el > 15*time.Second {
				t.Errorf("elapsed %v", el)
			}
			b, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatalf("helper never wrote its pids: %v", err)
			}
			for _, s := range strings.Fields(string(b)) {
				pid, _ := strconv.Atoi(s)
				waitFor(t, "process "+s+" to die", 5*time.Second, func() bool { return processGone(pid) })
			}
			if rec.ValidPass() {
				t.Error("timeout counted as pass")
			}
			if StatusExitCode(rec) != 3 {
				t.Errorf("exit code %d", StatusExitCode(rec))
			}
		})
	}
}

// A group member that ignores TERM must not outlive a timeout even though
// the leader exits on TERM (the kill timer would never fire), on the pipe-less
// background path as well as on Foreground.
func TestTimeoutKillsStragglerAfterLeaderExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix")
	}
	run := map[string]func(f *fx) (*model.VerifyRecord, error){
		"foreground": func(f *fx) (*model.VerifyRecord, error) { return Foreground(f.opt, f.root, "test") },
		"background": func(f *fx) (*model.VerifyRecord, error) {
			tgt, err := f.opt.withDefaults().resolve(f.root, []string{"test"})
			if err != nil {
				return nil, err
			}
			js, err := f.opt.createJob(tgt, tgt.specs[0])
			if err != nil {
				return nil, err
			}
			return f.opt.withDefaults().runExisting(js, nil)
		},
	}
	for name, fn := range run {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			pidfile := filepath.Join(f.sb.Root, "pids")
			spec := helperSpec("test", "term-leader", "BALLAST_HELPER_PIDFILE="+pidfile)
			spec.TimeoutS = 1
			f.setProject(spec)
			rec, err := fn(f)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != model.OutcomeTimeout {
				t.Fatalf("record = %+v", rec)
			}
			b, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatalf("helper never wrote its pids: %v", err)
			}
			fields := strings.Fields(string(b))
			if len(fields) != 2 {
				t.Fatalf("pidfile = %q", b)
			}
			for _, s := range fields {
				pid, _ := strconv.Atoi(s)
				waitFor(t, "process "+s+" to die", 2*time.Second, func() bool { return processGone(pid) })
			}
		})
	}
}

// A successful command that leaves a process holding its output is a pass
// on Foreground, exactly as in the background.
func TestForegroundPassDespiteLeftoverOutputHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by the Job Object path")
	}
	f := newFx(t)
	pidfile := filepath.Join(f.sb.Root, "pids")
	f.setProject(helperSpec("test", "leave-child", "BALLAST_HELPER_PIDFILE="+pidfile))
	t.Cleanup(func() {
		b, _ := os.ReadFile(pidfile)
		if fields := strings.Fields(string(b)); len(fields) == 2 { // leader (gone), leftover
			if pid, err := strconv.Atoi(fields[1]); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
		}
	})
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomePass || rec.ExitCode == nil || *rec.ExitCode != 0 || rec.Error != "" {
		t.Fatalf("record = %+v", rec)
	}
	log, _ := os.ReadFile(rec.LogPath)
	if !strings.Contains(string(log), "left a process holding its output") {
		t.Errorf("log lacks the note: %q", log)
	}
}

// The supersede marker survives the old job's markPID read-modify-write.
func TestSupersedeMarkerSurvivesMarkPID(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	tgt, err := f.opt.withDefaults().resolve(f.root, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.opt.createJob(tgt, tgt.specs[0])
	if err != nil {
		t.Fatal(err)
	}
	// The superseder acts between the child's read and write of the job file.
	stale := *old
	if _, err := f.opt.createJob(tgt, tgt.specs[0]); err != nil {
		t.Fatal(err)
	}
	if err := f.store.WriteJob(&stale); err != nil { // the child's lost-update write
		t.Fatal(err)
	}
	rec, err := f.opt.withDefaults().runExisting(&stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.Outcome != model.OutcomeSuperseded {
		t.Fatalf("record = %+v", rec)
	}
	if js, _ := f.store.ReadJob(old.RepoInst, old.Name, old.JobID); js == nil || js.State != model.JobDone || js.Outcome != model.OutcomeSuperseded {
		t.Errorf("job file = %+v", js)
	}
	if _, err := os.Stat(f.store.SupersededPath(old.RepoInst, old.Name, old.JobID)); err != nil {
		t.Errorf("marker: %v", err)
	}
}

func TestCommandKilledBySignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no signals on Windows")
	}
	f := newFx(t)
	f.setProject(helperSpec("test", "kill-self"))
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != model.OutcomeSignal || rec.Signal != "SIGKILL" || rec.ExitCode != nil {
		t.Fatalf("record = %+v", rec)
	}
	if StatusExitCode(rec) != 4 {
		t.Errorf("exit code %d", StatusExitCode(rec))
	}
}

func TestConfigErrors(t *testing.T) {
	f := newFx(t)
	notRepo := t.TempDir()
	cases := []struct {
		name  string
		setup func()
		cwd   string
		names []string
	}{
		{"not a repo", func() {}, notRepo, nil},
		{"empty cwd", func() {}, "", nil},
		{"no project.json", func() {}, f.root, nil},
		{"invalid project.json", func() {
			f.write(".ballast/project.json", `{"verify":[{"name":"BAD","argv":["x"]}]}`)
		}, f.root, nil},
		{"unknown name", func() {
			f.setProject(helperSpec("test", "exit"))
		}, f.root, []string{"nope"}},
		{"no required entries", func() {
			s := helperSpec("lint", "exit")
			s.Required = false
			f.setProject(s)
		}, f.root, nil},
		{"empty names", func() { f.setProject(helperSpec("test", "exit")) }, f.root, []string{}},
	}
	sp := &fakeSpawner{}
	f.opt.Spawner = sp
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			if st, err := Start(f.opt, tc.cwd, tc.names); !errors.Is(err, ErrConfig) || errors.Is(err, ErrInfra) || len(st) != 0 {
				t.Errorf("Start = %v, %v; want ErrConfig", st, err)
			}
			if len(tc.names) == 1 {
				if _, err := Foreground(f.opt, tc.cwd, tc.names[0]); !errors.Is(err, ErrConfig) {
					t.Errorf("Foreground err = %v", err)
				}
			}
		})
	}
	if len(sp.calls) != 0 {
		t.Errorf("a configuration error must not spawn: %+v", sp.calls)
	}
	o := f.opt
	o.Self = ""
	if _, err := Start(o, f.root, nil); !errors.Is(err, ErrConfig) {
		t.Errorf("no Self: %v", err)
	}
}

func TestStartProtocol(t *testing.T) {
	f := newFx(t)
	lint := helperSpec("lint", "exit")
	lint.Required = false
	build := helperSpec("build", "exit")
	f.setProject(helperSpec("test", "exit"), lint, build)
	sp := &fakeSpawner{}
	f.opt.Spawner = sp
	f.opt.Self = "/abs/ballast"

	// Default: every required entry, in project order.
	st, err := Start(f.opt, f.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 || st[0].Name != "test" || st[1].Name != "build" {
		t.Fatalf("started = %+v", st)
	}
	if len(sp.calls) != 2 {
		t.Fatalf("spawns = %d", len(sp.calls))
	}
	for i, c := range sp.calls {
		if len(c.argv) != 4 || c.argv[0] != "/abs/ballast" || c.argv[1] != "verify" || c.argv[2] != "--run-job" || c.argv[3] != st[i].JobID {
			t.Errorf("argv = %v", c.argv)
		}
		wantLog := f.store.LogPath(f.inst(), st[i].Name, st[i].JobID)
		if c.logPath != wantLog {
			t.Errorf("log path = %s, want %s", c.logPath, wantLog)
		}
		if len(c.env) == 0 {
			t.Error("child env empty")
		}
		js, err := f.store.ReadJob(f.inst(), st[i].Name, st[i].JobID)
		if err != nil || js == nil || js.State != model.JobRunning {
			t.Errorf("job status = %+v, %v", js, err)
			continue
		}
		if !js.Deadline.Equal(js.StartedAt.Add(1800 * time.Second)) {
			t.Errorf("deadline = %v for started %v (default timeout 1800s)", js.Deadline, js.StartedAt)
		}
		if _, err := os.Stat(f.store.SpecPath(js.RepoInst, js.Name, js.JobID)); err != nil {
			t.Errorf("sidecar missing: %v", err)
		}
	}
	if st[0].String() != "job="+st[0].JobID+" name=test" {
		t.Errorf("String = %q", st[0].String())
	}

	// Explicit names, including a non-required one; duplicates collapse.
	sp.calls = nil
	st, err = Start(f.opt, f.root, []string{"lint", "lint"})
	if err != nil || len(st) != 1 || st[0].Name != "lint" {
		t.Fatalf("Start(lint) = %+v, %v", st, err)
	}
	js, _ := f.store.ReadJob(f.inst(), "lint", st[0].JobID)
	if js == nil || !js.Deadline.Equal(js.StartedAt.Add(1800*time.Second)) {
		t.Errorf("lint job: %+v", js)
	}
}

func TestStartSpawnFailure(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "exit"))
	boom := errors.New("fork failed")
	f.opt.Spawner = &fakeSpawner{err: boom}
	st, err := Start(f.opt, f.root, nil)
	if !errors.Is(err, ErrInfra) || !errors.Is(err, boom) || errors.Is(err, ErrConfig) || len(st) != 0 {
		t.Fatalf("Start = %v, %v", st, err)
	}
	rec := f.latest("test")
	if rec == nil || rec.Outcome != model.OutcomeInfra || !strings.Contains(rec.Error, "fork failed") {
		t.Fatalf("record = %+v", rec)
	}
	js, _, _ := f.store.FindJob(rec.JobID)
	if js == nil || js.State != model.JobDone || js.Outcome != model.OutcomeInfra {
		t.Errorf("job = %+v", js)
	}
	if running, _ := f.store.RunningJobs(f.inst(), "test"); len(running) != 0 {
		t.Error("failed job still counts as running")
	}
	if au := f.audit.all(); len(au) != 1 || au[0].Verify.Outcome != model.OutcomeInfra {
		t.Errorf("audit = %+v", au)
	}
}

func TestAuditFailureIsNotFatal(t *testing.T) {
	f := newFx(t)
	f.audit.err = errors.New("audit disk full")
	f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v, %v", rec, err)
	}
	log, _ := os.ReadFile(rec.LogPath)
	if !strings.Contains(string(log), "audit append failed: audit disk full") {
		t.Errorf("log does not mention the audit failure: %q", log)
	}
	// And no audit sink at all is fine.
	f.opt.Audit = nil
	if _, err := Foreground(f.opt, f.root, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestSupersedeRunningJobRecordsIt(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	// A running job of the same name, owned by this very process (so no
	// signal is sent), with its sidecar.
	repo, _ := f.opt.Repo.Locate(f.root)
	tgt, err := f.opt.withDefaults().resolve(f.root, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.opt.createJob(tgt, tgt.specs[0])
	if err != nil {
		t.Fatal(err)
	}
	old.PID = os.Getpid()
	if err := f.store.WriteJob(old); err != nil {
		t.Fatal(err)
	}

	// A job of another name stays untouched.
	other := *old
	other.Name, other.JobID = "other", old.JobID+"x"
	if err := f.store.WriteJob(&other); err != nil {
		t.Fatal(err)
	}

	rec, err := Foreground(f.opt, f.root, "test")
	if err != nil || rec.Outcome != model.OutcomePass {
		t.Fatalf("%+v, %v", rec, err)
	}
	js, oldRec, err := f.store.FindJob(old.JobID)
	if err != nil || js == nil || js.State != model.JobDone || js.Outcome != model.OutcomeSuperseded {
		t.Fatalf("old job = %+v, %v", js, err)
	}
	if oldRec == nil || oldRec.Outcome != model.OutcomeSuperseded || oldRec.Name != "test" || oldRec.RepoInst != repo.Inst ||
		!oldRec.TStart.Equal(old.StartedAt) || oldRec.ArgvHash == "" || oldRec.ProjectJSONHash == "" {
		t.Errorf("superseded record = %+v", oldRec)
	}
	if StatusExitCode(oldRec) != 7 || oldRec.ValidPass() {
		t.Error("superseded record must map to 7 and never be a valid pass")
	}
	if latest := f.latest("test"); latest.JobID != rec.JobID {
		t.Errorf("latest = %s, want the new job %s", latest.JobID, rec.JobID)
	}
	if j, _ := f.store.ReadJob(repo.Inst, "other", other.JobID); j == nil || j.State != model.JobRunning {
		t.Errorf("job of another name touched: %+v", j)
	}
	// The supersede is audited like any other verify line.
	n := 0
	for _, a := range f.audit.all() {
		if a.Verify != nil && a.Verify.Outcome == model.OutcomeSuperseded {
			n++
		}
	}
	if n != 1 {
		t.Errorf("superseded audit lines = %d", n)
	}
}

func TestRunJobEdgeCases(t *testing.T) {
	f := newFx(t)
	f.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"))
	if _, err := RunJob(f.opt, "20261016T120000-nonexi"); !errors.Is(err, ErrConfig) {
		t.Errorf("unknown job: %v", err)
	}
	if _, err := RunJob(f.opt, "../../x"); err == nil {
		t.Error("traversal accepted")
	}
	f.opt.Spawner = &fakeSpawner{}
	st, err := Start(f.opt, f.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := RunJob(f.opt, st[0].JobID)
	if err != nil || rec.Outcome != model.OutcomePass || rec.JobID != st[0].JobID {
		t.Fatalf("RunJob = %+v, %v", rec, err)
	}
	// Running it again returns the finished record without running anything.
	before := len(f.audit.all())
	again, err := RunJob(f.opt, st[0].JobID)
	if err != nil || again == nil || again.JobID != rec.JobID || len(f.audit.all()) != before {
		t.Errorf("second RunJob = %+v, %v (audit %d→%d)", again, err, before, len(f.audit.all()))
	}
	// A job without its sidecar ends as infra.
	st, err = Start(f.opt, f.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	js, _, _ := f.store.FindJob(st[0].JobID)
	if err := os.Remove(f.store.SpecPath(js.RepoInst, js.Name, js.JobID)); err != nil {
		t.Fatal(err)
	}
	rec, err = RunJob(f.opt, st[0].JobID)
	if !errors.Is(err, ErrInfra) || rec == nil || rec.Outcome != model.OutcomeInfra {
		t.Errorf("missing sidecar: %+v, %v", rec, err)
	}
}

func TestStatusExitCode(t *testing.T) {
	for o, want := range map[model.Outcome]int{
		model.OutcomePass: 0, model.OutcomeFail: 1, model.OutcomeTimeout: 3, model.OutcomeSignal: 4,
		model.OutcomeInfra: 5, model.OutcomeSuperseded: 7, "weird": 5,
	} {
		if got := StatusExitCode(&model.VerifyRecord{Outcome: o}); got != want {
			t.Errorf("%s: %d, want %d", o, got, want)
		}
	}
	if StatusExitCode(nil) != 11 {
		t.Error("nil record must be 11")
	}
}

func TestLookPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission semantics")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	_ = os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "noexec"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(bin, "adir"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "local.sh"), []byte("#!/bin/sh\n"), 0o755)
	env := []string{"A=b", "PATH=/nonexistent" + string(os.PathListSeparator) + bin}
	if p, err := lookPath("tool", dir, env); err != nil || p != filepath.Join(bin, "tool") {
		t.Errorf("tool: %q, %v", p, err)
	}
	for _, name := range []string{"noexec", "adir", "missing"} {
		if p, err := lookPath(name, dir, env); err == nil {
			t.Errorf("%s resolved to %q", name, p)
		}
	}
	if p, err := lookPath("./local.sh", dir, nil); err != nil || p != filepath.Join(dir, "local.sh") {
		t.Errorf("relative: %q, %v", p, err)
	}
	if _, err := lookPath("./nope.sh", dir, nil); err == nil {
		t.Error("missing relative resolved")
	}
	if p, err := lookPath(filepath.Join(bin, "tool"), dir, nil); err != nil || p != filepath.Join(bin, "tool") {
		t.Errorf("absolute: %q, %v", p, err)
	}
	if _, err := lookPath("tool", dir, []string{"A=b"}); err == nil {
		t.Error("no PATH in env must not fall back to the process PATH")
	}
}

func TestConcurrentForegroundJobs(t *testing.T) {
	f := newFx(t)
	var specs []specJSON
	names := []string{"a", "b", "c", "d"}
	for _, n := range names {
		specs = append(specs, helperSpec(n, "exit", "BALLAST_HELPER_CODE=0"))
	}
	f.setProject(specs...)
	errs := make(chan error, len(names))
	recs := make(chan *model.VerifyRecord, len(names))
	for _, n := range names {
		go func(n string) {
			rec, err := Foreground(f.opt, f.root, n)
			errs <- err
			recs <- rec
		}(n)
	}
	for range names {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if rec := <-recs; rec.Outcome != model.OutcomePass || !rec.ValidPass() {
			t.Errorf("record = %+v", rec)
		}
	}
	for _, n := range names {
		if r := f.latest(n); r == nil || r.Name != n {
			t.Errorf("latest(%s) = %+v", n, r)
		}
	}
	if au := f.audit.all(); len(au) != len(names) {
		t.Errorf("audit lines = %d", len(au))
	}
}

func FuzzLookPath(f *testing.F) {
	for _, s := range []string{"go", "./x", "../x", "a/b", `a\b`, "", "C:\\x", "x.exe", "\x00"} {
		f.Add(s, "PATH=/bin")
	}
	f.Fuzz(func(t *testing.T, file, env string) {
		dir := t.TempDir()
		p, err := lookPath(file, dir, strings.Split(env, "\n"))
		if err == nil && p == "" {
			t.Fatal("empty path without error")
		}
	})
}
