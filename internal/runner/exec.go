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
	"github.com/libingtong/ballast/internal/sandbox"
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
	// The lock comes before the pid: whoever sees the pid in the job file
	// can ask the lock whether it still belongs to this runner.
	defer o.holdJobLock(js)()
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
		Sandbox: sandboxNone, Origin: side.origin(), Workspace: workspaceOf(side.origin()),
		SessionID: side.SessionID, AgentID: side.AgentID, LogPath: logPath,
	}
	// Frozen wrap-up order (docs/p2-design.md §3.4 "收尾顺序"): execute
	// returns after the end snapshot; the record is written (fsync) and
	// audited here; the private tree is removed last.
	cleanup := o.execute(js, side, rec, logPath, tee)
	defer func() {
		o.tr("cleanup")
		cleanup()
	}()

	// A job superseded meanwhile leaves its record to the superseder.
	if o.isSuperseded(js) {
		return o.recordOf(js), nil
	}
	if err := o.Evidence.WriteRecord(rec); err != nil {
		js.State, js.Outcome = model.JobDone, model.OutcomeInfra
		_ = o.Evidence.WriteJob(js)
		return rec, fmt.Errorf("%w: write record: %w", ErrInfra, err)
	}
	o.tr("record")
	o.audit(rec)
	o.tr("audit")
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

// launch is one command invocation prepared by execute.
type launch struct {
	argv       []string // the program and its arguments (wrapped when sandboxed)
	dir        string
	env        []string
	newSession bool // Setsid instead of Setpgid (sandboxed commands, §3.4 "终端")
	killAfter  bool // darwin: SIGKILL the session / process group after the leader exited
}

// trimRunner strips the "runner: " prefix the sentinel errors carry:
// VerifyRecord.Error is the text after it.
func trimRunner(err error) string {
	return strings.TrimPrefix(err.Error(), "runner: ")
}

// relTo renders p relative to the first of roots that contains it.
func relTo(roots []string, p string) string {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(p)
}

// execute fills rec: S_start, the command, S_end, outcome. It returns the
// function that removes what the job created (the private tree, the temp
// dir); runExisting calls it after the record and the audit line are
// written.
//
// Policy (docs/p2-design.md §3.4 table): origin=hook always needs the
// sandbox and runs in a materialized private copy; origin=cli follows the
// runner_sandbox policy and runs in the work tree. VerifyRecord.Tier is T1
// iff the runner's own judgement is T1 and the command ran sandboxed.
func (o Options) execute(js *model.JobStatus, side jobSpec, rec *model.VerifyRecord, logPath string, tee io.Writer) (cleanup func()) {
	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	var (
		j        tierJudgement
		kind     = sandbox.KindNone
		decided  bool // kind came from sandbox.Decide
		launched bool // the test command was started
	)
	defer func() { o.finalizeTier(rec, j, kind, decided, launched) }()
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
	cleanups = append(cleanups, func() { _ = logf.Close() })
	origin := side.origin()
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

	// The runner's own tier judgement; hook-passed information is never used.
	j = o.judgeTier(repo, side)
	if len(j.weakened) > 0 {
		o.auditDiag(rec, model.DiagTierMismatch, map[string]any{"entries": j.weakened})
	}
	policy := j.policy()
	var avail sandbox.Kind
	var availErr error
	if origin != model.OriginCLI || policy != model.SandboxOff {
		avail, availErr = o.sbx().Probe(o.sandboxOptions())
	}
	kind, err = sandbox.Decide(policy, origin, avail, availErr)
	if err != nil {
		kind = sandbox.KindNone
		fmt.Fprintf(logf, "# ballast: no usable sandbox: %v\n", availErr)
		infra(errors.New("sandbox_unavailable"))
		o.auditDiag(rec, model.DiagSandboxUnavailable, map[string]any{"origin": string(origin)})
		fmt.Fprintf(logf, "# ballast outcome=%s exit=- signal= s_start= s_end=\n", rec.Outcome)
		return
	}
	decided = true
	rec.Sandbox = string(kind)

	snap := snapshot.Options{FS: o.FS, Clock: o.Clock, AllowFallback: true, Env: hostEnv{o.Env}, Git: o.gitRunner()}
	childEnv := append(o.Env.Environ(), side.Spec.Env...)
	var (
		runRoot string // root of the tree the command runs in
		tree    Tree
		spec    sandbox.Spec
	)
	fail := func(err error) {
		infra(err)
		fmt.Fprintf(logf, "# ballast outcome=%s exit=- signal= s_start=%s s_end=\n", rec.Outcome, rec.SStart)
	}
	if origin == model.OriginHook {
		pin := PinnedEntry{Spec: side.Spec, Exempt: side.Exempt, ProjectJSONHash: side.ProjectJSONHash}
		tree, err = Materialize(o, *repo, js.JobID, pin)
		if err != nil {
			fail(errors.New(trimRunner(err)))
			return
		}
		cleanups = append(cleanups, func() {
			if err := CleanupTree(o, tree); err != nil {
				fmt.Fprintf(logf, "# ballast: remove private tree: %v\n", err)
			}
		})
		rec.SStart, rec.InputsHash = tree.SStart, tree.InputsHash
		runRoot = tree.Dir
	} else {
		var gitDirs []string
		if kind != sandbox.KindNone {
			gd, cd, gerr := o.sbx().ResolveGitDirs(o.FS, repo.Toplevel)
			if gerr != nil {
				fail(fmt.Errorf("sandbox_spec_invalid: %v", strings.TrimPrefix(gerr.Error(), "sandbox_spec_invalid: ")))
				return
			}
			gitDirs = dedupeStrings([]string{gd, cd})
		}
		if rec.SStart, err = snapshot.TreeS(snap, repo, filter); err != nil {
			fail(fmt.Errorf("snapshot (start): %w", err))
			return
		}
		runRoot = repo.Toplevel
		spec.GitDirs = gitDirs
	}
	runDir := jobCwd(runRoot, side.Spec)

	// Wrap.
	ln := launch{dir: runDir, env: childEnv}
	path, perr := lookPath(side.Spec.Argv[0], runDir, childEnv)
	if perr != nil {
		// Reported like a command that cannot be started.
		launched = false
		o.finishUnlaunched(js, side, rec, logf, repo, &tree, filter, origin, snap, perr)
		return
	}
	argv := append([]string{path}, side.Spec.Argv[1:]...)
	var wrapped sandbox.Wrapped
	var roots []string
	if kind != sandbox.KindNone {
		// origin=hook: the temp dir lives next to the private tree under
		// ~/.ballast/work/<job>/ (the agent's own sandbox cannot write there);
		// the inherited $TMPDIR comes from the agent's client and its directory
		// may be writable by the agent, so it is never the base (trust-model 2.3).
		tmpBase := ""
		if origin == model.OriginHook {
			tmpBase = tree.Root
		}
		tmp, terr := o.newJobTmp(tmpBase)
		if terr != nil {
			fail(fmt.Errorf("sandbox temp dir: %w", terr))
			return
		}
		cleanups = append(cleanups, func() { _ = o.FS.RemoveAll(tmp) })
		spec.TmpDir, spec.Network, spec.Home, spec.Cwd = tmp, j.info.Network, o.homeDir(), runDir
		spec.LoaderEnv = loaderEnv(childEnv)
		allow := j.info.RunnerAllowWrite
		if origin == model.OriginHook {
			spec.Tree, spec.WorkRoot = tree.Dir, filepath.Dir(tree.Root)
			spec.AllowWrite = append([]string{tree.Dir}, allow...)
			spec.RepoRoots = []string{tree.Dir}
			o.protectedSpec(&spec, j.info, "")
			roots = []string{tree.Dir}
		} else {
			spec.AllowWrite = []string{repo.Toplevel}
			for _, g := range spec.GitDirs {
				if !withinDir(g, repo.Toplevel) {
					spec.AllowWrite = append(spec.AllowWrite, g)
				}
			}
			spec.AllowWrite = append(spec.AllowWrite, allow...)
			spec.RepoRoots = []string{repo.Toplevel}
			o.protectedSpec(&spec, j.info, repo.Toplevel)
			roots = append([]string{repo.Toplevel}, spec.GitDirs...)
		}
		// Quarantine may also move an explicit protected path out of an
		// administrator's runner_allow_write root (sandbox.Quarantine refuses
		// paths outside the roots it is given).
		roots = append(roots, allow...)
		wrapped, err = o.sbx().Wrap(o.sandboxOptions(), kind, spec, argv)
		if err != nil {
			switch {
			case errors.Is(err, sandbox.ErrUnavailable):
				kind = sandbox.KindNone
				rec.Sandbox = sandboxNone
				decided = false
				o.auditDiag(rec, model.DiagSandboxUnavailable, map[string]any{"origin": string(origin)})
				fail(errors.New("sandbox_unavailable"))
			case errors.Is(err, sandbox.ErrInvalidSpec):
				fail(errors.New(err.Error()))
			default:
				fail(fmt.Errorf("sandbox_wrap_failed: %v", err))
			}
			return
		}
		ln.argv = wrapped.Argv
		ln.env = applyEnvChanges(stripLoaderEnv(childEnv), wrapped.Env)
		ln.newSession = wrapped.NewSession
		ln.killAfter = kind == sandbox.KindSandboxExec
	} else {
		ln.argv = argv
	}

	// A newer run may have replaced this job while its tree was being
	// materialized: do not start the command (the caller discards the record).
	if o.isSuperseded(js) {
		rec.TEnd = o.Clock.Now()
		rec.Outcome, rec.Signal = model.OutcomeSignal, "SIGTERM"
		return
	}
	launched = true
	outcome, exit, sig, runErr := o.runCommand(js, side, ln, logf, tee)
	rec.TEnd = o.Clock.Now()
	rec.Outcome, rec.Signal = outcome, sig
	if exit != nil {
		rec.ExitCode = exit
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}

	// PostCheck, then Quarantine (nothing sandboxed is left running here).
	if kind != sandbox.KindNone {
		o.tr("postcheck")
		found, perr := o.sbx().PostCheck(o.FS, spec, wrapped)
		switch {
		case len(found) > 0:
			o.tr("quarantine")
			ents := o.sbx().Quarantine(o.FS, found, roots, filepath.Join(o.Layout.QuarantineDir, js.JobID))
			rec.Outcome, rec.ExitCode, rec.Signal = model.OutcomeInfra, nil, ""
			rec.Error = "protected_path_created"
			rels := make([]string, 0, len(found))
			for _, p := range found {
				rels = append(rels, relTo(roots, p))
			}
			for _, e := range ents {
				if e.Err != "" {
					rec.Error += "; protected_path_residue"
					break
				}
			}
			o.auditDiag(rec, model.DiagSandboxQuarantine, map[string]any{"paths": rels})
		case perr != nil:
			rec.Outcome, rec.ExitCode, rec.Signal = model.OutcomeInfra, nil, ""
			rec.Error = fmt.Sprintf("sandbox_postcheck_failed: %v", perr)
		}
	}

	// S_end: on the copy for origin=hook, on the work tree for origin=cli.
	o.tr("tree_send")
	var sEnd model.TreeID
	if origin == model.OriginHook {
		sEnd, err = TreeSEnd(o, *repo, tree, side.Exempt)
	} else {
		sEnd, err = snapshot.TreeS(snap, repo, filter)
	}
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
	return
}

// finishUnlaunched ends a job whose command could not even be started
// (executable not found): outcome infra, end snapshot like any other run.
func (o Options) finishUnlaunched(js *model.JobStatus, side jobSpec, rec *model.VerifyRecord, logf io.Writer, repo *model.RepoInfo, tree *Tree, filter *snapshot.Filter, origin model.VerifyOrigin, snap snapshot.Options, cause error) {
	rec.TEnd = o.Clock.Now()
	rec.Outcome, rec.Error = model.OutcomeInfra, cause.Error()
	var sEnd model.TreeID
	var err error
	if origin == model.OriginHook {
		sEnd, err = TreeSEnd(o, *repo, *tree, side.Exempt)
	} else {
		sEnd, err = snapshot.TreeS(snap, repo, filter)
	}
	if err == nil {
		rec.SEnd = sEnd
	}
	fmt.Fprintf(logf, "# ballast outcome=%s exit=- signal= s_start=%s s_end=%s\n", rec.Outcome, rec.SStart, rec.SEnd)
}

// finalizeTier sets rec.Tier and rec.TierMissing (docs/p2-design.md §3.4):
// T1 iff the runner's own judgement is T1 and the command ran sandboxed;
// otherwise T0 with the reasons that apply on a host that declares T1.
func (o Options) finalizeTier(rec *model.VerifyRecord, j tierJudgement, kind sandbox.Kind, decided, launched bool) {
	rec.Sandbox = string(kind)
	if j.t1 && kind != sandbox.KindNone && launched {
		rec.Tier, rec.TierMissing = model.TierT1, nil
		return
	}
	rec.Tier = model.TierT0
	var missing []string
	if j.info.Tier != model.TierT1 && j.info.Declared == model.TierT1 {
		missing = append(missing, j.info.Missing...)
	}
	if len(j.weakened) > 0 {
		missing = append(missing, model.TierMissingLowerSandbox)
	}
	if j.info.Tier == model.TierT1 && decided && kind == sandbox.KindNone {
		missing = append(missing, model.TierMissingRunnerSandbox)
	}
	rec.TierMissing = dedupeStrings(missing)
}

func (o Options) sandboxOptions() sandbox.Options {
	return sandbox.Options{FS: o.FS, Env: o.Env, GOOS: o.Env.GOOS()}
}

func dedupeStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// withinDir reports whether p is dir or lies below it.
func withinDir(p, dir string) bool {
	p, dir = filepath.Clean(p), filepath.Clean(dir)
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// runCommand runs the prepared launch in its own process group (a new
// session when sandboxed) and classifies the end. runErr describes a failure
// to run at all (outcome infra).
func (o Options) runCommand(js *model.JobStatus, side jobSpec, ln launch, logf io.Writer, tee io.Writer) (outcome model.Outcome, exit *int, sig string, runErr error) {
	spec := side.Spec
	cmd := exec.Command(ln.argv[0], ln.argv[1:]...)
	cmd.Dir = ln.dir
	cmd.Env = ln.env
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
	tree.prepare(cmd, ln.newSession)
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

	o.tr("exit")
	if stopping {
		// The leader may be gone while other members of the group ignore
		// SIGTERM (the kill timer never fired): finish them off.
		tree.kill()
	}
	if ln.killAfter {
		// darwin: the sandbox follows every descendant, so the runner
		// ends the child's session / process group before it post-checks
		// and snapshots (docs/p2-design.md §3.4 "收尾顺序").
		tree.kill()
		o.tr("killpg")
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
