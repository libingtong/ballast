// Package inject (unit U4) builds continuous-injection text (plan B5;
// docs/architecture.md §6) and the Cursor static rules file.
//
// Hot path: yes. Imports: model, ports, stdlib. Pure: no I/O.
//
// Build(req) decides, per (client, normalized event), which parts to emit:
//
//	part        | when                                                           | budget (runes)
//	full        | Event == caps.FullInjectEvent: SessionStart (all sources) on   | ≤ 8000 total
//	            | claude/codex/cursor; Hermes: UserPromptSubmit when             |
//	            | !Session.FullInjected() || Event.IsFirstTurn                   |
//	anchor      | UserPromptSubmit when (PromptCount()+1) % anchor_every == 0;  | ≤ 1200
//	            | caps.MidTurnEvent when ToolCountSinceInject() ≥ batch_after_tools |
//	gen_summary | Session.Generation() != "" && != Rules.Generation, on any      | ≤ 1000
//	            | inject-capable event (diff of RuleDigest vs Rules)             |
//	job_results | NewJobs non-empty, on UserPromptSubmit or MidTurnEvent:        | ≤ 400
//	            | "verify <name>: <outcome> (job=<id>, S=<s_end[:12]>)"          |
//	notices     | config_invalid / rule_dropped notices, on SessionStart and the | ≤ 400
//	            | first inject-capable event after a generation change          |
//
// Order inside Text: notices, gen_summary, job_results, full|anchor.
// Non-SessionStart total ≤ 2500 runes. Text is empty when the event cannot
// carry context (caps.CanInject false) — then nothing is marked emitted.
// Full text format: a header line "Ballast rules (generation <g>):" then
// "- [<id>] <inject.text>" per rule having inject text, in rule order.
// Anchor format: "Ballast: " + "; ".join("[<id>] <anchor>"). Anchor
// default = first line of inject.text truncated to 80 runes; rules without
// inject text are omitted from both.
// Truncation: whole rule lines are dropped from the end and a final line
// "…(+N rules omitted)" is appended; never cut inside a line except a single
// line longer than the budget (cut at a rune boundary with "…").
//
// Acceptance (U4): testdata/inject.json golden cases (full/anchor cadence,
// generation summary added/removed/promoted, job results, Hermes first
// turn, Cursor postToolUse mid-turn carrier); budgets never exceeded
// (property test with 12 rules × 400 runes); Builder implements ports.Injector.
package inject
