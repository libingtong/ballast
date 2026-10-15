package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// MaxLineBytes is the longest transcript line the readers parse; longer
// lines are skipped (and counted) without being buffered.
const MaxLineBytes = 64 << 20

// lineLimit is MaxLineBytes; a variable so tests can exercise the cut-off.
var lineLimit = MaxLineBytes

// OpenTranscript returns the reader for client c over r.
func OpenTranscript(c model.ClientID, r io.Reader) (TranscriptReader, error) {
	if r == nil {
		return nil, errors.New("replay: nil transcript reader")
	}
	switch c {
	case model.ClientClaude:
		return newReader(r, flavorClaude), nil
	case model.ClientCursor:
		return newReader(r, flavorCursor), nil
	case model.ClientCodex:
		return newReader(r, flavorCodex), nil
	}
	return nil, fmt.Errorf("replay: no transcript reader for client %q: %w", c, model.ErrUnknownClient)
}

type flavor int

const (
	flavorClaude flavor = iota
	flavorCursor
	flavorCodex
)

// lineReader yields newline-terminated lines of any length up to
// MaxLineBytes without ever holding more than one line.
type lineReader struct {
	br  *bufio.Reader
	buf []byte
	n   int // lines returned so far (1-based number of the last line)
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, 256<<10)}
}

// next returns the next line without its terminator. tooLong reports a line
// over MaxLineBytes (returned empty). The slice is valid until the next call.
func (l *lineReader) next() (line []byte, tooLong bool, err error) {
	if cap(l.buf) > 4<<20 {
		l.buf = nil // do not pin the memory of one huge line
	}
	l.buf = l.buf[:0]
	for {
		chunk, rerr := l.br.ReadSlice('\n')
		if !tooLong {
			if len(l.buf)+len(chunk) > lineLimit {
				tooLong = true
				l.buf = l.buf[:0]
			} else {
				l.buf = append(l.buf, chunk...)
			}
		}
		switch {
		case rerr == nil:
			l.n++
			if tooLong {
				return nil, true, nil
			}
			return l.finish(), false, nil
		case errors.Is(rerr, bufio.ErrBufferFull):
			continue
		case errors.Is(rerr, io.EOF):
			if len(l.buf) == 0 && !tooLong {
				return nil, false, io.EOF
			}
			l.n++
			if tooLong {
				return nil, true, nil
			}
			return l.finish(), false, nil
		default:
			return nil, false, rerr
		}
	}
}

// finish trims the line terminator (and a UTF-8 BOM on the first line).
func (l *lineReader) finish() []byte {
	line := bytes.TrimRight(l.buf, "\r\n")
	if l.n == 1 {
		line = bytes.TrimPrefix(line, []byte("\xef\xbb\xbf"))
	}
	return line
}

// reader implements TranscriptReader for all three flavors.
type reader struct {
	lr      *lineReader
	fl      flavor
	queue   []Item
	head    int
	skipped int
	eof     bool

	// Codex session metadata arrives in dedicated records.
	sessionID string
	cwd       string

	// openText: an assistant text was emitted and no tool call or turn end
	// followed (the version-tolerant fallback turn-end detector).
	openText bool
	lastText string
	lastMeta Item
}

func newReader(r io.Reader, fl flavor) *reader {
	return &reader{lr: newLineReader(r), fl: fl}
}

// Skipped implements TranscriptReader.
func (r *reader) Skipped() int { return r.skipped }

// Next implements TranscriptReader.
func (r *reader) Next() (*Item, error) {
	for {
		if r.head < len(r.queue) {
			it := r.queue[r.head]
			r.queue[r.head] = Item{}
			r.head++
			if r.head == len(r.queue) {
				r.queue, r.head = r.queue[:0], 0
			}
			return &it, nil
		}
		if r.eof {
			return nil, io.EOF
		}
		line, tooLong, err := r.lr.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				r.eof = true
				r.flushTurn()
				continue
			}
			return nil, err
		}
		if tooLong {
			r.skipped++
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !r.parseLine(line) {
			r.skipped++
		}
	}
}

// flushTurn emits the fallback turn end for a trailing assistant answer.
func (r *reader) flushTurn() {
	if !r.openText {
		return
	}
	it := r.lastMeta
	it.Kind = ItemTurnEnd
	it.Text = r.lastText
	it.ToolName, it.ToolInput, it.ToolUseID = "", nil, ""
	it.UUID = fmt.Sprintf("%s#end", r.lastMeta.UUID)
	r.queue = append(r.queue, it)
	r.openText = false
}

// parseLine reports false when the line is not a JSON object.
func (r *reader) parseLine(line []byte) bool {
	if line[0] != '{' && !isSpaceThenBrace(line) {
		return false
	}
	if r.fl == flavorCodex {
		return r.parseCodex(line)
	}
	return r.parseChat(line)
}

func isSpaceThenBrace(line []byte) bool {
	t := bytes.TrimLeft(line, " \t")
	return len(t) > 0 && t[0] == '{'
}

// unmarshalLenient decodes data into v, treating field type mismatches
// (version drift) as non-fatal: the remaining fields are still filled.
func unmarshalLenient(data []byte, v any) bool {
	err := json.Unmarshal(data, v)
	if err == nil {
		return true
	}
	var te *json.UnmarshalTypeError
	return errors.As(err, &te)
}

type chatLine struct {
	Type             string          `json:"type"`
	Role             string          `json:"role"`
	Subtype          string          `json:"subtype"`
	UUID             string          `json:"uuid"`
	Timestamp        json.RawMessage `json:"timestamp"`
	SessionID        string          `json:"sessionId"`
	SessionID2       string          `json:"session_id"`
	Cwd              string          `json:"cwd"`
	AgentID          string          `json:"agentId"`
	IsSidechain      bool            `json:"isSidechain"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	GitBranch        string          `json:"gitBranch"`
	Message          json.RawMessage `json:"message"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (r *reader) parseChat(line []byte) bool {
	var cl chatLine
	if !unmarshalLenient(line, &cl) {
		return false
	}
	meta := Item{
		TS:        parseTS(cl.Timestamp),
		SessionID: firstNonEmpty(cl.SessionID, cl.SessionID2),
		Cwd:       cl.Cwd,
		AgentID:   cl.AgentID,
		GitBranch: cl.GitBranch,
	}
	if meta.AgentID == "" && cl.IsSidechain {
		meta.AgentID = "sidechain"
	}
	lineNo := r.lr.n
	base := cl.UUID
	if base == "" {
		base = fmt.Sprintf("line-%d", lineNo)
	}
	meta.UUID = base

	kind := cl.Type
	if kind != "user" && kind != "assistant" {
		if cl.Role == "user" || cl.Role == "assistant" {
			kind = cl.Role
		}
	}

	var out []Item
	switch kind {
	case "user":
		if cl.IsMeta {
			return true
		}
		var msg chatMessage
		if len(cl.Message) > 0 {
			unmarshalLenient(cl.Message, &msg)
		}
		if cl.IsCompactSummary {
			it := meta
			it.Kind = ItemCompact
			out = append(out, it)
			break
		}
		out = r.userItems(out, meta, msg.Content)
	case "assistant":
		var msg chatMessage
		if len(cl.Message) > 0 {
			unmarshalLenient(cl.Message, &msg)
		}
		out = r.assistantItems(out, meta, msg)
	case "system":
		if cl.Subtype == "compact_boundary" {
			it := meta
			it.Kind = ItemCompact
			out = append(out, it)
		}
	default:
		return true // unknown record type: tolerated, not an error
	}
	r.enqueue(out, base)
	return true
}

func (r *reader) userItems(out []Item, meta Item, content json.RawMessage) []Item {
	var text string
	if len(content) > 0 && content[0] == '"' {
		_ = json.Unmarshal(content, &text)
	} else if len(content) > 0 && content[0] == '[' {
		var blocks []block
		unmarshalLenient(content, &blocks)
		var texts []string
		for i := range blocks {
			b := &blocks[i]
			switch b.Type {
			case "tool_result":
				it := meta
				it.Kind = ItemToolResult
				it.ToolUseID = b.ToolUseID
				it.IsError = b.IsError
				it.Text = flattenContent(b.Content)
				out = append(out, it)
			case "text":
				texts = append(texts, b.Text)
			}
		}
		text = strings.Join(texts, "\n")
	}
	if text == "" {
		return out
	}
	text = cleanPrompt(text, r.fl)
	if text == "" {
		return out
	}
	it := meta
	it.Kind = ItemUserPrompt
	it.Text = text
	return append(out, it)
}

// cleanPrompt drops records that are not real prompts and unwraps Cursor's
// <user_query> envelope.
func cleanPrompt(text string, fl flavor) string {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "[Request interrupted by user"),
		strings.HasPrefix(t, "<local-command-stdout>"),
		strings.HasPrefix(t, "<local-command-caveat>"):
		return ""
	}
	if fl == flavorCursor {
		if inner, ok := strings.CutPrefix(t, "<user_query>"); ok {
			inner = strings.TrimSuffix(strings.TrimSpace(inner), "</user_query>")
			return strings.TrimSpace(inner)
		}
	}
	return text
}

func (r *reader) assistantItems(out []Item, meta Item, msg chatMessage) []Item {
	var blocks []block
	switch {
	case len(msg.Content) > 0 && msg.Content[0] == '[':
		unmarshalLenient(msg.Content, &blocks)
	case len(msg.Content) > 0 && msg.Content[0] == '"':
		var s string
		_ = json.Unmarshal(msg.Content, &s)
		blocks = []block{{Type: "text", Text: s}}
	}
	sawTool := false
	for i := range blocks {
		b := &blocks[i]
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			it := meta
			it.Kind = ItemAssistantText
			it.Text = b.Text
			out = append(out, it)
		case "tool_use":
			sawTool = true
			it := meta
			it.Kind = ItemToolUse
			it.ToolName = b.Name
			it.ToolInput = append([]byte(nil), b.Input...)
			it.ToolUseID = b.ID
			if it.ToolUseID == "" {
				it.ToolUseID = fmt.Sprintf("%s:tool%d", meta.UUID, i)
			}
			out = append(out, it)
		}
	}
	endTurn := msg.StopReason == "end_turn"
	if r.fl == flavorCursor && !sawTool && len(out) > 0 {
		endTurn = true // a text-only assistant record is the final answer
	}
	if endTurn && !sawTool && len(out) > 0 {
		last := meta
		last.Kind = ItemTurnEnd
		for i := len(out) - 1; i >= 0; i-- {
			if out[i].Kind == ItemAssistantText {
				last.Text = out[i].Text
				break
			}
		}
		out = append(out, last)
	}
	return out
}

func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	case '[':
		var blocks []block
		unmarshalLenient(raw, &blocks)
		var parts []string
		for i := range blocks {
			if blocks[i].Type == "text" || blocks[i].Text != "" {
				parts = append(parts, blocks[i].Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// enqueue assigns unique UUIDs, maintains the fallback turn-end detector and
// appends the items.
func (r *reader) enqueue(items []Item, base string) {
	if len(items) == 0 {
		return
	}
	for i := range items {
		if len(items) > 1 {
			items[i].UUID = fmt.Sprintf("%s#%d", base, i)
		}
	}
	for i := range items {
		it := &items[i]
		switch it.Kind {
		case ItemUserPrompt, ItemCompact:
			r.flushTurn()
		case ItemAssistantText:
			r.openText, r.lastText, r.lastMeta = true, it.Text, *it
		case ItemToolUse:
			r.openText = false
		case ItemTurnEnd:
			r.openText = false
		}
		r.queue = append(r.queue, *it)
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// parseTS accepts an RFC3339 string or a numeric epoch (ms when large, else s).
func parseTS(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}
		}
		return t.UTC()
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f <= 0 {
		return time.Time{}
	}
	if f > 1e11 {
		return time.UnixMilli(int64(f)).UTC()
	}
	return time.Unix(int64(f), 0).UTC()
}
