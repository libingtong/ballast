// Package hermes is the Hermes Agent (tier 3, best effort). snake_case events; pre_tool_call deny ⇒ {"decision":"block","reason":R}; pre_llm_call context ⇒ {"context":C} (prompt from extra.user_message, IsFirstTurn from extra.is_first_turn); everything else renders empty. Tool map: terminal/shell/execute_shell⇒Bash, read_file⇒Read, write_file/create_file⇒Write, patch_file/edit_file⇒Edit.
//
// Unit U6. See internal/adapter package doc for the shared contract.
package hermes
