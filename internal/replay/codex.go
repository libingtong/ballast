package replay

import (
	"encoding/json"
	"fmt"
	"strings"
)

// codexLine is one rollout record. Current rollouts wrap the body in
// "payload" with the record kind in "type"; older ones are the body itself.
type codexLine struct {
	Timestamp json.RawMessage `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexBody struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Content   json.RawMessage `json:"content"`
	LastMsg   string          `json:"last_agent_message"`
}

func (r *reader) parseCodex(line []byte) bool {
	var cl codexLine
	if !unmarshalLenient(line, &cl) {
		return false
	}
	var body codexBody
	kind := cl.Type
	if len(cl.Payload) > 0 && cl.Payload[0] == '{' {
		unmarshalLenient(cl.Payload, &body)
	} else {
		// Legacy flat record: the line is the body.
		unmarshalLenient(line, &body)
		kind = "response_item"
	}

	meta := Item{TS: parseTS(cl.Timestamp), SessionID: r.sessionID, Cwd: r.cwd}
	base := fmt.Sprintf("line-%d", r.lr.n)
	meta.UUID = base

	var out []Item
	switch kind {
	case "session_meta":
		if body.ID != "" {
			r.sessionID = body.ID
		}
		if body.Cwd != "" {
			r.cwd = body.Cwd
		}
		return true
	case "turn_context":
		if body.Cwd != "" {
			r.cwd = body.Cwd
		}
		return true
	case "compacted":
		it := meta
		it.Kind = ItemCompact
		out = append(out, it)
	case "event_msg":
		if body.Type == "task_complete" || body.Type == "turn_complete" {
			it := meta
			it.Kind = ItemTurnEnd
			it.Text = body.LastMsg
			if it.Text == "" && r.openText {
				it.Text = r.lastText
			}
			out = append(out, it)
		}
	case "response_item":
		out = r.codexResponse(out, meta, &body)
	default:
		return true
	}
	r.enqueue(out, base)
	return true
}

func (r *reader) codexResponse(out []Item, meta Item, b *codexBody) []Item {
	switch b.Type {
	case "message":
		text := codexText(b.Content)
		if strings.TrimSpace(text) == "" {
			return out
		}
		switch b.Role {
		case "user":
			t := strings.TrimSpace(text)
			if strings.HasPrefix(t, "<environment_context>") || strings.HasPrefix(t, "<user_instructions>") || strings.HasPrefix(t, "# AGENTS.md") {
				return out
			}
			it := meta
			it.Kind = ItemUserPrompt
			it.Text = text
			return append(out, it)
		case "assistant":
			it := meta
			it.Kind = ItemAssistantText
			it.Text = text
			return append(out, it)
		}
	case "function_call", "local_shell_call":
		it := meta
		it.Kind = ItemToolUse
		it.ToolName = b.Name
		if b.Type == "local_shell_call" && it.ToolName == "" {
			it.ToolName = "shell"
		}
		it.ToolUseID = b.CallID
		it.ToolInput = codexArguments(b.Arguments)
		return append(out, it)
	case "custom_tool_call":
		it := meta
		it.Kind = ItemToolUse
		it.ToolName = b.Name
		it.ToolUseID = b.CallID
		var patch string
		if len(b.Input) > 0 && b.Input[0] == '"' {
			_ = json.Unmarshal(b.Input, &patch)
		} else {
			patch = string(b.Input)
		}
		// The hook payload key for the apply_patch envelope is unverified
		// (docs/architecture.md §4); provide both spellings.
		in, _ := json.Marshal(map[string]string{"command": patch, "input": patch})
		it.ToolInput = in
		return append(out, it)
	case "function_call_output", "custom_tool_call_output":
		it := meta
		it.Kind = ItemToolResult
		it.ToolUseID = b.CallID
		it.Text = codexOutput(b.Output)
		return append(out, it)
	}
	return out
}

// codexText joins the input_text/output_text/text parts of a message.
func codexText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []block
	unmarshalLenient(raw, &parts)
	var texts []string
	for i := range parts {
		switch parts[i].Type {
		case "input_text", "output_text", "text":
			texts = append(texts, parts[i].Text)
		}
	}
	return strings.Join(texts, "\n")
}

// codexArguments turns the "arguments" field (a JSON-encoded string in
// rollouts, an object in some versions) into the tool input object.
func codexArguments(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		if json.Valid([]byte(s)) && strings.HasPrefix(strings.TrimSpace(s), "{") {
			return []byte(s)
		}
		in, _ := json.Marshal(map[string]string{"raw": s})
		return in
	}
	return append([]byte(nil), raw...)
}

// codexOutput extracts the text of a function_call_output "output".
func codexOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if raw[0] == '"' {
		_ = json.Unmarshal(raw, &s)
	} else {
		s = string(raw)
	}
	// Outputs are often a JSON document {"output": "...", "metadata": {...}}.
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") {
		var doc struct {
			Output  *string         `json:"output"`
			Content *string         `json:"content"`
			Raw     json.RawMessage `json:"-"`
		}
		if json.Unmarshal([]byte(t), &doc) == nil {
			switch {
			case doc.Output != nil:
				return *doc.Output
			case doc.Content != nil:
				return *doc.Content
			}
		}
	}
	return s
}
