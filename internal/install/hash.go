package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// EntryHash = hex(sha256(canonical JSON of the entry object)).
// Canonical = encoding/json of the decoded value (sorted map keys, no spaces).
func EntryHash(entryJSON []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(entryJSON))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("entry hash: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("entry hash: trailing data after JSON value")
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("entry hash: %w", err)
	}
	return sha256Hex(canon), nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CodexTrustHash mirrors Codex's trust hash: "sha256:" + hex(sha256(canonical
// JSON of {"event_name":<snake_event>,"hooks":[{"type":"command","command":C,
// "timeout":T,"async":false}]})) (pinrule codex.py:623-647; unverified for
// Codex versions other than 0.130).
func CodexTrustHash(snakeEvent, command string, timeout int) string {
	var b strings.Builder
	b.WriteString(`{"event_name":`)
	writePyJSONString(&b, snakeEvent)
	b.WriteString(`,"hooks":[{"async":false,"command":`)
	writePyJSONString(&b, command)
	b.WriteString(`,"timeout":`)
	b.WriteString(strconv.Itoa(timeout))
	b.WriteString(`,"type":"command"}]}`)
	return "sha256:" + sha256Hex([]byte(b.String()))
}

// writePyJSONString writes s the way Python's json.dumps(ensure_ascii=False)
// does: only the quote, backslash and control characters are escaped.
func writePyJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// snakeEvent converts a Codex PascalCase event name to the snake_case key
// used in trust state ("PreToolUse" -> "pre_tool_use").
func snakeEvent(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
