package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/snapshot"
	"github.com/libingtong/ballast/internal/version"
)

// Timing knobs (variables so tests can shorten them).
var (
	// killGrace is how long a terminated process group gets before SIGKILL.
	killGrace = 3 * time.Second
	// superviseInterval is how often a running job looks at its own job file
	// for a supersede mark (the signal alone can be lost or, on Windows,
	// does not exist).
	superviseInterval = time.Second
)

// runExisting executes the already created, running job js. tee, when
// non-nil, additionally receives the command output (Foreground).
func (o Options) runExisting(js *model.JobStatus, tee io.Writer) (*model.VerifyRecord, error) {
	side, err := o.readSpec(js)
	if err != nil {
		cause := fmt.Errorf("job spec of %s: %w", js.JobID, err)
		t := &target{}
		o.failJob(js, t, side.Spec, cause)
		return o.recordOf(js), fmt.Errorf("%w: %w", ErrInfra, cause)
	}
	if err := o.markPID(js); err != nil {
		if errors.Is(err, errNotRunning) {
			return o.recordOf(js), nil // superseded before it started
		}
		return nil, fmt.Errorf("%w: %w", ErrInfra, err)
	}
	if o.isSuperseded(js) {
		// markPID may have overwritten the superseder's job file.
		js.State, js.Outcome = model.JobDone, model.OutcomeSuperseded
		_ = o.Evidence.WriteJob(js)
		return o.recordOf(js), nil
	}

	logPath := o.Evidence.LogPath(js.RepoInst, js.Name, js.JobID)
	rec := &model.VerifyRecord{
		V: 1, JobID: js.JobID, Name: js.Name, RepoInst: js.RepoInst,
		ArgvHash: argvHash(side.Spec.Argv), ProjectJSONHash: side.ProjectJSONHash,
		BallastVersion: version.Version, Tier: model.TierT0, TStart: js.StartedAt,
		SessionID: side.SessionID, AgentID: side.AgentID, LogPath: logPath,
	}
	o.execute(js, side, rec, logPath, tee)

	// A job superseded meanwhile leaves its record to the superseder.
	if o.isSuperseded(js) {
		return o.recordOf(js), nil
	}
	if err := o.Evidence.WriteRecord(rec); err != nil {
		js.State, js.Outcome = model.JobDone, model.OutcomeInfra
		_ = o.Evidence.WriteJob(js)
		return rec, fmt.Errorf("%w: write record: %w", ErrInfra, err)
	}
	o.audit(rec)
	js.State, js.Outcome = model.JobDone, rec.Outcome
	if err := o.Evidence.WriteJob(js); err != nil {
		return rec, fmt.Errorf("%w: write job status: %w", ErrInfra, err)
	}
	return rec, nil
}

// recordOf returns the stored record of js, or nil.
func (o Options) recordOf(js *model.JobStatus) *model.VerifyRecord {
	_, rec, _ := o.Evidence.FindJob(js.JobID)
	return rec
}

// execute fills rec: S_start, the command, S_end, outcome.
func (o Options) execute(js *model.JobStatus, side jobSpec, rec *model.VerifyRecord, logPath string, tee io.Writer) {
	infra := func(err error) {
		rec.Outcome, rec.Error = model.OutcomeInfra, err.Error()
		rec.ExitCode, rec.Signal = nil, ""
		rec.TEnd = o.Clock.Now()
	}
	logf, err := o.FS.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		infra(fmt.Errorf("open log: %w", err))
		return
	}
	defer logf.Close()
	fmt.Fprintf(logf, "# ballast verify %s job=%s argv=%q\n", js.Name, js.JobID, side.Spec.Argv)

	repo, err := o.Repo.Locate(side.Toplevel)
	if err != nil {
		infra(fmt.Errorf("locate repository: %w", err))
		return
	}
	filter, err := snapshot.CompileExempt(side.Exempt)
	if err != nil {
		infra(fmt.Errorf("exempt: %w", err))
		return
	}
	snap := snapshot.Options{FS: o.FS, Clock: o.Clock, AllowFallback: true, Env: o.Env}
	if rec.SStart, err = snapshot.TreeS(snap, repo, filter); err != nil {
		infra(fmt.Errorf("snapshot (start): %w", err))
		return
	}

	outcome, exit, sig, runErr := o.runCommand(js, side, logf, tee)
	rec.TEnd = o.Clock.Now()
	rec.Outcome, rec.Signal = outcome, sig
	if exit != nil {
		rec.ExitCode = exit
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}

	sEnd, err := snapshot.TreeS(snap, repo, filter)
	if err != nil {
		rec.SEnd = ""
		if rec.Outcome == model.OutcomePass {
			rec.Outcome, rec.Error = model.OutcomeInfra, fmt.Sprintf("snapshot (end): %v", err)
			rec.ExitCode = nil
		}
	} else {
		rec.SEnd = sEnd
	}
	exitStr := "-"
	if rec.ExitCode != nil {
		exitStr = fmt.Sprint(*rec.ExitCode)
	}
	fmt.Fprintf(logf, "# ballast outcome=%s exit=%s signal=%s s_start=%s s_end=%s\n", rec.Outcome, exitStr, rec.Signal, rec.SStart, rec.SEnd)
}

// runCommand runs the entry's argv in its own process group and classifies
// the end. runErr describes a failure to run at all (outcome infra).
func (o Options) runCommand(js *model.JobStatus, side jobSpec, logf io.Writer, tee io.Writer) (outcome model.Outcome, exit *int, sig string, runErr error) {
	spec := side.Spec
	dir := jobCwd(side.Toplevel, spec)
	env := append(o.Env.Environ(), spec.Env...)
	path, err := lookPath(spec.Argv[0], dir, env)
	if err != nil {
		return model.OutcomeInfra, nil, "", err
	}
	cmd := exec.Command(path, spec.Argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	var out io.Writer = logf
	if tee != nil {
		out = io.MultiWriter(logf, tee)
	}
	if f, ok := logf.(*os.File); ok && tee == nil {
		cmd.Stdout, cmd.Stderr = f, f // no pipe: nothing to wait for after exit
	} else if logf != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	cmd.WaitDelay = 2 * time.Second
	var tree procTree
	tree.prepare(cmd)
	// Subscribe before starting: a TERM arriving in between must stop the
	// command, not orphan it.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return model.OutcomeInfra, nil, "", fmt.Errorf("start %s: %w", spec.Argv[0], err)
	}
	tree.attach(cmd.Process)
	defer tree.close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timeout := time.NewTimer(timeoutOf(spec))
	defer timeout.Stop()
	tick := time.NewTicker(superviseInterval)
	defer tick.Stop()

	var (
		timedOut    bool
		interrupted string
		stopping    bool
		killTimer   <-chan time.Time
	)
	stop := func() {
		if !stopping {
			stopping = true
			tree.terminate()
			killTimer = time.After(killGrace)
		}
	}
	var waitErr error
loop:
	for {
		select {
		case waitErr = <-done:
			break loop
		case <-timeout.C:
			timedOut = true
			stop()
		case s := <-sigs:
			interrupted = sigName(s)
			stop()
		case <-tick.C:
			if o.isSuperseded(js) {
				stop()
			}
		case <-killTimer:
			tree.kill()
		}
	}

	if stopping {
		// The leader may be gone while other members of the group ignore
		// SIGTERM (the kill timer never fired): finish them off.
		tree.kill()
	}

	switch {
	case timedOut:
		return model.OutcomeTimeout, nil, "", nil
	case interrupted != "":
		return model.OutcomeSignal, nil, interrupted, nil
	case stopping:
		return model.OutcomeSignal, nil, "SIGTERM", nil // superseded: the caller discards it
	}
	if waitErr == nil {
		z := 0
		return model.OutcomePass, &z, "", nil
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// The command succeeded but a leftover process still holds its
		// output pipe; the outcome is the command's, as for a background job.
		fmt.Fprintf(logf, "# ballast: command exited 0 but left a process holding its output (%v)\n", waitErr)
		z := 0
		return model.OutcomePass, &z, "", nil
	}
	if s, ok := exitSignal(waitErr); ok {
		return model.OutcomeSignal, nil, s, nil
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		c := ee.ExitCode()
		return model.OutcomeFail, &c, "", nil
	}
	return model.OutcomeInfra, nil, "", waitErr
}

func sigName(s os.Signal) string {
	switch s {
	case syscall.SIGTERM:
		return "SIGTERM"
	case os.Interrupt:
		return "SIGINT"
	}
	return s.String()
}

// lookPath resolves file the way a shell would, but against the PATH of the
// child environment (exec.LookPath consults the runner's own). On Windows the
// PATHEXT extensions are tried as well.
func lookPath(file, dir string, env []string) (string, error) {
	pathVar, pathext := "", ""
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		switch k {
		case "PATH":
			pathVar = v
		case "PATHEXT":
			pathext = v
		}
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		if pathext == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		exts = nil
		if filepath.Ext(file) != "" {
			exts = append(exts, "")
		}
		for _, e := range strings.Split(strings.ToLower(pathext), ";") {
			if e != "" {
				exts = append(exts, e)
			}
		}
	}
	if strings.ContainsAny(file, `/\`) {
		p := file
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		var err error
		for _, e := range exts {
			if err = canExec(p + e); err == nil {
				return p + e, nil
			}
		}
		return "", fmt.Errorf("%s: %w", file, err)
	}
	for _, d := range filepath.SplitList(pathVar) {
		if d == "" {
			d = "."
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(dir, d)
		}
		for _, e := range exts {
			p := filepath.Join(d, file+e)
			if canExec(p) == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s: executable file not found in PATH", file)
}

func canExec(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", p)
	}
	if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s: permission denied", p)
	}
	return nil
}

func stdout() io.Writer { return os.Stdout }
