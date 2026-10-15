package adapter_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/adapter"
	"github.com/libingtong/ballast/internal/model"
)

// pick maps arbitrary fuzz bytes onto a (client, native) pair of the tables.
func pick(c, n uint8) (model.ClientID, string) {
	id := model.AllClients[int(c)%len(model.AllClients)]
	tbl := model.NativeEventTable[id]
	return id, tbl[int(n)%len(tbl)].Native
}

func clientIndex(id model.ClientID) uint8 {
	for i, c := range model.AllClients {
		if c == id {
			return uint8(i)
		}
	}
	return 0
}

func nativeIndex(id model.ClientID, native string) uint8 {
	for i, ne := range model.NativeEventTable[id] {
		if ne.Native == native {
			return uint8(i)
		}
	}
	return 0
}

// seedCorpus yields (client, native, stdin) triples from the captured Claude
// payloads and the inline rows of testdata/parse.json.
func seedCorpus(t testing.TB) (out []struct {
	C, N  uint8
	Stdin []byte
}) {
	add := func(id model.ClientID, native string, stdin []byte) {
		out = append(out, struct {
			C, N  uint8
			Stdin []byte
		}{clientIndex(id), nativeIndex(id, native), stdin})
	}
	_ = filepath.Walk(filepath.Join(repoRoot, "docs/facts/payloads"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		var probe struct {
			Name string `json:"hook_event_name"`
		}
		_ = json.Unmarshal(b, &probe)
		add(model.ClientClaude, probe.Name, b)
		return nil
	})
	var tbl struct {
		Rows []parseRow `json:"rows"`
	}
	readJSON(t, "testdata/parse.json", &tbl)
	for _, r := range tbl.Rows {
		if r.Stdin != "" {
			add(model.ClientID(r.Client), r.Native, []byte(r.Stdin))
		}
	}
	for _, s := range []string{
		`{}`, `null`, `[]`, `{"tool_name":"apply_patch","tool_input":"*** Begin Patch\n*** Update File: a\n+x\n"}`,
		`{"tool_name":"Shell","tool_input":"{\"command\":\"ls\"}","tool_calls":[1,{"tool_name":2}]}`,
		`{"effort":[],"extra":{"user_message":[]},"workspace_roots":[1],"tool_response":[{"text":1}]}`,
		"\xef\xbb\xbf{\"session_id\":\"\xff\"}",
	} {
		for _, id := range model.AllClients {
			for _, ne := range model.NativeEventTable[id] {
				add(id, ne.Native, []byte(s))
			}
		}
	}
	return out
}

func checkParsed(t *testing.T, id model.ClientID, native string, ev *model.Event) {
	t.Helper()
	if ev == nil {
		t.Fatalf("%s/%s: nil event without error", id, native)
	}
	if ev.Client != id || ev.Native != native || ev.Name == "" {
		t.Fatalf("%s/%s: bad identity %+v", id, native, ev)
	}
	if ev.Name.IsToolEvent() && ev.Tool == nil {
		t.Fatalf("%s/%s: tool event without Tool", id, native)
	}
	if !ev.ReceivedAt.IsZero() {
		t.Fatalf("%s/%s: ReceivedAt set", id, native)
	}
	if _, err := json.Marshal(ev); err != nil {
		t.Fatalf("%s/%s: event not marshalable: %v", id, native, err)
	}
}

// FuzzParseEvent never panics; successful parses keep their invariants.
// Seed inputs run as plain tests under `go test`.
func FuzzParseEvent(f *testing.F) {
	for _, s := range seedCorpus(f) {
		f.Add(s.C, s.N, s.Stdin)
	}
	reg := adapter.Default()
	f.Fuzz(func(t *testing.T, c, n uint8, stdin []byte) {
		id, native := pick(c, n)
		a, _ := reg.Get(id)
		ev, err := a.ParseEvent(native, stdin)
		if err != nil {
			if ev != nil {
				t.Fatalf("event returned with error %v", err)
			}
			return
		}
		checkParsed(t, id, native, ev)
	})
}

// FuzzNormalizeTool never panics on arbitrary names and inputs.
func FuzzNormalizeTool(f *testing.F) {
	for _, name := range []string{"Bash", "apply_patch", "exec_command", "Shell", "terminal", "Task", "Write", "MCP:x", ""} {
		f.Add(uint8(0), name, []byte(`{"command":"ls","cmd":["a"],"file_path":"/a","edits":[{"new_string":"x"}]}`))
		f.Add(uint8(1), name, []byte(`"*** Begin Patch\n*** Update File: a\n*** End Patch"`))
	}
	reg := adapter.Default()
	f.Fuzz(func(t *testing.T, c uint8, name string, input []byte) {
		a, _ := reg.Get(model.AllClients[int(c)%len(model.AllClients)])
		tc, _ := a.NormalizeTool(name, json.RawMessage(input))
		if tc.RawName != name {
			t.Fatalf("RawName = %q, want %q", tc.RawName, name)
		}
	})
}

// FuzzRender: any decision either renders a single valid JSON line, nothing,
// or an error; it never panics and never emits "allow".
func FuzzRender(f *testing.F) {
	f.Add(uint8(0), uint8(2), "deny", "reason", "ctx")
	f.Add(uint8(1), uint8(3), "deny", "<&> ", "")
	f.Add(uint8(2), uint8(8), "deny", "\xff\xfe", "x")
	f.Add(uint8(3), uint8(1), "warn", "", "c")
	reg := adapter.Default()
	f.Fuzz(func(t *testing.T, c, n uint8, action, reason, context string) {
		id, native := pick(c, n)
		a, _ := reg.Get(id)
		ne, _ := model.LookupNative(id, native)
		ev := &model.Event{Client: id, Name: ne.Event, Native: native}
		out, err := a.Render(ev, &model.Decision{Event: ne.Event, Native: native, Action: model.Action(action), Reason: reason, Context: context})
		if err != nil || out == nil {
			return
		}
		if !utf8.Valid(out) || out[len(out)-1] != '\n' || strings.Count(string(out), "\n") != 1 {
			t.Fatalf("bad output %q", out)
		}
		var v map[string]any
		if json.Unmarshal(out, &v) != nil {
			t.Fatalf("invalid JSON %q", out)
		}
		if strings.Contains(string(out), `"permission":"allow"`) || strings.Contains(string(out), `"permissionDecision":"allow"`) {
			t.Fatalf("allow rendered: %q", out)
		}
	})
}

// TestConcurrentUse hammers the shared stateless adapters from many
// goroutines (run with -race) and checks results are identical.
func TestConcurrentUse(t *testing.T) {
	seeds := seedCorpus(t)
	reg := adapter.Default()
	type result struct {
		ev  *model.Event
		err string
	}
	run := func() []result {
		var rs []result
		for _, s := range seeds {
			id, native := pick(s.C, s.N)
			a, _ := reg.Get(id)
			ev, err := a.ParseEvent(native, s.Stdin)
			r := result{ev: ev}
			if err != nil {
				r.err = err.Error()
			}
			if ev != nil {
				d := &model.Decision{Event: ev.Name, Native: native, Action: model.ActionWarn, Reason: "r", Context: "c"}
				_, _ = a.Render(ev, d)
			}
			rs = append(rs, r)
		}
		return rs
	}
	want := run()
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if got := run(); !reflect.DeepEqual(got, want) {
					t.Error("concurrent results differ")
					return
				}
			}
		}()
	}
	wg.Wait()
}
