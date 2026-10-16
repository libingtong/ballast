package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func readRequest(t *testing.T, h *hookFx, inst, id string) model.VerifyRequestFile {
	t.Helper()
	p := filepath.Join(h.opt.Layout.RequestsDir, inst, id+".json")
	var f model.VerifyRequestFile
	if err := json.Unmarshal([]byte(readFileT(t, p)), &f); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("request file perms: %v %v", fi, err)
	}
	return f
}

func TestStartRequestWritesRequestAndJobs(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit", "BALLAST_HELPER_CODE=0"), helperSpec("lint", "exit", "BALLAST_HELPER_CODE=0"))
	sub := filepath.Join(h.root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	req := model.HookStartRequest{
		RequestID: h.opt.newJobID(), Cwd: sub, Names: []string{"test", "lint"}, Client: model.ClientClaude,
		SessionID: "s-1", AgentID: "a-1", ProjectDir: h.root,
	}
	started, err := StartRequest(h.opt, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(started) != 2 || started[0].JobID != req.RequestID || started[1].JobID == req.RequestID || started[0].Name != "test" || started[1].Name != "lint" {
		t.Fatalf("started = %+v", started)
	}
	f := readRequest(t, h, h.inst(), req.RequestID)
	if f.V != 1 || f.RequestID != req.RequestID || f.Origin != model.OriginHook || f.Client != model.ClientClaude ||
		f.SessionID != "s-1" || f.AgentID != "a-1" || f.Error != "" {
		t.Errorf("request file: %+v", f)
	}
	if len(f.Jobs) != 2 || f.Jobs[0] != (model.RequestedJob{Name: "test", JobID: started[0].JobID}) || f.Jobs[1] != (model.RequestedJob{Name: "lint", JobID: started[1].JobID}) {
		t.Errorf("jobs: %+v", f.Jobs)
	}
	// One detached child per job, running Ballast itself.
	if len(h.spawn.calls) != 2 {
		t.Fatalf("spawn calls: %d", len(h.spawn.calls))
	}
	for i, c := range h.spawn.calls {
		want := []string{h.opt.Self, "verify", "--run-job", started[i].JobID}
		if strings.Join(c.argv, " ") != strings.Join(want, " ") {
			t.Errorf("argv = %v", c.argv)
		}
		if c.logPath != h.store.LogPath(h.inst(), started[i].Name, started[i].JobID) {
			t.Errorf("log path %s", c.logPath)
		}
	}
	// Sidecars: origin hook, attribution from the request, not from the env.
	for _, s := range started {
		js, _, err := h.store.FindJob(s.JobID)
		if err != nil || js == nil || js.State != model.JobRunning {
			t.Fatalf("job %s: %+v %v", s.JobID, js, err)
		}
		side, err := h.opt.readSpec(js)
		if err != nil {
			t.Fatal(err)
		}
		if side.Origin != model.OriginHook || side.SessionID != "s-1" || side.AgentID != "a-1" || side.ProjectDir != h.root || side.Client != model.ClientClaude || side.RequestID != req.RequestID {
			t.Errorf("sidecar: %+v", side)
		}
	}
	// No names: every required entry.
	req2 := req
	req2.RequestID, req2.Names = h.opt.newJobID(), nil
	started, err = StartRequest(h.opt, req2)
	if err != nil || len(started) != 2 {
		t.Errorf("required entries: %+v %v", started, err)
	}
	// A repeated request supersedes the running jobs of the same names.
	if old, _ := h.store.RunningJobs(h.inst(), "test"); len(old) != 1 {
		t.Errorf("running test jobs after the second request: %d", len(old))
	}
}

func TestStartRequestConfigErrorsStillWriteTheFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *hookFx) (cwd string, names []string)
		want  string
	}{
		{"not a repository", func(h *hookFx) (string, []string) {
			d := filepath.Join(h.sb.Root, "plain")
			os.MkdirAll(d, 0o755)
			return d, nil
		}, "not inside a git repository"},
		{"no project.json", func(h *hookFx) (string, []string) { return h.root, nil }, "not found"},
		{"unknown name", func(h *hookFx) (string, []string) {
			h.setProject(helperSpec("test", "exit"))
			return h.root, []string{"nope"}
		}, "no verify entry named"},
		{"invalid project.json", func(h *hookFx) (string, []string) {
			h.write(".ballast/project.json", "{not json")
			return h.root, nil
		}, "project.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookFx(t)
			cwd, names := tc.setup(h)
			req := model.HookStartRequest{RequestID: h.opt.newJobID(), Cwd: cwd, Names: names, Client: model.ClientClaude}
			started, err := StartRequest(h.opt, req)
			if !errors.Is(err, ErrConfig) || len(started) != 0 {
				t.Fatalf("started %v err %v", started, err)
			}
			// Findable without knowing the scope, and explained.
			res, err := RequestStatus(h.opt, "", req.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Error, tc.want) || res.Worst != "infra" || res.ExitCode != 5 || len(res.Jobs) != 0 {
				t.Errorf("status: %+v (want error containing %q)", res, tc.want)
			}
			if len(h.spawn.calls) != 0 {
				t.Errorf("a child was spawned")
			}
			if tc.name == "not a repository" {
				entries, _ := os.ReadDir(h.opt.Layout.RequestsDir)
				if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "norepo-") {
					t.Errorf("scope dirs: %v", entries)
				}
			}
		})
	}
}

func TestStartRequestSpawnFailure(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit"), helperSpec("lint", "exit"))
	h.spawn.err = errors.New("fork failed")
	req := model.HookStartRequest{RequestID: h.opt.newJobID(), Cwd: h.root, Names: []string{"test", "lint"}, Client: model.ClientClaude}
	started, err := StartRequest(h.opt, req)
	if !errors.Is(err, ErrInfra) || len(started) != 0 {
		t.Fatalf("started %v err %v", started, err)
	}
	res, err := RequestStatus(h.opt, h.inst(), req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Worst != "infra" || res.ExitCode != 5 || len(res.Jobs) != 2 || !strings.Contains(res.Error, "fork failed") {
		t.Errorf("status: %+v", res)
	}
	for _, j := range res.Jobs {
		if j.Record == nil || j.Record.Outcome != model.OutcomeInfra || j.Record.Origin != model.OriginHook || j.Record.Workspace != model.WorkspaceCopy {
			t.Errorf("job %+v", j)
		}
	}
}

func TestStartRequestRejectsABadIDAndMissingSelf(t *testing.T) {
	h := newHookFx(t)
	h.setProject(helperSpec("test", "exit"))
	for _, id := range []string{"", "../x", "a/b", ".."} {
		if _, err := StartRequest(h.opt, model.HookStartRequest{RequestID: id, Cwd: h.root}); !errors.Is(err, ErrConfig) {
			t.Errorf("id %q: %v", id, err)
		}
	}
	h.opt.Self = ""
	id := h.opt.newJobID()
	if _, err := StartRequest(h.opt, model.HookStartRequest{RequestID: id, Cwd: h.root}); !errors.Is(err, ErrConfig) {
		t.Errorf("no self: %v", err)
	}
	if res, err := RequestStatus(h.opt, "", id); err != nil || !strings.Contains(res.Error, "ballast binary") {
		t.Errorf("status: %+v %v", res, err)
	}
}

// --- RequestStatus ---------------------------------------------------------

type statusJob struct {
	state string // pass fail timeout signal infra superseded running gone
}

func writeRequestWith(t *testing.T, h *hookFx, id string, jobs []statusJob, reqErr string) {
	t.Helper()
	inst := "inst0000000000aa"
	f := model.VerifyRequestFile{V: 1, RequestID: id, Origin: model.OriginHook, Jobs: []model.RequestedJob{}, Error: reqErr}
	now := time.Now()
	for i, j := range jobs {
		name := "n" + string(rune('a'+i))
		jobID := id
		if i > 0 {
			jobID = id + "-" + name
		}
		f.Jobs = append(f.Jobs, model.RequestedJob{Name: name, JobID: jobID})
		js := &model.JobStatus{V: 1, JobID: jobID, Name: name, RepoInst: inst, StartedAt: now.Add(-time.Minute), Deadline: now.Add(time.Hour)}
		switch j.state {
		case "gone":
			continue
		case "running":
			js.State, js.PID = model.JobRunning, os.Getpid()
			if err := h.store.WriteJob(js); err != nil {
				t.Fatal(err)
			}
			continue
		}
		js.State, js.Outcome = model.JobDone, model.Outcome(j.state)
		if err := h.store.WriteJob(js); err != nil {
			t.Fatal(err)
		}
		rec := &model.VerifyRecord{V: 1, JobID: jobID, Name: name, RepoInst: inst, Outcome: model.Outcome(j.state), TStart: now.Add(-time.Minute), TEnd: now, Tier: model.TierT0}
		if err := h.store.WriteRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(f)
	p := filepath.Join(h.opt.Layout.RequestsDir, inst, id+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRequestStatusWorstAndExitCodes(t *testing.T) {
	st := func(s ...string) []statusJob {
		var out []statusJob
		for _, x := range s {
			out = append(out, statusJob{x})
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		jobs  []statusJob
		err   string
		worst string
		exit  int
	}{
		{"single pass", st("pass"), "", "pass", 0},
		{"single fail", st("fail"), "", "fail", 1},
		{"single timeout", st("timeout"), "", "timeout", 3},
		{"single signal", st("signal"), "", "signal", 4},
		{"single infra", st("infra"), "", "infra", 5},
		{"single running", st("running"), "", "running", 6},
		{"single superseded", st("superseded"), "", "infra", 7},
		{"single job file gone", st("gone"), "", "infra", 5},
		{"pass pass", st("pass", "pass"), "", "pass", 0},
		{"pass running", st("pass", "running"), "", "running", 6},
		{"running infra", st("running", "infra"), "", "infra", 5},
		{"infra signal", st("infra", "signal"), "", "signal", 4},
		{"signal timeout", st("signal", "timeout"), "", "timeout", 3},
		{"timeout fail", st("timeout", "fail"), "", "fail", 1},
		{"fail everything", st("pass", "running", "infra", "signal", "timeout", "fail"), "", "fail", 1},
		{"superseded pass", st("superseded", "pass"), "", "infra", 5},
		{"superseded running", st("superseded", "running"), "", "infra", 5},
		{"gone pass", st("gone", "pass"), "", "infra", 5},
		{"error and no jobs", nil, "no project.json", "infra", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookFx(t)
			id := "20260101T000000-abcdef"
			writeRequestWith(t, h, id, tc.jobs, tc.err)
			for _, inst := range []string{"", "inst0000000000aa"} {
				res, err := RequestStatus(h.opt, inst, id)
				if err != nil {
					t.Fatal(err)
				}
				if res.Worst != tc.worst || res.ExitCode != tc.exit || res.Error != tc.err || res.RequestID != id || len(res.Jobs) != len(tc.jobs) {
					t.Errorf("inst %q: %+v, want worst %s exit %d", inst, res, tc.worst, tc.exit)
				}
				for i, j := range res.Jobs {
					want := tc.jobs[i].state
					if want == "gone" {
						want = "infra"
					}
					if j.Outcome != want {
						t.Errorf("job %d outcome %s want %s", i, j.Outcome, want)
					}
					if (j.Record != nil) != (want != "running" && tc.jobs[i].state != "gone") {
						t.Errorf("job %d record %v", i, j.Record)
					}
				}
			}
		})
	}
}

func TestRequestStatusNotFoundAndHostileIDs(t *testing.T) {
	h := newHookFx(t)
	if _, err := RequestStatus(h.opt, "", "20260101T000000-abcdef"); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("missing: %v", err)
	}
	writeRequestWith(t, h, "20260101T000000-abcdef", []statusJob{{"pass"}}, "")
	if _, err := RequestStatus(h.opt, "other-scope", "20260101T000000-abcdef"); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("wrong scope: %v", err)
	}
	for _, id := range []string{"", "..", "../x", "a/b", `a\b`, "x\x00y"} {
		if _, err := RequestStatus(h.opt, "", id); !errors.Is(err, ErrRequestNotFound) {
			t.Errorf("id %q: %v", id, err)
		}
	}
	if _, err := RequestStatus(h.opt, "../..", "20260101T000000-abcdef"); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("hostile scope accepted")
	}
	// A request file that names another id is rejected, not trusted.
	p := filepath.Join(h.opt.Layout.RequestsDir, "inst0000000000aa", "20260101T000000-ffffff.json")
	if err := os.WriteFile(p, []byte(`{"v":1,"request_id":"other","jobs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestStatus(h.opt, "", "20260101T000000-ffffff"); !errors.Is(err, ErrInfra) {
		t.Errorf("mismatched id: %v", err)
	}
}

// --- GCRequests ------------------------------------------------------------

func TestGCRequests(t *testing.T) {
	h := newHookFx(t)
	now := time.Now()
	age := func(p string, d time.Duration) {
		t.Helper()
		if err := os.Chtimes(p, now.Add(-d), now.Add(-d)); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(p, content string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	lay := h.opt.Layout
	day := 24 * time.Hour
	// requests/
	oldReq := mk(filepath.Join(lay.RequestsDir, "inst-a", "20250101T000000-aaaaaa.json"), "{}")
	oldLog := mk(filepath.Join(lay.RequestsDir, "inst-a", "20250101T000000-aaaaaa.spawn.log"), "x")
	newReq := mk(filepath.Join(lay.RequestsDir, "inst-a", "20260101T000000-bbbbbb.json"), "{}")
	onlyOld := mk(filepath.Join(lay.RequestsDir, "inst-b", "20250101T000000-cccccc.json"), "{}")
	age(oldReq, 31*day)
	age(oldLog, 40*day)
	age(newReq, 29*day)
	age(onlyOld, 90*day)
	// quarantine/
	oldQ := mk(filepath.Join(lay.QuarantineDir, "job-old", ".claude", "settings.local.json"), "{}")
	newQ := mk(filepath.Join(lay.QuarantineDir, "job-new", "f"), "x")
	age(filepath.Join(lay.QuarantineDir, "job-old"), 31*day)
	age(filepath.Join(lay.QuarantineDir, "job-new"), 2*day)
	// work/
	finishedJob := "20250101T000000-111111"
	runningJob := "20250101T000000-222222"
	orphanJob := "20250101T000000-333333"
	youngJob := "20250101T000000-444444"
	deadJob := "20250101T000000-555555"
	for _, j := range []string{finishedJob, runningJob, orphanJob, youngJob, deadJob} {
		mk(filepath.Join(lay.WorkDir, j, "tree", "f"), "x")
		mk(filepath.Join(lay.WorkDir, j, "index"), "x")
	}
	// A read-only directory and a symlink out of the tree inside an old one.
	outside := filepath.Join(h.sb.Root, "outside")
	mk(filepath.Join(outside, "keep"), "x")
	if err := os.Symlink(outside, filepath.Join(lay.WorkDir, orphanJob, "tree", "escape")); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(lay.WorkDir, orphanJob, "tree"), 0o500)
	for _, j := range []string{finishedJob, runningJob, orphanJob, deadJob} {
		age(filepath.Join(lay.WorkDir, j), 25*time.Hour)
	}
	age(filepath.Join(lay.WorkDir, youngJob), 2*time.Hour)
	inst := "inst-w"
	mkJob := func(id string, state model.JobState, pid int, deadline time.Time) {
		js := &model.JobStatus{V: 1, JobID: id, Name: "t", RepoInst: inst, State: state, PID: pid, StartedAt: now.Add(-time.Hour), Deadline: deadline}
		if err := h.store.WriteJob(js); err != nil {
			t.Fatal(err)
		}
	}
	mkJob(finishedJob, model.JobDone, 0, now.Add(time.Hour))
	mkJob(runningJob, model.JobRunning, os.Getpid(), now.Add(time.Hour))
	mkJob(youngJob, model.JobDone, 0, now.Add(time.Hour))
	mkJob(deadJob, model.JobRunning, 0, now.Add(-time.Hour)) // past its deadline: not alive

	n, err := GCRequests(h.opt)
	if err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	for p, want := range map[string]bool{
		oldReq: false, oldLog: false, newReq: true, onlyOld: false,
		filepath.Join(lay.RequestsDir, "inst-b"): false, filepath.Join(lay.RequestsDir, "inst-a"): true,
		filepath.Join(lay.QuarantineDir, "job-old"): false, newQ: true,
		filepath.Join(lay.WorkDir, finishedJob): false, filepath.Join(lay.WorkDir, runningJob): true,
		filepath.Join(lay.WorkDir, orphanJob): false, filepath.Join(lay.WorkDir, youngJob): true,
		filepath.Join(lay.WorkDir, deadJob): false, filepath.Join(outside, "keep"): true,
	} {
		if exists(p) != want {
			t.Errorf("%s exists = %v, want %v", strings.TrimPrefix(p, h.sb.Root), !want, want)
		}
	}
	_ = oldQ
	if n.Requests != 3 || n.Quarantine != 1 || n.Work != 3 {
		t.Errorf("counts = %+v", n)
	}
	// Idempotent, and fine when the directories do not exist.
	if n, err := GCRequests(h.opt); err != nil || n != (GCCounts{}) {
		t.Errorf("second pass: %+v %v", n, err)
	}
	h2 := newFx(t)
	if n, err := GCRequests(h2.opt); err != nil || n != (GCCounts{}) {
		t.Errorf("empty home: %+v %v", n, err)
	}
}

// A job superseded while its tree is being materialized never starts the
// command.
func TestSupersededDuringMaterializeDoesNotRun(t *testing.T) {
	h := newHookFx(t)
	marker := filepath.Join(h.sb.Root, "ran")
	h.setProject(helperSpec("test", "touch", "BALLAST_HELPER_FILE="+marker))
	started, _, err := h.startHook("test")
	if err != nil {
		t.Fatal(err)
	}
	done := false
	h.rg.before = func(args []string) {
		if !done && gitVerb(args) == "add" {
			done = true
			if _, _, err := h.startHook("test"); err != nil { // supersedes the job being materialized
				t.Error(err)
			}
		}
	}
	rec, err := RunJob(h.opt, started[0].JobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Errorf("the superseded job ran its command")
	}
	if rec == nil || rec.Outcome != model.OutcomeSuperseded {
		t.Errorf("record: %+v", rec)
	}
	noJobDir(t, h, started[0].JobID)
}
