// Package audit (unit U5) writes and reads the audit JSONL log (plan B8;
// docs/architecture.md §3).
//
// Hot path: Writer yes; Reader/Lint/GC no. Imports: model, ports, sys,
// layout, redact (P2: Redact wraps redact.String), stdlib.
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
// Implementation notes:
//   - Append stamps a missing TS with Clock.Now, writes TS in UTC and fills a
//     zero V with model.AuditVersion. A batch that spans a month boundary
//     needs one write per month; otherwise it is exactly one write.
//   - The minimal fallback line also carries "truncated":true (a key that is
//     not part of model.AuditRecord; the Reader ignores it, Lint exempts such
//     lines from per-type required-field checks).
//   - Scan selects Since <= TS < Until. ScanStats.Lines counts non-blank
//     lines, Corrupt non-object/unparsable/oversize (>1 MiB) lines, Skipped
//     unknown "v". Location.File is Dir joined with the file name.
//   - Lint required fields: hit ⇒ rule_id, decision; eval_summary ⇒ evals;
//     rule_dropped, rule_shadowed, promote ⇒ rule_id; verify ⇒ verify;
//     diag ⇒ detail.
//   - GC returns the full paths it removed; a file goes when its month ended
//     more than the retention ago.
//   - ClipRunes returns at most n runes: a cut result ends with the ellipsis,
//     which counts towards n. MarshalLine clips Matched to
//     model.MaxMatchedRunes after redaction.
//
// Acceptance (U5): 8 concurrent writers × 1000 records ⇒ every line parses
// (no interleaving); 64 MiB rotation creates .1; corrupt tail tolerated;
// `ballast audit lint` reports schema violations with file:line; Writer
// implements ports.AuditSink.
package audit
