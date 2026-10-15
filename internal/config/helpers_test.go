package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

var idRe = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

// syncBuffer is a goroutine-safe io.Writer for capturing diagnostics.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// fakeCompiler stands in for dsl: strict decoding plus the validation the
// loader tests need (version, ids, enums, duplicate ids).
type fakeCompiler struct {
	parses   atomic.Int32
	compiles atomic.Int32
}

var _ ports.RuleCompiler = (*fakeCompiler)(nil)

func (c *fakeCompiler) ParseFile(layer model.Layer, path string, data []byte) (*model.RuleFile, []model.Diagnostic, error) {
	c.parses.Add(1)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f model.RuleFile
	if err := dec.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	var diags []model.Diagnostic
	bad := func(code, msg, id string) {
		diags = append(diags, model.Diagnostic{Severity: model.SevError, Code: code, File: path, RuleID: id, Message: msg})
	}
	if f.Version != 1 {
		bad("version-unsupported", "version must be 1", "")
	}
	seen := map[string]bool{}
	for _, r := range f.Rules {
		if !idRe.MatchString(r.ID) {
			bad("bad-id", "bad id "+r.ID, r.ID)
		}
		if seen[r.ID] {
			bad("duplicate-id", "duplicate id "+r.ID, r.ID)
		}
		seen[r.ID] = true
		if !r.Event.IsRuleEvent() {
			bad("bad-event", "bad event "+string(r.Event), r.ID)
		}
		if r.Action != "" && !r.Action.Valid() {
			bad("bad-action", "bad action "+string(r.Action), r.ID)
		}
	}
	return &f, diags, nil
}

func (c *fakeCompiler) Compile(rules []model.LoadedRule, settings model.Settings, generation string) (*model.CompiledRuleSet, error) {
	c.compiles.Add(1)
	rs := &model.CompiledRuleSet{Generation: generation, Settings: settings}
	for _, lr := range rules {
		cr := model.CompiledRule{LoadedRule: lr, Action: lr.Rule.EffectiveAction(), Target: lr.Rule.Target}
		if cr.Target == model.ActionNone {
			cr.Target = model.DefaultTarget(lr.Rule.Event)
		}
		rs.Rules = append(rs.Rules, cr)
	}
	return rs, nil
}

// spec is a compact rule description for fixtures.
type spec struct {
	id      string
	enabled bool
	action  string
}

func on(id string) spec  { return spec{id: id, enabled: true, action: "warn"} }
func off(id string) spec { return spec{id: id, enabled: false, action: "warn"} }

func rulesJSON(settings string, specs ...spec) string {
	var rs []string
	for _, s := range specs {
		rs = append(rs, fmt.Sprintf(`{"id":%q,"event":"PreToolUse","action":%q,"enabled":%t,"message":"msg %s","examples":[]}`, s.id, s.action, s.enabled, s.id))
	}
	if settings != "" {
		settings = `"settings":` + settings + `,`
	}
	return `{"version":1,` + settings + `"rules":[` + strings.Join(rs, ",") + `]}`
}

// env is one isolated loader environment.
type env struct {
	t      testing.TB
	sb     *testenv.Sandbox
	lay    layout.Layout
	repo   string // repo root (no .git needed; the loader takes Repo from the request)
	stderr *syncBuffer
	comp   *fakeCompiler
	opt    config.Options
}

func newEnv(t testing.TB) *env {
	t.Helper()
	sb := testenv.New(t)
	e := &env{t: t, sb: sb, lay: sb.Layout(t), repo: filepath.Join(sb.Root, "work", "proj"), stderr: &syncBuffer{}, comp: &fakeCompiler{}}
	if err := os.MkdirAll(e.repo, 0o700); err != nil {
		t.Fatal(err)
	}
	e.opt = config.Options{
		Layout: e.lay, FS: sys.OSFS{}, Clock: sys.NewFakeClock(time.Unix(1_800_000_000, 0)),
		Compiler: e.comp, Version: "test-1", IDs: &sys.SeqID{}, Stderr: e.stderr,
	}
	return e
}

func (e *env) loader() *config.Loader { return config.NewLoader(e.opt) }

func (e *env) req() model.LoadRequest {
	return model.LoadRequest{Client: model.ClientClaude, Cwd: e.repo, Repo: &model.RepoInfo{Toplevel: e.repo}}
}

func (e *env) path(layer model.Layer) string {
	switch layer {
	case model.LayerOrg:
		return e.lay.OrgRules
	case model.LayerRepo:
		return layout.RepoRules(e.repo)
	}
	return e.lay.UserRules
}

func (e *env) write(layer model.Layer, content string) {
	e.t.Helper()
	p := e.path(layer)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// bump gives the file a distinct mtime so stat detects the rewrite even on
// coarse-timestamp file systems.
func (e *env) bump(layer model.Layer, d time.Duration) {
	e.t.Helper()
	p := e.path(layer)
	fi, err := os.Stat(p)
	if err != nil {
		e.t.Fatal(err)
	}
	mt := fi.ModTime().Add(d)
	if err := os.Chtimes(p, mt, mt); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) load() *model.CompiledRuleSet {
	e.t.Helper()
	rs, err := e.loader().Load(e.req())
	if err != nil {
		e.t.Fatalf("Load: %v", err)
	}
	return rs
}

func ids(rs *model.CompiledRuleSet) []string {
	out := []string{}
	for _, r := range rs.Rules {
		out = append(out, r.Rule.ID)
	}
	return out
}

func noticeKinds(rs *model.CompiledRuleSet) []string {
	out := []string{}
	for _, n := range rs.Notices {
		out = append(out, fmt.Sprintf("%s/%s/%s", n.Kind, n.Layer, n.RuleID))
	}
	return out
}

func source(rs *model.CompiledRuleSet, l model.Layer) model.LayerSource {
	for _, s := range rs.Sources {
		if s.Layer == l {
			return s
		}
	}
	return model.LayerSource{}
}
