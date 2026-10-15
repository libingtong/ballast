package adapter_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/adapter"
	"github.com/libingtong/ballast/internal/model"
)

const repoRoot = "../.."

func readJSON(t testing.TB, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func adapterFor(t testing.TB, client string) interface {
	ParseEvent(string, []byte) (*model.Event, error)
	Render(*model.Event, *model.Decision) ([]byte, error)
} {
	t.Helper()
	a, ok := adapter.Default().Get(model.ClientID(client))
	if !ok {
		t.Fatalf("no adapter for %q", client)
	}
	return a
}

type renderRow struct {
	Client   string `json:"client"`
	Native   string `json:"native"`
	Decision struct {
		Action  model.Action `json:"action"`
		Reason  string       `json:"reason"`
		Context string       `json:"context"`
	} `json:"decision"`
	Stdout string `json:"stdout"`
	Error  bool   `json:"error"`
}

func TestRenderGolden(t *testing.T) {
	var tbl struct {
		Rows []renderRow `json:"rows"`
	}
	readJSON(t, "testdata/render.json", &tbl)
	if len(tbl.Rows) == 0 {
		t.Fatal("no rows")
	}
	for i, r := range tbl.Rows {
		a := adapterFor(t, r.Client)
		ne, ok := model.LookupNative(model.ClientID(r.Client), r.Native)
		if !ok {
			t.Fatalf("row %d: unknown native %s/%s", i, r.Client, r.Native)
		}
		ev := &model.Event{Client: model.ClientID(r.Client), Name: ne.Event, Native: r.Native}
		d := &model.Decision{Event: ne.Event, Native: r.Native, Action: r.Decision.Action, Reason: r.Decision.Reason, Context: r.Decision.Context}
		out, err := a.Render(ev, d)
		name := r.Client + "/" + r.Native + "/" + string(r.Decision.Action)
		if r.Error {
			if err == nil {
				t.Errorf("row %d %s: want error, got %q", i, name, out)
			}
			continue
		}
		if err != nil {
			t.Errorf("row %d %s: %v", i, name, err)
			continue
		}
		if string(out) != r.Stdout {
			t.Errorf("row %d %s:\n got %q\nwant %q", i, name, out, r.Stdout)
		}
	}
}

type parseRow struct {
	Client string         `json:"client"`
	Native string         `json:"native"`
	File   string         `json:"file"`
	Stdin  string         `json:"stdin"`
	Error  string         `json:"error"`
	Want   map[string]any `json:"want"`
}

func TestParseGolden(t *testing.T) {
	var tbl struct {
		Rows []parseRow `json:"rows"`
	}
	readJSON(t, "testdata/parse.json", &tbl)
	for i, r := range tbl.Rows {
		a := adapterFor(t, r.Client)
		stdin := []byte(r.Stdin)
		if r.File != "" {
			b, err := os.ReadFile(filepath.Join(repoRoot, r.File))
			if err != nil {
				t.Fatalf("row %d: %v", i, err)
			}
			stdin = b
		}
		ev, err := a.ParseEvent(r.Native, stdin)
		if r.Error != "" {
			want := map[string]error{"bad_input": model.ErrBadInput, "unknown_event": model.ErrUnknownEvent}[r.Error]
			if want == nil {
				t.Fatalf("row %d: unknown error kind %q", i, r.Error)
			}
			if !errors.Is(err, want) {
				t.Errorf("row %d: err = %v, want %v", i, err, want)
			}
			continue
		}
		if err != nil {
			t.Errorf("row %d: %v", i, err)
			continue
		}
		got := eventMap(t, ev)
		compareWant(t, i, "", got, r.Want)
	}
}

// eventMap is the JSON view of ev plus the derived fields rows may assert.
func eventMap(t *testing.T, ev *model.Event) map[string]any {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["batch_calls_len"] = float64(len(ev.BatchCalls))
	m["internal_compact_agent"] = ev.IsInternalCompactAgent()
	m["has_last_assistant_message"] = ev.HasLastAssistantMessage
	m["has_agent_type"] = ev.HasAgentType
	return m
}

func compareWant(t *testing.T, row int, prefix string, got, want map[string]any) {
	t.Helper()
	for k, w := range want {
		g, present := got[k]
		if sub, ok := w.(map[string]any); ok {
			gm, _ := g.(map[string]any)
			if gm == nil {
				t.Errorf("row %d: %s%s missing, want object", row, prefix, k)
				continue
			}
			compareWant(t, row, prefix+k+".", gm, sub)
			continue
		}
		if !present {
			if !isZero(w) {
				t.Errorf("row %d: %s%s missing, want %v", row, prefix, k, w)
			}
			continue
		}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("row %d: %s%s = %#v, want %#v", row, prefix, k, g, w)
		}
	}
}

func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// TestFactsPayloadsParse feeds every captured Claude payload through
// claudecode.ParseEvent using its own hook_event_name.
func TestFactsPayloadsParse(t *testing.T) {
	a := adapterFor(t, "claude-code")
	n := 0
	err := filepath.Walk(filepath.Join(repoRoot, "docs/facts/payloads"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var probe struct {
			Name string `json:"hook_event_name"`
		}
		if err := json.Unmarshal(b, &probe); err != nil || probe.Name == "" {
			t.Errorf("%s: no hook_event_name", path)
			return nil
		}
		ev, err := a.ParseEvent(probe.Name, b)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		if ev.SessionID == "" || ev.Native != probe.Name || ev.Name == "" {
			t.Errorf("%s: incomplete event %+v", path, ev)
		}
		if ev.Name.IsToolEvent() && ev.Tool == nil {
			t.Errorf("%s: tool event without Tool", path)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 50 {
		t.Fatalf("only %d payloads parsed; walk broken?", n)
	}
}

func TestCapabilitiesAndNativeEventsMatchModel(t *testing.T) {
	reg := adapter.Default()
	if len(reg.All()) != len(model.AllClients) {
		t.Fatalf("registry has %d adapters, want %d", len(reg.All()), len(model.AllClients))
	}
	for _, id := range model.AllClients {
		a, ok := reg.Get(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		if a.ID() != id {
			t.Errorf("%s: ID() = %s", id, a.ID())
		}
		if !reflect.DeepEqual(a.Capabilities(), model.Capabilities(id)) {
			t.Errorf("%s: Capabilities differ from model", id)
		}
		if !reflect.DeepEqual(a.NativeEvents(), model.NativeEventTable[id]) {
			t.Errorf("%s: NativeEvents differ from model", id)
		}
	}
	if _, ok := reg.Get("nope"); ok {
		t.Error("Get(unknown) = ok")
	}
}

// TestRenderMatrix walks every client × native event × action: supported
// cells render valid compact JSON ending in a newline and never "allow";
// unsupported cells return an error.
func TestRenderMatrix(t *testing.T) {
	for _, id := range model.AllClients {
		a, _ := adapter.Default().Get(id)
		cs := model.Capabilities(id)
		for _, ne := range model.NativeEventTable[id] {
			for _, act := range []model.Action{model.ActionWarn, model.ActionAsk, model.ActionDeny} {
				ev := &model.Event{Client: id, Name: ne.Event, Native: ne.Native}
				d := &model.Decision{Event: ne.Event, Native: ne.Native, Action: act, Reason: "[r] why", Context: "ctx"}
				out, err := a.Render(ev, d)
				_, supported := cs.Lookup(ne.Event, act)
				label := string(id) + "/" + ne.Native + "/" + string(act)
				if !supported {
					if err == nil {
						t.Errorf("%s: unsupported cell rendered %q", label, out)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: %v", label, err)
					continue
				}
				checkOutput(t, label, out)
				if !bytes.Contains(out, []byte("why")) {
					t.Errorf("%s: reason missing from %q", label, out)
				}
			}
			// Context-only injection.
			ev := &model.Event{Client: id, Name: ne.Event, Native: ne.Native}
			out, err := a.Render(ev, &model.Decision{Event: ne.Event, Native: ne.Native, Context: "ctx"})
			label := string(id) + "/" + ne.Native + "/inject"
			if err != nil {
				t.Errorf("%s: %v", label, err)
				continue
			}
			if cs.CanInject(ne.Event) != (out != nil) {
				t.Errorf("%s: CanInject=%v but output=%q", label, cs.CanInject(ne.Event), out)
			}
			if out != nil {
				checkOutput(t, label, out)
			}
			// log and none never render anything.
			for _, act := range []model.Action{model.ActionNone, model.ActionLog} {
				out, err := a.Render(ev, &model.Decision{Event: ne.Event, Native: ne.Native, Action: act})
				if err != nil || out != nil {
					t.Errorf("%s/%q: out=%q err=%v, want empty", label, act, out, err)
				}
			}
		}
	}
}

func checkOutput(t *testing.T, label string, out []byte) {
	t.Helper()
	if len(out) == 0 || out[len(out)-1] != '\n' || bytes.Count(out, []byte("\n")) != 1 {
		t.Errorf("%s: output not a single newline-terminated line: %q", label, out)
		return
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Errorf("%s: invalid JSON %q: %v", label, out, err)
		return
	}
	if bytes.Contains(bytes.ToLower(out), []byte(`"allow"`)) || bytes.Contains(out, []byte(`"approve"`)) {
		t.Errorf("%s: allow-like output %q", label, out)
	}
}

func TestRenderNilAndNoEvent(t *testing.T) {
	a, _ := adapter.Default().Get(model.ClientClaude)
	if out, err := a.Render(nil, &model.Decision{}); out != nil || err != nil {
		t.Errorf("empty decision: %q %v", out, err)
	}
	// ev == nil falls back to the decision's own event.
	out, err := a.Render(nil, &model.Decision{Event: model.EventStop, Native: "Stop", Action: model.ActionDeny, Reason: "r"})
	if err != nil || string(out) != "{\"decision\":\"block\",\"reason\":\"r\"}\n" {
		t.Errorf("nil event: %q %v", out, err)
	}
	if _, err := a.Render(nil, &model.Decision{Action: model.ActionDeny, Reason: "r"}); err == nil {
		t.Error("deny with unknown event must fail")
	}
	if _, err := a.Render(&model.Event{Name: model.EventPreToolUse}, &model.Decision{Action: "bogus"}); err == nil {
		t.Error("bogus action must fail")
	}
}

func BenchmarkParseEventPreToolUse(b *testing.B) {
	stdin, err := os.ReadFile(filepath.Join(repoRoot, "docs/facts/payloads/bash-ok/03-PreToolUse.json"))
	if err != nil {
		b.Fatal(err)
	}
	a := adapterFor(b, "claude-code")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := a.ParseEvent("PreToolUse", stdin); err != nil {
			b.Fatal(err)
		}
	}
}
