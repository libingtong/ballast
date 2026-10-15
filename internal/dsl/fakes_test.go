package dsl

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// ---- fake PathService ----------------------------------------------------

// fakePaths is a unit-test PathService: join with the base and clean, "~/"
// expansion, doublestar matching with list-level negation, no symlinks.
type fakePaths struct{}

func (fakePaths) Normalize(p, base string, ctx model.PathContext) string {
	p = strings.ReplaceAll(p, "\\", "/")
	switch {
	case p == "~":
		p = ctx.Home
	case strings.HasPrefix(p, "~/"):
		p = ctx.Home + p[1:]
	}
	if !path.IsAbs(p) {
		if base == "" {
			base = ctx.Cwd
		}
		p = path.Join(base, p)
	}
	return path.Clean(p)
}

func (f fakePaths) Candidates(raw string, ctx model.PathContext) []string {
	abs := f.Normalize(raw, ctx.Cwd, ctx)
	out := []string{abs}
	root := strings.TrimSuffix(ctx.ProjectRoot, "/")
	if root != "" && strings.HasPrefix(abs, root+"/") {
		out = append(out, strings.TrimPrefix(abs, root+"/"))
	}
	return out
}

type fakeGlobs struct {
	patterns []string
	pos, neg []string
	fold     bool
}

func (f fakePaths) CompileGlobs(patterns []string, ctx model.PathContext) (ports.GlobMatcher, error) {
	g := &fakeGlobs{patterns: patterns, fold: ctx.GOOS == "windows" || ctx.GOOS == "darwin"}
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = ctx.Home + p[1:]
		}
		if !strings.Contains(p, "/") {
			p = "**/" + p
		}
		if g.fold {
			p = strings.ToLower(p)
		}
		if !doublestar.ValidatePattern(p) {
			return nil, errors.New("bad glob " + p)
		}
		if neg {
			g.neg = append(g.neg, p)
		} else {
			g.pos = append(g.pos, p)
		}
	}
	return g, nil
}

func (g *fakeGlobs) Patterns() []string { return g.patterns }

func (g *fakeGlobs) Match(cands ...string) bool {
	matched := false
	for _, c := range cands {
		if g.fold {
			c = strings.ToLower(c)
		}
		for _, p := range g.neg {
			if ok, _ := doublestar.Match(p, c); ok {
				return false
			}
		}
		for _, p := range g.pos {
			if ok, _ := doublestar.Match(p, c); ok {
				matched = true
			}
		}
	}
	return matched
}

// ---- fake shell parsers (canned CommandLists from testdata) ----------------

type cannedEntry struct {
	List  *model.CommandList `json:"list"`
	Error string             `json:"error"`
}

type cannedParsers struct {
	mu      sync.Mutex
	Bash    map[string]cannedEntry `json:"bash"`
	PS      map[string]cannedEntry `json:"ps"`
	missing []string
}

func loadCanned(t testing.TB) *cannedParsers {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c cannedParsers
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}

func (c *cannedParsers) lookup(m map[string]cannedEntry, src string) (*model.CommandList, error) {
	if e, ok := m[src]; ok {
		if e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return e.List, nil
	}
	c.mu.Lock()
	c.missing = append(c.missing, src)
	c.mu.Unlock()
	return nil, errors.New("no canned parse for " + src)
}

func (c *cannedParsers) ParseBash(src string) (*model.CommandList, error) {
	return c.lookup(c.Bash, src)
}

func (c *cannedParsers) ParsePowerShell(src string) (*model.CommandList, error) {
	return c.lookup(c.PS, src)
}

func (c *cannedParsers) assertNoMisses(t testing.TB) {
	t.Helper()
	if len(c.missing) > 0 {
		sort.Strings(c.missing)
		t.Errorf("commands without a canned parse in testdata/commands.json: %q", c.missing)
	}
}

// ---- fake claude-code adapter / registry -----------------------------------

type fakeClaude struct{}

func (fakeClaude) ID() model.ClientID                { return model.ClientClaude }
func (fakeClaude) Capabilities() model.CapabilitySet { return model.Capabilities(model.ClientClaude) }
func (fakeClaude) NativeEvents() []model.NativeEvent {
	return model.NativeEventTable[model.ClientClaude]
}
func (fakeClaude) Render(*model.Event, *model.Decision) ([]byte, error) {
	return nil, errors.New("fake adapter does not render")
}

type claudePayload struct {
	SessionID            string          `json:"session_id"`
	Cwd                  string          `json:"cwd"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	ToolUseID            string          `json:"tool_use_id"`
	AgentID              string          `json:"agent_id"`
	AgentType            *string         `json:"agent_type"`
	Prompt               string          `json:"prompt"`
	Error                string          `json:"error"`
	LastAssistantMessage *string         `json:"last_assistant_message"`
	StopHookActive       bool            `json:"stop_hook_active"`
}

func (a fakeClaude) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	ne, ok := model.LookupNative(model.ClientClaude, native)
	if !ok {
		return nil, model.ErrUnknownEvent
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stdin, &m); err != nil || m == nil {
		return nil, model.ErrBadInput
	}
	var p claudePayload
	_ = json.Unmarshal(stdin, &p) // tolerant: type mismatches leave zero values
	ev := &model.Event{
		Client: model.ClientClaude, Name: ne.Event, Native: native,
		SessionID: p.SessionID, Cwd: p.Cwd, AgentID: p.AgentID, Prompt: p.Prompt, Error: p.Error,
		ToolResponse: p.ToolResponse, StopHookActive: p.StopHookActive,
	}
	if p.AgentType != nil {
		ev.AgentType, ev.HasAgentType = *p.AgentType, true
	}
	if p.LastAssistantMessage != nil {
		ev.LastAssistantMessage, ev.HasLastAssistantMessage = *p.LastAssistantMessage, true
	}
	if len(p.ToolResponse) > 0 {
		ev.ToolResponseText = FlattenToolResponse(p.ToolResponse)
	}
	if p.ToolName != "" {
		tc, _ := a.NormalizeTool(p.ToolName, p.ToolInput)
		tc.UseID = p.ToolUseID
		ev.Tool = &tc
	}
	return ev, nil
}

func (fakeClaude) NormalizeTool(raw string, input json.RawMessage) (model.ToolCall, error) {
	tc := model.ToolCall{Name: raw, RawName: raw, Input: input, Fields: map[string][]string{}}
	var obj map[string]any
	if err := json.Unmarshal(input, &obj); err != nil {
		return tc, err
	}
	flattenStrings("", obj, tc.Fields)
	str := func(k string) string { s, _ := obj[k].(string); return s }
	switch raw {
	case model.ToolBash, model.ToolPowerShell:
		tc.Shell = model.ShellBash
		if raw == model.ToolPowerShell {
			tc.Shell = model.ShellPowerShell
		}
		tc.Command = str("command")
		tc.RunInBackground, _ = obj["run_in_background"].(bool)
	case model.ToolEdit:
		tc.Targets = []string{str("file_path")}
		tc.Edits = []model.FileEdit{{Path: str("file_path"), OldString: str("old_string"), NewString: str("new_string")}}
	case model.ToolWrite:
		tc.Targets = []string{str("file_path")}
		tc.Edits = []model.FileEdit{{Path: str("file_path"), NewString: str("content")}}
	case model.ToolMultiEdit:
		tc.Targets = []string{str("file_path")}
		if es, ok := obj["edits"].([]any); ok {
			for _, e := range es {
				if em, ok := e.(map[string]any); ok {
					o, _ := em["old_string"].(string)
					n, _ := em["new_string"].(string)
					tc.Edits = append(tc.Edits, model.FileEdit{Path: str("file_path"), OldString: o, NewString: n})
				}
			}
		}
	case model.ToolNotebookEdit:
		tc.Targets = []string{str("notebook_path")}
		tc.Edits = []model.FileEdit{{Path: str("notebook_path"), NewString: str("new_source")}}
	case model.ToolRead:
		tc.Targets = []string{str("file_path")}
	case "Task":
		tc.Name = model.ToolAgent
	}
	return tc, nil
}

func flattenStrings(prefix string, v any, out map[string][]string) {
	switch x := v.(type) {
	case string:
		out[prefix] = append(out[prefix], x)
	case map[string]any:
		for k, el := range x {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenStrings(key, el, out)
		}
	case []any:
		for _, el := range x {
			flattenStrings(prefix, el, out)
		}
	}
}

type fakeRegistry struct{}

func (fakeRegistry) Get(id model.ClientID) (ports.Adapter, bool) {
	if id == model.ClientClaude {
		return fakeClaude{}, true
	}
	return nil, false
}
func (fakeRegistry) All() []ports.Adapter { return []ports.Adapter{fakeClaude{}} }

// newTestEngine returns an Engine wired with all fakes.
func newTestEngine(t testing.TB) (*Engine, *cannedParsers) {
	t.Helper()
	c := loadCanned(t)
	return NewEngine(Options{Paths: fakePaths{}, Bash: c, PS: c, Adapters: fakeRegistry{}, GOOS: "linux"}), c
}

// ---- helpers ---------------------------------------------------------------

// staticCommands is a ShellCommands returning fixed values.
type staticCommands struct {
	cl  *model.CommandList
	err error
}

func (s staticCommands) Get() (*model.CommandList, error) { return s.cl, s.err }

// decodeRule decodes one rule from JSON (non-strict) for tests.
func decodeRule(t testing.TB, js string) model.Rule {
	t.Helper()
	var r model.Rule
	dec := json.NewDecoder(bytes.NewReader([]byte(js)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decode rule: %v\n%s", err, js)
	}
	return r
}

func ruleFileJSON(rules ...string) []byte {
	return []byte(`{"version":1,"rules":[` + strings.Join(rules, ",") + `]}`)
}
