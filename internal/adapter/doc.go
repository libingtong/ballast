// Package adapter (unit U6) hosts the per-client protocol adapters
// (subpackages claudecode, codex, cursor, hermes), their shared helpers
// (subpackage common) and the default registry.
//
// Hot path: yes. Imports: model, ports, stdlib. No regexp at init.
//
// Frozen rules (docs/architecture.md §4–§5):
//   - The client is given by the command line (--client); adapters never
//     sniff. ParseEvent receives the native event name from --event and
//     maps it with model.NativeEventTable (unknown ⇒ model.ErrUnknownEvent).
//   - Optional-field tolerance: every field may be missing or mistyped; a
//     mistyped field is ignored (zero), never an error (M0a conclusion 6).
//     `effort` may be an object {"level":…} or a string. Unknown keys ignored.
//   - Session id: session_id, else conversation_id (Cursor), else
//     sessionId; agent id: agent_id, else subagent_id (Cursor).
//   - NormalizeTool: Codex apply_patch ⇒ Edit with Targets = every
//     "*** Update/Add/Delete File:" path (Add ⇒ TargetAdds, Delete ⇒
//     TargetDeletes, "*** Move to:" adds the destination), Edits from hunks
//     (+ lines → NewString, - lines → OldString); exec_command / shell ⇒
//     Bash (command from "cmd" string or argv array joined with
//     shell-quoting, "command" fallback; Shell=bash); Cursor Shell ⇒ Bash;
//     Hermes terminal/shell/execute_shell ⇒ Bash, read_file ⇒ Read,
//     write_file/create_file ⇒ Write, patch_file/edit_file ⇒ Edit;
//     Task ⇒ Agent; Claude PowerShell stays PowerShell (Shell=powershell);
//     dedicated gate events use NativeEvent.ToolHint (Cursor
//     beforeShellExecution ⇒ Bash with command from "command";
//     beforeReadFile ⇒ Read with file_path from "file_path"/"path";
//     beforeMCPExecution ⇒ "mcp__<server>__<tool>" from server/tool_name).
//     Fields: every string leaf of tool_input, keyed by dotted path with
//     array indices dropped ("edits.new_string"). Edits: Edit ⇒ one
//     {file_path, old_string, new_string}; MultiEdit ⇒ one per edits[];
//     Write ⇒ {file_path, "", content}; NotebookEdit ⇒ {notebook_path, "",
//     new_source}; apply_patch ⇒ per file from hunks. Targets: file_path /
//     notebook_path / every patch target (raw strings, not normalized).
//   - Render: d.Empty() ⇒ nil. Never output allow/approve/permission:"allow".
//     The exact shapes are model.Capabilities(c).Cells[i].Render plus the
//     inject-only shapes in testdata/render.json. Context and Reason are
//     joined as Reason + "\n\n" + Context when both are rendered into the
//     same field. JSON is compact, UTF-8, no HTML escaping, newline-terminated.
//   - Render on an unsupported (event, action) returns an error (the hook
//     degrades before calling Render, so this indicates a bug ⇒ fail open).
//
// Acceptance (U6): testdata/render.json golden rows (every client × every
// supported cell + inject-only + empty); docs/facts/payloads/** parse
// without error through claudecode.ParseEvent (fields asserted in
// testdata/parse.json); fuzz ParseEvent never panics; each adapter's
// Capabilities()/NativeEvents() equal the model tables.
package adapter
