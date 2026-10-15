// Package hook (unit U7) is the hot-path pipeline behind
// `ballast hook --client <id> --event <native>` (plan B7; docs/architecture.md §2, §7, §9).
//
// Imports: model, ports, sys, layout, version, stdlib ONLY. Every other
// unit is reached through Deps (ports interfaces) wired by internal/cli
// (U12). No net/*, no os/exec (detached children go through ports.Spawner).
//
// Pipeline (single in-memory Decision, single commit point):
//
//	args → start watchdog(150ms) → read stdin (≤16 MiB) → adapter.ParseEvent
//	→ stamp ReceivedAt → Repo.Locate (ErrNotRepo ok) → Rules.Load
//	→ State.Load (lock-free) → [ignored events short-circuit, see below]
//	→ build EvalInput (lazy shell parse, PathContext, WorldView,
//	  Evidence = Deps.Evidence.Open(repo, deadline): no I/O, project.json
//	  is loaded lazily by the provider only if an evidence predicate runs)
//	→ Engine.Evaluate (rule events only) → capability check per hit
//	→ Injector.Build → Decision{Action, Reason, Context, Hits}
//	→ StateDelta → State.Commit (lock timeout = diag, not error)
//	→ audit records (hits, notices, diags, eval_summary) → Audit.Append
//	→ adapter.Render → ONE stdout write under the commit mutex → exit 0
//
// Audit common fields: RepoInst = repo.Inst (or the norepo scope),
// RepoRemoteHash = repo.RemoteHash (filled by RepoLocator.Locate; the hook
// never parses .git/config itself), Generation, Tier, BallastVersion.
//
// eval_summary (frozen, v1.6): on Stop/SubagentStop/SessionEnd the delta
// carries FlushEvals + FlushedEvals = the counts read lock-free PLUS this
// event's own evaluation (delta.Evals). State.Commit merges delta.Evals first
// and then subtracts FlushedEvals, so the flushing event leaves nothing
// behind (SubagentStop drops the state right after; Codex/Cursor have no
// SessionEnd). The eval_summary record is appended ONLY when State.Commit
// returned Applied && !Duplicate && !LockTimeout. On LockTimeout/Duplicate
// the record is omitted (nothing was merged or subtracted; the earlier
// counts are flushed next time); the lock timeout itself is still audited as
// diag. Counts that no flushing event ever reaches are reported by
// `ballast gc` as eval_summary with detail.cause=gc (state.Store.GCFlush).
//
// Failure protocol (frozen): any error, panic, or watchdog expiry BEFORE the
// stdout commit ⇒ stdout stays EMPTY, stderr "ballast: <msg>\n", exit 1.
// Errors that are NOT exceptions (handled inline, audited as diag):
// ErrNotRepo, state lock timeout, corrupt state (reset), predicate unknowns,
// snapshot fallback timeout, cache/LKG write failure, Spawner failure.
// Exit code 2 is NEVER used (it would block, plan A7). Unknown --client or
// --event, malformed stdin ⇒ exception path (exit 1, empty stdout).
// After a successful stdout write the process exits 0 even if later cleanup
// fails (decision already effective, documented boundary).
//
// Watchdog: time.AfterFunc(Deps.Watchdog); on fire it takes the commit
// mutex; if stdout was not committed it writes the stderr line and calls
// Deps.Exit(1). The committer holds the same mutex while writing stdout, so
// a partial stdout write can never race the watchdog. Deadline for
// expensive predicate work = start + Watchdog − 30ms.
//
// Event handling specifics:
//   - SubagentStop of the internal compact agent (Event.IsInternalCompactAgent)
//     ⇒ no rule evaluation; state DropAgent; empty stdout.
//   - SubagentStop ⇒ evaluate, flush eval_summary, then DropAgent AFTER the
//     audit write. SessionEnd ⇒ flush eval_summary, DropSession; empty stdout.
//   - SessionStart ⇒ no rules; full injection; if gc.stamp is older than 24h
//     (or missing) touch gc.stamp, then Spawner.SpawnDetached([self, "gc"],
//     Env.Environ(), "") after the stdout commit. A stamp or spawn failure
//     never fails the invocation but is audited as diag gc_spawn_failed
//     {stage: "stamp"|"spawn", error} in a second best-effort Append; after
//     a failed spawn the stamp is removed so a later SessionStart retries
//     (v1.3). The hook alone maintains gc.stamp; `ballast gc` does not.
//   - Stop/SubagentStop flush eval_summary (FlushEvals) every time.
//   - AgentResponse (Cursor) ⇒ store LastAssistantText; empty stdout.
//   - Stop/SubagentStop without last_assistant_message (any client; in
//     practice Cursor) ⇒ the engine falls back to state LastAssistantText;
//     no fallback when the state is unreadable (dsl-semantics §5.5).
//     LastAssistantText stores the LAST 4000 runes of the message (v1.3).
//   - Dedupe: StateDelta.DedupeKey = "<Event>:<tool_use_id>"; a duplicate
//     delivery still evaluates and renders the same decision but does not
//     double count.
//   - PreToolUse ⇒ ToolCall=true; Read/Edit/Write/MultiEdit/NotebookEdit
//     PostToolUse (and Cursor beforeReadFile/postToolUse equivalents) ⇒
//     ReadPaths = normalized Targets.
//   - Shell reads (frozen; needed because Codex has no Read tool): on
//     PostToolUse of a tool with Shell bash|powershell, ReadPaths also gets
//     Paths.Normalize(p, ev.Cwd, ctx) for every p in
//     model.ShellReadPaths(Commands.Get()) — the single shared rule (also
//     used by replay; do not re-implement it here). Parse error ⇒ no shell
//     reads (no error). ShellReadPaths drops relative paths when the list
//     contains cd/pushd/popd (PowerShell Set-Location family), because the
//     parser does not track the working directory. Exit status is not
//     consulted: a PostToolUse means the client considered the call run. PreToolUse and PostToolUseFailure never add
//     shell reads (the command has not run / failed). Shell Writes are not
//     added to the read set.
//
// Decisions recorded in contract v1.3 (previously implementation choices):
//   - ErrNotRepo ⇒ diag not_a_repo is audited ONCE per session, on
//     SessionStart (architecture §10 "audited as diag" is satisfied per
//     session, not per event). Any other Locate error ⇒ degrade to no repo
//     and audit diag repo_locate_failed on that event.
//   - Config notices (rule_dropped, rule_shadowed, config_invalid) are
//     audited exactly when the injection surfaced them (Injection.Kinds has
//     InjectNotices), not on every event.
//   - A blocking decision (deny/ask) renders no injection text, so none of
//     the injection bookkeeping (AnchorEmitted, FullEmitted, ReportedJobs,
//     Generation/RuleDigest) is recorded for it.
//   - Generation and RuleDigest are recorded together, exactly when the
//     injector returned a non-nil Injection.RuleDigest (it does so on every
//     event the client can inject on); events that cannot carry a
//     generation summary never consume the change.
//   - BuildDelta has no SessionState: it only sets FlushEvals; the
//     pipeline then sets FlushedEvals = lock-free Load counts (only when the
//     state was readable) + delta.Evals.
//   - A rule that is unknown because its verify job is still running
//     (cause verify_fresh:job_running) never hits, but when its configured
//     action is warn or stricter and the (client,event) can carry a warn,
//     the decision gains a warn-level notice (plan B3). Log-tier rules stay
//     silent.
//   - Verify job reports: "unreported finished jobs" are those finished
//     after LastJobReportAt, or after the session's StartedAt when nothing
//     was reported yet; with neither, nothing is reported (a new session is
//     not flooded with the repo's history). At most 16 per event.
//   - EventSummary.Text is redacted by a local minimal redactor (the hook
//     may not import audit); audit.Writer redacts again on write, so the
//     state ring is the only place holding the narrower redaction.
//   - State readability: readable ⇔ Load returned no error (and, for
//     defensive fakes, not Exists && !Readable && !Corrupt); see
//     ports.StateStore.Load.
//
// Acceptance (U7): scripts/fault.sh with a `-tags ballast_fault` build:
// for every FaultPoint × {error, panic, hang} ⇒ stdout empty, exit 1,
// stderr prefix "ballast:" — including FaultDecided with a deny Decision;
// testdata/pipeline.json with fake ports (including the shell-read and
// eval_summary-on-lock-timeout rows); unit tests with fake ports for each
// event kind; `go list -deps ./internal/hook` has no net
// and no os/exec (internal/archtest). Integration acceptance (U12): bench
// p95 ≤ 30ms (scenarios 1–11, plan B10) with real ports.
package hook
