// Package cursor is the Cursor (tier 2). camelCase events; session = conversation_id; agent = subagent_id. Gate events preToolUse/beforeShellExecution/beforeMCPExecution/beforeReadFile render {"permission":"deny"|"ask","user_message":R,"agent_message":R}. sessionStart/postToolUse inject via {"additional_context":C}; beforeSubmitPrompt via Claude nested hookSpecificOutput. stop/subagentStop deny ⇒ {"followup_message":R} (continuation semantics). afterAgentResponse is audit-only: the text field is stored in state for a later stop. Tool map: Shell⇒Bash, Task⇒Agent, Write stays Write (Cursor has no Edit).
//
// Unit U6. See internal/adapter package doc for the shared contract.
package cursor
