package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// ReportVersion is Report.V.
const ReportVersion = 1

// maxUnknownSamples bounds the unknown samples kept per rule (counts stay exact).
const maxUnknownSamples = 50

// maxPending bounds the tool calls per agent awaiting their result.
const maxPending = 256

// shellReadPaths is the shared shell-read rule (model.ShellReadPaths); a
// variable only so tests can run before U2's implementation is merged.
var shellReadPaths = model.ShellReadPaths

type scopeKey struct{ session, agent string }

type pendingCall struct {
	name  string
	input []byte
}

// scope is the replay state of one (session, agent).
type scope struct {
	st      *memState
	pending map[string]pendingCall
	order   []string
	cwd     string
	branch  string
	lastTS  time.Time
	exists  map[string]bool
	agent   string
	session string
}

// runner is one replay.
type runner struct {
	opt     Options
	deps    Deps
	client  model.ClientID
	adapter ports.Adapter

	rs         *model.CompiledRuleSet
	rulesHash  string
	metas      map[string]*ruleMeta
	eventRules map[model.EventName]bool
	anchorEvery,
	batchAfter int

	needsEvidence bool
	probe         *model.CompiledRuleSet // matcher-only variant of the rules that have `when` (nil: none)
	initial       *initialState
	scopes        map[scopeKey]*scope
	// per-session facts
	cwdBySession   map[string]string
	writeBySession map[string][]time.Time // sorted file-tool write times
	evidence       map[string][]model.VerifyRecord
	evidenceFile   []model.VerifyRecord
	projects       map[string]projectEntry // by project root
	pathCtx        map[string]model.PathContext
	homeRaw        string
	homeNorm       string

	// subWritesNoted: sub-agent file writes were pre-scanned into
	// writeBySession; inSubagentFile: a sub-agent file is being replayed
	// (its writes must not be noted twice).
	subWritesNoted, inSubagentFile bool

	items, skipped int
	order          []string
	summaries      map[string]*RuleSummary
	samples        []Sample
	unknownKept    map[string]int
	transcriptRef  string
}

// Run replays one transcript.
func Run(opt Options, deps Deps) (*Report, error) {
	r, err := newRunner(opt, deps)
	if err != nil {
		return nil, err
	}
	if opt.RulesPath != "" {
		if err := r.loadRulesFile(); err != nil {
			return nil, err
		}
	}

	if opt.Subagents && r.client == model.ClientClaude {
		// Sub-agent streams are replayed after the main one; their file
		// writes must already be known when a later main-agent event asks
		// for evidence freshness (contract v1.3, review u9 minor).
		if err := r.prescanSubagentWrites(); err != nil {
			return nil, err
		}
	}

	f, err := deps.FS.OpenFile(opt.Transcript, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("replay: opening transcript: %w", err)
	}
	err = r.processStream(f, "")
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}

	if opt.Subagents && r.client == model.ClientClaude {
		if err := r.processSubagents(); err != nil {
			return nil, err
		}
	}
	if err := r.ensureRules(""); err != nil {
		return nil, err
	}
	return r.report(), nil
}

func newRunner(opt Options, deps Deps) (*runner, error) {
	if opt.Transcript == "" {
		return nil, fmt.Errorf("%w: no transcript given", ErrUsage)
	}
	client := opt.Client
	if client == "" {
		client = model.ClientClaude
	}
	switch opt.Format {
	case "", "json", "md":
	default:
		return nil, fmt.Errorf("%w: unknown format %q (want json or md)", ErrUsage, opt.Format)
	}
	switch opt.EvidenceMode {
	case "", "auto":
	case "file":
		if opt.EvidenceFile == "" {
			return nil, fmt.Errorf("%w: evidence mode file needs an evidence file", ErrUsage)
		}
	default:
		return nil, fmt.Errorf("%w: unknown evidence mode %q (want auto or file)", ErrUsage, opt.EvidenceMode)
	}
	switch client {
	case model.ClientClaude, model.ClientCodex, model.ClientCursor:
	default:
		return nil, fmt.Errorf("%w: no transcript reader for client %q (%w)", ErrUsage, client, model.ErrUnknownClient)
	}
	if deps.FS == nil || deps.Engine == nil || deps.Adapters == nil || deps.Paths == nil || deps.Clock == nil {
		return nil, errors.New("replay: missing dependency (FS, Clock, Adapters, Engine or Paths)")
	}
	if opt.RulesPath == "" && deps.Rules == nil {
		return nil, errors.New("replay: missing dependency Rules (no --rules path given)")
	}
	if opt.RulesPath != "" && deps.Compiler == nil {
		return nil, errors.New("replay: missing dependency Compiler (needed for --rules)")
	}
	adapter, ok := deps.Adapters.Get(client)
	if !ok {
		return nil, fmt.Errorf("replay: no adapter for client %q: %w", client, model.ErrUnknownClient)
	}

	r := &runner{
		opt: opt, deps: deps, client: client, adapter: adapter,
		scopes:         map[scopeKey]*scope{},
		cwdBySession:   map[string]string{},
		writeBySession: map[string][]time.Time{},
		projects:       map[string]projectEntry{},
		evidence:       map[string][]model.VerifyRecord{},
		pathCtx:        map[string]model.PathContext{},
		summaries:      map[string]*RuleSummary{},
		unknownKept:    map[string]int{},
	}
	if opt.InitialState != "" {
		is, err := loadInitialState(deps.FS, opt.InitialState)
		if err != nil {
			return nil, err
		}
		r.initial = is
	}
	r.homeRaw = deps.Home
	if r.homeRaw == "" {
		r.homeRaw, _ = sys.OSEnv{}.HomeDir()
	}
	ctx0 := model.PathContext{Home: r.homeRaw, GOOS: runtime.GOOS}
	if r.homeRaw != "" {
		r.homeNorm = deps.Paths.Normalize(r.homeRaw, "", ctx0)
	}
	r.transcriptRef = r.refOf(opt.Transcript)
	if opt.EvidenceMode == "file" {
		recs, err := loadEvidenceFile(deps.FS, opt.EvidenceFile)
		if err != nil {
			return nil, err
		}
		r.evidenceFile = recs
	}
	return r, nil
}

// refOf renders a path "~/"-relative when it lies under the home directory.
func (r *runner) refOf(path string) string {
	p := filepath.Clean(path)
	if r.homeRaw != "" {
		home := filepath.Clean(r.homeRaw)
		if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}

// ---------------------------------------------------------------- rules ---

func (r *runner) loadRulesFile() error {
	data, err := r.deps.FS.ReadFile(r.opt.RulesPath)
	if err != nil {
		return fmt.Errorf("replay: reading rules: %w", err)
	}
	rf, diags, err := r.deps.Compiler.ParseFile(model.LayerFile, r.opt.RulesPath, data)
	if err != nil {
		return fmt.Errorf("%w: rules file %s: %v", ErrUsage, r.opt.RulesPath, err)
	}
	for _, d := range diags {
		if d.Severity == model.SevError {
			return fmt.Errorf("%w: rules file %s: %s: %s", ErrUsage, r.opt.RulesPath, d.Code, d.Message)
		}
	}
	var loaded []model.LoadedRule
	for i := range rf.Rules {
		if rf.Rules[i].IsEnabled() {
			loaded = append(loaded, model.LoadedRule{Rule: rf.Rules[i], Layer: model.LayerFile, Source: r.opt.RulesPath, Index: i})
		}
	}
	var settings model.Settings
	if rf.Settings != nil {
		settings = *rf.Settings
	}
	sum := sha256.Sum256(data)
	rs, err := r.deps.Compiler.Compile(loaded, settings, hex.EncodeToString(sum[:])[:16])
	if err != nil {
		return fmt.Errorf("%w: compiling %s: %v", ErrUsage, r.opt.RulesPath, err)
	}
	r.setRules(rs)
	return nil
}

// ensureRules loads the rules in effect once, at the first transcript item
// (its cwd selects the repo layer).
func (r *runner) ensureRules(cwd string) error {
	if r.rs != nil {
		return nil
	}
	rs, err := r.deps.Rules.Load(model.LoadRequest{Client: r.client, Cwd: cwd, Now: r.deps.Clock.Now()})
	if err != nil {
		return fmt.Errorf("replay: loading rules: %w", err)
	}
	r.setRules(rs)
	return nil
}

func (r *runner) setRules(rs *model.CompiledRuleSet) {
	if rs == nil {
		rs = &model.CompiledRuleSet{}
	}
	r.rs = rs
	r.rulesHash = rs.Generation
	if r.rulesHash == "" {
		var ids []string
		for i := range rs.Rules {
			ids = append(ids, rs.Rules[i].Rule.ID)
		}
		sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
		r.rulesHash = hex.EncodeToString(sum[:])[:16]
	}
	r.metas = map[string]*ruleMeta{}
	r.probe = nil
	r.eventRules = map[model.EventName]bool{}
	r.anchorEvery, r.batchAfter = model.DefaultAnchorEvery, model.DefaultBatchAfterTools
	if in := rs.Settings.Inject; in != nil {
		if in.AnchorEvery != nil {
			r.anchorEvery = *in.AnchorEvery
		}
		if in.BatchAfterTools != nil {
			r.batchAfter = *in.BatchAfterTools
		}
	}
	for i := range rs.Rules {
		cr := &rs.Rules[i]
		if !cr.Rule.AppliesTo(r.client) {
			continue
		}
		m := analyzeRule(cr)
		r.metas[m.id] = m
		r.needsEvidence = r.needsEvidence || m.usesVerify
		r.eventRules[m.event] = true
		r.order = append(r.order, m.id)
		r.summaries[m.id] = &RuleSummary{RuleID: m.id, Counts: map[Class]int{KnownHit: 0, KnownMiss: 0, Unknown: 0, Approximate: 0}}
	}
	r.probe = r.buildProbe(rs)
}

// buildProbe compiles the matcher-only variant of every rule that has `when`
// conditions: the same rules with `when` removed. Evaluating it tells whether
// a false outcome was already decided by the matcher (doc.go "Classification").
// nil when there is nothing to probe, no Compiler, or compiling fails (the
// classification then simply does not use matcher misses).
func (r *runner) buildProbe(rs *model.CompiledRuleSet) *model.CompiledRuleSet {
	if r.deps.Compiler == nil {
		return nil
	}
	var loaded []model.LoadedRule
	for i := range rs.Rules {
		cr := &rs.Rules[i]
		if m := r.metas[cr.Rule.ID]; m == nil || !m.hasWhen {
			continue
		}
		lr := cr.LoadedRule
		lr.Rule.When = nil
		loaded = append(loaded, lr)
	}
	if len(loaded) == 0 {
		return nil
	}
	probe, err := r.deps.Compiler.Compile(loaded, rs.Settings, rs.Generation)
	if err != nil {
		return nil
	}
	return probe
}

// matcherMisses evaluates the probe rule set and returns the ids of the rules
// whose match alone is false for the event.
func (r *runner) matcherMisses(in *model.EvalInput, sc *scope) map[string]bool {
	if r.probe == nil {
		return nil
	}
	pin := *in
	pin.World = &replayWorld{r: r, sc: sc} // own world: its disk flag must not leak
	v, err := r.deps.Engine.Evaluate(r.probe, &pin)
	if err != nil || v == nil {
		return nil
	}
	out := map[string]bool{}
	for _, res := range v.Results {
		if res.Outcome == model.TriFalse {
			out[res.RuleID] = true
		}
	}
	return out
}

// -------------------------------------------------------- initial state ---

// initialState is the state subset of model.Fixture (doc.go, --initial-state).
type initialState struct {
	read                 []string
	priorHits            map[string]int
	prevPrompt           string
	toolCountSinceInject *int
}

var initialStateKeys = map[string]bool{"read": true, "prior_hits": true, "prev_prompt": true, "tool_count_since_inject": true}

func loadInitialState(fsys sys.FS, path string) (*initialState, error) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("replay: reading initial state: %w", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("%w: initial state %s is not a JSON object: %v", ErrUsage, path, err)
	}
	var bad []string
	for k := range keys {
		if !initialStateKeys[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, fmt.Errorf("%w: initial state %s: key(s) %s not allowed (only read, prior_hits, prev_prompt, tool_count_since_inject)", ErrUsage, path, strings.Join(bad, ", "))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var fx model.Fixture
	if err := dec.Decode(&fx); err != nil {
		return nil, fmt.Errorf("%w: initial state %s: %v", ErrUsage, path, err)
	}
	return &initialState{read: fx.Read, priorHits: fx.PriorHits, prevPrompt: fx.PrevPrompt, toolCountSinceInject: fx.ToolCountSinceInject}, nil
}

// -------------------------------------------------------------- streams ---

// subagentFiles lists <transcript>/subagents/*.jsonl, sorted.
func (r *runner) subagentFiles() (dir string, names []string, err error) {
	dir = filepath.Join(strings.TrimSuffix(r.opt.Transcript, filepath.Ext(r.opt.Transcript)), "subagents")
	ents, err := r.deps.FS.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dir, nil, nil
		}
		return dir, nil, fmt.Errorf("replay: reading subagent transcripts: %w", err)
	}
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return dir, names, nil
}

// prescanSubagentWrites notes the time of every successful file-tool write
// (Edit/Write/MultiEdit/NotebookEdit tool_use with a non-error tool_result,
// the same rule as registerReads) in the sub-agent transcripts, before the
// main stream is replayed.
func (r *runner) prescanSubagentWrites() error {
	dir, names, err := r.subagentFiles()
	if err != nil {
		return err
	}
	for _, name := range names {
		f, err := r.deps.FS.OpenFile(filepath.Join(dir, name), os.O_RDONLY, 0)
		if err != nil {
			return fmt.Errorf("replay: opening subagent transcript: %w", err)
		}
		err = r.scanWrites(f)
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	r.subWritesNoted = true
	return nil
}

func (r *runner) scanWrites(src io.Reader) error {
	tr, err := OpenTranscript(r.client, src)
	if err != nil {
		return err
	}
	pending := map[string]bool{}
	for {
		it, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("replay: reading transcript: %w", err)
		}
		switch it.Kind {
		case ItemToolUse:
			switch it.ToolName {
			case model.ToolEdit, model.ToolWrite, model.ToolMultiEdit, model.ToolNotebookEdit:
				if it.ToolUseID != "" && len(pending) < maxPending {
					pending[it.ToolUseID] = true
				}
			}
		case ItemToolResult:
			if pending[it.ToolUseID] {
				delete(pending, it.ToolUseID)
				if !it.IsError && !it.TS.IsZero() {
					r.noteWrite(it.SessionID, it.TS)
				}
			}
		}
	}
}

func (r *runner) processSubagents() error {
	dir, names, err := r.subagentFiles()
	if err != nil {
		return err
	}
	r.inSubagentFile = true
	defer func() { r.inSubagentFile = false }()
	for _, name := range names {
		agent := strings.TrimPrefix(strings.TrimSuffix(name, ".jsonl"), "agent-")
		f, err := r.deps.FS.OpenFile(filepath.Join(dir, name), os.O_RDONLY, 0)
		if err != nil {
			return fmt.Errorf("replay: opening subagent transcript: %w", err)
		}
		err = r.processStream(f, agent)
		if cerr := f.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) processStream(src io.Reader, fileAgent string) error {
	tr, err := OpenTranscript(r.client, src)
	if err != nil {
		return err
	}
	for {
		it, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("replay: reading transcript: %w", err)
		}
		if fileAgent != "" && (it.AgentID == "" || it.AgentID == "sidechain") {
			it.AgentID = fileAgent
		}
		if !r.opt.Subagents && it.AgentID != "" {
			continue
		}
		if err := r.ensureRules(it.Cwd); err != nil {
			return err
		}
		r.items++
		r.handle(it)
	}
	r.skipped += tr.Skipped()
	return nil
}

// --------------------------------------------------------------- items ---

func (r *runner) scopeFor(it *Item) *scope {
	agent := it.AgentID
	if agent == "" {
		agent = "main"
	}
	key := scopeKey{it.SessionID, agent}
	sc := r.scopes[key]
	if sc == nil {
		sc = &scope{st: newMemState(r.rulesHash), pending: map[string]pendingCall{}, exists: map[string]bool{}, agent: agent, session: it.SessionID}
		r.scopes[key] = sc
		if it.Cwd != "" {
			sc.cwd = it.Cwd
		} else {
			sc.cwd = r.cwdBySession[it.SessionID]
		}
		if agent == "main" && r.initial != nil {
			r.seed(sc)
		}
	}
	if it.Cwd != "" {
		sc.cwd = it.Cwd
		r.cwdBySession[it.SessionID] = it.Cwd
	}
	if it.GitBranch != "" {
		sc.branch = it.GitBranch
	}
	if !it.TS.IsZero() {
		sc.lastTS = it.TS
	}
	return sc
}

func (r *runner) seed(sc *scope) {
	is := r.initial
	for _, p := range is.read {
		n := r.norm(sc, p)
		sc.st.addRead(n, time.Time{})
		sc.exists[n] = true
	}
	for id, n := range is.priorHits {
		sc.st.priorHits[id] = n
	}
	if is.prevPrompt != "" {
		sc.st.promptTail = tailRunes(is.prevPrompt, promptTailRune)
	}
	if is.toolCountSinceInject != nil {
		sc.st.sinceInject = *is.toolCountSinceInject
	}
}

func (r *runner) handle(it *Item) {
	sc := r.scopeFor(it)
	ts := it.TS
	if ts.IsZero() {
		ts = sc.lastTS
	}
	switch it.Kind {
	case ItemUserPrompt:
		if sc.agent != "main" {
			return // a sub-agent's prompt is the Task input; the client fires no UserPromptSubmit for it
		}
		ev := r.buildEvent(sc, model.EventUserPromptSubmit, it, payloadIn{prompt: it.Text})
		hits := r.runRules(sc, ev, it, ts)
		sc.st.touch(ts, model.EventUserPromptSubmit, "")
		sc.st.onPrompt(it.Text, r.anchorEvery)
		sc.st.onHits(hits)
	case ItemToolUse:
		if len(sc.pending) >= maxPending && len(sc.order) > 0 {
			delete(sc.pending, sc.order[0])
			sc.order = sc.order[1:]
		}
		if it.ToolUseID != "" {
			sc.pending[it.ToolUseID] = pendingCall{name: it.ToolName, input: it.ToolInput}
			sc.order = append(sc.order, it.ToolUseID)
		}
		ev := r.buildEvent(sc, model.EventPreToolUse, it, payloadIn{toolName: it.ToolName, toolInput: it.ToolInput, toolUseID: it.ToolUseID})
		hits := r.runRules(sc, ev, it, ts)
		sc.st.touch(ts, model.EventPreToolUse, it.ToolName)
		sc.st.onToolCall()
		sc.st.onHits(hits)
	case ItemToolResult:
		r.handleResult(sc, it, ts)
	case ItemAssistantText:
		sc.st.lastText = it.Text
	case ItemTurnEnd:
		name := model.EventStop
		if sc.agent != "main" {
			name = model.EventSubagentStop
		}
		text := it.Text
		if text == "" {
			text = sc.st.lastText
		}
		ev := r.buildEvent(sc, name, it, payloadIn{lastText: &text})
		hits := r.runRules(sc, ev, it, ts)
		sc.st.touch(ts, name, "")
		sc.st.onHits(hits)
		sc.st.lastText = ""
	case ItemCompact:
		sc.st.sinceInject = 0
	}
}

// addPending records a call awaiting its result, evicting the oldest still
// unresolved call once maxPending are outstanding.
func (sc *scope) addPending(id string, pc pendingCall) {
	if _, dup := sc.pending[id]; !dup {
		for len(sc.pending) >= maxPending && len(sc.order) > 0 {
			old := sc.order[0]
			sc.order = sc.order[1:]
			delete(sc.pending, old) // no-op for ids already resolved
		}
		// Resolved ids stay in order until they reach the front; compact
		// when they dominate so the slice stays proportional to maxPending.
		if len(sc.order) >= 4*maxPending {
			live := sc.order[:0]
			for _, o := range sc.order {
				if _, ok := sc.pending[o]; ok {
					live = append(live, o)
				}
			}
			sc.order = live
		}
		sc.order = append(sc.order, id)
	}
	sc.pending[id] = pc
}

func (r *runner) handleResult(sc *scope, it *Item, ts time.Time) {
	pc, ok := sc.pending[it.ToolUseID]
	if !ok {
		return
	}
	delete(sc.pending, it.ToolUseID)
	name := model.EventPostToolUse
	pin := payloadIn{toolName: pc.name, toolInput: pc.input, toolUseID: it.ToolUseID}
	failed := it.IsError && r.hasEvent(model.EventPostToolUseFailure)
	if failed {
		name = model.EventPostToolUseFailure
		pin.errText = it.Text
	} else {
		resp, _ := json.Marshal(it.Text)
		pin.toolResponse = resp
	}
	ev := r.buildEvent(sc, name, it, pin)
	hits := r.runRules(sc, ev, it, ts)
	sc.st.touch(ts, name, pc.name)
	sc.st.onHits(hits)
	if failed {
		sc.st.failureAt = ts
		return
	}
	if ev != nil && ev.Tool != nil {
		r.registerReads(sc, ev, ts)
	}
	sc.st.onMidTurn(r.batchAfter)
}

// noteWrite records a file-tool write time, keeping the list sorted (sub-agent
// streams are replayed after the main one).
func (r *runner) noteWrite(session string, ts time.Time) {
	w := r.writeBySession[session]
	i := sort.Search(len(w), func(i int) bool { return w[i].After(ts) })
	w = append(w, time.Time{})
	copy(w[i+1:], w[i:])
	w[i] = ts
	r.writeBySession[session] = w
}

// lastWriteAt is the latest write at or before ts (the latest overall when ts
// is unknown); zero when none.
func (r *runner) lastWriteAt(session string, ts time.Time) time.Time {
	w := r.writeBySession[session]
	if ts.IsZero() {
		if len(w) == 0 {
			return time.Time{}
		}
		return w[len(w)-1]
	}
	i := sort.Search(len(w), func(i int) bool { return w[i].After(ts) })
	if i == 0 {
		return time.Time{}
	}
	return w[i-1]
}

// projectEntry caches one repo's project.json.
type projectEntry struct {
	required []string
	cause    string // non-empty when the required list is unavailable
}

// requiredNames resolves the required verify names of the repo at ctx's
// project root from .ballast/project.json (verify_fresh without names).
func (r *runner) requiredNames(ctx model.PathContext) func() ([]string, string) {
	return func() ([]string, string) {
		root := ctx.ProjectRoot
		e, ok := r.projects[root]
		if !ok {
			e = r.loadProject(root)
			r.projects[root] = e
		}
		return e.required, e.cause
	}
}

func (r *runner) loadProject(root string) projectEntry {
	if root == "" {
		return projectEntry{cause: causeNoProject}
	}
	if r.deps.Project != nil {
		pc, err := r.deps.Project.LoadProject(filepath.FromSlash(root))
		switch {
		case err != nil:
			return projectEntry{cause: causeProjectInvalid}
		case pc == nil:
			return projectEntry{cause: causeNoProject}
		}
		if req := pc.Required(); len(req) > 0 {
			return projectEntry{required: req}
		}
		return projectEntry{cause: causeNoRequired}
	}
	data, err := r.deps.FS.ReadFile(layout.ProjectJSON(filepath.FromSlash(root)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return projectEntry{cause: causeNoProject}
		}
		return projectEntry{cause: causeProjectInvalid}
	}
	var pc model.ProjectConfig
	if err := json.Unmarshal(data, &pc); err != nil {
		return projectEntry{cause: causeProjectInvalid}
	}
	req := pc.Required()
	if len(req) == 0 {
		return projectEntry{cause: causeNoRequired}
	}
	return projectEntry{required: req}
}

// registerReads applies the PostToolUse read-set rule of internal/hook/doc.go.
func (r *runner) registerReads(sc *scope, ev *model.Event, ts time.Time) {
	tool := ev.Tool
	switch tool.Name {
	case model.ToolRead, model.ToolEdit, model.ToolWrite, model.ToolMultiEdit, model.ToolNotebookEdit:
		for _, p := range tool.Targets {
			n := r.norm(sc, p)
			sc.st.addRead(n, ts)
			sc.exists[n] = true
		}
		for _, p := range tool.TargetDeletes {
			sc.exists[r.norm(sc, p)] = false
		}
		if tool.Name != model.ToolRead && !(r.subWritesNoted && r.inSubagentFile) {
			r.noteWrite(sc.session, ts)
		}
	}
	if tool.Shell != model.ShellNone {
		cl, err := r.parseShell(tool)
		if err != nil || cl == nil {
			return
		}
		for _, p := range shellReadPaths(cl) {
			n := r.norm(sc, p)
			sc.st.addRead(n, ts)
			sc.exists[n] = true
		}
	}
}

func (r *runner) parseShell(tool *model.ToolCall) (*model.CommandList, error) {
	switch tool.Shell {
	case model.ShellBash:
		if r.deps.Bash == nil {
			return nil, errors.New("replay: no bash parser")
		}
		return r.deps.Bash.ParseBash(tool.Command)
	case model.ShellPowerShell:
		if r.deps.PS == nil {
			return nil, errors.New("replay: no PowerShell parser")
		}
		return r.deps.PS.ParsePowerShell(tool.Command)
	}
	return nil, nil
}

func (r *runner) hasEvent(name model.EventName) bool {
	for _, ne := range model.NativeEventTable[r.client] {
		if ne.Event == name {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------- evaluation ---

type payloadIn struct {
	prompt       string
	toolName     string
	toolInput    []byte
	toolUseID    string
	toolResponse json.RawMessage
	errText      string
	lastText     *string
}

type hookPayload struct {
	SessionID            string          `json:"session_id,omitempty"`
	ConversationID       string          `json:"conversation_id,omitempty"`
	Cwd                  string          `json:"cwd,omitempty"`
	HookEventName        string          `json:"hook_event_name"`
	AgentID              string          `json:"agent_id,omitempty"`
	SubagentID           string          `json:"subagent_id,omitempty"`
	Prompt               string          `json:"prompt,omitempty"`
	ToolName             string          `json:"tool_name,omitempty"`
	ToolInput            json.RawMessage `json:"tool_input,omitempty"`
	ToolUseID            string          `json:"tool_use_id,omitempty"`
	ToolResponse         json.RawMessage `json:"tool_response,omitempty"`
	Error                string          `json:"error,omitempty"`
	LastAssistantMessage *string         `json:"last_assistant_message,omitempty"`
}

// buildEvent synthesizes the client's hook payload for a transcript item and
// parses it with the client's adapter (the same normalization as the live
// hook). It returns nil when the event does not exist on the client or the
// adapter rejects the payload.
func (r *runner) buildEvent(sc *scope, name model.EventName, it *Item, in payloadIn) *model.Event {
	native := ""
	for _, ne := range model.NativeEventTable[r.client] {
		if ne.Event == name {
			native = ne.Native
			break
		}
	}
	if native == "" {
		return nil
	}
	if !r.eventRules[name] && name != model.EventPostToolUse {
		return nil // nothing to evaluate and no state to derive from it
	}
	p := hookPayload{
		SessionID: it.SessionID, Cwd: sc.cwd, HookEventName: native,
		Prompt: in.prompt, ToolName: in.toolName, ToolUseID: in.toolUseID,
		ToolResponse: in.toolResponse, Error: in.errText, LastAssistantMessage: in.lastText,
	}
	if len(in.toolInput) > 0 && json.Valid(in.toolInput) {
		p.ToolInput = in.toolInput
	}
	if sc.agent != "main" {
		p.AgentID = sc.agent
	}
	if r.client == model.ClientCursor {
		p.ConversationID = it.SessionID
		p.SubagentID = p.AgentID
	}
	data, err := json.Marshal(&p)
	if err != nil {
		return nil
	}
	ev, err := r.adapter.ParseEvent(native, data)
	if err != nil || ev == nil {
		return nil
	}
	ev.ReceivedAt = it.TS
	return ev
}

// runRules evaluates the rules of ev.Name and records the judgements. It
// returns the ids of the rules that hit (feeds prior_hit).
func (r *runner) runRules(sc *scope, ev *model.Event, it *Item, ts time.Time) []string {
	if ev == nil || !r.eventRules[ev.Name] {
		return nil
	}
	ctx := r.pathContext(sc)
	in := &model.EvalInput{
		Event: ev, Client: r.client, Paths: ctx, Session: sc.st, SessionReadable: true,
		Now: ts,
	}
	if ev.Tool != nil && ev.Tool.Shell != model.ShellNone {
		tool := ev.Tool
		in.Commands = model.NewLazyCommands(tool.Command, func(string) (*model.CommandList, error) {
			cl, err := r.parseShell(tool)
			if err == nil && cl == nil {
				err = errors.New("replay: shell parser returned no result")
			}
			return cl, err
		})
	}
	recs, hasEvidence := r.evidenceFor(sc.session)
	if hasEvidence {
		in.Evidence = newEvidenceView(recs, ts, r.lastWriteAt(sc.session, ts)).withRequired(r.requiredNames(ctx))
	}

	world := &replayWorld{r: r, sc: sc}
	in.World = world
	verdict, err := r.deps.Engine.Evaluate(r.rs, in)
	var results []model.RuleResult
	var hits []model.RuleHit
	if err != nil || verdict == nil {
		for _, id := range r.order {
			if m := r.metas[id]; m.event == ev.Name {
				results = append(results, model.RuleResult{RuleID: id, Outcome: model.TriUnknown, UnknownCause: causeEvalError})
			}
		}
	} else {
		results, hits = verdict.Results, verdict.Hits
	}

	var misses map[string]bool
	probed := false
	var hitIDs []string
	for _, h := range hits {
		hitIDs = append(hitIDs, h.RuleID)
	}
	for _, res := range results {
		m := r.metas[res.RuleID]
		sum := r.summaries[res.RuleID]
		if m == nil || sum == nil {
			continue
		}
		facts := evalFacts{hasEvidence: hasEvidence, diskUsed: world.disk}
		if res.Outcome == model.TriFalse {
			if !m.hasWhen {
				facts.matcherMiss = true // match is the whole rule
			} else {
				if !probed {
					probed, misses = true, r.matcherMisses(in, sc)
				}
				facts.matcherMiss = misses[res.RuleID]
			}
		}
		class, cause := classify(m, res, facts, r.client)
		sum.Counts[class]++
		switch {
		case res.Outcome == model.TriTrue && class != Unknown:
			s := Sample{
				ID: SampleID(r.transcriptRef, it.UUID, res.RuleID, r.rulesHash), RuleID: res.RuleID,
				Class: class, Event: ev.Name, TS: it.TS, UUID: it.UUID,
			}
			for _, h := range hits {
				if h.RuleID == res.RuleID {
					s.Matched = h.Matched
					break
				}
			}
			r.samples = append(r.samples, s)
		case class == Unknown && r.unknownKept[res.RuleID] < maxUnknownSamples:
			r.unknownKept[res.RuleID]++
			r.samples = append(r.samples, Sample{
				ID: SampleID(r.transcriptRef, it.UUID, res.RuleID, r.rulesHash), RuleID: res.RuleID,
				Class: Unknown, Event: ev.Name, TS: it.TS, UUID: it.UUID, UnknownCause: cause,
			})
		}
	}
	return hitIDs
}

// evidenceFor returns the trusted evidence records for a session and
// whether any exist.
func (r *runner) evidenceFor(session string) ([]model.VerifyRecord, bool) {
	if !r.needsEvidence {
		return nil, false
	}
	if r.opt.EvidenceMode == "file" {
		return r.evidenceFile, len(r.evidenceFile) > 0
	}
	recs, ok := r.evidence[session]
	if !ok {
		recs = loadEvidenceDir(r.deps.FS, r.deps.EvidenceDir, session)
		r.evidence[session] = recs
	}
	return recs, len(recs) > 0
}

// pathContext builds (and caches) the PathContext of a scope's cwd.
func (r *runner) pathContext(sc *scope) model.PathContext {
	if ctx, ok := r.pathCtx[sc.cwd]; ok {
		return ctx
	}
	goos := runtime.GOOS
	if looksWindows(sc.cwd) {
		goos = "windows"
	}
	ctx := model.PathContext{Home: r.homeNorm, GOOS: goos}
	if sc.cwd != "" {
		ctx.Cwd = r.deps.Paths.Normalize(sc.cwd, "", ctx)
		ctx.ProjectRoot = ctx.Cwd
		dir := filepath.Clean(sc.cwd)
		for i := 0; i < 64; i++ {
			if _, err := r.deps.FS.Stat(filepath.Join(dir, ".git")); err == nil {
				ctx.ProjectRoot = r.deps.Paths.Normalize(dir, "", ctx)
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	r.pathCtx[sc.cwd] = ctx
	return ctx
}

func looksWindows(cwd string) bool {
	if strings.HasPrefix(cwd, `\\`) {
		return true
	}
	return len(cwd) >= 3 && cwd[1] == ':' && (cwd[2] == '\\' || cwd[2] == '/') &&
		(cwd[0] >= 'a' && cwd[0] <= 'z' || cwd[0] >= 'A' && cwd[0] <= 'Z')
}

func (r *runner) norm(sc *scope, p string) string {
	return r.deps.Paths.Normalize(p, sc.cwd, r.pathContext(sc))
}

// replayWorld answers path_exists from the transcript first, then from the
// current disk (approximate); env and branch come from the transcript.
type replayWorld struct {
	disk bool // some path_exists answer came from the current disk (approximate)
	r    *runner
	sc   *scope
}

func (w *replayWorld) PathExists(norm string) model.Tri {
	if v, ok := w.sc.exists[norm]; ok {
		return model.TriOf(v)
	}
	w.disk = true
	_, err := w.r.deps.FS.Stat(filepath.FromSlash(norm))
	switch {
	case err == nil:
		return model.TriTrue
	case errors.Is(err, fs.ErrNotExist):
		return model.TriFalse
	}
	return model.TriUnknown
}

func (w *replayWorld) Getenv(string) (string, bool) { return "", false }

func (w *replayWorld) Branch() (string, model.Tri) {
	if w.sc.branch == "" {
		return "", model.TriUnknown
	}
	return w.sc.branch, model.TriTrue
}
