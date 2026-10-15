// Package replay (unit U9) re-evaluates rules offline over recorded
// transcripts (plan B9).
//
// Not hot path. Imports: model, ports, sys, layout, stdlib. Rules are
// compiled and evaluated through injected ports (RuleCompiler,
// RuleEvaluator, BashParser, PowerShellParser, PathService).
//
// Classification of every (rule, synthesized event) evaluation (frozen;
// revised in contract v1.4 to follow the ACTUAL evaluation path rather than
// the static dependencies of the rule):
//   - known-miss, by the matcher: the outcome is false AND the rule's `match`
//     alone (the rule evaluated without its `when` conditions) is already
//     false for this event. Matchers read only the hook payload, so this is
//     exact whatever predicates the `when` list uses: a Bash call never
//     reaches read-before-edit's path_exists. It does not apply to Stop-family
//     rules (see approximate), and it takes precedence over the verify
//     evidence requirement below. Needs Deps.Compiler to compile the
//     matcher-only variant of the rules; without it this step is skipped and
//     the rules below apply unchanged.
//   - known-hit / known-miss, otherwise: outcome true/false and every
//     predicate the evaluation consulted is reconstructable from the
//     transcript itself (read_this_session, tool_count_since_inject,
//     prev_prompt_regex, prior_hit, is_subagent, matchers, and path_exists
//     answered from the transcript: a path read/written/deleted earlier in
//     the replay).
//   - approximate: the evaluation consulted env_equals or branch_glob; or a
//     path_exists that had to fall back to the CURRENT disk (no transcript
//     fact about the path; judged per evaluated event, so every
//     path_exists rule of an event whose evaluation touched the disk is
//     approximate); or is a Stop-family rule evaluated on the final assistant
//     text of the turn (stop_hook_summary lines are reference only, never
//     payload); or a verify rule with evidence (freshness is inferred); or a
//     state-dependent rule on Cursor (its transcripts record no tool results).
//   - unknown: outcome unknown; or verify_fresh / verify_failed_since_read
//     without TRUSTED evidence (records under ~/.ballast/evidence matched by
//     session_id and time, or --evidence <file>) unless the matcher already
//     missed. A `ballast verify` call in the transcript is NOT evidence.
//
// Session state (frozen): replay does NOT import internal/hook or
// internal/state (archtest allowlist). It keeps its own in-memory
// model.SessionState per (session, agent) and updates it with the same
// rules as the hook (internal/hook/doc.go): ToolCall per PreToolUse, read
// set from Read-family targets, file-tool writes, and — for Bash/PowerShell
// tool calls whose tool_result is present in the transcript — the
// model.ShellReadPaths (the same shared function the hook uses), so Codex
// `sed -n`/`cat` reads satisfy read_this_session offline too; prompt tail;
// prior hits; tool_count_since_inject reset on synthesized anchors.
//
// Contract v1.3 additions: with --subagents (Claude) the sub-agent
// transcripts are pre-scanned for successful file-tool writes BEFORE the
// main stream is replayed, so verify_fresh views of later main-agent events
// see earlier sub-agent writes (writes are tracked per session). The
// required verify names come from Deps.Project (U8's strict loader, wired
// by the CLI) when set — missing ⇒ no_project, invalid ⇒ project_invalid,
// both unknown, exactly like the hook — and from a lenient decode only when
// Deps.Project is nil.
//
// --initial-state <file> (Options.InitialState), frozen format: one JSON
// object decoded strictly as model.Fixture, of which ONLY the state keys
// are allowed: "read" (paths, relative to the transcript cwd),
// "prior_hits", "prev_prompt", "tool_count_since_inject". Any other
// fixture key (exists, verify_fresh, env, branch, ...) ⇒ usage error
// (CLI exit 2). It seeds the main agent of every session in the transcript.
//
// Transcript readers (TranscriptReader): Claude (jsonl, role in "type",
// tool_use/tool_result blocks, sub-agents under <session>/subagents/ with
// --subagents), Codex (rollout jsonl), Cursor (agent-transcripts jsonl,
// role in "role"; transcript_path often null ⇒ mostly approximate/unknown).
// Readers stream line by line (bufio.Reader, lines up to 64 MiB), skip
// unparsable lines (counted), tolerate unknown record types (version drift).
//
// Sample id = hex(sha256(transcript_ref + "\x00" + uuid + "\x00" + rule_id +
// "\x00" + rules_hash))[:16] — stable across runs. Labels:
// ~/.ballast/labels/<rule_id>.jsonl lines {"sample_id","label":"tp"|"fp","note"}.
// Precision = tp / (tp+fp) over labeled known-hit samples only.
//
// Acceptance (U9): scripts/replay.sh over the 3 local transcripts
// (~/.ballast/replay-set.txt, one >10MB): report validates against the
// Report JSON shape, peak RSS < 200MB on 40MB input, ≥20 labels all
// resolvable to sample ids in the report.
package replay
