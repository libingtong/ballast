// Package audit (unit U5) writes and reads the audit JSONL log (plan B8;
// docs/architecture.md §3).
//
// Hot path: Writer yes; Reader/Lint/GC no. Imports: model, ports, sys,
// layout, stdlib.
//
// Write protocol (frozen):
//   - Files: <AuditDir>/YYYY-MM.jsonl, then YYYY-MM.1.jsonl, .2, ... The
//     active file of a month is the highest existing suffix; when its size
//     is ≥ MaxFileBytes (64 MiB) before a write, the writer creates the next
//     suffix (no renames ⇒ no races between concurrent writers). Month from
//     the record TS in UTC.
//   - Each record is marshalled to one line ≤ model.MaxAuditLineBytes (8 KiB
//     incl. '\n'). Oversized lines are shrunk by truncating, in order:
//     Detail, Context, Matched, then dropping Evals entries; a record that
//     still does not fit is replaced by a minimal {ts,v,type,"truncated":true}.
//   - All lines of one Append are concatenated into ONE buffer and written
//     with ONE write call on a file opened O_WRONLY|O_APPEND|O_CREATE 0600
//     (Windows: FILE_APPEND_DATA + LockFileEx around the write). n != len ⇒
//     error (short write). Any error is returned (the hook treats it as an
//     exception and fails open, plan B7).
//   - Strings in Matched / Context are redacted (Redact) before marshal.
//
// Read protocol: Reader streams files in chronological order; a line that
// fails to parse is counted (Stats.Corrupt) and skipped — a corrupt trailing
// line is normal after a crash. Lines with unknown "v" are skipped and counted.
//
// Retention (GC, run by `ballast gc`): audit files whose month ended more
// than 90 days ago are deleted.
//
// Acceptance (U5): 8 concurrent writers × 1000 records ⇒ every line parses
// (no interleaving); 64 MiB rotation creates .1; corrupt tail tolerated;
// `ballast audit lint` reports schema violations with file:line; Writer
// implements ports.AuditSink.
package audit
