// Package hermes is the Hermes Agent (tier 3, best effort). snake_case events; pre_tool_call deny ⇒ {"decision":"block","reason":R}; pre_llm_call context ⇒ {"context":C} (prompt from extra.user_message, IsFirstTurn from extra.is_first_turn); everything else renders empty. Tool map: terminal/shell/execute_shell⇒Bash, read_file⇒Read, write_file/create_file⇒Write, patch/patch_file/edit_file⇒Edit (V4A mode: every file of the envelope in `patch`, see patch.go); execute_code keeps its raw name.
//
// P2 (docs/p2-design.md section 7.1, offline `hermes hooks test` evidence in
// docs/facts/hermes-cases.json): pre_verify ⇒ EventStop (final answer from
// extra.final_response, extra.attempt>0 ⇒ StopHookActive); a Stop deny renders
// {"decision":"block","reason":R}, which Hermes reads as "keep going"
// (continue semantics, bounded by agent.max_verify_nudges; lint warns with
// hermes-stop-continue). Verified means "serialization, output parsing and
// blocking semantics verified offline"; WHEN a real agent fires the hooks is
// not verified.
//
// patch.go is a copy of internal/adapter/codex/patch.go (with the Hermes
// "*** Move File: a -> b" dialect and a whole-text header scan added); the two
// copies are not shared on purpose (adapter/codex and adapter/common are not
// touched by P2-HERMES).
//
// Unit U6. See internal/adapter package doc for the shared contract.
package hermes
