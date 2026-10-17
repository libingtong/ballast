package hook

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// callLog records the order in which the pipeline touches its collaborators.
type callLog struct {
	mu  sync.Mutex
	seq []string
}

func (c *callLog) add(s string) {
	c.mu.Lock()
	c.seq = append(c.seq, s)
	c.mu.Unlock()
}

func (c *callLog) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seq...)
}

func (c *callLog) index(s string) int {
	for i, x := range c.list() {
		if x == s {
			return i
		}
	}
	return -1
}

// ---- adapter --------------------------------------------------------------

type fakeAdapter struct {
	id  model.ClientID
	log *callLog
	// renderErr makes Render fail.
	renderErr error
	// parseOverride replaces ParseEvent when set.
	parseOverride func(native string, stdin []byte) (*model.Event, error)
}

func (a *fakeAdapter) ID() model.ClientID                { return a.id }
func (a *fakeAdapter) Capabilities() model.CapabilitySet { return model.Capabilities(a.id) }
func (a *fakeAdapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[a.id] }

func (a *fakeAdapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	a.log.add("parse")
	if a.parseOverride != nil {
		return a.parseOverride(native, stdin)
	}
	ne, ok := model.LookupNative(a.id, native)
	if !ok {
		return nil, fmt.Errorf("%w: %s", model.ErrUnknownEvent, native)
	}
	var m map[string]any
	if err := json.Unmarshal(stdin, &m); err != nil || m == nil {
		return nil, model.ErrBadInput
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := m[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	ev := &model.Event{
		Client:         a.id,
		Name:           ne.Event,
		Native:         native,
		SessionID:      str("session_id", "conversation_id"),
		TranscriptPath: str("transcript_path"),
		Cwd:            str("cwd"),
		AgentID:        str("agent_id", "subagent_id"),
		AgentType:      str("agent_type"),
		Prompt:         str("prompt"),
		Source:         str("source"),
	}
	_, ev.HasAgentType = m["agent_type"]
	if s, ok := m["last_assistant_message"].(string); ok {
		ev.LastAssistantMessage, ev.HasLastAssistantMessage = s, true
	}
	if s, ok := m["text"].(string); ok && ne.Event == model.EventAgentResponse {
		ev.LastAssistantMessage, ev.HasLastAssistantMessage = s, true
	}
	if raw, ok := m["tool_response"]; ok {
		ev.ToolResponse, _ = json.Marshal(raw)
	}
	if name := str("tool_name"); name != "" || ne.ToolHint != "" {
		if name == "" {
			name = ne.ToolHint
		}
		in, _ := m["tool_input"].(map[string]any)
		t := &model.ToolCall{Name: name, RawName: name, UseID: str("tool_use_id")}
		switch name {
		case "Bash":
			t.Shell = model.ShellBash
		case "PowerShell":
			t.Shell = model.ShellPowerShell
		}
		if c, ok := in["command"].(string); ok {
			t.Command = c
		}
		for _, k := range []string{"file_path", "notebook_path", "path"} {
			if s, ok := in[k].(string); ok && s != "" {
				t.Targets = append(t.Targets, s)
			} else if s, ok := m[k].(string); ok && s != "" { // Cursor puts them at top level
				t.Targets = append(t.Targets, s)
			}
		}
		if arr, ok := in["targets"].([]any); ok {
			for _, x := range arr {
				if s, ok := x.(string); ok {
					t.Targets = append(t.Targets, s)
				}
			}
		}
		ev.Tool = t
	}
	return ev, nil
}

func (a *fakeAdapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	return model.ToolCall{Name: rawName, RawName: rawName}, nil
}

func (a *fakeAdapter) Render(ev *model.Event, d *model.Decision) ([]byte, error) {
	a.log.add("render")
	if a.renderErr != nil {
		return nil, a.renderErr
	}
	if d.Empty() {
		return nil, nil
	}
	if d.Action.Severity() >= model.ActionWarn.Severity() {
		if _, ok := a.Capabilities().Lookup(d.Event, d.Action); !ok {
			return nil, fmt.Errorf("unsupported %s/%s", d.Event, d.Action)
		}
	}
	var out any
	switch d.Action {
	case model.ActionDeny, model.ActionAsk:
		if d.Event == model.EventPreToolUse {
			out = map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "PreToolUse", "permissionDecision": string(d.Action), "permissionDecisionReason": d.Reason}}
		} else {
			out = map[string]any{"decision": "block", "reason": d.Reason}
		}
	default:
		text := d.Context
		if d.Action == model.ActionWarn && d.Reason != "" {
			text = d.Reason + "\n\n" + d.Context
		}
		out = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": string(d.Event), "additionalContext": text}}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type fakeRegistry struct {
	ad map[model.ClientID]*fakeAdapter
}

func (r fakeRegistry) Get(id model.ClientID) (ports.Adapter, bool) {
	a, ok := r.ad[id]
	if !ok {
		return nil, false
	}
	return a, true
}

func (r fakeRegistry) All() []ports.Adapter {
	var out []ports.Adapter
	for _, id := range model.AllClients {
		if a, ok := r.ad[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

// ---- rules / engine / injector --------------------------------------------

type fakeRules struct {
	log *callLog
	rs  *model.CompiledRuleSet
	err error
}

func (f *fakeRules) Load(req model.LoadRequest) (*model.CompiledRuleSet, error) {
	f.log.add("load")
	if f.err != nil {
		return nil, f.err
	}
	return f.rs, nil
}

type fakeEngine struct {
	log     *callLog
	mu      sync.Mutex
	verdict *model.Verdict
	err     error
	panicv  any
	sleep   time.Duration
	inputs  []*model.EvalInput
	each    func(in *model.EvalInput) *model.Verdict
}

func (f *fakeEngine) Evaluate(rs *model.CompiledRuleSet, in *model.EvalInput) (*model.Verdict, error) {
	f.log.add("evaluate")
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
	if f.panicv != nil {
		panic(f.panicv)
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.each != nil {
		return f.each(in), nil
	}
	return f.verdict, nil
}

func (f *fakeEngine) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func (f *fakeEngine) lastInput() *model.EvalInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) == 0 {
		return nil
	}
	return f.inputs[len(f.inputs)-1]
}

type fakeInjector struct {
	log  *callLog
	mu   sync.Mutex
	inj  *model.Injection
	err  error
	reqs []*model.InjectRequest
}

func (f *fakeInjector) Build(req *model.InjectRequest) (*model.Injection, error) {
	f.log.add("inject")
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.inj == nil {
		return &model.Injection{}, nil
	}
	cp := *f.inj
	return &cp, nil
}

func (f *fakeInjector) lastReq() *model.InjectRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return nil
	}
	return f.reqs[len(f.reqs)-1]
}

// ---- shell parsers ---------------------------------------------------------

type fakeBash struct {
	mu     sync.Mutex
	lists  map[string]*model.CommandList
	err    error
	panicv any
	calls  int
}

func (f *fakeBash) ParseBash(src string) (*model.CommandList, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.panicv != nil {
		panic(f.panicv)
	}
	if f.err != nil {
		return nil, f.err
	}
	if cl, ok := f.lists[src]; ok {
		return cl, nil
	}
	return &model.CommandList{Shell: model.ShellBash}, nil
}

func (f *fakeBash) ParsePowerShell(src string) (*model.CommandList, error) {
	cl, err := f.ParseBash(src)
	if cl != nil {
		c := *cl
		c.Shell = model.ShellPowerShell
		return &c, err
	}
	return cl, err
}

func (f *fakeBash) parseCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ---- paths ------------------------------------------------------------------

type fakePaths struct{}

func (fakePaths) Normalize(p, base string, ctx model.PathContext) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = ctx.Home + p[1:]
	}
	if isDriveAbs(p) { // a native Windows path (sandbox homes): slash form
		p = strings.ReplaceAll(p, `\`, "/")
	}
	if !path.IsAbs(p) && !isDriveAbs(p) {
		p = path.Join(base, p)
	}
	return path.Clean(p)
}

// isDriveAbs reports "C:\..." and "C:/...".
func isDriveAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}

func (fakePaths) Candidates(raw string, ctx model.PathContext) []string { return []string{raw} }

func (fakePaths) CompileGlobs(patterns []string, ctx model.PathContext) (ports.GlobMatcher, error) {
	return nil, errors.New("not used")
}

// ---- state --------------------------------------------------------------------

type fakeSession struct {
	started    time.Time
	hasStart   bool
	reads      map[string]time.Time
	evals      map[string]model.EvalCount
	lastText   string
	generation string
	reported   map[string]bool
	lastJobAt  time.Time
	recent     []model.EventSummary
}

func (s *fakeSession) Seen(string) bool { return false }
func (s *fakeSession) HasRead(n string) bool {
	_, ok := s.reads[n]
	return ok
}
func (s *fakeSession) LastReadAt(n string) (time.Time, bool) { t, ok := s.reads[n]; return t, ok }
func (s *fakeSession) StartedAt() (time.Time, bool)          { return s.started, s.hasStart }
func (s *fakeSession) ToolCount() int                        { return 0 }
func (s *fakeSession) ToolCountSinceInject() int             { return 0 }
func (s *fakeSession) PromptCount() int                      { return 0 }
func (s *fakeSession) PrevPromptTail() string                { return "" }
func (s *fakeSession) PriorHits(string) int                  { return 0 }
func (s *fakeSession) Generation() string                    { return s.generation }
func (s *fakeSession) RuleDigest() map[string]model.Action   { return nil }
func (s *fakeSession) FullInjected() bool                    { return false }
func (s *fakeSession) ReportedJob(id string) bool            { return s.reported[id] }
func (s *fakeSession) LastJobReportAt() time.Time            { return s.lastJobAt }
func (s *fakeSession) LastAssistantText() string             { return s.lastText }
func (s *fakeSession) LastToolFailureAt() (time.Time, bool)  { return time.Time{}, false }
func (s *fakeSession) Recent(n int) []model.EventSummary {
	if n >= len(s.recent) {
		return s.recent
	}
	return s.recent[len(s.recent)-n:]
}
func (s *fakeSession) Evals() map[string]model.EvalCount { return s.evals }
func (s *fakeSession) EventSeq() int64                   { return 0 }

type commitCall struct {
	key   model.ScopeKey
	delta model.StateDelta
}

type fakeStore struct {
	log *callLog
	mu  sync.Mutex

	session  *fakeSession // returned by Load for every key (nil ⇒ empty)
	loadInfo model.StateLoadInfo
	loadErr  error

	lockTimeout bool
	duplicate   bool
	corrupt     bool
	commitErr   error
	commitInfo  *model.StateCommitInfo // overrides everything above when set
	commitSleep time.Duration

	commits      []commitCall
	droppedAgent []model.ScopeKey
	droppedSess  []model.ScopeKey
	dropErr      error
	seq          int64
}

func (f *fakeStore) Load(key model.ScopeKey) (model.SessionState, model.StateLoadInfo, error) {
	f.log.add("state.load")
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.session
	if s == nil {
		s = &fakeSession{}
	}
	return s, f.loadInfo, f.loadErr
}

func (f *fakeStore) Commit(key model.ScopeKey, d *model.StateDelta) (model.StateCommitInfo, error) {
	f.log.add("commit")
	if f.commitSleep > 0 {
		time.Sleep(f.commitSleep)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitErr != nil {
		return model.StateCommitInfo{}, f.commitErr
	}
	f.commits = append(f.commits, commitCall{key: key, delta: *d})
	if f.commitInfo != nil {
		return *f.commitInfo, nil
	}
	f.seq++
	info := model.StateCommitInfo{Applied: true, EventSeq: f.seq, Corrupt: f.corrupt}
	if f.lockTimeout {
		info = model.StateCommitInfo{LockTimeout: true, LockWait: 20 * time.Millisecond}
	}
	if f.duplicate {
		info.Duplicate = true
	}
	return info, nil
}

func (f *fakeStore) DropAgent(key model.ScopeKey) error {
	f.log.add("drop_agent")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.droppedAgent = append(f.droppedAgent, key)
	return f.dropErr
}

func (f *fakeStore) DropSession(key model.ScopeKey) error {
	f.log.add("drop_session")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.droppedSess = append(f.droppedSess, key)
	return f.dropErr
}

func (f *fakeStore) lastCommit(t testing.TB) commitCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.commits) == 0 {
		t.Fatal("no state commit happened")
	}
	return f.commits[len(f.commits)-1]
}

func (f *fakeStore) commitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commits)
}

// ---- audit ----------------------------------------------------------------------

type fakeAudit struct {
	log   *callLog
	mu    sync.Mutex
	err   error
	calls [][]model.AuditRecord
}

func (f *fakeAudit) Append(recs []model.AuditRecord) error {
	f.log.add("audit")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, append([]model.AuditRecord(nil), recs...))
	return nil
}

func (f *fakeAudit) all() []model.AuditRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.AuditRecord
	for _, c := range f.calls {
		out = append(out, c...)
	}
	return out
}

func (f *fakeAudit) appendCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeAudit) types() []model.AuditType {
	var out []model.AuditType
	for _, r := range f.all() {
		out = append(out, r.Type)
	}
	return out
}

// ---- repo / evidence / spawner -------------------------------------------------

type fakeRepo struct {
	log      *callLog
	info     *model.RepoInfo
	err      error
	branch   string
	branchTr model.Tri
	mu       sync.Mutex
	branches int
}

func (f *fakeRepo) Locate(cwd string) (*model.RepoInfo, error) {
	f.log.add("locate")
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func (f *fakeRepo) Branch(repo *model.RepoInfo) (string, model.Tri) {
	f.mu.Lock()
	f.branches++
	f.mu.Unlock()
	return f.branch, f.branchTr
}

type fakeView struct {
	finished []model.VerifyRecord
	since    []time.Time
	mu       sync.Mutex
}

func (v *fakeView) VerifyFresh(model.FreshQuery) model.FreshResult {
	return model.FreshResult{Value: model.TriUnknown, Cause: "no_project"}
}
func (v *fakeView) LastFailureAfter(time.Time) (model.Tri, time.Time) {
	return model.TriUnknown, time.Time{}
}
func (v *fakeView) FinishedSince(t time.Time) []model.VerifyRecord {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.since = append(v.since, t)
	var out []model.VerifyRecord
	for _, r := range v.finished {
		if r.TEnd.After(t) {
			out = append(out, r)
		}
	}
	return out
}

type fakeEvidence struct {
	mu       sync.Mutex
	view     *fakeView
	err      error
	opens    int
	deadline time.Time
	repos    []*model.RepoInfo
}

func (f *fakeEvidence) Open(repo *model.RepoInfo, deadline time.Time) (model.EvidenceView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	f.deadline = deadline
	f.repos = append(f.repos, repo)
	if f.err != nil {
		return nil, f.err
	}
	return f.view, nil
}

type fakeSpawner struct {
	mu    sync.Mutex
	argvs [][]string
	envs  [][]string
	logs  []string
	err   error
}

func (f *fakeSpawner) SpawnDetached(argv []string, env []string, logPath string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argvs = append(f.argvs, append([]string(nil), argv...))
	f.envs = append(f.envs, env)
	f.logs = append(f.logs, logPath)
	return 4242, f.err
}

func (f *fakeSpawner) logPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

// fakeTier is a ports.TierProbe returning a fixed TierInfo.
type fakeTier struct {
	mu    sync.Mutex
	info  model.TierInfo
	calls int
}

func (f *fakeTier) Probe() model.TierInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.info
}

func (f *fakeTier) probeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeProject is a ports.ProjectSandboxChecker: dir → entries.
type fakeProject struct {
	mu    sync.Mutex
	byDir map[string][]string
	dirs  []string
}

func (f *fakeProject) Weakened(dir string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs = append(f.dirs, dir)
	return append([]string(nil), f.byDir[dir]...)
}

func (f *fakeProject) checked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dirs...)
}

func (f *fakeSpawner) spawned() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.argvs...)
}

// ---- harness ----------------------------------------------------------------------

var testNow = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

type harness struct {
	t        *testing.T
	sb       *testenv.Sandbox
	clock    *sys.FakeClock
	log      *callLog
	adapters fakeRegistry
	rules    *fakeRules
	engine   *fakeEngine
	bash     *fakeBash
	state    *fakeStore
	audit    *fakeAudit
	repo     *fakeRepo
	evidence *fakeEvidence
	injector *fakeInjector
	spawner  *fakeSpawner
	deps     *Deps

	exitMu sync.Mutex
	exits  []int
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sb := testenv.New(t)
	h := newCoreHarness(sb.Env(), sb.Layout(t))
	h.t, h.sb = t, sb
	return h
}

// newCoreHarness wires all fakes; it needs no *testing.T so the re-exec child
// process can use it too.
func newCoreHarness(env sys.Env, lay layout.Layout) *harness {
	log := &callLog{}
	h := &harness{
		clock:  sys.NewFakeClock(testNow),
		log:    log,
		rules:  &fakeRules{log: log, rs: &model.CompiledRuleSet{Generation: "gen1"}},
		engine: &fakeEngine{log: log, verdict: &model.Verdict{}},
		bash:   &fakeBash{lists: map[string]*model.CommandList{}},
		state:  &fakeStore{log: log},
		audit:  &fakeAudit{log: log},
		repo: &fakeRepo{log: log, branch: "main", branchTr: model.TriTrue, info: &model.RepoInfo{
			Toplevel: "/repo", Inst: "0123456789abcdef", ObjectFormat: "sha1", RemoteHash: "fedcba9876543210",
		}},
		evidence: &fakeEvidence{view: &fakeView{}},
		injector: &fakeInjector{log: log},
		spawner:  &fakeSpawner{},
	}
	h.adapters = fakeRegistry{ad: map[model.ClientID]*fakeAdapter{}}
	for _, id := range model.AllClients {
		h.adapters.ad[id] = &fakeAdapter{id: id, log: log}
	}
	h.deps = &Deps{
		Clock:    h.clock,
		Env:      env,
		FS:       sys.OSFS{},
		Layout:   lay,
		Adapters: h.adapters,
		Rules:    h.rules,
		Engine:   h.engine,
		Bash:     h.bash,
		PS:       h.bash,
		Paths:    fakePaths{},
		State:    h.state,
		Audit:    h.audit,
		Repo:     h.repo,
		Evidence: h.evidence,
		Injector: h.injector,
		Spawner:  h.spawner,
		Self:     "/opt/ballast/bin/ballast",
		Watchdog: 5 * time.Second,
		Exit: func(code int) {
			h.exitMu.Lock()
			h.exits = append(h.exits, code)
			h.exitMu.Unlock()
		},
		Version: "9.9.9-test",
	}
	return h
}

func (h *harness) exitCalls() []int {
	h.exitMu.Lock()
	defer h.exitMu.Unlock()
	return append([]int(nil), h.exits...)
}

// main runs Main against the harness and returns the exit code.
func (h *harness) main(client model.ClientID, native, stdin string) int {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	return Main([]string{"--client", string(client), "--event", native}, strings.NewReader(stdin), &h.stdout, &h.stderr, h.deps)
}

// claude runs a claude-code event.
func (h *harness) claude(native, stdin string) int { return h.main(model.ClientClaude, native, stdin) }

// wantFailure asserts the exception protocol.
func (h *harness) wantFailure(code int) {
	h.t.Helper()
	if code != 1 {
		h.t.Errorf("exit code = %d, want 1", code)
	}
	if h.stdout.Len() != 0 {
		h.t.Errorf("stdout not empty on failure: %q", h.stdout.String())
	}
	if !strings.HasPrefix(h.stderr.String(), "ballast: ") || !strings.HasSuffix(h.stderr.String(), "\n") ||
		strings.Count(h.stderr.String(), "\n") != 1 {
		h.t.Errorf("stderr = %q, want one line starting with \"ballast: \"", h.stderr.String())
	}
}

const (
	preToolBash = `{"session_id":"s1","cwd":"/repo","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"git status"}}`
	denyJSON    = `"permissionDecision":"deny"`
)

func denyVerdict() *model.Verdict {
	return &model.Verdict{
		Action:  model.ActionDeny,
		Reason:  "[r] no",
		Hits:    []model.RuleHit{{RuleID: "r", Layer: model.LayerUser, Action: model.ActionDeny, Target: model.ActionDeny, Effective: model.ActionDeny, Message: "no", Matched: "bash node 0: git status"}},
		Results: []model.RuleResult{{RuleID: "r", Outcome: model.TriTrue}},
	}
}

var errTest = errors.New("injected test error")

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func sha256Sum(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// errFS fails every Lstat with a permission error.
type errFS struct{ sys.FS }

func (errFS) Lstat(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
