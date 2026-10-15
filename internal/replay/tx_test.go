package replay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tx builds a synthetic Claude Code transcript.
type tx struct {
	lines []string
	n     int
	sid   string
	cwd   string
	agent string
	side  bool
	ts    time.Time
	br    string
}

func newTx(sid, cwd string) *tx {
	return &tx{sid: sid, cwd: cwd, ts: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
}

func (x *tx) line(typ string, msg any, extra map[string]any) *tx {
	x.n++
	x.ts = x.ts.Add(time.Second)
	m := map[string]any{
		"type": typ, "uuid": fmt.Sprintf("u%03d", x.n), "timestamp": x.ts.Format(time.RFC3339Nano),
		"sessionId": x.sid, "cwd": x.cwd, "message": msg,
	}
	if x.agent != "" {
		m["agentId"] = x.agent
	}
	if x.side {
		m["isSidechain"] = true
	}
	if x.br != "" {
		m["gitBranch"] = x.br
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	x.lines = append(x.lines, string(b))
	return x
}

func (x *tx) user(text string) *tx {
	return x.line("user", map[string]any{"role": "user", "content": text}, nil)
}

func (x *tx) text(text string) *tx {
	return x.line("assistant", map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}, nil)
}

func (x *tx) final(text string) *tx {
	return x.line("assistant", map[string]any{"role": "assistant", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": text}}}, nil)
}

func (x *tx) tool(id, name string, input any) *tx {
	return x.line("assistant", map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}, nil)
}

func (x *tx) result(id, text string, isErr bool) *tx {
	return x.line("user", map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isErr}}}, nil)
}

func (x *tx) raw(s string) *tx {
	x.lines = append(x.lines, s)
	return x
}

func (x *tx) bytes() []byte { return []byte(strings.Join(x.lines, "\n") + "\n") }

func (x *tx) write(t testing.TB, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, x.bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
