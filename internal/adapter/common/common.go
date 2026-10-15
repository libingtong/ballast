package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// utf8BOM is stripped from hook stdin (Windows shells may prepend it).
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Fields is a tolerant view over a JSON object's top-level keys.
type Fields map[string]json.RawMessage

// DecodeObject decodes stdin into Fields; non-object ⇒ model.ErrBadInput.
func DecodeObject(stdin []byte) (Fields, error) {
	stdin = bytes.TrimPrefix(stdin, utf8BOM)
	var f map[string]json.RawMessage
	if err := json.Unmarshal(stdin, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", model.ErrBadInput, err)
	}
	if f == nil { // literal null
		return nil, fmt.Errorf("%w: not a JSON object", model.ErrBadInput)
	}
	return Fields(f), nil
}

// Has reports whether key is present (even when null).
func (f Fields) Has(key string) bool {
	_, ok := f[key]
	return ok
}

// Str returns the string at key; mistyped/missing ⇒ ("", false).
func (f Fields) Str(key string) (string, bool) {
	raw := f[key]
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// FirstStr returns the first non-empty string among keys ("" when none).
func (f Fields) FirstStr(keys ...string) string {
	for _, k := range keys {
		if s, ok := f.Str(k); ok && s != "" {
			return s
		}
	}
	return ""
}

// Bool returns the bool at key; mistyped/missing ⇒ false.
func (f Fields) Bool(key string) bool {
	return string(bytes.TrimSpace(f[key])) == "true"
}

// Int64 returns the integer at key; mistyped/missing ⇒ (0,false). Integral
// and fractional JSON numbers are accepted (fractions truncate).
func (f Fields) Int64(key string) (int64, bool) {
	raw := bytes.TrimSpace(f[key])
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	s := string(raw)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, true
	}
	x, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 9e18 {
		return 0, false
	}
	return int64(x), true
}

// Obj returns the object at key as Fields; mistyped/missing ⇒ nil.
func (f Fields) Obj(key string) Fields {
	raw := f[key]
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return Fields(m)
}

// Effort extracts effort.level (object) or effort (string).
func (f Fields) Effort() string {
	raw := f["effort"]
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		s, _ := f.Str("effort")
		return s
	case '{':
		s, _ := f.Obj("effort").Str("level")
		return s
	}
	return ""
}

// FillCommon populates the common Event fields (session/agent/cwd/...) and
// every Claude-style field that exists on the payload. Tool normalization is
// done separately by FillTools. Mistyped fields are ignored.
func FillCommon(ev *model.Event, f Fields) {
	ev.SessionID = f.FirstStr("session_id", "conversation_id", "sessionId")
	ev.TranscriptPath, _ = f.Str("transcript_path")
	ev.Cwd, _ = f.Str("cwd")
	ev.PromptID, _ = f.Str("prompt_id")
	ev.PermissionMode, _ = f.Str("permission_mode")
	ev.Effort = f.Effort()
	ev.AgentID = f.FirstStr("agent_id", "subagent_id")
	ev.AgentType, ev.HasAgentType = f.Str("agent_type")

	ev.Source, _ = f.Str("source")
	ev.Prompt, _ = f.Str("prompt")
	ev.Reason, _ = f.Str("reason")
	ev.Trigger, _ = f.Str("trigger")

	ev.StopHookActive = f.Bool("stop_hook_active")
	ev.LastAssistantMessage, ev.HasLastAssistantMessage = f.Str("last_assistant_message")
	ev.AgentTranscriptPath, _ = f.Str("agent_transcript_path")

	ev.Error, _ = f.Str("error")
	ev.ExitCode = ParseExitCode(ev.Error)
	ev.IsInterrupt = f.Bool("is_interrupt")
	if ms, ok := f.Int64("duration_ms"); ok {
		ev.DurationMS = &ms
	}
	SetToolResponse(ev, f["tool_response"])
}

// SetToolResponse stores a verbatim tool response (ignored when absent/null).
func SetToolResponse(ev *model.Event, raw json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	ev.ToolResponse = append(json.RawMessage(nil), raw...)
	ev.ToolResponseText = ResponseText(raw)
}

// Normalizer is an adapter's NormalizeTool.
type Normalizer func(rawName string, input json.RawMessage) (model.ToolCall, error)

// FillTools fills ev.Tool (tool events or any payload carrying tool_name) and
// ev.BatchCalls (tool_calls[]) through norm. Normalization errors are
// ignored: the returned ToolCall is still usable.
func FillTools(ev *model.Event, f Fields, norm Normalizer) {
	name, _ := f.Str("tool_name")
	if name != "" || ev.Name.IsToolEvent() {
		tc, _ := norm(name, UnwrapInput(f["tool_input"]))
		tc.UseID, _ = f.Str("tool_use_id")
		ev.Tool = &tc
	}
	for _, cf := range ObjectArray(f["tool_calls"]) {
		if cf == nil {
			continue
		}
		n, _ := cf.Str("tool_name")
		tc, _ := norm(n, UnwrapInput(cf["tool_input"]))
		tc.UseID, _ = cf.Str("tool_use_id")
		ev.BatchCalls = append(ev.BatchCalls, tc)
	}
}

// ObjectArray decodes a JSON array element by element; elements that are not
// objects yield nil entries and a non-array value yields nil.
func ObjectArray(raw json.RawMessage) []Fields {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]Fields, len(items))
	for i, it := range items {
		var m map[string]json.RawMessage
		if json.Unmarshal(it, &m) == nil && m != nil {
			out[i] = Fields(m)
		}
	}
	return out
}

// UnwrapInput trims a tool_input value. null/empty ⇒ nil. A JSON string that
// itself contains a JSON object or array (Cursor beforeMCPExecution) is
// unwrapped; any other string is kept as is.
func UnwrapInput(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			t := strings.TrimSpace(s)
			if len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) {
				return json.RawMessage(t)
			}
		}
	}
	return raw
}

// Lookup resolves (client, native) through model.NativeEventTable.
func Lookup(client model.ClientID, native string) (model.NativeEvent, error) {
	ne, ok := model.LookupNative(client, native)
	if !ok {
		return model.NativeEvent{}, fmt.Errorf("%w: %s/%q", model.ErrUnknownEvent, client, native)
	}
	return ne, nil
}

// FlattenStrings returns every string leaf of a JSON value keyed by dotted
// path with array indices dropped (see adapter package doc). A top-level
// scalar has no path and yields nil. There is no depth cut-off (contract
// v1.3): encoding/json already bounds nesting at 10000, and a silent cut
// would hide deep leaves from input_regex (fail-open). The path is built in
// one shared buffer, so deep nesting costs linear, not quadratic, work.
func FlattenStrings(raw json.RawMessage) map[string][]string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	out := map[string][]string{}
	var buf []byte
	flatten(v, &buf, out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func flatten(v any, path *[]byte, out map[string][]string) {
	switch x := v.(type) {
	case string:
		if len(*path) > 0 {
			k := string(*path)
			out[k] = append(out[k], x)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		n := len(*path)
		for _, k := range keys {
			if n > 0 {
				*path = append(*path, '.')
			}
			*path = append(*path, k...)
			flatten(x[k], path, out)
			*path = (*path)[:n]
		}
	case []any:
		for _, e := range x {
			flatten(e, path, out)
		}
	}
}

// ParseExitCode parses a leading "Exit code N" line; nil when absent.
func ParseExitCode(errText string) *int {
	s := strings.TrimLeft(errText, " \t\r\n")
	const prefix = "Exit code "
	if !strings.HasPrefix(s, prefix) {
		return nil
	}
	s = s[len(prefix):]
	end := 0
	if end < len(s) && s[end] == '-' {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end < len(s) && s[end] != '\n' && s[end] != '\r' && s[end] != ' ' && s[end] != '\t' {
		return nil
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return nil
	}
	return &n
}

// ResponseText flattens tool_response for the text matcher (model.Event.ToolResponseText):
// a string is returned as is; an object with stdout/stderr strings yields
// the non-empty parts joined by "\n"; an array of {text} blocks yields the
// texts joined by "\n"; anything else yields its compact JSON.
func ResponseText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case '{':
		f := Fields{}
		if json.Unmarshal(raw, (*map[string]json.RawMessage)(&f)) == nil {
			out, hasOut := f.Str("stdout")
			errS, hasErr := f.Str("stderr")
			if hasOut || hasErr {
				return JoinNonEmpty("\n", out, errS)
			}
		}
	case '[':
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
			var parts []string
			for _, it := range items {
				t, ok := Fields(it).Str("text")
				if !ok {
					parts = nil
					break
				}
				parts = append(parts, t)
			}
			if parts != nil {
				return JoinNonEmpty("\n", parts...)
			}
		}
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// JoinNonEmpty joins the non-empty parts with sep.
func JoinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// MarshalCompact encodes v as compact JSON without HTML escaping + "\n".
func MarshalCompact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// JoinReasonContext joins reason and context with a blank line, skipping empties.
func JoinReasonContext(reason, context string) string {
	switch {
	case reason == "":
		return context
	case context == "":
		return reason
	}
	return reason + "\n\n" + context
}
