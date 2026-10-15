package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func newStore(t testing.TB) (*Store, *sys.FakeClock, *sandbox) {
	t.Helper()
	sb := newSandbox(t)
	clock := sys.NewFakeClock(epoch)
	return NewStore(sb.options(t, clock, &sys.SeqID{})), clock, sb
}

func rec(inst, name, job string, start time.Time, outcome model.Outcome, s model.TreeID) *model.VerifyRecord {
	return &model.VerifyRecord{
		V: 1, JobID: job, Name: name, RepoInst: inst, Tier: model.TierT0,
		SStart: s, SEnd: s, TStart: start, TEnd: start.Add(time.Minute), Outcome: outcome,
	}
}

func TestRecordPathAndWrite(t *testing.T) {
	st, _, _ := newStore(t)
	r := rec("0123456789abcdef", "test", "20261016T120000-aabbcc", epoch, model.OutcomePass, "t1")
	p := st.RecordPath(r)
	wantSuffix := filepath.Join("evidence", "0123456789abcdef", "test", fmt.Sprintf("%020d-20261016T120000-aabbcc.json", epoch.UnixNano()))
	if !strings.HasSuffix(p, wantSuffix) {
		t.Fatalf("path %s lacks suffix %s", p, wantSuffix)
	}
	if err := st.WriteRecord(r); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(p))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
		}
	}
	got, err := st.Latest(r.RepoInst, r.Name)
	if err != nil || got == nil {
		t.Fatalf("Latest = %v, %v", got, err)
	}
	if got.JobID != r.JobID || got.SEnd != "t1" || !got.TStart.Equal(epoch) || got.Outcome != model.OutcomePass || !got.ValidPass() {
		t.Errorf("round trip lost data: %+v", got)
	}
	// V defaults to 1 on disk; the caller's record is untouched.
	r2 := rec("0123456789abcdef", "test", "j2", epoch.Add(time.Second), model.OutcomeFail, "t2")
	r2.V = 0
	if err := st.WriteRecord(r2); err != nil {
		t.Fatal(err)
	}
	if r2.V != 0 {
		t.Error("WriteRecord modified its argument")
	}
	data, _ := os.ReadFile(st.RecordPath(r2))
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw["v"] != float64(1) {
		t.Errorf("on-disk record: %s (%v)", data, err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestWriteRecordRejectsUnsafeComponents(t *testing.T) {
	st, _, _ := newStore(t)
	for _, r := range []*model.VerifyRecord{
		rec("../x", "test", "j", epoch, model.OutcomePass, "t"),
		rec("abc", "a/b", "j", epoch, model.OutcomePass, "t"),
		rec("abc", "..", "j", epoch, model.OutcomePass, "t"),
		rec("abc", "test", "", epoch, model.OutcomePass, "t"),
		rec("abc", "test", "a\\b", epoch, model.OutcomePass, "t"),
		rec("", "test", "j", epoch, model.OutcomePass, "t"),
	} {
		if err := st.WriteRecord(r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if _, err := st.Latest("../x", "n"); err == nil {
		t.Error("Latest accepted ../x")
	}
	if _, err := st.Latest("abc", "n/../.."); err == nil {
		t.Error("Latest accepted traversal in name")
	}
	if err := st.WriteJob(&model.JobStatus{JobID: "j", Name: "../n", RepoInst: "abc"}); err == nil {
		t.Error("WriteJob accepted traversal")
	}
}

func TestWriteRecordAtomicOnFailure(t *testing.T) {
	_, clock, sb := newStore(t)
	boom := errors.New("disk full")
	opt := sb.options(t, clock, &sys.SeqID{})
	opt.FS = sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"Rename": ""}, Err: boom}
	st := NewStore(opt)
	r := rec("abcd", "test", "j1", epoch, model.OutcomePass, "t")
	if err := st.WriteRecord(r); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	ents, _ := os.ReadDir(filepath.Dir(st.RecordPath(r)))
	if len(ents) != 0 {
		t.Errorf("directory not clean: %v", ents)
	}
	if got, _ := st.Latest("abcd", "test"); got != nil {
		t.Errorf("Latest = %+v", got)
	}
}

func TestLatestSelection(t *testing.T) {
	st, _, _ := newStore(t)
	// Written out of order; t_start decides, not the write order or outcome.
	for i, o := range []struct {
		off     time.Duration
		outcome model.Outcome
	}{{5, model.OutcomePass}, {1, model.OutcomeFail}, {9, model.OutcomeFail}, {7, model.OutcomePass}} {
		r := rec("inst", "test", "job"+strconv.Itoa(i), epoch.Add(o.off*time.Second), o.outcome, "t")
		if err := st.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Latest("inst", "test")
	if err != nil || got == nil || got.JobID != "job2" {
		t.Fatalf("Latest = %+v, %v; want job2", got, err)
	}
	for _, k := range [][2]string{{"inst", "lint"}, {"other", "test"}} {
		if got, err := st.Latest(k[0], k[1]); got != nil || err != nil {
			t.Errorf("Latest(%v) = %+v, %v", k, got, err)
		}
	}
	// Same t_start, different job: the job id is the tiebreak, deterministic.
	_ = st.WriteRecord(rec("inst", "test", "zzz", epoch.Add(9*time.Second), model.OutcomePass, "t"))
	if got, _ := st.Latest("inst", "test"); got.JobID != "zzz" {
		t.Errorf("tie: %s", got.JobID)
	}
}

func TestLatestSkipsTempAndForeignFiles(t *testing.T) {
	st, _, _ := newStore(t)
	r := rec("inst", "test", "j1", epoch, model.OutcomePass, "t")
	if err := st.WriteRecord(r); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(st.RecordPath(r))
	zzz := fmt.Sprintf("%020d-zzz.json", epoch.Add(time.Hour).UnixNano())
	writeFile(t, filepath.Join(dir, zzz+".tmp.123.abcd"), "partial")
	writeFile(t, filepath.Join(dir, "README"), "junk")
	if err := os.MkdirAll(filepath.Join(dir, "zzzdir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := st.Latest("inst", "test")
	if err != nil || got == nil || got.JobID != "j1" {
		t.Fatalf("Latest = %+v, %v", got, err)
	}
}

func TestLatestCorrupt(t *testing.T) {
	st, _, _ := newStore(t)
	r := rec("inst", "test", "j1", epoch, model.OutcomePass, "t")
	_ = st.WriteRecord(r)
	dir := filepath.Dir(st.RecordPath(r))
	writeFile(t, filepath.Join(dir, fmt.Sprintf("%020d-bad.json", epoch.Add(time.Second).UnixNano())), "{not json")
	got, err := st.Latest("inst", "test")
	if got != nil || !errors.Is(err, errCorrupt) {
		t.Fatalf("Latest = %+v, %v; want corrupt error", got, err)
	}
}

func TestFinishedSince(t *testing.T) {
	st, _, _ := newStore(t)
	mk := func(name, job string, startOff, durS int, o model.Outcome) {
		r := rec("inst", name, job, epoch.Add(time.Duration(startOff)*time.Second), o, "t")
		r.TEnd = r.TStart.Add(time.Duration(durS) * time.Second)
		if err := st.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	mk("test", "a", 0, 10, model.OutcomePass)   // ends +10
	mk("test", "b", 20, 10, model.OutcomeFail)  // ends +30
	mk("lint", "c", 5, 60, model.OutcomeFail)   // ends +65, started before `since`
	mk("lint", "d", 100, 5, model.OutcomeInfra) // ends +105
	_ = st.WriteRecord(rec("elsewhere", "test", "e", epoch.Add(50*time.Second), model.OutcomeFail, "t"))

	ids := func(since time.Duration) string {
		recs, err := st.FinishedSince("inst", epoch.Add(since))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range recs {
			out = append(out, r.JobID)
		}
		return strings.Join(out, ",")
	}
	for since, want := range map[time.Duration]string{
		-time.Second:      "a,b,c,d",
		9 * time.Second:   "a,b,c,d",
		10 * time.Second:  "b,c,d", // t_end > since is strict: a ends exactly at +10
		30 * time.Second:  "c,d",
		64 * time.Second:  "c,d",
		65 * time.Second:  "d",
		200 * time.Second: "",
	} {
		if got := ids(since); got != want {
			t.Errorf("FinishedSince(+%v) = %q, want %q", since, got, want)
		}
	}
	if recs, err := st.FinishedSince("nobody", epoch); err != nil || recs != nil {
		t.Errorf("unknown repo: %v, %v", recs, err)
	}
	// A record that started so long ago it cannot have finished after `since`
	// is skipped without being read: corrupting it must not matter.
	old := rec("inst", "test", "old", epoch.Add(-72*time.Hour), model.OutcomeFail, "t")
	_ = st.WriteRecord(old)
	writeFile(t, st.RecordPath(old), "{corrupt")
	if got := ids(0); got != "b,c,d" && got != "a,b,c,d" {
		t.Errorf("with old corrupt record: %q", got)
	}
}

func TestJobsRoundTripAndFind(t *testing.T) {
	st, clock, _ := newStore(t)
	now := clock.Now()
	js := &model.JobStatus{JobID: "20261016T120000-aaaaaa", Name: "test", RepoInst: "inst1", State: model.JobRunning, PID: os.Getpid(), StartedAt: now, Deadline: now.Add(time.Hour)}
	if err := st.WriteJob(js); err != nil {
		t.Fatal(err)
	}
	js2 := &model.JobStatus{JobID: "20261016T120001-bbbbbb", Name: "lint", RepoInst: "inst2", State: model.JobDone, Outcome: model.OutcomePass, StartedAt: now, Deadline: now.Add(time.Hour)}
	if err := st.WriteJob(js2); err != nil {
		t.Fatal(err)
	}
	r := rec("inst2", "lint", js2.JobID, now, model.OutcomePass, "tt")
	if err := st.WriteRecord(r); err != nil {
		t.Fatal(err)
	}

	got, gr, err := st.FindJob(js.JobID)
	if err != nil || got == nil || got.PID != os.Getpid() || got.V != 1 || gr != nil {
		t.Fatalf("running job: %+v, %+v, %v", got, gr, err)
	}
	got, gr, err = st.FindJob(js2.JobID)
	if err != nil || got == nil || got.State != model.JobDone || gr == nil || gr.SEnd != "tt" {
		t.Fatalf("finished job: %+v, %+v, %v", got, gr, err)
	}
	// Record without a job file (job files already collected).
	orphan := rec("inst3", "test", "20261016T130000-cccccc", now, model.OutcomeFail, "t")
	_ = st.WriteRecord(orphan)
	got, gr, err = st.FindJob(orphan.JobID)
	if err != nil || got != nil || gr == nil || gr.Outcome != model.OutcomeFail {
		t.Fatalf("orphan record: %+v, %+v, %v", got, gr, err)
	}
	got, gr, err = st.FindJob("20261016T999999-zzzzzz")
	if err != nil || got != nil || gr != nil {
		t.Fatalf("unknown: %+v %+v %v", got, gr, err)
	}
	if _, _, err := st.FindJob("../x"); err == nil {
		t.Error("traversal accepted")
	}
	if _, _, err := st.FindJob(""); err == nil {
		t.Error("empty id accepted")
	}
	if j, err := st.ReadJob("inst1", "test", js.JobID); err != nil || j == nil {
		t.Errorf("ReadJob = %+v, %v", j, err)
	}
	if j, err := st.ReadJob("inst1", "test", "nope"); err != nil || j != nil {
		t.Errorf("ReadJob missing = %+v, %v", j, err)
	}
}

func TestRunningJobs(t *testing.T) {
	st, clock, _ := newStore(t)
	now := clock.Now()
	dead := deadPID(t)
	id := func(off time.Duration, suffix string) string {
		return now.Add(off).UTC().Format("20060102T150405") + "-" + suffix
	}
	write := func(js model.JobStatus) {
		t.Helper()
		js.RepoInst, js.Name = "inst", "test"
		if err := st.WriteJob(&js); err != nil {
			t.Fatal(err)
		}
	}
	write(model.JobStatus{JobID: id(-time.Minute, "alive0"), State: model.JobRunning, PID: os.Getpid(), StartedAt: now.Add(-time.Minute), Deadline: now.Add(time.Hour)})
	write(model.JobStatus{JobID: id(-time.Minute, "dead00"), State: model.JobRunning, PID: dead, StartedAt: now.Add(-time.Minute), Deadline: now.Add(time.Hour)})
	write(model.JobStatus{JobID: id(-2*time.Hour, "late00"), State: model.JobRunning, PID: os.Getpid(), StartedAt: now.Add(-2 * time.Hour), Deadline: now.Add(-time.Hour)})
	write(model.JobStatus{JobID: id(-time.Minute, "done00"), State: model.JobDone, Outcome: model.OutcomePass, PID: os.Getpid(), StartedAt: now.Add(-time.Minute), Deadline: now.Add(time.Hour)})
	write(model.JobStatus{JobID: id(-time.Second, "nopid0"), State: model.JobRunning, StartedAt: now.Add(-time.Second), Deadline: now.Add(time.Hour)})
	write(model.JobStatus{JobID: id(-time.Hour, "nopid1"), State: model.JobRunning, StartedAt: now.Add(-time.Hour), Deadline: now.Add(time.Hour)})
	write(model.JobStatus{JobID: id(-time.Minute, "nodead"), State: model.JobRunning, PID: os.Getpid(), StartedAt: now.Add(-time.Minute)}) // zero deadline

	// Foreign files in the directory are ignored.
	dir := filepath.Dir(st.JobPath("inst", "test", "x"))
	writeFile(t, filepath.Join(dir, id(0, "spec00")+".spec.json"), `{"toplevel":"/x"}`)
	writeFile(t, filepath.Join(dir, id(0, "log000")+".log"), "output")
	writeFile(t, filepath.Join(dir, id(0, "junk00")+".json"), "{broken")

	jobs, err := st.RunningJobs("inst", "test")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, j := range jobs {
		ids = append(ids, j.JobID[16:])
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "alive0,nopid0" {
		t.Errorf("running = %v", ids)
	}
	if jobs, err := st.RunningJobs("inst", "lint"); err != nil || len(jobs) != 0 {
		t.Errorf("other name: %v, %v", jobs, err)
	}
	if jobs, err := st.RunningJobs("nope", "test"); err != nil || len(jobs) != 0 {
		t.Errorf("other repo: %v, %v", jobs, err)
	}

	// A little later the PID-less job is past its start grace.
	clock.Advance(JobStartGrace + time.Second)
	jobs, _ = st.RunningJobs("inst", "test")
	if len(jobs) != 1 || !strings.HasSuffix(jobs[0].JobID, "alive0") {
		t.Errorf("after grace: %+v", jobs)
	}
	if st.JobActive(nil) {
		t.Error("JobActive(nil)")
	}
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t testing.TB) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Error("own pid not alive")
	}
	if ProcessAlive(deadPID(t)) {
		t.Error("exited process alive")
	}
	for _, pid := range []int{0, -1} {
		if ProcessAlive(pid) {
			t.Errorf("pid %d alive", pid)
		}
	}
}

// RunningJobsWithin reads only job files younger than the entry's timeout
// plus slack, judged by the id's time prefix.
func TestRunningJobsWithin(t *testing.T) {
	st, clock, _ := newStore(t)
	now := clock.Now()
	write := func(age time.Duration, suffix string) {
		t.Helper()
		js := model.JobStatus{
			JobID: now.Add(-age).UTC().Format("20060102T150405") + "-" + suffix, Name: "test", RepoInst: "inst",
			State: model.JobRunning, PID: os.Getpid(), StartedAt: now.Add(-age), Deadline: now.Add(time.Hour),
		}
		if err := st.WriteJob(&js); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Minute, "young0")
	write(25*time.Minute, "mid000")
	write(2*time.Hour, "old000")
	ids := func(timeoutS int) string {
		jobs, err := st.RunningJobsWithin("inst", "test", timeoutS)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, j := range jobs {
			out = append(out, j.JobID[16:])
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		timeoutS int
		want     string
	}{
		{0, "mid000,young0"}, // default 1800s + slack: about 41 minutes
		{60, "young0"},       // 1 minute + slack + grace: about 11 minutes
		{3 * 3600, "mid000,old000,young0"},
		{90 * 3600, "mid000,old000,young0"}, // capped at the 25h bound
	} {
		if got := ids(tc.timeoutS); got != tc.want {
			t.Errorf("timeout_s=%d: running = %q, want %q", tc.timeoutS, got, tc.want)
		}
	}
}

func TestGC(t *testing.T) {
	st, clock, _ := newStore(t)
	now := clock.Now()
	day := 24 * time.Hour
	paths := map[string]string{}
	put := func(label, name, job string, age time.Duration) {
		r := rec("inst", name, job, now.Add(-age), model.OutcomePass, "t")
		if err := st.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
		paths[label] = st.RecordPath(r)
	}
	put("test-old", "test", "o1", 40*day)
	put("test-older", "test", "o2", 35*day)
	put("test-mid", "test", "m1", 20*day)
	put("test-new", "test", "n1", day)
	put("lint-only", "lint", "l1", 90*day) // latest of its name: kept although ancient
	put("fmt-old", "fmt", "f1", 50*day)    // latest of its name
	put("fmt-older", "fmt", "f2", 60*day)

	jid := func(age time.Duration, s string) string {
		return now.Add(-age).UTC().Format("20060102T150405") + "-" + s
	}
	job := func(label string, js model.JobStatus, withLog bool) {
		js.RepoInst, js.Name = "inst", "test"
		if err := st.WriteJob(&js); err != nil {
			t.Fatal(err)
		}
		paths[label] = st.JobPath("inst", "test", js.JobID)
		writeFile(t, st.SpecPath("inst", "test", js.JobID), "{}")
		paths[label+"/spec"] = st.SpecPath("inst", "test", js.JobID)
		writeFile(t, st.SupersededPath("inst", "test", js.JobID), "")
		paths[label+"/superseded"] = st.SupersededPath("inst", "test", js.JobID)
		writeFile(t, st.LockPath("inst", "test", js.JobID), "")
		paths[label+"/lock"] = st.LockPath("inst", "test", js.JobID)
		if withLog {
			writeFile(t, st.LogPath("inst", "test", js.JobID), "log")
			paths[label+"/log"] = st.LogPath("inst", "test", js.JobID)
		}
	}
	job("done-old", model.JobStatus{JobID: jid(8*day, "d00001"), State: model.JobDone, Outcome: model.OutcomePass, StartedAt: now.Add(-8 * day), Deadline: now.Add(-8*day + time.Hour)}, true)
	job("done-new", model.JobStatus{JobID: jid(day, "d00002"), State: model.JobDone, Outcome: model.OutcomeFail, StartedAt: now.Add(-day), Deadline: now.Add(-day + time.Hour)}, true)
	job("running-live", model.JobStatus{JobID: jid(time.Minute, "r00001"), State: model.JobRunning, PID: os.Getpid(), StartedAt: now.Add(-time.Minute), Deadline: now.Add(time.Hour)}, true)
	job("running-dead", model.JobStatus{JobID: jid(time.Hour, "r00002"), State: model.JobRunning, PID: deadPID(t), StartedAt: now.Add(-time.Hour), Deadline: now.Add(time.Hour)}, true)
	job("running-ancient", model.JobStatus{JobID: jid(10*day, "r00003"), State: model.JobRunning, PID: os.Getpid(), StartedAt: now.Add(-10 * day), Deadline: now.Add(-10*day + time.Hour)}, false)
	orphanLog := st.LogPath("inst", "test", jid(9*day, "o00001")) // job file long gone
	writeFile(t, orphanLog, "orphan")
	orphanLock := st.LockPath("inst", "test", jid(9*day, "o00002"))
	writeFile(t, orphanLock, "")

	if err := st.GC(); err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for label, want := range map[string]bool{
		"test-old": false, "test-older": false, "test-mid": true, "test-new": true,
		"lint-only": true, "fmt-old": true, "fmt-older": false,
		"done-old": false, "done-old/spec": false, "done-old/log": false, "done-old/superseded": false, "done-old/lock": false,
		"done-new": true, "done-new/spec": true, "done-new/log": true, "done-new/superseded": true, "done-new/lock": true,
		"running-live": true, "running-live/log": true,
		"running-dead": true, "running-dead/log": true,
		"running-ancient": false, "running-ancient/spec": false, "running-ancient/superseded": false, "running-ancient/lock": false,
	} {
		if exists(paths[label]) != want {
			t.Errorf("%s exists = %v, want %v", label, !want, want)
		}
	}
	if exists(orphanLog) || exists(orphanLock) {
		t.Error("orphan log or lock kept")
	}
	js, err := st.ReadJob("inst", "test", jid(time.Hour, "r00002"))
	if err != nil || js == nil || js.State != model.JobDone || js.Outcome != model.OutcomeInfra {
		t.Errorf("dead job after GC: %+v, %v", js, err)
	}
	if js, _ := st.ReadJob("inst", "test", jid(time.Minute, "r00001")); js == nil || js.State != model.JobRunning {
		t.Errorf("live job touched: %+v", js)
	}
	if err := st.GC(); err != nil { // idempotent
		t.Fatal(err)
	}

	// Once every file of a repo is collected the empty directories go too.
	clock.Advance(100 * day)
	if err := st.GC(); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(st.opt.Layout.JobsDir, "inst")) {
		t.Error("empty jobs directory kept")
	}
	if !exists(paths["test-new"]) {
		t.Error("latest record of a name must survive any age")
	}
}

func TestGCEmptyAndMissing(t *testing.T) {
	st, _, _ := newStore(t)
	if err := st.GC(); err != nil {
		t.Fatalf("GC on a fresh home: %v", err)
	}
}

func TestStoreConcurrency(t *testing.T) {
	st, _, _ := newStore(t)
	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			// Readers never see a partial record.
			if _, err := st.Latest("inst", "test"); err != nil {
				done <- err
				return
			}
			if _, err := st.FinishedSince("inst", epoch.Add(-time.Hour)); err != nil {
				done <- err
				return
			}
		}
	}()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				r := rec("inst", "test", fmt.Sprintf("job-%d-%d", w, i), epoch.Add(time.Duration(w*perWriter+i)*time.Millisecond), model.OutcomePass, "t")
				if err := st.WriteRecord(r); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("reader saw: %v", err)
	}
	recs, err := st.FinishedSince("inst", epoch.Add(-time.Hour))
	if err != nil || len(recs) != writers*perWriter {
		t.Fatalf("records = %d, %v; want %d", len(recs), err, writers*perWriter)
	}
	got, _ := st.Latest("inst", "test")
	if got == nil || !got.TStart.Equal(epoch.Add(time.Duration(writers*perWriter-1)*time.Millisecond)) {
		t.Errorf("latest = %+v", got)
	}
}

// TestMain re-executes the test binary as a record writer for the
// multi-process test.
func TestMain(m *testing.M) {
	if dir := os.Getenv("BALLAST_TEST_EVIDENCE_WRITER"); dir != "" {
		os.Exit(writerMain(dir))
	}
	os.Exit(m.Run())
}

func writerMain(home string) int {
	id := os.Getenv("BALLAST_TEST_WRITER_ID")
	n, _ := strconv.Atoi(os.Getenv("BALLAST_TEST_WRITER_N"))
	opt := Options{FS: sys.OSFS{}, Clock: sys.RealClock{}, IDs: sys.RandomID{}}
	opt.Layout.EvidenceDir = filepath.Join(home, "evidence")
	st := NewStore(opt)
	for i := 0; i < n; i++ {
		r := rec("inst", "test", fmt.Sprintf("p%s-%d", id, i), epoch.Add(time.Duration(i)*time.Second), model.OutcomePass, "t")
		if err := st.WriteRecord(r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if _, err := st.Latest("inst", "test"); err != nil {
			fmt.Fprintln(os.Stderr, "reader:", err)
			return 1
		}
	}
	return 0
}

func TestStoreMultiProcess(t *testing.T) {
	home := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const procs, n = 6, 20
	var wg sync.WaitGroup
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			cmd := exec.Command(exe, "-test.run=^$")
			cmd.Env = append(os.Environ(),
				"BALLAST_TEST_EVIDENCE_WRITER="+home, "BALLAST_TEST_WRITER_ID="+strconv.Itoa(p), "BALLAST_TEST_WRITER_N="+strconv.Itoa(n))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("writer %d: %v\n%s", p, err, out)
			}
		}(p)
	}
	wg.Wait()
	opt := Options{FS: sys.OSFS{}}
	opt.Layout.EvidenceDir = filepath.Join(home, "evidence")
	st := NewStore(opt)
	recs, err := st.FinishedSince("inst", epoch.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.JobID] = true
	}
	if len(seen) != procs*n {
		t.Errorf("distinct intact records = %d, want %d", len(seen), procs*n)
	}
	ents, _ := os.ReadDir(filepath.Join(home, "evidence", "inst", "test"))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestMissingDirsAreNotErrors(t *testing.T) {
	st, _, _ := newStore(t)
	if r, err := st.Latest("a", "b"); r != nil || err != nil {
		t.Errorf("Latest: %v %v", r, err)
	}
	if r, err := st.RunningJobs("a", "b"); r != nil || err != nil {
		t.Errorf("RunningJobs: %v %v", r, err)
	}
	if _, _, err := st.FindJob("20261016T120000-aaaaaa"); err != nil {
		t.Errorf("FindJob: %v", err)
	}
}
