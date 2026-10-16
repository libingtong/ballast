// Package hook (unit U7) is the hot-path pipeline behind
// `ballast hook --client <id> --event <native>` (plan B7; docs/architecture.md §2, §7, §9).
//
// Imports: model, ports, sys, layout, version, redact (L0, stdlib only; the
// one relaxation of this rule, docs/p2-design.md §2.2), stdlib ONLY. Every other
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
//   - EventSummary.Text is redacted with internal/redact (P2, §4.1: the same
//     rules as the audit writer, which redacts again on write).
//   - State readability: readable ⇔ Load returned no error (and, for
//     defensive fakes, not Exists && !Readable && !Corrupt); see
//     ports.StateStore.Load.
//
// P2 additions (unit P2-HOOK; docs/p2-design.md §3.2, §3.3, §5.1; p2.go):
//   - First event of a session (State.StartedAt() false for the scope), in
//     this order: diag env_redirect_ignored {vars} when Deps.RedirectsIgnored
//     is non-empty; diag lower_sandbox_weakened {entries} when (claude-code
//     and) Deps.ProjectSandbox reports entries for the session's project
//     directories ($CLAUDE_PROJECT_DIR when absolute, else the event cwd and
//     the repo top level). Nothing is written to the session state. Only
//     judged when the state was readable: an unreadable state cannot tell a
//     new session from an old one, so these diags are skipped (and state_
//     unreadable is audited) instead of repeating on every event. The
//     entries come from repo-controlled files and are bounded: at most 8
//     entries of 80 runes each; a cut list ends with a "…等 N 项" entry (the
//     detail stays {entries}).
//   - Synthetic deny (claude-code PreToolUse only), merged with the rule
//     verdict after the capability check: deny is the strictest action, the
//     synthetic reason comes first, rule reasons follow within BudgetReason;
//     no rule hit is fabricated and the eval counters are untouched.
//     (a) Deps.WeakenedProject == deny and ProjectSandbox non-nil: every
//     PreToolUse (any tool) of a session whose project layer is weakened is
//     denied with the frozen reason, diag lower_sandbox_deny {tool}; this
//     wins over (b) and nothing is started. (b) a canonical
//     `ballast verify` (model.ParseVerifyRequest) under a T1 Probe: request id
//     = UTC job-id timestamp + Deps.IDs.NewID(3), Spawner.SpawnDetached(
//     model.HookStartArgv(Self, …), Env.Environ(), <RequestsDir>/<repo_inst>/
//     <id>.spawn.log), deny with the frozen reason, diag verify_hook_start
//     {request_id, names, spawn_ok}; a spawn failure (also no IDs, no
//     Spawner/Self, non-absolute cwd) still denies with the failure reason and
//     audits only verify_spawn_failed {request_id, names, error} (no
//     verify_hook_start: nothing was started). T0
//     or no Tier ⇒ ordinary evaluation (declared T1 but detected T0 ⇒ diag
//     tier_mismatch {declared, missing}). With Tier == nil the command is not
//     even parsed here. The spawn happens before the commit point, so a later
//     exception (fail open) leaves the detached verify running.
//   - ConfigChange: audit only. One config_change line {source, file,
//     protected} from Event.Source and Event.ConfigFile (never the payload);
//     file is "<repo>/…", "~/…" or the basename, protected is judged on
//     PathService-normalized paths (Ballast home, org rules / managed
//     directory, <repo>/.ballast, Claude settings*.json). No rules, no
//     session state, empty stdout, same B7 failure protocol.
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
