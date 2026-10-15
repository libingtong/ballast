// Package codex is the Codex (tier 2). Events: SessionStart, UserPromptSubmit, PreToolUse, PermissionRequest (→PreToolUse; deny rendered as hookSpecificOutput.decision.behavior="deny"), PostToolUse, Stop. ask is never rendered. Empty decision ⇒ empty stdout (Codex treats it as allow={}); NEVER permissionDecision:"allow". Tool normalization: apply_patch ⇒ Edit (ParseApplyPatch), exec_command/shell ⇒ Bash. Hooks only fire in the interactive TUI (pinrule observation, unverified).
//
// Unit U6. See internal/adapter package doc for the shared contract.
package codex
