package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

type pipelineFile struct {
	Cases []pipelineCase `json:"cases"`
}

type pipelineCase struct {
	Desc      string             `json:"desc"`
	Client    string             `json:"client"`
	Native    string             `json:"native"`
	Stdin     string             `json:"stdin"`
	StdinRaw  *string            `json:"stdin_raw"`
	Verdict   *model.Verdict     `json:"verdict"`
	Injection *model.Injection   `json:"injection"`
	Commands  *model.CommandList `json:"commands"`
	State     *struct {
		Evals map[string]model.EvalCount `json:"evals"`
	} `json:"state"`
	AuditError       bool `json:"audit_error"`
	StateLockTimeout bool `json:"state_lock_timeout"`
	StateDuplicate   bool `json:"state_duplicate"`
	GCStampAgeH      *int `json:"gc_stamp_age_h"`
	Want             struct {
		Exit           int       `json:"exit"`
		Stdout         *string   `json:"stdout"`
		StdoutContains string    `json:"stdout_contains"`
		StderrPrefix   string    `json:"stderr_prefix"`
		AuditTypes     *[]string `json:"audit_types"`
		AuditDecision  string    `json:"audit_decision"`
		AuditDetail    string    `json:"audit_detail"`
		Spawned        []string  `json:"spawned"`
		Evaluated      *bool     `json:"evaluated"`
		DropAgent      bool      `json:"drop_agent"`
		DropSession    bool      `json:"drop_session"`
		Delta          *struct {
			ToolCall   *bool     `json:"tool_call"`
			DedupeKey  *string   `json:"dedupe_key"`
			ReadPaths  *[]string `json:"read_paths"`
			FlushEvals *bool     `json:"flush_evals"`
		} `json:"delta"`
	} `json:"want"`
}

func loadPipeline(t *testing.T) []pipelineCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "pipeline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f pipelineFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("decode pipeline.json: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("pipeline.json has no cases")
	}
	return f.Cases
}

// TestPipelineJSON runs every row of testdata/pipeline.json against fake ports.
func TestPipelineJSON(t *testing.T) {
	for _, c := range loadPipeline(t) {
		t.Run(c.Desc, func(t *testing.T) {
			h := newHarness(t)
			client := model.ClientClaude
			if c.Client != "" {
				client = model.ClientID(c.Client)
			}
			stdin := ""
			switch {
			case c.StdinRaw != nil:
				stdin = *c.StdinRaw
			case c.Stdin != "":
				b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(c.Stdin)))
				if err != nil {
					t.Fatal(err)
				}
				stdin = string(b)
			}
			if c.Verdict != nil {
				h.engine.verdict = c.Verdict
			}
			if c.Injection != nil {
				h.injector.inj = c.Injection
			}
			if c.Commands != nil {
				h.bash.err = nil
				h.bash.lists = map[string]*model.CommandList{}
				// Any command string maps to the fixture's CommandList.
				cl := c.Commands
				h.bash.lists["*"] = cl
				h.deps.Bash = starParser{cl}
				h.deps.PS = starParser{cl}
			}
			if c.State != nil {
				h.state.session = &fakeSession{evals: c.State.Evals}
			}
			h.state.lockTimeout = c.StateLockTimeout
			h.state.duplicate = c.StateDuplicate
			if c.AuditError {
				h.audit.err = os.ErrPermission
			}
			stamp := h.deps.Layout.GCStamp
			if c.GCStampAgeH != nil {
				if err := os.MkdirAll(filepath.Dir(stamp), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stamp, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				old := h.clock.Now().Add(-time.Duration(*c.GCStampAgeH) * time.Hour)
				if err := os.Chtimes(stamp, old, old); err != nil {
					t.Fatal(err)
				}
			}

			code := h.main(client, c.Native, stdin)
			w := c.Want
			if code != w.Exit {
				t.Errorf("exit = %d, want %d (stderr %q)", code, w.Exit, h.stderr.String())
			}
			out := h.stdout.String()
			if w.Stdout != nil && out != *w.Stdout {
				t.Errorf("stdout = %q, want %q", out, *w.Stdout)
			}
			if w.StdoutContains != "" && !strings.Contains(out, w.StdoutContains) {
				t.Errorf("stdout = %q, want it to contain %q", out, w.StdoutContains)
			}
			if w.Exit == 1 {
				h.wantFailure(code)
			}
			if w.StderrPrefix != "" && !strings.HasPrefix(h.stderr.String(), w.StderrPrefix) {
				t.Errorf("stderr = %q, want prefix %q", h.stderr.String(), w.StderrPrefix)
			}
			if w.AuditTypes != nil {
				got := []string{}
				for _, ty := range h.audit.types() {
					got = append(got, string(ty))
				}
				if !reflect.DeepEqual(got, *w.AuditTypes) {
					t.Errorf("audit types = %v, want %v", got, *w.AuditTypes)
				}
			}
			if w.AuditDecision != "" {
				found := false
				for _, r := range h.audit.all() {
					if r.Type == model.AuditHit && r.Decision == w.AuditDecision {
						found = true
					}
				}
				if !found {
					t.Errorf("no hit record with decision %q in %+v", w.AuditDecision, h.audit.all())
				}
			}
			if w.AuditDetail != "" {
				found := false
				for _, r := range h.audit.all() {
					if strings.Contains(string(r.Detail), w.AuditDetail) {
						found = true
					}
				}
				if !found {
					t.Errorf("no audit record with detail %q in %+v", w.AuditDetail, h.audit.all())
				}
			}
			if len(w.Spawned) > 0 {
				sp := h.spawner.spawned()
				if len(sp) != 1 || !reflect.DeepEqual(sp[0][1:], w.Spawned) {
					t.Errorf("spawned = %v, want one child running %v", sp, w.Spawned)
				}
			} else if sp := h.spawner.spawned(); len(sp) != 0 {
				t.Errorf("unexpected spawn: %v", sp)
			}
			if w.Evaluated != nil && (h.engine.calls() > 0) != *w.Evaluated {
				t.Errorf("evaluated = %v, want %v", h.engine.calls() > 0, *w.Evaluated)
			}
			if got := len(h.state.droppedAgent) > 0; got != w.DropAgent {
				t.Errorf("drop_agent = %v, want %v", got, w.DropAgent)
			}
			if got := len(h.state.droppedSess) > 0; got != w.DropSession {
				t.Errorf("drop_session = %v, want %v", got, w.DropSession)
			}
			if w.Delta != nil {
				cc := h.state.lastCommit(t).delta
				if w.Delta.ToolCall != nil && cc.ToolCall != *w.Delta.ToolCall {
					t.Errorf("delta.tool_call = %v, want %v", cc.ToolCall, *w.Delta.ToolCall)
				}
				if w.Delta.DedupeKey != nil && cc.DedupeKey != *w.Delta.DedupeKey {
					t.Errorf("delta.dedupe_key = %q, want %q", cc.DedupeKey, *w.Delta.DedupeKey)
				}
				if w.Delta.FlushEvals != nil && cc.FlushEvals != *w.Delta.FlushEvals {
					t.Errorf("delta.flush_evals = %v, want %v", cc.FlushEvals, *w.Delta.FlushEvals)
				}
				if w.Delta.ReadPaths != nil {
					got := cc.ReadPaths
					if got == nil {
						got = []string{}
					}
					if !reflect.DeepEqual(got, *w.Delta.ReadPaths) {
						t.Errorf("delta.read_paths = %v, want %v", got, *w.Delta.ReadPaths)
					}
				}
			}
		})
	}
}

// starParser returns the same CommandList for every source string.
type starParser struct{ cl *model.CommandList }

func (s starParser) ParseBash(string) (*model.CommandList, error)       { return s.cl, nil }
func (s starParser) ParsePowerShell(string) (*model.CommandList, error) { return s.cl, nil }
