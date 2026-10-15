// Package claudecode is the Claude Code (tier 1, full support). Input: Claude hook stdin (docs/facts/payloads). PowerShell tool stays PowerShell (Shell=powershell); Bash has Shell=bash. Render shapes: model.Capabilities(claude-code). Inject-only output: {"hookSpecificOutput":{"hookEventName":<native>,"additionalContext":C}} on SessionStart, UserPromptSubmit, PostToolBatch, PostToolUse(Failure).
//
// Unit U6. See internal/adapter package doc for the shared contract.
package claudecode
