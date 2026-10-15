package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// e2eRules are extra rules (beyond the shipped defaults) that give every
// client protocol path a deterministic trigger word.
const e2eRules = `[
 {"id":"e2e-stop-block","action":"deny","event":"Stop",
  "match":{"text":{"source":"last_assistant_message","regex":["BLOCKME"]}},
  "message":"e2e stop block",
  "examples":[{"name":"hit","expect":"hit","input":{"last_assistant_message":"BLOCKME now"}},
              {"name":"miss","expect":"miss","input":{"last_assistant_message":"all fine"}}]},
 {"id":"e2e-stop-warn","action":"warn","event":"Stop","clients":["claude-code"],
  "match":{"text":{"source":"last_assistant_message","regex":["WARNME"]}},
  "message":"e2e stop warn",
  "examples":[{"name":"hit","expect":"hit","input":{"last_assistant_message":"WARNME now"}},
              {"name":"miss","expect":"miss","input":{"last_assistant_message":"all fine"}}]},
 {"id":"e2e-prompt-warn","action":"warn","event":"UserPromptSubmit",
  "match":{"text":{"source":"prompt","regex":["PINGPONG"]}},
  "message":"e2e prompt warn",
  "examples":[{"name":"hit","expect":"hit","input":{"prompt":"PINGPONG"}},
              {"name":"miss","expect":"miss","input":{"prompt":"hello"}}]},
 {"id":"e2e-post-warn","action":"warn","event":"PostToolUse","clients":["claude-code","codex","cursor"],
  "match":{"tool":["Bash"],"text":{"source":"tool_response","regex":["LEAKED"]}},
  "message":"e2e post warn",
  "examples":[{"name":"hit","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"echo"},"tool_response":{"stdout":"LEAKED","stderr":""}}},
              {"name":"miss","expect":"miss","input":{"tool_name":"Bash","tool_input":{"command":"echo"},"tool_response":{"stdout":"fine","stderr":""}}}]},
 {"id":"e2e-shadow","action":"log","target":"deny","event":"PreToolUse",
  "match":{"bash":{"cmd":"frobnicate"}},
  "message":"e2e shadow rule",
  "examples":[{"name":"hit","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"frobnicate --all"}}},
              {"name":"miss","expect":"miss","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}}]}
]`

// installE2ERules writes the user-layer rules: the shipped default pack with
// read-before-edit and no-long-sleep promoted to deny, plus e2eRules.
func installE2ERules(t *testing.T, s *sandbox) string {
	t.Helper()
	var pack struct {
		Schema  string           `json:"$schema,omitempty"`
		Version int              `json:"version"`
		Rules   []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repoRoot, "rules", "default.json"))), &pack); err != nil {
		t.Fatal(err)
	}
	pack.Schema = ""
	for _, r := range pack.Rules {
		if id := r["id"]; id == "read-before-edit" || id == "no-long-sleep" {
			r["action"] = "deny"
		}
	}
	var extra []map[string]any
	if err := json.Unmarshal([]byte(e2eRules), &extra); err != nil {
		t.Fatal(err)
	}
	pack.Rules = append(pack.Rules, extra...)
	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.sb.BallastHome, "rules.json")
	writeFile(t, path, string(data))
	// The generated file must itself be valid: lint with examples, all clients.
	s.mustExit(0, "", "", "lint", "--client", "all", "--examples", path)
	s.mustExit(0, "", "", "test-rules", path)
	return path
}

// hook runs `ballast hook` for client/event with a JSON payload on stdin.
func (s *sandbox) hook(client, event string, payload map[string]any) result {
	s.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.run("", string(b), "hook", "--client", client, "--event", event)
}

// wantEmpty asserts the "no decision" protocol: exit 0, empty stdout.
func wantEmpty(t *testing.T, what string, r result) {
	t.Helper()
	if r.code != 0 || r.stdout != "" {
		t.Errorf("%s: exit %d stdout %q stderr %q; want exit 0 and empty stdout", what, r.code, r.stdout, r.stderr)
	}
}

// wantJSON asserts exit 0 and a single JSON object line on stdout.
func wantJSON(t *testing.T, what string, r result) map[string]any {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("%s: exit %d stdout %q stderr %q", what, r.code, r.stdout, r.stderr)
	}
	if !strings.HasSuffix(r.stdout, "\n") || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("%s: stdout must be exactly one JSON line, got %q", what, r.stdout)
	}
	return jsonOut(t, r.stdout)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func TestHookClaudeCode(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	a := filepath.Join(s.work, "a.go")
	writeFile(t, a, "package a\n")
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"session_id": "cc-1", "cwd": s.work}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	out := wantJSON(t, "SessionStart", s.hook("claude-code", "SessionStart", base(map[string]any{"source": "startup"})))
	if dig(out, "hookSpecificOutput", "hookEventName") != "SessionStart" || !strings.Contains(str(dig(out, "hookSpecificOutput", "additionalContext")), "no-long-sleep") {
		t.Errorf("SessionStart should inject the rule pack: %v", out)
	}

	out = wantJSON(t, "UserPromptSubmit", s.hook("claude-code", "UserPromptSubmit", base(map[string]any{"prompt": "PINGPONG"})))
	if !strings.Contains(str(dig(out, "hookSpecificOutput", "additionalContext")), "[e2e-prompt-warn]") {
		t.Errorf("UserPromptSubmit warn missing: %v", out)
	}

	// PreToolUse deny and the empty "no decision" case.
	out = wantJSON(t, "PreToolUse sleep", s.hook("claude-code", "PreToolUse", base(map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "sleep 300"}, "tool_use_id": "t1"})))
	if dig(out, "hookSpecificOutput", "permissionDecision") != "deny" || !strings.Contains(str(dig(out, "hookSpecificOutput", "permissionDecisionReason")), "[no-long-sleep]") {
		t.Errorf("sleep 300 should be denied: %v", out)
	}
	wantEmpty(t, "PreToolUse ls", s.hook("claude-code", "PreToolUse", base(map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_use_id": "t2"})))

	// read-before-edit: deny, then allowed after a PostToolUse Read.
	edit := base(map[string]any{"tool_name": "Edit", "tool_use_id": "t3",
		"tool_input": map[string]any{"file_path": a, "old_string": "a", "new_string": "b"}})
	out = wantJSON(t, "Edit unread", s.hook("claude-code", "PreToolUse", edit))
	if dig(out, "hookSpecificOutput", "permissionDecision") != "deny" || !strings.Contains(str(dig(out, "hookSpecificOutput", "permissionDecisionReason")), "[read-before-edit]") {
		t.Errorf("unread edit should be denied: %v", out)
	}
	wantEmpty(t, "PostToolUse Read", s.hook("claude-code", "PostToolUse", base(map[string]any{
		"tool_name": "Read", "tool_input": map[string]any{"file_path": a}, "tool_response": map[string]any{"content": "package a"}, "tool_use_id": "t4"})))
	edit["tool_use_id"] = "t5"
	wantEmpty(t, "Edit after Read", s.hook("claude-code", "PreToolUse", edit))

	out = wantJSON(t, "PostToolUse warn", s.hook("claude-code", "PostToolUse", base(map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "echo"}, "tool_response": map[string]any{"stdout": "LEAKED", "stderr": ""}, "tool_use_id": "t6"})))
	if dig(out, "hookSpecificOutput", "hookEventName") != "PostToolUse" || !strings.Contains(str(dig(out, "hookSpecificOutput", "additionalContext")), "[e2e-post-warn]") {
		t.Errorf("PostToolUse warn missing: %v", out)
	}

	// Stop: deny → decision:block; warn → systemMessage; nothing → empty.
	out = wantJSON(t, "Stop block", s.hook("claude-code", "Stop", base(map[string]any{"last_assistant_message": "BLOCKME", "stop_hook_active": false})))
	if out["decision"] != "block" || !strings.Contains(str(out["reason"]), "[e2e-stop-block]") {
		t.Errorf("Stop block: %v", out)
	}
	out = wantJSON(t, "Stop warn", s.hook("claude-code", "Stop", base(map[string]any{"last_assistant_message": "WARNME"})))
	if !strings.Contains(str(out["systemMessage"]), "[e2e-stop-warn]") || out["decision"] != nil {
		t.Errorf("Stop warn: %v", out)
	}
	wantEmpty(t, "Stop fine", s.hook("claude-code", "Stop", base(map[string]any{"last_assistant_message": "all good"})))
	wantEmpty(t, "SessionEnd", s.hook("claude-code", "SessionEnd", base(nil)))
}

func TestHookCodex(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	a := filepath.Join(s.work, "a.go")
	writeFile(t, a, "package a\n")
	base := func(sid string, extra map[string]any) map[string]any {
		m := map[string]any{"session_id": sid, "cwd": s.work}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	const patch = "*** Begin Patch\n*** Update File: a.go\n@@\n-package a\n+package b\n*** End Patch\n"

	out := wantJSON(t, "SessionStart", s.hook("codex", "SessionStart", base("cx-0", map[string]any{"source": "startup"})))
	if dig(out, "hookSpecificOutput", "hookEventName") != "SessionStart" {
		t.Errorf("SessionStart: %v", out)
	}
	out = wantJSON(t, "PreToolUse sleep", s.hook("codex", "PreToolUse", base("cx-0", map[string]any{
		"tool_name": "exec_command", "tool_input": map[string]any{"cmd": "sleep 300"}})))
	if dig(out, "hookSpecificOutput", "permissionDecision") != "deny" {
		t.Errorf("codex deny shape: %v", out)
	}
	out = wantJSON(t, "PermissionRequest sleep", s.hook("codex", "PermissionRequest", base("cx-0", map[string]any{
		"tool_name": "exec_command", "tool_input": map[string]any{"cmd": []string{"bash", "-lc", "sleep 300"}}})))
	if dig(out, "hookSpecificOutput", "hookEventName") != "PermissionRequest" || dig(out, "hookSpecificOutput", "decision", "behavior") != "deny" {
		t.Errorf("codex PermissionRequest deny shape: %v", out)
	}
	out = wantJSON(t, "UserPromptSubmit", s.hook("codex", "UserPromptSubmit", base("cx-0", map[string]any{"prompt": "PINGPONG"})))
	if !strings.Contains(str(dig(out, "hookSpecificOutput", "additionalContext")), "[e2e-prompt-warn]") {
		t.Errorf("UserPromptSubmit: %v", out)
	}
	out = wantJSON(t, "Stop", s.hook("codex", "Stop", base("cx-0", map[string]any{"last_assistant_message": "BLOCKME"})))
	if out["decision"] != "block" {
		t.Errorf("codex Stop: %v", out)
	}

	// Control: without a prior read, apply_patch on an existing file is denied.
	out = wantJSON(t, "apply_patch unread", s.hook("codex", "PreToolUse", base("cx-ctl", map[string]any{
		"tool_name": "apply_patch", "tool_input": map[string]any{"input": patch}})))
	if dig(out, "hookSpecificOutput", "permissionDecision") != "deny" || !strings.Contains(str(dig(out, "hookSpecificOutput", "permissionDecisionReason")), "[read-before-edit]") {
		t.Fatalf("control: unread apply_patch should hit read-before-edit: %v", out)
	}

	// Acceptance (architecture §8.3): read through a shell command, then
	// apply_patch the same file: no read-before-edit hit.
	sess := "cx-read"
	wantEmpty(t, "exec_command sed PreToolUse", s.hook("codex", "PreToolUse", base(sess, map[string]any{
		"tool_name": "exec_command", "tool_input": map[string]any{"cmd": "sed -n 1,80p a.go"}, "tool_use_id": "c1"})))
	wantEmpty(t, "exec_command sed PostToolUse", s.hook("codex", "PostToolUse", base(sess, map[string]any{
		"tool_name": "exec_command", "tool_input": map[string]any{"cmd": "sed -n 1,80p a.go"},
		"tool_response": map[string]any{"output": "package a"}, "tool_use_id": "c1"})))
	r := s.hook("codex", "PreToolUse", base(sess, map[string]any{
		"tool_name": "apply_patch", "tool_input": map[string]any{"input": patch}, "tool_use_id": "c2"}))
	wantEmpty(t, "apply_patch after sed -n", r)
	if strings.Contains(r.stdout+r.stderr, "read-before-edit") {
		t.Errorf("read-before-edit must not fire after a shell read: %q %q", r.stdout, r.stderr)
	}
	// And the audit trail has no read-before-edit hit for that session.
	for _, rec := range auditRecords(t, s) {
		if rec["session_id"] == sess && rec["rule_id"] == "read-before-edit" && rec["type"] == "hit" {
			t.Errorf("audit shows a read-before-edit hit in session %s: %v", sess, rec)
		}
	}
}

func TestHookCursor(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"conversation_id": "cu-1", "cwd": s.work}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	out := wantJSON(t, "sessionStart", s.hook("cursor", "sessionStart", base(nil)))
	if !strings.Contains(str(out["additional_context"]), "no-long-sleep") {
		t.Errorf("sessionStart additional_context: %v", out)
	}
	for _, ev := range []string{"beforeShellExecution"} {
		out = wantJSON(t, ev, s.hook("cursor", ev, base(map[string]any{"command": "sleep 300"})))
		if out["permission"] != "deny" || !strings.Contains(str(out["user_message"]), "[no-long-sleep]") || out["agent_message"] != out["user_message"] {
			t.Errorf("%s deny shape: %v", ev, out)
		}
	}
	out = wantJSON(t, "preToolUse Shell", s.hook("cursor", "preToolUse", base(map[string]any{
		"tool_name": "Shell", "tool_input": map[string]any{"command": "sleep 300"}})))
	if out["permission"] != "deny" {
		t.Errorf("preToolUse deny shape: %v", out)
	}
	wantEmpty(t, "beforeShellExecution ls", s.hook("cursor", "beforeShellExecution", base(map[string]any{"command": "ls"})))
	out = wantJSON(t, "beforeSubmitPrompt", s.hook("cursor", "beforeSubmitPrompt", base(map[string]any{"prompt": "PINGPONG"})))
	if dig(out, "hookSpecificOutput", "hookEventName") != "UserPromptSubmit" || !strings.Contains(str(dig(out, "hookSpecificOutput", "additionalContext")), "[e2e-prompt-warn]") {
		t.Errorf("beforeSubmitPrompt nested context: %v", out)
	}
	out = wantJSON(t, "postToolUse", s.hook("cursor", "postToolUse", base(map[string]any{
		"tool_name": "Shell", "tool_input": map[string]any{"command": "echo"}, "tool_output": "LEAKED", "tool_response": map[string]any{"stdout": "LEAKED"}})))
	if !strings.Contains(str(out["additional_context"]), "[e2e-post-warn]") {
		t.Errorf("postToolUse additional_context: %v", out)
	}
	// Cursor's stop has no text of its own: afterAgentResponse stores it.
	wantEmpty(t, "afterAgentResponse", s.hook("cursor", "afterAgentResponse", base(map[string]any{"text": "BLOCKME"})))
	out = wantJSON(t, "stop", s.hook("cursor", "stop", base(map[string]any{"status": "completed"})))
	if !strings.Contains(str(out["followup_message"]), "[e2e-stop-block]") {
		t.Errorf("stop followup_message: %v", out)
	}
}

func TestHookHermes(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"session_id": "he-1", "cwd": s.work}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	out := wantJSON(t, "pre_tool_call", s.hook("hermes", "pre_tool_call", base(map[string]any{
		"tool_name": "terminal", "tool_input": map[string]any{"command": "sleep 300"}})))
	if out["decision"] != "block" || !strings.Contains(str(out["reason"]), "[no-long-sleep]") {
		t.Errorf("hermes block shape: %v", out)
	}
	wantEmpty(t, "pre_tool_call ls", s.hook("hermes", "pre_tool_call", base(map[string]any{
		"tool_name": "terminal", "tool_input": map[string]any{"command": "ls"}})))
	out = wantJSON(t, "pre_llm_call first", s.hook("hermes", "pre_llm_call", base(map[string]any{
		"extra": map[string]any{"user_message": "PINGPONG", "is_first_turn": true}})))
	ctx := str(out["context"])
	if !strings.Contains(ctx, "[e2e-prompt-warn]") || !strings.Contains(ctx, "no-long-sleep") {
		t.Errorf("hermes context should carry the warn and the full rule text on the first turn: %v", out)
	}
	wantEmpty(t, "post_tool_call", s.hook("hermes", "post_tool_call", base(map[string]any{
		"tool_name": "terminal", "tool_input": map[string]any{"command": "echo"}, "tool_response": "LEAKED"})))
	wantEmpty(t, "on_session_start", s.hook("hermes", "on_session_start", base(nil)))
	wantEmpty(t, "on_session_end", s.hook("hermes", "on_session_end", base(nil)))
}

// TestHookAbnormalProtocol: every abnormal invocation exits 1 with an empty
// stdout and a stderr line that starts with "ballast:". Exit 2 would block
// the tool call and must never appear.
func TestHookAbnormalProtocol(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	events := map[string]string{"claude-code": "PreToolUse", "codex": "PreToolUse", "cursor": "preToolUse", "hermes": "pre_tool_call"}
	type tc struct {
		name  string
		stdin string
		args  []string
	}
	var cases []tc
	for client, ev := range events {
		base := []string{"hook", "--client", client, "--event", ev}
		cases = append(cases,
			tc{client + " corrupt json", "{not json", base},
			tc{client + " json array", "[1,2]", base},
			tc{client + " json string", `"x"`, base},
			tc{client + " empty stdin", "", base},
			tc{client + " truncated", `{"session_id":"s","tool_na`, base},
			tc{client + " unknown event", `{"session_id":"s"}`, []string{"hook", "--client", client, "--event", "NoSuchEvent"}},
		)
	}
	cases = append(cases,
		tc{"unknown client", `{"session_id":"s"}`, []string{"hook", "--client", "nobody", "--event", "PreToolUse"}},
		tc{"event of another client", `{"session_id":"s"}`, []string{"hook", "--client", "claude-code", "--event", "preToolUse"}},
		tc{"missing client", `{}`, []string{"hook", "--event", "PreToolUse"}},
		tc{"missing event", `{}`, []string{"hook", "--client", "claude-code"}},
		tc{"unknown flag", `{}`, []string{"hook", "--client", "claude-code", "--event", "PreToolUse", "--bogus"}},
		tc{"positional argument", `{}`, []string{"hook", "claude-code"}},
		tc{"no arguments", `{}`, []string{"hook"}},
	)
	sort.Slice(cases, func(i, j int) bool { return cases[i].name < cases[j].name })
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := s.run("", c.stdin, c.args...)
			if r.code != 1 {
				t.Errorf("exit %d, want 1 (stdout %q stderr %q)", r.code, r.stdout, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout must be empty, got %q", r.stdout)
			}
			if !strings.HasPrefix(r.stderr, "ballast:") {
				t.Errorf("stderr must start with \"ballast:\", got %q", r.stderr)
			}
		})
	}
}

// TestHookFactsPayloads runs every captured Claude Code payload of
// docs/facts/payloads through the complete binary: no sample may make the
// hook fail, and any stdout must be a single JSON object.
func TestHookFactsPayloads(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	installed := map[string]bool{}
	for _, ne := range model.NativeEventTable[model.ClientClaude] {
		installed[ne.Native] = true
	}
	root := filepath.Join(repoRoot, "docs", "facts", "payloads")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			name := strings.TrimSuffix(f.Name(), ".json")
			_, event, ok := strings.Cut(name, "-")
			if !ok || !installed[event] {
				// PreCompact, PostCompact and SubagentStart are not hooks
				// Ballast installs (model.NativeEventTable), so Claude Code
				// never sends them to the binary.
				continue
			}
			ran++
			t.Run(d.Name()+"/"+name, func(t *testing.T) {
				payload := readFile(t, filepath.Join(root, d.Name(), f.Name()))
				r := s.run("", payload, "hook", "--client", "claude-code", "--event", event)
				if r.code != 0 {
					t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
				}
				if r.stdout != "" {
					jsonOut(t, r.stdout)
				}
				if r.stderr != "" {
					t.Logf("stderr: %s", r.stderr)
				}
			})
		}
	}
	if ran < 40 {
		t.Errorf("only %d payload files ran; the capture set shrank", ran)
	}
}

func TestHookHelpHasNoSideEffects(t *testing.T) {
	s := newSandbox(t)
	// A home that does not exist: if --help touched anything under it the
	// directory would appear.
	s.setenv("BALLAST_HOME", filepath.Join(s.sb.Root, "nope", ".ballast"))
	s.setenv("HOME", filepath.Join(s.sb.Root, "nope"))
	for _, args := range [][]string{
		{"--help"}, {"-h"}, {"help"}, {"hook", "--help"}, {"hook", "-h"},
		{"lint", "--help"}, {"test-rules", "-h"}, {"config", "show", "--help"}, {"verify", "--help"}, {"verify", "--check", "-h"},
		{"init", "--help"}, {"stats", "--help"}, {"rules", "promote", "--help"}, {"replay", "--help"}, {"audit", "lint", "--help"},
		{"install", "--help"}, {"uninstall", "--help"}, {"doctor", "--help"}, {"org", "render", "--help"}, {"gc", "--help"}, {"version", "--help"},
	} {
		r := s.run("", "", args...)
		if r.code != 0 || r.stdout == "" || r.stderr != "" {
			t.Errorf("ballast %v: exit %d stdout %q stderr %q; want exit 0, help on stdout", args, r.code, r.stdout, r.stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(s.sb.Root, "nope")); err == nil {
		t.Errorf("--help created %s", filepath.Join(s.sb.Root, "nope"))
	}
	if ents, _ := os.ReadDir(s.work); len(ents) != 0 {
		t.Errorf("--help left files in the working directory: %v", ents)
	}
}

func TestUnknownCommandAndNoArgs(t *testing.T) {
	s := newSandbox(t)
	r := s.mustExit(2, "", "", "frobnicate")
	if !strings.HasPrefix(r.stderr, "ballast:") || r.stdout != "" {
		t.Errorf("unknown command: stdout %q stderr %q", r.stdout, r.stderr)
	}
	s.mustExit(2, "", "")
	r = s.mustExit(0, "", "", "version")
	if !strings.HasPrefix(r.stdout, "ballast ") {
		t.Errorf("version output: %q", r.stdout)
	}
}

// auditRecords reads every audit line under BALLAST_HOME.
func auditRecords(t *testing.T, s *sandbox) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(s.sb.BallastHome, "audit", "*.jsonl"))
	var out []map[string]any
	for _, f := range files {
		for _, line := range strings.Split(readFile(t, f), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("audit line is not JSON: %v: %s", err, line)
			}
			out = append(out, m)
		}
	}
	return out
}
