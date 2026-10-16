package hook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func auditDump(h *harness) string {
	var b strings.Builder
	for _, r := range h.audit.all() {
		b.WriteString(string(r.Type) + " " + string(r.Detail) + "; ")
	}
	return b.String()
}

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
	// P2 inputs. Tier wires a fake TierProbe; ProjectSandbox (dir → entries;
	// an empty object still wires the checker) a fake ProjectSandboxChecker;
	// Env sets variables in the hook's environment; SessionStarted makes the
	// fake state report StartedAt (i.e. not the first event of the session).
	Tier             *model.TierInfo     `json:"tier"`
	ProjectSandbox   map[string][]string `json:"project_sandbox"`
	WeakenedProject  string              `json:"weakened_project"`
	RedirectsIgnored []string            `json:"redirects_ignored"`
	Env              map[string]string   `json:"env"`
	IDsNil           bool                `json:"ids_nil"`
	SpawnError       bool                `json:"spawn_error"`
	SessionStarted   bool                `json:"session_started"`
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
		// P2 wants. DiagNames is the exact ordered list of diag names in the
		// audit; DiagDetails maps a diag name to its exact detail minus the
		// "diag" key; SpawnArgv the exact spawned argvs ($SELF = Deps.Self);
		// SpawnLog a suffix of the spawn log path; StdoutAll fragments that
		// must all appear in stdout; ProbeCalls the Tier.Probe call count;
		// ProjectChecked the dirs the checker was asked about, in order.
		DiagNames      *[]string                  `json:"diag_names"`
		DiagDetails    map[string]json.RawMessage `json:"diag_details"`
		SpawnArgv      [][]string                 `json:"spawn_argv"`
		SpawnLog       string                     `json:"spawn_log"`
		StdoutAll      []string                   `json:"stdout_all"`
		ProbeCalls     *int                       `json:"probe_calls"`
		ProjectChecked *[]string                  `json:"project_checked"`
		DropAgent      bool                       `json:"drop_agent"`
		DropSession    bool                       `json:"drop_session"`
		Delta          *struct {
			ToolCall   *bool     `json:"tool_call"`
			DedupeKey  *string   `json:"dedupe_key"`
			ReadPaths  *[]string `json:"read_paths"`
			FlushEvals *bool     `json:"flush_evals"`
			// FlushedEvals is the exact set of counts the commit flushes
			// (prior state counts plus this event's own evaluation).
			FlushedEvals map[string]model.EvalCount `json:"flushed_evals"`
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
			if c.SessionStarted {
				if h.state.session == nil {
					h.state.session = &fakeSession{}
				}
				h.state.session.started, h.state.session.hasStart = testNow.Add(-time.Minute), true
			}
			var tier *fakeTier
			if c.Tier != nil {
				tier = &fakeTier{info: *c.Tier}
				h.deps.Tier = tier
			}
			var proj *fakeProject
			if c.ProjectSandbox != nil {
				proj = &fakeProject{byDir: c.ProjectSandbox}
				h.deps.ProjectSandbox = proj
			}
			h.deps.WeakenedProject = model.WeakenedProjectPolicy(c.WeakenedProject)
			h.deps.RedirectsIgnored = c.RedirectsIgnored
			for k, v := range c.Env {
				h.sb.Vars[k] = v
			}
			if !c.IDsNil {
				h.deps.IDs = &sys.SeqID{}
			}
			if c.SpawnError {
				h.spawner.err = errors.New("fork failed: resource temporarily unavailable")
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
			if w.SpawnArgv != nil {
				want := make([][]string, len(w.SpawnArgv))
				for i, a := range w.SpawnArgv {
					want[i] = append([]string(nil), a...)
					for j := range want[i] {
						want[i][j] = strings.ReplaceAll(want[i][j], "$SELF", h.deps.Self)
					}
				}
				got := h.spawner.spawned()
				if len(got) == 0 && len(want) == 0 {
					got, want = nil, nil
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("spawned = %q, want %q", got, want)
				}
				if w.SpawnLog != "" {
					logs := h.spawner.logPaths()
					if len(logs) != 1 || !strings.HasSuffix(filepath.ToSlash(logs[0]), w.SpawnLog) ||
						!strings.HasPrefix(logs[0], h.deps.Layout.RequestsDir) {
						t.Errorf("spawn log = %q, want <RequestsDir>/…%s", logs, w.SpawnLog)
					}
				}
			} else if len(w.Spawned) > 0 {
				sp := h.spawner.spawned()
				if len(sp) != 1 || !reflect.DeepEqual(sp[0][1:], w.Spawned) {
					t.Errorf("spawned = %v, want one child running %v", sp, w.Spawned)
				}
			} else if sp := h.spawner.spawned(); len(sp) != 0 {
				t.Errorf("unexpected spawn: %v", sp)
			}
			for _, frag := range w.StdoutAll {
				if !strings.Contains(out, frag) {
					t.Errorf("stdout = %q, want it to contain %q", out, frag)
				}
			}
			if w.ProbeCalls != nil {
				got := 0
				if tier != nil {
					got = tier.probeCalls()
				}
				if got != *w.ProbeCalls {
					t.Errorf("Tier.Probe calls = %d, want %d", got, *w.ProbeCalls)
				}
			}
			if w.ProjectChecked != nil {
				got := []string{}
				if proj != nil {
					got = append(got, proj.checked()...)
				}
				if !reflect.DeepEqual(got, *w.ProjectChecked) {
					t.Errorf("project checker dirs = %v, want %v", got, *w.ProjectChecked)
				}
			}
			if w.DiagNames != nil {
				got := []string{}
				for _, r := range h.audit.all() {
					if r.Type != model.AuditDiag {
						continue
					}
					var m map[string]json.RawMessage
					if err := json.Unmarshal(r.Detail, &m); err != nil {
						t.Fatalf("diag detail %s: %v", r.Detail, err)
					}
					var name string
					_ = json.Unmarshal(m["diag"], &name)
					got = append(got, name)
				}
				if !reflect.DeepEqual(got, *w.DiagNames) {
					t.Errorf("diag names = %v, want %v", got, *w.DiagNames)
				}
			}
			for name, wantDetail := range w.DiagDetails {
				found := false
				for _, r := range h.audit.all() {
					var m map[string]json.RawMessage
					if r.Type != model.AuditDiag || json.Unmarshal(r.Detail, &m) != nil {
						continue
					}
					var dn string
					_ = json.Unmarshal(m["diag"], &dn)
					if dn != name {
						continue
					}
					delete(m, "diag")
					var a, b any
					got, _ := json.Marshal(m)
					_ = json.Unmarshal(got, &a)
					_ = json.Unmarshal(wantDetail, &b)
					if reflect.DeepEqual(a, b) {
						found = true
					}
				}
				if !found {
					t.Errorf("no diag %s with detail %s in %s", name, wantDetail, auditDump(h))
				}
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
				if w.Delta.FlushedEvals != nil {
					if !reflect.DeepEqual(cc.FlushedEvals, w.Delta.FlushedEvals) {
						t.Errorf("delta.flushed_evals = %v, want %v", cc.FlushedEvals, w.Delta.FlushedEvals)
					}
					var sum map[string]model.EvalCount
					for _, r := range h.audit.all() {
						if r.Type == model.AuditEvalSummary {
							sum = r.Evals
						}
					}
					if !reflect.DeepEqual(sum, w.Delta.FlushedEvals) {
						t.Errorf("eval_summary evals = %v, want %v", sum, w.Delta.FlushedEvals)
					}
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
