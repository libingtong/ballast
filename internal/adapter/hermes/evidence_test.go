package hermes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// The offline Hermes evidence (docs/facts/hermes-cases.json and
// docs/facts/payloads/hermes/, produced by scripts/probe-clients/hermes.sh)
// is the only reason the NativeEventTable rows may say Verified: these tests
// tie the two together and replay every captured payload through the adapter.

const factsDir = "../../../docs/facts"

type evidenceCase struct {
	CaseID       string   `json:"case_id"`
	Events       []string `json:"events"`
	Result       string   `json:"result"`
	Client       string   `json:"client"`
	EvidenceKind string   `json:"evidence_kind"`
	Recheck      string   `json:"recheck"`
	Evidence     []string `json:"evidence"`
}

func loadCases(t *testing.T) []evidenceCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(factsDir, "hermes-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cs []evidenceCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestVerifiedRowsHavePassingEvidence(t *testing.T) {
	passing := map[string]bool{}
	for _, c := range loadCases(t) {
		if c.Client != "hermes" || c.EvidenceKind != "offline-hooks-test" {
			t.Errorf("%s: client=%q evidence_kind=%q", c.CaseID, c.Client, c.EvidenceKind)
		}
		if c.Result == "needs_credentials" && c.Recheck == "" {
			t.Errorf("%s: needs_credentials without a recheck command", c.CaseID)
		}
		if c.Result == "pass" {
			for _, e := range c.Events {
				passing[e] = true
			}
		}
		for _, ev := range c.Evidence {
			if _, err := os.Stat(filepath.Join(factsDir, ev)); err != nil {
				t.Errorf("%s: evidence %s: %v", c.CaseID, ev, err)
			}
		}
	}
	for _, ne := range model.NativeEventTable[model.ClientHermes] {
		if ne.Verified && !passing[ne.Native] {
			t.Errorf("%s is Verified but no passing offline case covers it", ne.Native)
		}
	}
}

func TestInstalledConfigCoversTheTable(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(factsDir, "payloads/hermes/install-config/config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	if strings.Contains(cfg, "fail_closed") || strings.Contains(cfg, "failClosed") {
		t.Error("the installer must never write fail_closed (a Ballast failure would block every tool call)")
	}
	for _, ne := range model.NativeEventTable[model.ClientHermes] {
		if ne.Install && !strings.Contains(cfg, "hook --client hermes --event "+ne.Native+"\"") {
			t.Errorf("installed config lacks %s", ne.Native)
		}
	}
}

// TestCapturedPayloadsParse replays every stdin captured from `hermes hooks
// test` through ParseEvent.
func TestCapturedPayloadsParse(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(factsDir, "payloads/hermes/*/*.stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 15 {
		t.Fatalf("only %d captured payloads found; evidence directory broken?", len(files))
	}
	a := New()
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var probe struct {
			Name string `json:"hook_event_name"`
		}
		if err := json.Unmarshal(b, &probe); err != nil || probe.Name == "" {
			t.Errorf("%s: no hook_event_name", f)
			continue
		}
		ev, err := a.ParseEvent(probe.Name, b)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		seen[probe.Name] = true
		if ev.SessionID == "" || ev.Native != probe.Name || ev.Name == "" || ev.Cwd != "<repo>" {
			t.Errorf("%s: incomplete event %+v", f, ev)
		}
		if ev.Name.IsToolEvent() && ev.Tool == nil {
			t.Errorf("%s: tool event without Tool", f)
		}
	}
	var got []string
	for k := range seen {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"on_session_end", "on_session_start", "post_tool_call", "pre_llm_call", "pre_tool_call", "pre_verify"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("captured events %v, want %v", got, want)
	}
}

func parseFile(t *testing.T, rel string) *model.Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(factsDir, "payloads/hermes", rel))
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Name string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatal(err)
	}
	ev, err := New().ParseEvent(probe.Name, b)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return ev
}

// TestCapturedPayloadFields pins what the adapter extracts from the real
// payload shapes (the fields the rules and the Stop handling rely on).
func TestCapturedPayloadFields(t *testing.T) {
	ev := parseFile(t, "terminal-deny/current.stdin")
	if ev.Name != model.EventPreToolUse || ev.Tool.Name != model.ToolBash || ev.Tool.Command != "sleep 300" || ev.Tool.Shell != model.ShellBash {
		t.Errorf("terminal: %+v / %+v", ev, ev.Tool)
	}

	ev = parseFile(t, "write-file-tamper/current.stdin")
	if ev.Tool.Name != model.ToolWrite || !reflect.DeepEqual(ev.Tool.Targets, []string{"<home>/.ballast/rules.json"}) {
		t.Errorf("write_file: %+v", ev.Tool)
	}

	ev = parseFile(t, "patch-replace/current.stdin")
	if ev.Tool.Name != model.ToolEdit || !reflect.DeepEqual(ev.Tool.Targets, []string{"<home>/.ballast/rules.json"}) ||
		!reflect.DeepEqual(ev.Tool.Edits, []model.FileEdit{{Path: "<home>/.ballast/rules.json", OldString: "a", NewString: "b"}}) {
		t.Errorf("patch replace: %+v", ev.Tool)
	}

	ev = parseFile(t, "patch-v4a/current.stdin")
	if ev.Tool.Name != model.ToolEdit || !reflect.DeepEqual(ev.Tool.Targets, []string{"src/ok.go", "<home>/.ballast/rules.json"}) {
		t.Errorf("patch v4a: %+v", ev.Tool)
	}

	ev = parseFile(t, "execute-code/current.stdin")
	if ev.Tool.Name != "execute_code" || ev.Tool.RawName != "execute_code" || len(ev.Tool.Targets) != 0 {
		t.Errorf("execute_code: %+v", ev.Tool)
	}

	ev = parseFile(t, "prellm-turns/first.stdin")
	if ev.Name != model.EventUserPromptSubmit || ev.Prompt != "hello" || !ev.IsFirstTurn {
		t.Errorf("pre_llm_call first: %+v", ev)
	}
	ev = parseFile(t, "prellm-turns/second.stdin")
	if ev.Prompt != "and now?" || ev.IsFirstTurn {
		t.Errorf("pre_llm_call second: %+v", ev)
	}

	ev = parseFile(t, "post-tool-call/current.stdin")
	if ev.Name != model.EventPostToolUse || ev.Tool.Name != model.ToolBash || ev.Tool.Command != "ls" || ev.ToolResponseText == "" {
		t.Errorf("post_tool_call: %+v", ev)
	}

	ev = parseFile(t, "pre-verify/claims-done.stdin")
	if ev.Name != model.EventStop || ev.Native != "pre_verify" || !ev.HasLastAssistantMessage ||
		ev.LastAssistantMessage != "All done - the change is applied." || ev.StopHookActive || ev.Tool != nil {
		t.Errorf("pre_verify attempt 0: %+v", ev)
	}
	ev = parseFile(t, "pre-verify/still-working.stdin")
	if !ev.StopHookActive || ev.LastAssistantMessage != "Still working on it." {
		t.Errorf("pre_verify attempt 1: %+v", ev)
	}

	ev = parseFile(t, "session-lifecycle/end.stdin")
	if ev.Name != model.EventSessionEnd || ev.Tool != nil {
		t.Errorf("on_session_end: %+v", ev)
	}
}
