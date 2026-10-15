package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/testenv"
)

// FuzzHookMain feeds arbitrary stdin and event names through the whole
// pipeline. Invariants: the exit code is 0 or 1; exit 1 ⇒ empty stdout and a
// single "ballast: " stderr line; exit 0 ⇒ empty stderr and stdout that is
// either empty or one JSON object line. The seed corpus runs as a normal test.
func FuzzHookMain(f *testing.F) {
	sb := testenv.New(f)
	h := newCoreHarness(sb.Env(), sb.Layout(f))
	h.deps.Layout.GCStamp = filepath.Join(sb.Root, "gc.stamp")
	h.engine.verdict = denyVerdict()
	h.deps.Watchdog = 10 * 1e9

	f.Add("PreToolUse", []byte(preToolBash))
	f.Add("PostToolUse", []byte(`{"tool_name":"Edit","tool_input":{"file_path":"a.go"},"cwd":"/repo"}`))
	f.Add("SessionStart", []byte(`{"session_id":"s","cwd":"/repo"}`))
	f.Add("SubagentStop", []byte(`{"agent_id":"a","agent_type":""}`))
	f.Add("Stop", []byte(`{"last_assistant_message":"x"}`))
	f.Add("SessionEnd", []byte(`{}`))
	f.Add("PreToolUse", []byte(`{"tool_name":"Bash","tool_input":{"command":123},"cwd":42}`))
	f.Add("PreToolUse", []byte(`[[[[[[[[[[[[[[[[[[[[[[[[[[`))
	f.Add("PreToolUse", []byte("\xff\xfe\x00{"))
	f.Add("", []byte(`{}`))
	f.Add("Bogus", []byte(`{}`))
	f.Add("PreToolUse", []byte(`{"tool_name":"Bash","tool_input":{"command":"`+strings.Repeat("a", 100000)+`"}}`))
	if files, err := filepath.Glob(filepath.Join("..", "..", "docs", "facts", "payloads", "bash-ok", "*.json")); err == nil {
		for _, p := range files {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			native := strings.TrimSuffix(strings.SplitN(filepath.Base(p), "-", 2)[1], ".json")
			f.Add(native, b)
		}
	}

	f.Fuzz(func(t *testing.T, native string, stdin []byte) {
		var out, errb bytes.Buffer
		code := Main([]string{"--client", "claude-code", "--event", native}, bytes.NewReader(stdin), &out, &errb, h.deps)
		switch code {
		case 1:
			if out.Len() != 0 {
				t.Fatalf("exit 1 with stdout %q", out.String())
			}
			if !strings.HasPrefix(errb.String(), "ballast: ") || strings.Count(errb.String(), "\n") != 1 {
				t.Fatalf("stderr = %q", errb.String())
			}
		case 0:
			if errb.Len() != 0 {
				t.Fatalf("exit 0 with stderr %q", errb.String())
			}
			if out.Len() > 0 {
				var v map[string]any
				if err := json.Unmarshal(out.Bytes(), &v); err != nil || out.Bytes()[out.Len()-1] != '\n' {
					t.Fatalf("stdout is not one JSON object line: %q (%v)", out.String(), err)
				}
			}
		default:
			t.Fatalf("exit code %d", code)
		}
		if exits := h.exitCalls(); len(exits) != 0 {
			t.Fatalf("watchdog fired: %v", exits)
		}
	})
}

// FuzzBuildDelta: BuildDelta is pure and must tolerate any combination of
// event fields without panicking or aliasing its inputs.
func FuzzBuildDelta(f *testing.F) {
	f.Add("PreToolUse", "Bash", "id", "git status", "p")
	f.Add("UserPromptSubmit", "", "", "", "héllo wörld")
	f.Add("Stop", "Edit", "x", "", "")
	f.Add("", "", "", "\xff", "\xff")
	f.Fuzz(func(t *testing.T, name, tool, id, cmd, prompt string) {
		ev := &model.Event{Name: model.EventName(name), Prompt: prompt}
		if tool != "" || id != "" || cmd != "" {
			ev.Tool = &model.ToolCall{Name: tool, UseID: id, Command: cmd, Targets: []string{cmd}}
		}
		paths := []string{"/a", "/b"}
		d := BuildDelta(ev, &model.CompiledRuleSet{Generation: "g"}, &model.Verdict{Hits: []model.RuleHit{{RuleID: "r", Effective: model.ActionWarn}}}, &model.Injection{RuleDigest: map[string]model.Action{}}, paths, testNow)
		if d == nil || len(d.ReadPaths) != 2 {
			t.Fatalf("delta: %+v", d)
		}
		paths[0] = "mutated"
		if d.ReadPaths[0] != "/a" {
			t.Fatal("delta aliases readPaths")
		}
		if d.Summary != nil && len([]rune(d.Summary.Text)) > model.MaxEventSummaryRunes {
			t.Fatalf("summary text too long: %d", len([]rune(d.Summary.Text)))
		}
		if len([]rune(d.PromptTail)) > 200 {
			t.Fatalf("prompt tail too long: %d", len([]rune(d.PromptTail)))
		}
	})
}
