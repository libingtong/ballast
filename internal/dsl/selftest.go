package dsl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// TestCase is the result of one example.
type TestCase struct {
	File    string           `json:"file"`
	RuleID  string           `json:"rule_id"`
	Example string           `json:"example"` // name or "#<index>"
	Client  model.ClientID   `json:"client"`
	Expect  model.ExpectKind `json:"expect"`
	Got     model.Tri        `json:"got"`
	Pass    bool             `json:"pass"`
	Detail  string           `json:"detail,omitempty"` // UnknownCause or error
	// Skipped is set when the example could not run because no adapter is
	// registered for its client; skipped cases are neither passed nor failed.
	Skipped bool `json:"skipped,omitempty"`
}

// TestReport aggregates test-rules results.
type TestReport struct {
	Cases   []TestCase `json:"cases"`
	Passed  int        `json:"passed"`
	Failed  int        `json:"failed"`
	Skipped int        `json:"skipped,omitempty"`
}

// caseResult is a TestCase plus the location Lint needs.
type caseResult struct {
	TestCase
	ruleIdx, exIdx int
	badInput       bool
}

func (c *caseResult) failureMessage() string {
	if c.badInput {
		return fmt.Sprintf("example %s: %s", c.Example, c.Detail)
	}
	msg := fmt.Sprintf("example %s on %s: expected %s but the rule evaluated to %s", c.Example, c.Client, c.Expect, c.Got)
	if c.Detail != "" {
		msg += " (" + c.Detail + ")"
	}
	return msg
}

// TestRules runs every example of every rule in the given files. Each
// example is evaluated in isolation: its rule alone (as a one-rule
// CompiledRuleSet, action forced to deny so capability degradation cannot
// mask a hit), the example input parsed by the real adapter of
// Example.Client, and FixtureEnv for predicates. Files are decoded and
// validated first; invalid files produce failed cases, not an error.
func (e *Engine) TestRules(inputs []LintInput) (*TestReport, error) {
	rep := &TestReport{Cases: []TestCase{}}
	for _, in := range inputs {
		rf, ds, err := e.parse(in.Path, in.Data)
		if err != nil || HasErrors(ds) {
			for _, d := range ds {
				if d.Severity != model.SevError {
					continue
				}
				rep.Cases = append(rep.Cases, TestCase{
					File: in.Path, RuleID: d.RuleID, Client: model.ClientClaude, Expect: model.ExpectHit,
					Got: model.TriUnknown, Detail: d.Code + ": " + d.Message + pointerSuffix(d.Pointer),
				})
				rep.Failed++
			}
			continue
		}
		for _, c := range e.runFile(in.Path, rf) {
			rep.Cases = append(rep.Cases, c.TestCase)
			switch {
			case c.Skipped:
				rep.Skipped++
			case c.Pass:
				rep.Passed++
			default:
				rep.Failed++
			}
		}
	}
	return rep, nil
}

func pointerSuffix(p string) string {
	if p == "" {
		return ""
	}
	return " at " + p
}

func (e *Engine) runFile(file string, rf *model.RuleFile) []caseResult {
	var out []caseResult
	for ri := range rf.Rules {
		r := &rf.Rules[ri]
		for ei := range r.Examples {
			c := e.runExample(file, r, ei)
			c.ruleIdx, c.exIdx = ri, ei
			out = append(out, c)
		}
	}
	return out
}

// fixtureNow is the fake clock of every example.
var fixtureNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func expectTri(k model.ExpectKind) model.Tri {
	switch k {
	case model.ExpectHit:
		return model.TriTrue
	case model.ExpectMiss:
		return model.TriFalse
	}
	return model.TriUnknown
}

func (e *Engine) runExample(file string, r *model.Rule, ei int) (cr caseResult) {
	ex := &r.Examples[ei]
	client := ex.Client
	if client == "" {
		client = model.ClientClaude
	}
	name := ex.Name
	if name == "" {
		name = fmt.Sprintf("#%d", ei)
	}
	cr.TestCase = TestCase{File: file, RuleID: r.ID, Example: name, Client: client, Expect: ex.Expect}
	fail := func(format string, args ...any) caseResult {
		cr.Pass, cr.Detail = false, fmt.Sprintf(format, args...)
		return cr
	}
	defer func() {
		if p := recover(); p != nil {
			cr.Pass, cr.Detail = false, fmt.Sprintf("internal error: %v", p)
		}
	}()

	if e.opt.Adapters == nil {
		cr.Skipped, cr.Detail = true, "no adapter registry configured"
		return cr
	}
	ad, ok := e.opt.Adapters.Get(client)
	if !ok {
		cr.Skipped, cr.Detail = true, "no adapter available for client "+string(client)
		return cr
	}
	native := ex.Native
	if native == "" {
		for _, ne := range model.NativeEventTable[client] {
			if ne.Event == r.Event {
				native = ne.Native
				break
			}
		}
		if native == "" {
			cr.badInput = true
			return fail("event %s does not exist on client %s", r.Event, client)
		}
	}

	fx := ex.Fixture
	if fx == nil {
		fx = &model.Fixture{}
	}
	pc := e.fixtureContext(fx)

	stdin, err := fillPayload(ex.Input, pc.Cwd, native)
	if err != nil {
		cr.badInput = true
		return fail("input is not a JSON object: %v", err)
	}
	ev, err := ad.ParseEvent(native, stdin)
	if err != nil {
		cr.badInput = true
		return fail("adapter %s cannot parse the input: %v", client, err)
	}
	ev.ReceivedAt = fixtureNow

	sess, evid, world := FixtureEnv(fx, pc)
	in := &model.EvalInput{
		Event: ev, Client: client, Paths: pc,
		Session: sess, SessionReadable: true, Evidence: evid, World: world,
		Now: fixtureNow, Deadline: fixtureNow.Add(time.Second),
	}
	if t := ev.Tool; t != nil && t.Command != "" {
		in.Commands = model.NewLazyCommands(t.Command, e.shellParser(t.Shell))
	}

	one := *r
	one.Enabled = nil
	one.Action = model.ActionDeny
	rs, err := e.Compile([]model.LoadedRule{{Rule: one, Layer: model.LayerFile, Source: file}}, model.Settings{}, "selftest")
	if err != nil {
		return fail("compile: %v", err)
	}
	v, err := e.Evaluate(rs, in)
	if err != nil {
		return fail("evaluate: %v", err)
	}
	if len(v.Results) == 0 {
		cr.Got = model.TriFalse
		cr.Detail = "rule does not apply to this event or client"
	} else {
		cr.Got = v.Results[0].Outcome
		cr.Detail = v.Results[0].UnknownCause
	}
	cr.Pass = cr.Got == expectTri(ex.Expect)
	return cr
}

func (e *Engine) shellParser(k model.ShellKind) func(string) (*model.CommandList, error) {
	return func(src string) (*model.CommandList, error) {
		switch {
		case k == model.ShellPowerShell && e.opt.PS != nil:
			return e.opt.PS.ParsePowerShell(src)
		case k != model.ShellPowerShell && e.opt.Bash != nil:
			return e.opt.Bash.ParseBash(src)
		}
		return nil, errors.New("no shell parser configured")
	}
}

// fixtureContext derives the PathContext of an example fixture.
func (e *Engine) fixtureContext(fx *model.Fixture) model.PathContext {
	cwd, home, goos := fx.Cwd, fx.Home, fx.GOOS
	if cwd == "" {
		cwd = "/repo"
	}
	if home == "" {
		home = "/home/user"
	}
	if goos == "" {
		goos = "linux"
	}
	pc := model.PathContext{Cwd: cwd, ProjectRoot: cwd, Home: home, GOOS: goos}
	if e.opt.Paths != nil {
		pc.Home = e.opt.Paths.Normalize(home, "", pc)
		pc.Cwd = e.opt.Paths.Normalize(cwd, "", pc)
		pc.ProjectRoot = pc.Cwd
	}
	return pc
}

// fillPayload adds the common hook fields (session_id, cwd,
// hook_event_name) an example input may omit.
func fillPayload(input json.RawMessage, cwd, native string) ([]byte, error) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(input))
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("null")
	}
	set := func(k string, v any) {
		if _, ok := m[k]; !ok {
			b, _ := json.Marshal(v)
			m[k] = b
		}
	}
	set("session_id", "ballast-selftest")
	set("cwd", cwd)
	set("hook_event_name", native)
	return json.Marshal(m)
}

// FixtureEnv builds fake SessionState / EvidenceView / WorldView from a
// fixture (exported so other units' tests can reuse it). Paths in the
// fixture are resolved against fx.Cwd (default "/repo") and fx.Home (default
// "/home/user"); lookups compare the same simple normalization, case-folded
// for goos windows and darwin. The EvidenceView is nil (every evidence
// predicate unknown) when fx.NotARepo is set.
func FixtureEnv(fx *model.Fixture, ctx model.PathContext) (model.SessionState, model.EvidenceView, model.WorldView) {
	if fx == nil {
		fx = &model.Fixture{}
	}
	n := fixtureNorm{cwd: firstNonEmpty(fx.Cwd, ctx.Cwd, "/repo"), home: firstNonEmpty(fx.Home, ctx.Home, "/home/user")}
	goos := firstNonEmpty(fx.GOOS, ctx.GOOS)
	n.fold = goos == "windows" || goos == "darwin"

	failed := n.set(fx.FailedSinceRead)
	sess := &fixSession{
		read:     map[string]time.Time{},
		prior:    fx.PriorHits,
		prompt:   fx.PrevPrompt,
		sinceInj: 0,
		n:        n,
	}
	if fx.ToolCountSinceInject != nil {
		sess.sinceInj = *fx.ToolCountSinceInject
	}
	for _, p := range fx.Read {
		k := n.norm(p)
		if failed[k] {
			sess.read[k] = fixtureNow.Add(time.Second)
		} else {
			sess.read[k] = fixtureNow.Add(3 * time.Second)
		}
	}
	var evid model.EvidenceView
	if !fx.NotARepo {
		fresh := model.TriUnknown
		if t, ok := model.ParseTri(fx.VerifyFresh); ok {
			fresh = t
		}
		evid = &fixEvidence{fresh: fresh, failed: len(fx.FailedSinceRead) > 0}
	}
	w := &fixWorld{exists: n.set(fx.Exists), env: fx.Env, n: n}
	if fx.Branch != nil {
		w.branch, w.branchSet = *fx.Branch, true
	}
	return sess, evid, w
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// fixtureNorm is a minimal path normalizer for fixtures.
type fixtureNorm struct {
	cwd, home string
	fold      bool
}

func (n fixtureNorm) norm(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	switch {
	case p == "~":
		p = n.home
	case strings.HasPrefix(p, "~/"):
		p = n.home + p[1:]
	}
	if !path.IsAbs(p) && !(len(p) > 1 && p[1] == ':') {
		p = path.Join(n.cwd, p)
	}
	p = path.Clean(p)
	if n.fold {
		p = strings.ToLower(p)
	}
	return p
}

func (n fixtureNorm) set(paths []string) map[string]bool {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[n.norm(p)] = true
	}
	return m
}

type fixSession struct {
	read     map[string]time.Time
	prior    map[string]int
	prompt   string
	sinceInj int
	n        fixtureNorm
}

func (s *fixSession) Seen(string) bool { return false }
func (s *fixSession) HasRead(norm string) bool {
	_, ok := s.read[s.n.norm(norm)]
	return ok
}
func (s *fixSession) LastReadAt(norm string) (time.Time, bool) {
	t, ok := s.read[s.n.norm(norm)]
	return t, ok
}
func (s *fixSession) StartedAt() (time.Time, bool)        { return fixtureNow, true }
func (s *fixSession) ToolCount() int                      { return s.sinceInj }
func (s *fixSession) ToolCountSinceInject() int           { return s.sinceInj }
func (s *fixSession) PromptCount() int                    { return 1 }
func (s *fixSession) PrevPromptTail() string              { return lastRunes(s.prompt, 200) }
func (s *fixSession) PriorHits(ruleID string) int         { return s.prior[ruleID] }
func (s *fixSession) Generation() string                  { return "" }
func (s *fixSession) RuleDigest() map[string]model.Action { return nil }
func (s *fixSession) FullInjected() bool                  { return false }
func (s *fixSession) ReportedJob(string) bool             { return false }
func (s *fixSession) LastJobReportAt() time.Time          { return time.Time{} }
func (s *fixSession) LastAssistantText() string           { return "" }
func (s *fixSession) LastToolFailureAt() (time.Time, bool) {
	return time.Time{}, false
}
func (s *fixSession) Recent(int) []model.EventSummary { return nil }
func (s *fixSession) Evals() map[string]model.EvalCount {
	return nil
}
func (s *fixSession) EventSeq() int64 { return 0 }

type fixEvidence struct {
	fresh  model.Tri
	failed bool
}

func (f *fixEvidence) VerifyFresh(model.FreshQuery) model.FreshResult {
	switch f.fresh {
	case model.TriTrue:
		return model.FreshResult{Value: model.TriTrue, Cause: "fresh"}
	case model.TriFalse:
		return model.FreshResult{Value: model.TriFalse, Cause: "stale"}
	}
	return model.FreshResult{Value: model.TriUnknown, Cause: "fixture_unknown"}
}

func (f *fixEvidence) LastFailureAfter(after time.Time) (model.Tri, time.Time) {
	if !f.failed {
		return model.TriFalse, time.Time{}
	}
	at := fixtureNow.Add(2 * time.Second)
	return model.TriOf(after.Before(at)), at
}

func (f *fixEvidence) FinishedSince(time.Time) []model.VerifyRecord { return nil }

type fixWorld struct {
	exists    map[string]bool
	env       map[string]string
	branch    string
	branchSet bool
	n         fixtureNorm
}

func (w *fixWorld) PathExists(norm string) model.Tri {
	return model.TriOf(w.exists[w.n.norm(norm)])
}

func (w *fixWorld) Getenv(name string) (string, bool) {
	v, ok := w.env[name]
	return v, ok
}

func (w *fixWorld) Branch() (string, model.Tri) {
	switch {
	case !w.branchSet:
		return "", model.TriUnknown
	case w.branch == "":
		return "", model.TriFalse
	}
	return w.branch, model.TriTrue
}
