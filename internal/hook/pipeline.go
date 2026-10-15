package hook

import (
	"errors"
	"fmt"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Implementation notes on points the package doc leaves open:
//   - Every FaultPoint is consulted on every event that runs the full
//     pipeline, even when the stage itself is skipped (e.g. no evaluation on
//     SessionStart), so the fault matrix does not depend on the event used.
//   - A blocking decision (deny/ask) is rendered without the injection, so
//     none of the injection's bookkeeping (anchor/full/jobs/generation) is
//     recorded for it. The generation is recorded only together with a rule
//     digest from the injector.
//   - ErrNotRepo is audited as diag not_a_repo once per session (SessionStart)
//     to keep non-repo sessions from writing a line per tool call. Config
//     notices are audited when the injection surfaced them (Kinds notices).
//   - Lifecycle cleanup (DropAgent/DropSession, GC spawn) runs after the
//     stdout commit and never fails the invocation.

// shellReadPaths is the single shared shell-read rule (U2). It is a variable
// only so tests can substitute it.
var shellReadPaths = model.ShellReadPaths

// maxNewJobs bounds the finished-job list handed to the injector.
const maxNewJobs = 16

// invocation carries the state of one hook run.
type invocation struct {
	deps  *Deps
	guard *guard
	start time.Time
	wd    time.Duration
}

// fault consults the injector at p. An armed point never lets the pipeline
// continue: error returns an error, panic panics, hang blocks until the
// watchdog fires (then returns an error for tests whose Exit returns).
func (inv *invocation) fault(p FaultPoint) error {
	if inv.deps.Faults == nil {
		return nil
	}
	m := inv.deps.Faults.Hit(p)
	if m == nil {
		return nil
	}
	switch *m {
	case FaultPanic:
		panic(fmt.Sprintf("fault injected at %s", p))
	case FaultHang:
		inv.guard.wait(2 * inv.wd)
		return fmt.Errorf("fault injected at %s: hang interrupted", p)
	}
	return fmt.Errorf("fault injected at %s", p)
}

// run executes the pipeline up to the stdout write. The returned post func
// performs the lifecycle cleanup that must follow the commit; it never
// panics and ignores failures.
func (inv *invocation) run(args Args, stdin []byte) (res *Result, post func(), err error) {
	defer func() {
		if r := recover(); r != nil {
			res, post, err = nil, nil, fmt.Errorf("panic: %v", r)
		}
	}()
	return inv.pipeline(args, stdin)
}

func (inv *invocation) pipeline(args Args, stdin []byte) (*Result, func(), error) {
	d := inv.deps

	ad, ok := d.Adapters.Get(args.Client)
	if !ok || ad == nil {
		return nil, nil, fmt.Errorf("%w: %q", model.ErrUnknownClient, string(args.Client))
	}
	ev, err := ad.ParseEvent(args.Native, stdin)
	if err != nil {
		return nil, nil, fmt.Errorf("parse event: %w", err)
	}
	if ev == nil || ev.Name == "" {
		return nil, nil, errors.New("parse event: adapter returned no event")
	}
	if err := inv.fault(FaultParse); err != nil {
		return nil, nil, err
	}
	if ev.Client == "" {
		ev.Client = args.Client
	}
	if ev.Native == "" {
		ev.Native = args.Native
	}
	ev.ReceivedAt = d.Clock.Now()
	caps := ad.Capabilities()

	var diags []diag

	var repo *model.RepoInfo
	if ev.Cwd != "" {
		r, lerr := d.Repo.Locate(ev.Cwd)
		switch {
		case lerr == nil:
			repo = r
		case errors.Is(lerr, model.ErrNotRepo):
			// Audited once per session (SessionStart) to keep non-repo
			// sessions from writing a line per tool call.
			if ev.Name == model.EventSessionStart {
				diags = append(diags, diag{name: "not_a_repo"})
			}
		default:
			diags = append(diags, diag{name: "repo_locate_failed", fields: map[string]any{"error": lerr.Error()}})
		}
	}
	pc, normCwd := inv.pathContext(ev, repo)
	key := scopeFor(ev, repo, normCwd)

	rs, err := d.Rules.Load(model.LoadRequest{Client: args.Client, Cwd: ev.Cwd, Repo: repo, Now: ev.ReceivedAt})
	if err != nil {
		return nil, nil, fmt.Errorf("load rules: %w", err)
	}
	if rs == nil {
		return nil, nil, errors.New("load rules: loader returned no rule set")
	}
	if err := inv.fault(FaultLoad); err != nil {
		return nil, nil, err
	}

	st, linfo, lerr := d.State.Load(key)
	if st == nil {
		st = emptyState{}
	}
	readable := lerr == nil && !(linfo.Exists && !linfo.Readable && !linfo.Corrupt)
	if lerr != nil {
		diags = append(diags, diag{name: "state_unreadable", fields: map[string]any{"error": lerr.Error()}})
	}
	if linfo.Corrupt {
		diags = append(diags, diag{name: "state_corrupt"})
	}
	if err := inv.fault(FaultStateRead); err != nil {
		return nil, nil, err
	}

	if ev.IsInternalCompactAgent() {
		// Claude's compaction helper: no rules, no bookkeeping, state dropped.
		res := &Result{Event: ev, Decision: &model.Decision{Event: ev.Name, Native: ev.Native, Action: model.ActionNone}}
		return res, inv.post(ev, key, false), nil
	}

	if ev.Name.IsStopFamily() && !ev.HasLastAssistantMessage && readable {
		if t := st.LastAssistantText(); t != "" {
			ev.LastAssistantMessage, ev.HasLastAssistantMessage = t, true
		}
	}

	// Evaluate.
	var view model.EvidenceView
	ruleEvent := ev.Name.IsRuleEvent()
	if repo != nil && (ruleEvent || ev.Name == model.EventUserPromptSubmit || ev.Name == caps.MidTurnEvent) {
		v, oerr := d.Evidence.Open(repo, inv.start.Add(inv.deadlineSpan()))
		if oerr != nil {
			diags = append(diags, diag{name: "evidence_open_failed", fields: map[string]any{"error": oerr.Error()}})
		} else {
			view = v
		}
	}
	cmds := inv.shellCommands(ev)
	verdict := &model.Verdict{}
	if ruleEvent {
		in := &model.EvalInput{
			Event:           ev,
			Client:          args.Client,
			Paths:           pc,
			Commands:        cmds,
			Session:         st,
			SessionReadable: readable,
			Evidence:        view,
			World:           newWorld(d.Env, d.FS, d.Repo, repo),
			Now:             ev.ReceivedAt,
			Deadline:        inv.start.Add(inv.deadlineSpan()),
		}
		v, eerr := d.Engine.Evaluate(rs, in)
		if eerr != nil {
			return nil, nil, fmt.Errorf("evaluate: %w", eerr)
		}
		if v != nil {
			verdict = v
		}
	}
	if err := inv.fault(FaultEvaluate); err != nil {
		return nil, nil, err
	}

	// Form the single in-memory Decision.
	adj, capDiags := applyCapabilities(ev, caps, verdict)
	diags = append(diags, capDiags...)
	blocking := adj.Action == model.ActionDeny || adj.Action == model.ActionAsk

	var inj *model.Injection
	if ev.Name != model.EventSessionEnd && ev.Name != model.EventAgentResponse {
		req := &model.InjectRequest{
			Event:   ev,
			Client:  args.Client,
			Rules:   rs,
			Session: st,
			NewJobs: newJobs(ev, caps, st, view, readable),
			Now:     ev.ReceivedAt,
		}
		if ruleEvent {
			req.Verdict = adj
		}
		inj, err = d.Injector.Build(req)
		if err != nil {
			return nil, nil, fmt.Errorf("build injection: %w", err)
		}
		if blocking {
			// A blocking decision renders the reason only; the injection was
			// not shown, so none of its bookkeeping may be recorded.
			inj = nil
		}
	}

	dec := &model.Decision{Event: ev.Name, Native: ev.Native, Action: adj.Action, Hits: adj.Hits}
	if adj.Action.Severity() >= model.ActionWarn.Severity() {
		dec.Reason = adj.Reason
	}
	if inj != nil {
		dec.Context = inj.Text
	}
	if err := inv.fault(FaultDecided); err != nil {
		return nil, nil, err
	}

	// State.
	reads := inv.readPaths(ev, pc, cmds, blocking)
	delta := buildDelta(ev, rs, adj, inj, reads, ev.ReceivedAt)
	if delta.FlushEvals && readable {
		delta.FlushedEvals = cloneEvals(st.Evals())
	}
	if err := inv.fault(FaultStateCommit); err != nil {
		return nil, nil, err
	}
	cinfo, err := d.State.Commit(key, delta)
	if err != nil {
		return nil, nil, fmt.Errorf("commit state: %w", err)
	}
	if cinfo.LockTimeout {
		diags = append(diags, diag{name: "lock_timeout", fields: map[string]any{"wait_us": cinfo.LockWait.Microseconds()}})
	}
	if cinfo.Corrupt {
		diags = append(diags, diag{name: "state_corrupt", fields: map[string]any{"phase": "commit"}})
	}

	// Audit.
	ab := &auditBuilder{
		deps: d, ev: ev, rs: rs, repo: repo, key: key, caps: caps, st: st, readable: readable,
		cinfo: cinfo, inj: inj, start: inv.start,
	}
	recs := ab.records(adj, diags, delta)
	if err := inv.fault(FaultAuditWrite); err != nil {
		return nil, nil, err
	}
	if len(recs) > 0 {
		if err := d.Audit.Append(recs); err != nil {
			return nil, nil, fmt.Errorf("append audit: %w", err)
		}
	}

	// Render.
	var out []byte
	if !dec.Empty() {
		out, err = ad.Render(ev, dec)
		if err != nil {
			return nil, nil, fmt.Errorf("render: %w", err)
		}
	}
	if err := inv.fault(FaultRender); err != nil {
		return nil, nil, err
	}
	return &Result{Stdout: out, Decision: dec, Audit: recs, Event: ev}, inv.post(ev, key, true), nil
}

// deadlineSpan is how long after the start expensive predicate work may run.
func (inv *invocation) deadlineSpan() time.Duration {
	if inv.wd <= DeadlineMargin {
		return 0
	}
	return inv.wd - DeadlineMargin
}

// pathContext derives the PathContext and the normalized cwd of an event.
func (inv *invocation) pathContext(ev *model.Event, repo *model.RepoInfo) (model.PathContext, string) {
	d := inv.deps
	pc := model.PathContext{GOOS: d.Env.GOOS()}
	if home, err := d.Env.HomeDir(); err == nil && home != "" {
		pc.Home = d.Paths.Normalize(home, "", pc)
	}
	cwd := ""
	if ev.Cwd != "" {
		cwd = d.Paths.Normalize(ev.Cwd, "", pc)
	}
	pc.Cwd = cwd
	pc.ProjectRoot = cwd
	if repo != nil && repo.Toplevel != "" {
		pc.ProjectRoot = d.Paths.Normalize(repo.Toplevel, "", pc)
	}
	return pc, cwd
}

// shellCommands returns the lazy parse of the event's shell command, or nil.
func (inv *invocation) shellCommands(ev *model.Event) model.ShellCommands {
	t := ev.Tool
	if t == nil || t.Command == "" {
		return nil
	}
	var parse func(string) (*model.CommandList, error)
	switch t.Shell {
	case model.ShellBash:
		if p := inv.deps.Bash; p != nil {
			parse = p.ParseBash
		}
	case model.ShellPowerShell:
		if p := inv.deps.PS; p != nil {
			parse = p.ParsePowerShell
		}
	default:
		return nil
	}
	if parse == nil {
		return model.NewLazyCommands(t.Command, func(string) (*model.CommandList, error) {
			return nil, fmt.Errorf("no %s parser configured", t.Shell)
		})
	}
	return model.NewLazyCommands(t.Command, safeParse(parse))
}

// safeParse turns a parser panic into a parse error (matchers go unknown).
func safeParse(parse func(string) (*model.CommandList, error)) func(string) (*model.CommandList, error) {
	return func(src string) (cl *model.CommandList, err error) {
		defer func() {
			if r := recover(); r != nil {
				cl, err = nil, fmt.Errorf("shell parser panic: %v", r)
			}
		}()
		return parse(src)
	}
}

// readPaths computes the normalized read-set additions of the event (see the
// package doc: file-tool targets and shell Reads on PostToolUse, plus Cursor's
// beforeReadFile when the read is not blocked).
func (inv *invocation) readPaths(ev *model.Event, pc model.PathContext, cmds model.ShellCommands, blocking bool) []string {
	t := ev.Tool
	if t == nil {
		return nil
	}
	var raw []string
	switch {
	case ev.Name == model.EventPostToolUse && isFileTool(t.Name):
		raw = t.Targets
	case ev.Name == model.EventPreToolUse && ev.Client == model.ClientCursor &&
		ev.Native == "beforeReadFile" && t.Name == model.ToolRead && !blocking:
		raw = t.Targets
	case ev.Name == model.EventPostToolUse && cmds != nil &&
		(t.Shell == model.ShellBash || t.Shell == model.ShellPowerShell):
		// A parse error means no shell reads, not an exception.
		if cl, err := cmds.Get(); err == nil && cl != nil {
			raw = shellReadPaths(cl)
		}
	}
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		if r == "" {
			continue
		}
		n := inv.deps.Paths.Normalize(r, ev.Cwd, pc)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func isFileTool(name string) bool {
	switch name {
	case model.ToolRead, model.ToolEdit, model.ToolWrite, model.ToolMultiEdit, model.ToolNotebookEdit:
		return true
	}
	return false
}

// newJobs lists finished verify jobs this session has not been told about.
// Without a previous report the window starts at the session's first event,
// so a new session is not flooded with the repo's history.
func newJobs(ev *model.Event, caps model.CapabilitySet, st model.SessionState, view model.EvidenceView, readable bool) []model.VerifyRecord {
	if view == nil || !readable {
		return nil
	}
	if ev.Name != model.EventUserPromptSubmit && ev.Name != caps.MidTurnEvent {
		return nil
	}
	since := st.LastJobReportAt()
	if since.IsZero() {
		t, ok := st.StartedAt()
		if !ok {
			return nil
		}
		since = t
	}
	var out []model.VerifyRecord
	for _, r := range view.FinishedSince(since) {
		if st.ReportedJob(r.JobID) {
			continue
		}
		out = append(out, r)
		if len(out) == maxNewJobs {
			break
		}
	}
	return out
}

// post returns the cleanup that follows the stdout commit (and audit write).
func (inv *invocation) post(ev *model.Event, key model.ScopeKey, spawnGC bool) func() {
	return func() {
		defer func() { _ = recover() }()
		d := inv.deps
		switch ev.Name {
		case model.EventSubagentStop:
			if ev.AgentID != "" {
				_ = d.State.DropAgent(key)
			}
		case model.EventSessionEnd:
			_ = d.State.DropSession(key)
		case model.EventSessionStart:
			if spawnGC {
				inv.maybeSpawnGC()
			}
		}
	}
}

// maybeSpawnGC starts a detached `ballast gc` when gc.stamp is missing or
// older than GCInterval. The stamp is touched first so concurrent session
// starts do not spawn a storm; any failure is ignored.
func (inv *invocation) maybeSpawnGC() {
	d := inv.deps
	if d.Spawner == nil || d.Self == "" || d.Layout.GCStamp == "" {
		return
	}
	now := d.Clock.Now()
	if fi, err := d.FS.Stat(d.Layout.GCStamp); err == nil && now.Sub(fi.ModTime()) < GCInterval {
		return
	}
	if err := touchFile(d, d.Layout.GCStamp); err != nil {
		return
	}
	_, _ = d.Spawner.SpawnDetached([]string{d.Self, "gc"}, d.Env.Environ(), "")
}

func touchFile(d *Deps, path string) error {
	return sys.WriteFileAtomic(d.FS, sys.RandomID{}, path, nil, 0o600, false)
}
