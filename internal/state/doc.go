// Package state (unit U5) stores per-(repo_inst, session, agent) session
// state with a cross-process lock (plan B6, v0.4 §3; docs/architecture.md §3).
//
// Hot path: yes. Imports: model, ports, sys, layout, stdlib (syscall on
// unix; syscall.NewLazyDLL("kernel32.dll") LockFileEx/UnlockFileEx on
// Windows — no golang.org/x/sys).
//
// File: layout.StatePath(SanitizeComponent(repo_inst), SanitizeComponent(session),
// SanitizeComponent(agent)) + ".lock" sibling. Format: Record (JSON, "v":1).
// Directories 0700, files 0600.
//
// Transaction protocol (frozen):
//  1. Load: lock-free read for evaluation. Not-exist ⇒ empty Record.
//     Unparsable / v≠1 ⇒ empty Record, info.Corrupt (hook audits
//     diag:state_corrupt). Other I/O errors ⇒ empty Record, Readable=false,
//     and the error is returned (the hook does NOT fail; predicates that
//     need state become unknown).
//  2. Commit: open/create "<file>.lock"; non-blocking exclusive lock retried
//     every 200µs for at most LockBudget (20ms). Timeout ⇒ return
//     {LockTimeout:true}, nil error (delta dropped, hook audits diag).
//  3. Under the lock: re-read the file (corrupt ⇒ reset), merge the delta
//     (Merge), WriteFileAtomic without fsync, unlock (close).
//  4. Merge is commutative for concurrent deltas: counters add, sets union,
//     ring appends with store-assigned seq, last-writer-wins for scalars,
//     dedupe by StateDelta.DedupeKey (seen list capped at SeenCap, FIFO).
//  5. Flushed eval counts are SUBTRACTED (floored at 0), not zeroed.
//
// Caps: SeenCap 512, RingCap 8, ReadCap 4096 (evict oldest read time),
// ReportedJobsCap 64, LastAssistantText ≤ 4000 runes, PrevPromptTail ≤ 200 runes.
// GC: removes session dirs whose newest file mtime is older than TTL (7d);
// invoked by `ballast gc` (detached from SessionStart, ≤ once/day).
// DropAgent/DropSession (contract v1.3) remove state files only while
// holding their lock and never unlink "<file>.lock"; the lock files and the
// emptied session directory are left to GC. A lock timeout leaves the file
// for GC (nil error).
//
// Implementation notes:
//   - Every non-duplicate delta consumes one event_seq (with or without a
//     Summary); Summary.Seq is that value. Commit returns it in EventSeq.
//   - A duplicate delta (DedupeKey already in "seen") applies only
//     Generation, RuleDigest and ReportedJobs. Info.Duplicate is true; Applied
//     is true only if one of those changed the file (otherwise the file is not
//     rewritten).
//   - A tool call and AnchorEmitted in one delta leave tool_count_since_inject
//     at 0. ReadAt keeps the newest time per path; LastAssistantText keeps the
//     last 4000 runes, PrevPromptTail the last 200.
//   - Flushed evals that reach all-zero are removed from "evals".
//   - GC(ttl<=0) uses TTL; a session is stale when its newest file (or the
//     directory itself when empty) is older than ttl. Empty repo directories
//     are removed.
//   - On Windows the rename can fail while a lock-free reader has the file
//     open; Commit retries it briefly.
//
// Acceptance (U5): scripts/concurrency.sh — 2–8 independent processes
// committing to the same key lose no update (sum of applied deltas ==
// event_seq); duplicate tool_use_id applied once; corrupt file recovers;
// lock timeout path returns LockTimeout without error; Windows build
// compiles (GOOS=windows go vet); Store implements ports.StateStore and
// *Record implements model.SessionState.
package state
