// Package stats (unit U9) computes rule statistics from the audit log,
// samples hits for review, and promotes rules (plan B2).
//
// Not hot path. Imports: model, ports, sys, layout, stdlib. The audit log
// is read through ports.AuditSource (audit.Reader in production). Rule-file rewriting for promote goes through the injected
// PromoteDeps (no direct dsl/config import).
//
// Semantics (frozen):
//   - evals/unknown come ONLY from eval_summary records (sum of Evals);
//     hits = count of `hit` records with decision ∈ {warn, ask, deny};
//     would = `hit` records with decision would_*; sessions = distinct
//     session_id among hit records. Row action/target = from the most recent
//     hit or eval_summary generation's rule (or the current rules when given).
//   - --since accepts Nd / Nh / RFC3339; default 7d. --by rule|session|repo.
//   - Sample(rule, n): the n most recent hit records (decision incl.
//     would_*) with their Context (≤2 summaries) and Matched; writes
//     reviews/<rule_id>.json {v:1, rule_id, generation, reviewed_at,
//     sample_ids:[...], samples:[...]} atomically. sample id =
//     hex(sha256(ts|session_id|tool_use_id|rule_id))[:16].
//   - Promote(id, to): rule must exist in the repo or user layer (org ⇒
//     refused, exit 4); `to` must be stricter than the current action and
//     pass dsl capability checks for the installed clients; gate: hits+would
//     in the last Window (default 7d) ≥ MinHits (default 5) AND a review
//     file newer than the rule's last change exists. --force skips the gate
//     (never the capability check) and is audited. The rule file is
//     rewritten with only that rule's "action" (and "target" if equal)
//     changed: JSON re-encoded with 2-space indentation, key order preserved
//     via a token-level rewrite of the single value (implementation detail
//     of U9; must round-trip through dsl.DecodeRuleFile). Audit type "promote".
//
// Acceptance (U9): golden stats over testdata/audit-sample.jsonl;
// promote refuses without review, succeeds with review, --force audited.
package stats
