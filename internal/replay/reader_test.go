package replay

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func readAll(t testing.TB, c model.ClientID, data string) ([]Item, int) {
	t.Helper()
	tr, err := OpenTranscript(c, strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var items []Item
	for {
		it, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, *it)
	}
	return items, tr.Skipped()
}

func kinds(items []Item) []ItemKind {
	var out []ItemKind
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return out
}

func TestOpenTranscriptClients(t *testing.T) {
	for _, c := range []model.ClientID{model.ClientClaude, model.ClientCodex, model.ClientCursor} {
		if _, err := OpenTranscript(c, strings.NewReader("")); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	if _, err := OpenTranscript(model.ClientHermes, strings.NewReader("")); !errors.Is(err, model.ErrUnknownClient) {
		t.Errorf("hermes: %v", err)
	}
	if _, err := OpenTranscript(model.ClientClaude, nil); err == nil {
		t.Error("nil reader accepted")
	}
}

func TestClaudeReaderItems(t *testing.T) {
	x := newTx("s1", "/work/p")
	x.br = "feat/x"
	x.user("fix the bug")
	x.text("looking")
	x.tool("toolu_1", "Read", map[string]any{"file_path": "/work/p/a.go"})
	x.result("toolu_1", "package a", false)
	x.final("done")
	items, skipped := readAll(t, model.ClientClaude, string(x.bytes()))
	if skipped != 0 {
		t.Fatalf("skipped = %d", skipped)
	}
	want := []ItemKind{ItemUserPrompt, ItemAssistantText, ItemToolUse, ItemToolResult, ItemAssistantText, ItemTurnEnd}
	if !reflect.DeepEqual(kinds(items), want) {
		t.Fatalf("kinds = %v, want %v", kinds(items), want)
	}
	p, use, res, end := items[0], items[2], items[3], items[5]
	if p.Text != "fix the bug" || p.SessionID != "s1" || p.Cwd != "/work/p" || p.GitBranch != "feat/x" || p.UUID != "u001" {
		t.Errorf("prompt = %+v", p)
	}
	if use.ToolName != "Read" || use.ToolUseID != "toolu_1" || string(use.ToolInput) != `{"file_path":"/work/p/a.go"}` {
		t.Errorf("tool_use = %+v", use)
	}
	if res.ToolUseID != "toolu_1" || res.Text != "package a" || res.IsError {
		t.Errorf("tool_result = %+v", res)
	}
	// The stop_reason line yields text + end: unique UUIDs.
	if end.Text != "done" || items[4].UUID == end.UUID {
		t.Errorf("turn end = %+v / %+v", items[4], end)
	}
	if !p.TS.Equal(time.Date(2026, 10, 10, 9, 0, 1, 0, time.UTC)) {
		t.Errorf("ts = %v", p.TS)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.UUID] {
			t.Errorf("duplicate uuid %s", it.UUID)
		}
		seen[it.UUID] = true
	}
}

func TestClaudeReaderMultiBlockLineUUIDs(t *testing.T) {
	line := `{"type":"assistant","uuid":"abc","timestamp":"2026-10-10T09:00:00Z","sessionId":"s","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"hi"},` +
		`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},` +
		`{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"pwd"}}]}}` + "\n"
	items, _ := readAll(t, model.ClientClaude, line)
	var uuids []string
	for _, it := range items {
		uuids = append(uuids, it.UUID)
	}
	if !reflect.DeepEqual(uuids, []string{"abc#0", "abc#1", "abc#2"}) {
		t.Errorf("uuids = %v", uuids)
	}
}

func TestClaudeReaderUserBlocks(t *testing.T) {
	data := strings.Join([]string{
		// user with tool_result array + text
		`{"type":"user","uuid":"a","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]},{"type":"text","text":"also this"}]}}`,
		// error result
		`{"type":"user","uuid":"b","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"boom","is_error":true}]}}`,
		// meta, interrupt and local-command records are not prompts
		`{"type":"user","uuid":"c","isMeta":true,"message":{"role":"user","content":"Caveat: ..."}}`,
		`{"type":"user","uuid":"d","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","uuid":"e","message":{"role":"user","content":"<local-command-stdout>x</local-command-stdout>"}}`,
		// compaction
		`{"type":"user","uuid":"f","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued..."}}`,
		`{"type":"system","uuid":"g","subtype":"compact_boundary"}`,
		`{"type":"system","uuid":"h","subtype":"stop_hook_summary"}`,
		// unknown record types are tolerated, not counted as skipped
		`{"type":"summary","summary":"x"}`,
		`{"type":"file-history-snapshot","snapshot":{}}`,
		`{"type":"brand-new-type-from-the-future","a":[1,2,3]}`,
	}, "\n")
	items, skipped := readAll(t, model.ClientClaude, data)
	if skipped != 0 {
		t.Errorf("skipped = %d", skipped)
	}
	want := []ItemKind{ItemToolResult, ItemUserPrompt, ItemToolResult, ItemCompact, ItemCompact}
	if !reflect.DeepEqual(kinds(items), want) {
		t.Fatalf("kinds = %v, want %v", kinds(items), want)
	}
	if items[0].Text != "line1\nline2" || items[1].Text != "also this" || !items[2].IsError || items[2].Text != "boom" {
		t.Errorf("items = %+v", items)
	}
}

func TestReaderCorruptLinesAreSkippedAndCounted(t *testing.T) {
	data := strings.Join([]string{
		`{"type":"user","uuid":"a","message":{"role":"user","content":"one"}}`,
		`{"type":"user","uuid":"b","message":{"role":"user","content":"tr`, // truncated
		``,
		`   `,
		`not json at all`,
		`[1,2,3]`,
		`42`,
		`"str"`,
		`{"type":"user","uuid":"c"} trailing garbage`,
		"\x00\x01\x02",
		`{"type":"user","uuid":"d","message":{"role":"user","content":"two"}}`,
	}, "\n")
	for _, c := range []model.ClientID{model.ClientClaude, model.ClientCursor, model.ClientCodex} {
		items, skipped := readAll(t, c, data)
		if skipped != 7 {
			t.Errorf("%s: skipped = %d, want 7", c, skipped)
		}
		if c == model.ClientClaude && len(items) != 2 {
			t.Errorf("claude items = %+v", items)
		}
	}
}

func TestReaderVersionDrift(t *testing.T) {
	data := strings.Join([]string{
		// numeric timestamp (ms), numeric uuid, bool where a string is expected
		`{"type":"user","uuid":12345,"timestamp":1760000000000,"sessionId":true,"cwd":["x"],"message":{"role":"user","content":"drifted"}}`,
		// seconds epoch and an object message content
		`{"type":"user","uuid":"b","timestamp":1760000001,"message":{"role":"user","content":{"weird":1}}}`,
		// message is a string
		`{"type":"assistant","uuid":"c","message":"just text"}`,
		// extra unknown fields everywhere
		`{"type":"assistant","uuid":"d","newField":{"a":1},"message":{"role":"assistant","model":"x","usage":{"in":1},"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"ok","citations":[]}]}}`,
	}, "\n")
	items, skipped := readAll(t, model.ClientClaude, data)
	if skipped != 0 {
		t.Fatalf("skipped = %d", skipped)
	}
	if len(items) < 2 || items[0].Kind != ItemUserPrompt || items[0].Text != "drifted" {
		t.Fatalf("items = %+v", items)
	}
	if got := items[0].TS; got.Unix() != 1760000000 {
		t.Errorf("ms epoch parsed as %v", got)
	}
	if items[0].UUID != "line-1" { // uuid of the wrong type falls back to the line number
		t.Errorf("uuid = %q", items[0].UUID)
	}
	var texts []string
	for _, it := range items {
		if it.Kind == ItemAssistantText {
			texts = append(texts, it.Text)
		}
	}
	if !reflect.DeepEqual(texts, []string{"ok"}) {
		t.Errorf("assistant texts = %v", texts)
	}
}

func TestReaderSidechainAndAgent(t *testing.T) {
	data := `{"type":"user","uuid":"a","isSidechain":true,"agentId":"ag1","message":{"role":"user","content":"sub task"}}` + "\n" +
		`{"type":"user","uuid":"b","isSidechain":true,"message":{"role":"user","content":"unnamed"}}` + "\n"
	items, _ := readAll(t, model.ClientClaude, data)
	if items[0].AgentID != "ag1" || items[1].AgentID != "sidechain" {
		t.Errorf("agents = %q %q", items[0].AgentID, items[1].AgentID)
	}
}

func TestFallbackTurnEnd(t *testing.T) {
	// No stop_reason anywhere (older transcripts): the answer is closed by the
	// next prompt, and a trailing answer by EOF.
	x := newTx("s", "/w")
	x.user("q1").text("thinking").tool("t1", "Bash", map[string]any{"command": "ls"}).result("t1", "out", false).text("answer one").
		user("q2").text("answer two")
	items, _ := readAll(t, model.ClientClaude, string(x.bytes()))
	var ends []string
	for _, it := range items {
		if it.Kind == ItemTurnEnd {
			ends = append(ends, it.Text)
		}
	}
	if !reflect.DeepEqual(ends, []string{"answer one", "answer two"}) {
		t.Errorf("turn ends = %v; kinds = %v", ends, kinds(items))
	}
	// The end of turn 1 comes before the prompt of turn 2.
	var order []ItemKind
	for _, it := range items {
		if it.Kind == ItemTurnEnd || it.Kind == ItemUserPrompt {
			order = append(order, it.Kind)
		}
	}
	if !reflect.DeepEqual(order, []ItemKind{ItemUserPrompt, ItemTurnEnd, ItemUserPrompt, ItemTurnEnd}) {
		t.Errorf("order = %v", order)
	}
	// An explicit end_turn is not duplicated by the fallback.
	x2 := newTx("s", "/w")
	x2.user("q").final("only once")
	items, _ = readAll(t, model.ClientClaude, string(x2.bytes()))
	n := 0
	for _, it := range items {
		if it.Kind == ItemTurnEnd {
			n++
		}
	}
	if n != 1 {
		t.Errorf("turn ends = %d", n)
	}
	// A turn that ends on a tool call (interrupted) has no final text, no Stop.
	x3 := newTx("s", "/w")
	x3.user("q").text("calling").tool("t", "Bash", map[string]any{"command": "ls"})
	items, _ = readAll(t, model.ClientClaude, string(x3.bytes()))
	for _, it := range items {
		if it.Kind == ItemTurnEnd {
			t.Errorf("unexpected turn end %+v", it)
		}
	}
}

func TestLongLines(t *testing.T) {
	old := lineLimit
	lineLimit = 1 << 20
	t.Cleanup(func() { lineLimit = old })

	big := strings.Repeat("x", 3<<20) // over the limit, spans many buffer fills
	mid := strings.Repeat("y", 600<<10)
	data := `{"type":"user","uuid":"a","message":{"role":"user","content":"before"}}` + "\n" +
		`{"type":"user","uuid":"big","message":{"role":"user","content":"` + big + `"}}` + "\n" +
		`{"type":"user","uuid":"mid","message":{"role":"user","content":"` + mid + `"}}` + "\n" +
		`{"type":"user","uuid":"z","message":{"role":"user","content":"after"}}` // no trailing newline
	items, skipped := readAll(t, model.ClientClaude, data)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(items) != 3 || items[0].Text != "before" || len(items[1].Text) != len(mid) || items[2].Text != "after" {
		t.Fatalf("items = %d: %v", len(items), kinds(items))
	}
	// CRLF endings.
	items, skipped = readAll(t, model.ClientClaude, `{"type":"user","uuid":"a","message":{"role":"user","content":"crlf"}}`+"\r\n")
	if skipped != 0 || len(items) != 1 || items[0].Text != "crlf" {
		t.Errorf("crlf: %+v skipped=%d", items, skipped)
	}
}

func TestBOMOnFirstLine(t *testing.T) {
	items, skipped := readAll(t, model.ClientClaude, "\xef\xbb\xbf"+`{"type":"user","uuid":"a","message":{"role":"user","content":"bom"}}`+"\n")
	if skipped != 0 || len(items) != 1 || items[0].Text != "bom" {
		t.Errorf("items=%+v skipped=%d", items, skipped)
	}
}

func TestCursorReader(t *testing.T) {
	data := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nrefactor foo\n</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"I will read the file."},{"type":"tool_use","name":"Read","input":{"file_path":"/w/foo.go"}}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"Done refactoring."}]}}`,
	}, "\n")
	items, skipped := readAll(t, model.ClientCursor, data)
	if skipped != 0 {
		t.Fatal(skipped)
	}
	want := []ItemKind{ItemUserPrompt, ItemAssistantText, ItemToolUse, ItemAssistantText, ItemTurnEnd}
	if !reflect.DeepEqual(kinds(items), want) {
		t.Fatalf("kinds = %v", kinds(items))
	}
	if items[0].Text != "refactor foo" || items[2].ToolName != "Read" || items[2].ToolUseID == "" || items[4].Text != "Done refactoring." {
		t.Errorf("items = %+v", items)
	}
	if items[0].UUID != "line-1" {
		t.Errorf("synthesized uuid = %q", items[0].UUID)
	}
}

func TestCodexReader(t *testing.T) {
	data := strings.Join([]string{
		`{"timestamp":"2026-10-10T09:00:00Z","type":"session_meta","payload":{"id":"sess-9","cwd":"/w/codex"}}`,
		`{"timestamp":"2026-10-10T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd...</environment_context>"}]}}`,
		`{"timestamp":"2026-10-10T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"add a flag"}]}}`,
		`{"timestamp":"2026-10-10T09:00:03Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n 1,80p a.go\"}","call_id":"call_1"}}`,
		`{"timestamp":"2026-10-10T09:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"{\"output\":\"package a\",\"metadata\":{\"exit_code\":0}}"}}`,
		`{"timestamp":"2026-10-10T09:00:05Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"call_2","input":"*** Begin Patch\n*** Update File: a.go\n*** End Patch"}}`,
		`{"timestamp":"2026-10-10T09:00:06Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_2","output":"Done!"}}`,
		`{"timestamp":"2026-10-10T09:00:07Z","type":"turn_context","payload":{"cwd":"/w/codex/sub"}}`,
		`{"timestamp":"2026-10-10T09:00:08Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"added"}]}}`,
		`{"timestamp":"2026-10-10T09:00:09Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"added the flag"}}`,
		`{"timestamp":"2026-10-10T09:00:10Z","type":"event_msg","payload":{"type":"agent_message","message":"dup"}}`,
		`{"timestamp":"2026-10-10T09:00:11Z","type":"compacted","payload":{"message":"summary"}}`,
		`{"timestamp":"2026-10-10T09:00:12Z","type":"brand_new","payload":{}}`,
	}, "\n")
	items, skipped := readAll(t, model.ClientCodex, data)
	if skipped != 0 {
		t.Fatal(skipped)
	}
	want := []ItemKind{ItemUserPrompt, ItemToolUse, ItemToolResult, ItemToolUse, ItemToolResult, ItemAssistantText, ItemTurnEnd, ItemCompact}
	if !reflect.DeepEqual(kinds(items), want) {
		t.Fatalf("kinds = %v", kinds(items))
	}
	if items[0].SessionID != "sess-9" || items[0].Cwd != "/w/codex" || items[0].Text != "add a flag" {
		t.Errorf("prompt = %+v", items[0])
	}
	if items[1].ToolName != "exec_command" || string(items[1].ToolInput) != `{"cmd":"sed -n 1,80p a.go"}` || items[1].ToolUseID != "call_1" {
		t.Errorf("exec_command = %+v", items[1])
	}
	if items[2].Text != "package a" || items[2].IsError {
		t.Errorf("exec output = %+v", items[2])
	}
	if items[3].ToolName != "apply_patch" || !strings.Contains(string(items[3].ToolInput), "*** Update File: a.go") {
		t.Errorf("apply_patch = %+v", items[3])
	}
	if items[4].Text != "Done!" {
		t.Errorf("patch output = %+v", items[4])
	}
	if items[5].Cwd != "/w/codex/sub" || items[6].Text != "added the flag" {
		t.Errorf("turn: %+v / %+v", items[5], items[6])
	}
	if !items[6].TS.Equal(time.Date(2026, 10, 10, 9, 0, 9, 0, time.UTC)) {
		t.Errorf("ts = %v", items[6].TS)
	}
}

func TestCodexLegacyFlatRecords(t *testing.T) {
	data := `{"type":"message","role":"user","content":[{"type":"input_text","text":"old format"}]}` + "\n" +
		`{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"ls\"]}","call_id":"c1"}` + "\n"
	items, _ := readAll(t, model.ClientCodex, data)
	if !reflect.DeepEqual(kinds(items), []ItemKind{ItemUserPrompt, ItemToolUse}) || items[0].Text != "old format" {
		t.Errorf("items = %+v", items)
	}
}

func FuzzTranscriptReader(f *testing.F) {
	seeds := []string{
		`{"type":"user","uuid":"a","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"x"}],"is_error":true}]}}`,
		`{"role":"assistant","message":{"content":"s"}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{}","call_id":"c"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
		`{"timestamp":1e30,"uuid":{},"sessionId":[]}`,
		`{"type":"user","message":{"content":[null,1,"x",{"type":null}]}}`,
		"{\"type\":\"user\"}\n\n\x00\n[",
		`{"type":"assistant","message":{"stop_reason":"end_turn","content":[]}}`,
		``,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, c := range []model.ClientID{model.ClientClaude, model.ClientCursor, model.ClientCodex} {
			tr, err := OpenTranscript(c, strings.NewReader(s))
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for {
				it, err := tr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s: %v", c, err)
				}
				if it.UUID == "" {
					t.Fatalf("%s: item without uuid: %+v", c, it)
				}
				if n++; n > 10*(len(s)+1) {
					t.Fatalf("%s: reader does not terminate", c)
				}
			}
			if tr.Skipped() < 0 {
				t.Fatal("negative skipped")
			}
		}
	})
}
