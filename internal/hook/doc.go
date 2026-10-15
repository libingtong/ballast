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
// eval_summary (frozen): on Stop/SubagentStop/SessionEnd the delta carries
// FlushEvals + FlushedEvals (= the counts read lock-free). The eval_summary
// record is appended ONLY when State.Commit returned Applied && !Duplicate
// && !LockTimeout. On LockTimeout/Duplicate the record is omitted (the
// counts were not subtracted and are flushed next time); the lock timeout
// itself is still audited as diag.
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
//     (or missing) Spawner.SpawnDetached([self, "gc"]) and failures ignored.
//   - Stop/SubagentStop flush eval_summary (FlushEvals) every time.
//   - AgentResponse (Cursor) ⇒ store LastAssistantText; empty stdout.
//   - Cursor stop without last_assistant_message ⇒ use state LastAssistantText.
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
// Acceptance (U7): scripts/fault.sh with a `-tags ballast_fault` build:
// for every FaultPoint × {error, panic, hang} ⇒ stdout empty, exit 1,
// stderr prefix "ballast:" — including FaultDecided with a deny Decision;
// testdata/pipeline.json with fake ports (including the shell-read and
// eval_summary-on-lock-timeout rows); unit tests with fake ports for each
// event kind; `go list -deps ./internal/hook` has no net
// and no os/exec (internal/archtest). Integration acceptance (U12): bench
// p95 ≤ 30ms (scenarios 1–11, plan B10) with real ports.
package hook
