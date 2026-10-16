package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/libingtong/ballast/internal/evidence"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/runner"
)

var (
	jobIDOnce sync.Once
	jobIDRe   *regexp.Regexp
)

// isJobID reports whether s looks like a job id (UTC timestamp + "-" + hex).
// The regexp is compiled on first use (never at package init).
func isJobID(s string) bool {
	jobIDOnce.Do(func() { jobIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{4,}$`) })
	return jobIDRe.MatchString(s)
}

func runVerify(a *app, c *command, args []string) int {
	if hasHookStart(args) {
		return a.verifyHookStart(args)
	}
	fs := newFlagSet("verify")
	var status, check, commit, worktree, foreground, asJSON bool
	var runJob string
	fs.BoolVar(&status, "status", false, "")
	fs.BoolVar(&check, "check", false, "")
	fs.BoolVar(&commit, "commit", false, "")
	fs.BoolVar(&worktree, "worktree", false, "")
	fs.BoolVar(&foreground, "foreground", false, "")
	fs.StringVar(&runJob, "run-job", "", "")
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	modes := 0
	for _, on := range []bool{status, check, foreground, runJob != ""} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return a.usageErr(c, "--status, --check, --foreground and --run-job are mutually exclusive")
	}
	if (commit || worktree) && !check {
		return a.usageErr(c, "--commit/--worktree only apply to --check")
	}
	if commit && worktree {
		return a.usageErr(c, "--commit and --worktree are mutually exclusive")
	}
	if asJSON && !status {
		return a.usageErr(c, "--json only applies to --status")
	}
	switch {
	case status:
		return a.verifyStatus(c, pos, asJSON)
	case check:
		if len(pos) > 0 {
			return a.usageErr(c, "--check takes no names")
		}
		return a.verifyCheck(worktree)
	case foreground:
		if len(pos) != 1 {
			return a.usageErr(c, "--foreground needs exactly one verify name")
		}
		return a.verifyForeground(pos[0])
	case runJob != "":
		if len(pos) > 0 {
			return a.usageErr(c, "--run-job takes no names")
		}
		return a.verifyRunJob(runJob)
	}
	return a.verifyStart(pos)
}

// hasHookStart reports whether the internal --hook-start flag appears before
// the "--" terminator (names after it can never be flags).
func hasHookStart(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--hook-start" {
			return true
		}
	}
	return false
}

// verifyHookStart implements the internal `verify --hook-start …` child the
// hook spawns when it denies a canonical `ballast verify` under T1
// (docs/p2-design.md §3.3). The frozen argv grammar is parsed by
// model.ParseHookStartArgv; runner.StartRequest does the rest. Exit codes:
// 0 started, 2 configuration error (the request file still carries the
// error, so `verify --status <request_id>` can explain), 5 infrastructure.
func (a *app) verifyHookStart(args []string) int {
	req, err := model.ParseHookStartArgv(append([]string{"verify"}, args...))
	if err != nil {
		return a.errorf(ExitUsage, "verify: %v", err)
	}
	opt, err := a.runnerOptions(nil)
	if err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	started, err := runner.StartRequest(opt, req)
	for _, s := range started {
		fmt.Fprintln(a.out, s.String())
	}
	if err != nil {
		return a.runnerExit(err)
	}
	return ExitOK
}

// runnerOptions builds the runner configuration. Self is the symlink-
// resolved running binary: the detached child must re-execute exactly it.
func (a *app) runnerOptions(out io.Writer) (runner.Options, error) {
	self, err := resolvedSelf()
	if err != nil {
		return runner.Options{}, fmt.Errorf("cannot locate the ballast binary: %w", err)
	}
	return runner.Options{
		Layout:   a.lay,
		FS:       a.fs,
		Clock:    a.clock,
		IDs:      a.ids,
		Env:      a.env,
		Self:     self,
		Evidence: evidence.NewStore(a.evidenceOptions()),
		Audit:    a.auditWriter(),
		Repo:     a.locator(),
		Project:  &project.Loader{FS: a.fs},
		Spawner:  runner.Spawner{},
		Tier:     a.tierProbe(self),
		Output:   out,
	}, nil
}

// runnerExit maps a runner error to the verify exit codes (2 configuration,
// 5 infrastructure).
func (a *app) runnerExit(err error) int {
	code := ExitVerifyInfra
	if errors.Is(err, runner.ErrConfig) {
		code = ExitUsage
	}
	return a.errorf(code, "verify: %v", err)
}

func (a *app) verifyStart(names []string) int {
	opt, err := a.runnerOptions(nil)
	if err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	started, err := runner.Start(opt, a.cwd, names)
	for _, s := range started {
		fmt.Fprintln(a.out, s.String())
	}
	if err != nil {
		return a.runnerExit(err)
	}
	return ExitOK
}

func (a *app) verifyForeground(name string) int {
	opt, err := a.runnerOptions(a.out)
	if err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	rec, err := runner.Foreground(opt, a.cwd, name)
	if err != nil {
		return a.runnerExit(err)
	}
	fmt.Fprintf(a.errw, "ballast verify: %s -> %s\n", name, rec.Outcome)
	return runner.StatusExitCode(rec)
}

func (a *app) verifyRunJob(id string) int {
	opt, err := a.runnerOptions(nil)
	if err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	rec, err := runner.RunJob(opt, id)
	if err != nil {
		return a.runnerExit(err)
	}
	return runner.StatusExitCode(rec)
}

// statusRow is one line of `verify --status`.
type statusRow struct {
	Name      string `json:"name,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	State     string `json:"state"`
	Outcome   string `json:"outcome,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	ValidPass bool   `json:"valid_pass,omitempty"`
	TStart    string `json:"t_start,omitempty"`
	TEnd      string `json:"t_end,omitempty"`
	LogPath   string `json:"log_path,omitempty"`
	Error     string `json:"error,omitempty"`
	code      int
}

func recordRow(rec *model.VerifyRecord) statusRow {
	return statusRow{
		Name: rec.Name, JobID: rec.JobID, State: "done", Outcome: string(rec.Outcome), ExitCode: rec.ExitCode,
		ValidPass: rec.ValidPass(), TStart: rec.TStart.UTC().Format("2006-01-02T15:04:05Z"),
		TEnd: rec.TEnd.UTC().Format("2006-01-02T15:04:05Z"), LogPath: rec.LogPath, Error: rec.Error,
		code: runner.StatusExitCode(rec),
	}
}

// worstStatus folds several per-name exit codes into one: running first,
// then the most severe failure, then superseded / no record, 0 only when
// every name passed.
func worstStatus(codes []int) int {
	order := []int{ExitVerifyRunning, ExitVerifyInfra, ExitVerifySignal, ExitVerifyTimeout, ExitVerifyFail, ExitVerifySuperseded, ExitVerifyNoRecord}
	for _, want := range order {
		for _, c := range codes {
			if c == want {
				return want
			}
		}
	}
	return ExitVerifyPass
}

func (a *app) verifyStatus(c *command, pos []string, asJSON bool) int {
	if len(pos) > 1 {
		return a.usageErr(c, "--status takes at most one job id or name")
	}
	store := evidence.NewStore(a.evidenceOptions())
	var rows []statusRow

	if len(pos) == 1 && isJobID(pos[0]) {
		// The request id of a hook-started verify is also the id of its FIRST
		// job, so for a request with several jobs the plain job lookup would
		// show (and exit with) that one job only; the aggregate must win.
		if code, handled := a.requestStatus(pos[0], asJSON, true); handled {
			return code
		}
		js, rec, err := store.FindJob(pos[0])
		if err != nil {
			return a.errorf(ExitVerifyInfra, "verify: %v", err)
		}
		if js == nil && rec == nil {
			// No job has that id: it may be the request id a hook-started
			// verify announced in its deny reason (P2, architecture §7).
			if code, handled := a.requestStatus(pos[0], asJSON, false); handled {
				return code
			}
		}
		row := statusRow{JobID: pos[0], State: "none", code: ExitVerifyNoRecord}
		switch {
		case js != nil && store.JobActive(js):
			row = statusRow{Name: js.Name, JobID: js.JobID, State: "running", code: ExitVerifyRunning}
		case rec != nil:
			row = recordRow(rec)
		case js != nil && js.State == model.JobRunning:
			row = statusRow{Name: js.Name, JobID: js.JobID, State: "lost", Error: "runner exited without writing a record", code: ExitVerifyInfra}
		case js != nil:
			row = statusRow{Name: js.Name, JobID: js.JobID, State: "done", Outcome: string(js.Outcome), code: ExitVerifyNoRecord}
		}
		rows = append(rows, row)
		return a.printStatus(rows, asJSON)
	}

	repo, err := a.repo()
	if err != nil {
		return a.errorf(ExitInfra, "verify: %v", err)
	}
	if repo == nil {
		return a.errorf(ExitUsage, "verify: not inside a git repository")
	}
	names := pos
	if len(names) == 0 {
		cfg, err := (&project.Loader{FS: a.fs}).LoadProject(repo.Toplevel)
		if err != nil {
			return a.errorf(ExitUsage, "verify: %v", err)
		}
		if cfg == nil {
			return a.errorf(ExitUsage, "verify: no .ballast/project.json in %s (run: ballast init)", repo.Toplevel)
		}
		names = cfg.Required()
		if len(names) == 0 {
			return a.errorf(ExitUsage, "verify: project.json has no required verify entries; give a name")
		}
	}
	for _, name := range names {
		running, err := store.RunningJobs(repo.Inst, name)
		if err != nil {
			return a.errorf(ExitVerifyInfra, "verify: %v", err)
		}
		if len(running) > 0 {
			rows = append(rows, statusRow{Name: name, JobID: running[0].JobID, State: "running", code: ExitVerifyRunning})
			continue
		}
		rec, err := store.Latest(repo.Inst, name)
		if err != nil {
			return a.errorf(ExitVerifyInfra, "verify: %v", err)
		}
		if rec == nil {
			rows = append(rows, statusRow{Name: name, State: "none", code: ExitVerifyNoRecord})
			continue
		}
		rows = append(rows, recordRow(rec))
	}
	return a.printStatus(rows, asJSON)
}

// requestStatus is the `verify --status <request_id>` fallback: when no job
// has the id it reads the request file written by the hook-started verify
// (runner.RequestStatus) and prints its jobs; the exit code is the worst
// outcome (fail > timeout > signal > infra > running > pass). handled=false
// means "no such request" and the caller reports the ordinary no-record
// result (exit 11).
//
// multiOnly restricts the handling to requests with more than one job and
// treats every read problem as "not handled": it is the pre-check that keeps
// a plain `--status <job id>` unchanged for single-job requests and for ids
// that are no request at all.
func (a *app) requestStatus(id string, asJSON bool, multiOnly bool) (code int, handled bool) {
	opt := runner.Options{Layout: a.lay, FS: a.fs, Clock: a.clock, IDs: a.ids, Env: a.env}
	res, err := runner.RequestStatus(opt, "", id)
	if err != nil {
		if multiOnly || errors.Is(err, runner.ErrRequestNotFound) {
			return 0, false
		}
		return a.errorf(ExitVerifyInfra, "verify: %v", err), true
	}
	if multiOnly && len(res.Jobs) < 2 {
		return 0, false
	}
	rows := make([]statusRow, 0, len(res.Jobs))
	for _, j := range res.Jobs {
		switch {
		case j.Record != nil:
			rows = append(rows, recordRow(j.Record))
		case j.Outcome == "running":
			rows = append(rows, statusRow{Name: j.Name, JobID: j.JobID, State: "running", code: ExitVerifyRunning})
		default:
			rows = append(rows, statusRow{Name: j.Name, JobID: j.JobID, State: "lost", Outcome: j.Outcome,
				Error: "no record and no running job (the runner did not start or was lost)", code: ExitVerifyInfra})
		}
	}
	if len(rows) == 0 {
		rows = append(rows, statusRow{State: "failed", Outcome: "infra", Error: res.Error, code: ExitVerifyInfra})
	}
	if res.Error != "" && len(res.Jobs) > 0 {
		rows[0].Error = strings.TrimSpace(rows[0].Error + " request: " + res.Error)
	}
	if asJSON {
		out := map[string]any{"request_id": res.RequestID, "jobs": rows, "exit": res.ExitCode}
		if res.Error != "" {
			out["error"] = res.Error
		}
		if err := a.writeJSON(out); err != nil {
			return a.errorf(ExitInfra, "%v", err), true
		}
		return res.ExitCode, true
	}
	fmt.Fprintf(a.out, "request=%s\n", res.RequestID)
	a.printStatusRows(rows)
	return res.ExitCode, true
}

func (a *app) printStatus(rows []statusRow, asJSON bool) int {
	codes := make([]int, len(rows))
	for i, r := range rows {
		codes[i] = r.code
	}
	code := worstStatus(codes)
	if asJSON {
		if err := a.writeJSON(map[string]any{"jobs": rows, "exit": code}); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
		return code
	}
	a.printStatusRows(rows)
	return code
}

func (a *app) printStatusRows(rows []statusRow) {
	for _, r := range rows {
		var parts []string
		if r.Name != "" {
			parts = append(parts, "name="+r.Name)
		}
		if r.JobID != "" {
			parts = append(parts, "job="+r.JobID)
		}
		parts = append(parts, "state="+r.State)
		if r.Outcome != "" {
			parts = append(parts, "outcome="+r.Outcome)
		}
		if r.ExitCode != nil {
			parts = append(parts, fmt.Sprintf("exit_code=%d", *r.ExitCode))
		}
		if r.Outcome == string(model.OutcomePass) && !r.ValidPass {
			parts = append(parts, "valid_pass=false(tree changed while running)")
		}
		if r.TEnd != "" {
			parts = append(parts, "finished="+r.TEnd)
		}
		if r.Error != "" {
			parts = append(parts, "error="+oneLine(r.Error))
		}
		fmt.Fprintln(a.out, strings.Join(parts, " "))
	}
}

// verifyCheck implements `verify --check` for git pre-commit hooks.
func (a *app) verifyCheck(worktree bool) int {
	co := evidence.CheckOptions{Mode: evidence.CheckCommit}
	if worktree {
		co.Mode = evidence.CheckWorktree
	} else if idx, ok := a.env.Getenv("GIT_INDEX_FILE"); ok && idx != "" {
		// Under a pre-commit hook git points GIT_INDEX_FILE at the index it is
		// about to commit (relative to the hook's cwd, the work tree top).
		if !filepath.IsAbs(idx) {
			idx = filepath.Join(a.cwd, idx)
		}
		co.IndexFile = idx
	}
	res, err := evidence.Check(a.evidenceOptions(), a.cwd, co)
	if err != nil {
		return a.errorf(ExitCheckUndecidable, "verify --check: %v", err)
	}
	code := checkExitCode(res)
	msg := checkMessage(res)
	if code == ExitCheckFresh {
		fmt.Fprintln(a.out, msg)
	} else {
		fmt.Fprintln(a.errw, msg)
	}
	return code
}

// checkExitCode maps a FreshResult to the `verify --check` exit codes.
func checkExitCode(r model.FreshResult) int {
	switch r.Value {
	case model.TriTrue:
		return ExitCheckFresh
	case model.TriFalse:
		if r.Cause == "no_record" {
			return ExitCheckNoEvidence
		}
		return ExitCheckStale
	}
	if r.Cause == "job_running" {
		return ExitCheckRunning
	}
	return ExitCheckUndecidable
}

func checkMessage(r model.FreshResult) string {
	var b strings.Builder
	b.WriteString("ballast verify --check: ")
	switch checkExitCode(r) {
	case ExitCheckFresh:
		b.WriteString("fresh")
	case ExitCheckStale:
		b.WriteString("stale")
	case ExitCheckNoEvidence:
		b.WriteString("no evidence")
	case ExitCheckRunning:
		b.WriteString("a verify job is still running")
	default:
		b.WriteString("cannot decide")
	}
	b.WriteString(" (" + r.Cause + ")")
	if len(r.Stale) > 0 {
		b.WriteString(" names=" + strings.Join(r.Stale, ","))
	} else if len(r.Names) > 0 {
		b.WriteString(" names=" + strings.Join(r.Names, ","))
	}
	if r.Value != model.TriTrue {
		b.WriteString("; run: ballast verify")
	}
	return b.String()
}
