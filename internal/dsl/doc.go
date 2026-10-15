// Package dsl (unit U3) implements the Ballast rule language: decoding and
// validation of rule files, lint (including the client × event × action
// capability check, field applicability and injection budgets),
// compilation, matchers, three-valued predicate evaluation, multi-rule
// arbitration, and the examples self-test (`ballast test-rules`).
//
// The normative semantics are docs/dsl-semantics.md (frozen with
// schema/rules.v1.json). This package must implement exactly that document.
//
// Hot path: Compile/Evaluate yes. Imports: model, ports, sys,
// github.com/bmatcuk/doublestar/v4, stdlib (regexp = RE2). NO direct
// import of paths/bashparse/psparse/adapter
// implementations: they arrive through Options (ports.PathService,
// ports.BashParser, ports.PowerShellParser, ports.AdapterRegistry) so U3
// can be developed and tested with fakes. Predicates consult only
// EvalInput.Session / Evidence / World (no I/O of their own).
//
// Name globs (frozen): tool, bash.cmd/ps.cmd, pipe_from, pipe_to,
// agent_type and branch_glob use doublestar.Match DIRECTLY, case-SENSITIVE
// on every GOOS (ps.cmd matches the already lower-cased Name), with the
// same list-level "!" negation as paths (list matches ⇔ ≥1 positive
// pattern matches AND no negative pattern matches; negation-only list ⇒
// lint glob-only-negation). Patterns are validated with doublestar.ValidatePattern
// at ParseFile time (bad-glob). Only path, writes and the $target/literal
// path arguments of predicates go through ports.PathService (normalization,
// candidates, "~/" expansion, GOOS case folding).
//
// Evaluation contract (summary; details in dsl-semantics §8–§9):
//   - Only rules with Rule.Event == Event.Name and AppliesTo(client) run.
//   - Bindings: match produces a set of bindings (each tool target that
//     satisfies `path`; each CommandNode that satisfies `bash`/`ps`; or one
//     empty binding). Rule outcome = OR over bindings of AND(when...).
//     No binding ⇒ TriFalse (match failed) unless a matcher was unknown ⇒
//     TriUnknown.
//   - Unknown never hits; RuleResult.UnknownCause names the first cause.
//   - Arbitration: Verdict.Action = strictest Effective action of hits;
//     Effective = Action unless model.Capabilities(client).Lookup fails ⇒
//     ActionLog (capability_degraded); Reason = "[id] message" lines of hits
//     at Verdict.Action (warn/ask/deny) in rule order, ≤ model.BudgetReason
//     runes, truncated with "…(+N)".
//   - Regexes compile lazily on first use and are cached per Engine
//     (CompiledRuleSet is per-process; no global mutable state).
//
// Acceptance (U3, unit level — fakes only, no other unit's implementation):
//   - testdata/tri.json (Kleene tables + binding OR semantics) passes.
//   - Lint and TestRules over every rule in schema/examples/ whose examples
//     are all claude-code (no "client" other than claude-code), using fake
//     ports: a fake AdapterRegistry whose claude-code ParseEvent/
//     NormalizeTool decodes the M0a payload shape (tool_name, tool_input,
//     prompt, last_assistant_message, cwd), a fake PathService (join with
//     cwd + clean, "~/" expansion, exact/doublestar matching, no
//     symlinks), and fake Bash/PowerShell parsers returning canned
//     CommandLists keyed by command string (U3 writes these fixtures in its
//     own testdata). Examples with client codex/cursor/hermes are SKIPPED
//     at unit level and reported as skipped.
//   - Lint rejects: input_regex.command on Edit (field-not-applicable),
//     action ask on codex (action-unsupported), deny on PostToolUse,
//     "!x"-only glob lists, non-RE2 regexes (backreference/lookaround),
//     Cond objects with ≠1 key, >12 enabled rules for --layer org.
//   - Evaluate never panics (fuzz with random EvalInput) and returns
//     TriUnknown for parse errors of shell commands.
//   - Engine implements ports.RuleCompiler and ports.RuleEvaluator.
//
// Integration acceptance (U12, real adapters/paths/parsers wired):
// `ballast lint --client all` and `ballast test-rules` pass over ALL of
// schema/examples/, rules/, rules/optional/, examples/ and
// testdata/bash-regress.json, including the codex apply_patch examples.
package dsl
