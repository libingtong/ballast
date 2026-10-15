package model

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

func TestTriKleene(t *testing.T) {
	T, F, U := TriTrue, TriFalse, TriUnknown
	and := [][3]Tri{{T, T, T}, {T, F, F}, {T, U, U}, {F, U, F}, {U, U, U}, {F, F, F}}
	for _, c := range and {
		if got := c[0].And(c[1]); got != c[2] {
			t.Errorf("%v AND %v = %v, want %v", c[0], c[1], got, c[2])
		}
		if got := c[1].And(c[0]); got != c[2] {
			t.Errorf("%v AND %v = %v, want %v (commutative)", c[1], c[0], got, c[2])
		}
	}
	or := [][3]Tri{{T, U, T}, {F, U, U}, {F, F, F}, {T, F, T}, {U, U, U}}
	for _, c := range or {
		if got := c[0].Or(c[1]); got != c[2] {
			t.Errorf("%v OR %v = %v, want %v", c[0], c[1], got, c[2])
		}
	}
	if T.Not() != F || F.Not() != T || U.Not() != U {
		t.Error("Not")
	}
	if AllOf() != T || AnyOf() != F {
		t.Error("empty folds")
	}
	var zero Tri
	if zero != TriUnknown {
		t.Error("zero Tri must be unknown (fail-open)")
	}
}

func TestCmp(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	c := Cmp{Gte: f(60)}
	if !c.Eval(60) || c.Eval(59.9) {
		t.Error("gte")
	}
	r := Cmp{Gte: f(1), Lt: f(5)}
	if !r.Eval(1) || r.Eval(5) {
		t.Error("range")
	}
	if c.EvalTri(100, false) != TriUnknown {
		t.Error("unknown input")
	}
	if !(Cmp{}).Empty() {
		t.Error("empty")
	}
}

func TestActionSeverity(t *testing.T) {
	if Stricter(ActionWarn, ActionDeny) != ActionDeny || Stricter(ActionAsk, ActionLog) != ActionAsk {
		t.Error("Stricter")
	}
	if WouldLabel(ActionDeny) != "would_deny" || WouldLabel("") != "log" {
		t.Error("WouldLabel")
	}
}

func TestStringListAndInject(t *testing.T) {
	var s struct {
		A StringList `json:"a"`
		B StringList `json:"b"`
		I *Inject    `json:"i"`
		J *Inject    `json:"j"`
	}
	in := `{"a":"x","b":["y","z"],"i":"text","j":{"text":"t","anchor":"a"}}`
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.A) != 1 || s.A[0] != "x" || len(s.B) != 2 || s.I.Text != "text" || s.J.Anchor != "a" {
		t.Errorf("decoded %+v", s)
	}
	if err := json.Unmarshal([]byte(`{"j":{"text":"t","bogus":1}}`), &s); err == nil {
		t.Error("unknown inject field must fail")
	}
}

// TestSchemaExamplesDecode keeps schema/examples/* (and therefore the JSON
// Schema) in lock-step with the Go types.
func TestSchemaExamplesDecode(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "schema", "examples", "*.json"))
	if err != nil || len(files) < 6 {
		t.Fatalf("need ≥6 schema examples, got %d (%v)", len(files), err)
	}
	seen := map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checkRuleFile(t, f, data, seen)
	}
}

// TestContentFilesDecode is U11's unit-level gate (architecture §8.3):
// rules/*.json and rules/optional/*.json (ids unique across both),
// examples/*.json and testdata/bash-regress.json (ids unique per file) must
// strictly decode and satisfy the structural checks. Absent files are
// skipped so the test passes in worktrees without U11 content. Semantic
// lint and example execution are U12 integration acceptance.
func TestContentFilesDecode(t *testing.T) {
	root := filepath.Join("..", "..")
	var shared []string
	for _, pat := range []string{"rules/*.json", "rules/optional/*.json"} {
		m, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pat)))
		shared = append(shared, m...)
	}
	seen := map[string]string{}
	for _, f := range shared {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checkRuleFile(t, f, data, seen)
	}
	perFile, _ := filepath.Glob(filepath.Join(root, "examples", "*.json"))
	if _, err := os.Stat(filepath.Join(root, "testdata", "bash-regress.json")); err == nil {
		perFile = append(perFile, filepath.Join(root, "testdata", "bash-regress.json"))
	}
	for _, f := range perFile {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checkRuleFile(t, f, data, map[string]string{})
	}
	if len(shared)+len(perFile) == 0 {
		t.Skip("no U11 content files yet")
	}
}

// TestDSLDocExamplesDecode decodes every ```json block of docs/dsl-semantics.md.
func TestDSLDocExamplesDecode(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "dsl-semantics.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllSubmatch(doc, -1)
	if len(blocks) < 6 {
		t.Fatalf("dsl-semantics.md must contain ≥6 complete json examples, found %d", len(blocks))
	}
	seen := map[string]string{}
	for i, b := range blocks {
		checkRuleFile(t, "dsl-semantics.md#json"+strconv.Itoa(i+1), b[1], seen)
	}
}

func checkRuleFile(t *testing.T, f string, data []byte, seen map[string]string) {
	t.Helper()
	idRe := regexp.MustCompile(`^[a-z0-9-]{3,40}$`)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rf RuleFile
	if err := dec.Decode(&rf); err != nil {
		t.Errorf("%s: %v", f, err)
		return
	}
	if rf.Version != 1 {
		t.Errorf("%s: version %d", f, rf.Version)
	}
	if len(rf.Rules) == 0 {
		t.Errorf("%s: no rules", f)
	}
	for _, r := range rf.Rules {
		if !idRe.MatchString(r.ID) {
			t.Errorf("%s: bad id %q", f, r.ID)
		}
		if prev, dup := seen[r.ID]; dup {
			t.Errorf("%s: id %q duplicates %s", f, r.ID, prev)
		}
		seen[r.ID] = f
		if !r.Event.IsRuleEvent() {
			t.Errorf("%s/%s: event %q", f, r.ID, r.Event)
		}
		if r.Action != ActionNone && !r.Action.Valid() {
			t.Errorf("%s/%s: action %q", f, r.ID, r.Action)
		}
		if r.Target != ActionNone && !r.Target.Valid() {
			t.Errorf("%s/%s: target %q", f, r.ID, r.Target)
		}
		if r.Match == nil && len(r.When) == 0 {
			t.Errorf("%s/%s: empty rule", f, r.ID)
		}
		var hit, non int
		for _, ex := range r.Examples {
			switch ex.Expect {
			case ExpectHit:
				hit++
			case ExpectMiss, ExpectUnknown:
				non++
			default:
				t.Errorf("%s/%s: expect %q", f, r.ID, ex.Expect)
			}
			if len(ex.Input) == 0 || ex.Input[0] != '{' {
				t.Errorf("%s/%s: example input must be an object", f, r.ID)
			}
			if ex.Client != "" && !ex.Client.Valid() {
				t.Errorf("%s/%s: example client %q", f, r.ID, ex.Client)
			}
			if fx := ex.Fixture; fx != nil && fx.VerifyFresh != "" {
				if _, ok := ParseTri(fx.VerifyFresh); !ok {
					t.Errorf("%s/%s: fixture verify_fresh %q", f, r.ID, fx.VerifyFresh)
				}
			}
		}
		if hit == 0 || non == 0 {
			t.Errorf("%s/%s: needs ≥1 hit and ≥1 non-hit example", f, r.ID)
		}
		for _, c := range r.Clients {
			if !c.Valid() {
				t.Errorf("%s/%s: client %q", f, r.ID, c)
			}
		}
		if runeLen(r.Message) > MaxMessageRunes {
			t.Errorf("%s/%s: message too long", f, r.ID)
		}
		if r.Inject != nil && (runeLen(r.Inject.Text) > MaxInjectTextRunes || runeLen(r.Inject.Anchor) > MaxAnchorRunes) {
			t.Errorf("%s/%s: inject too long", f, r.ID)
		}
	}
}

func runeLen(s string) int { return len([]rune(s)) }

// TestCapabilityTablesConsistent: every rule event a client claims has a
// native event mapped to it, and inject events are reachable.
func TestCapabilityTablesConsistent(t *testing.T) {
	for _, c := range AllClients {
		cs := Capabilities(c)
		if cs.Client != c {
			t.Errorf("%s: Client field %q", c, cs.Client)
		}
		mapped := map[EventName]bool{}
		for _, ne := range NativeEventTable[c] {
			mapped[ne.Event] = true
		}
		for _, e := range cs.RuleEvents {
			if !mapped[e] {
				t.Errorf("%s: rule event %s has no native event", c, e)
			}
		}
		for _, e := range cs.InjectEvents {
			if !mapped[e] {
				t.Errorf("%s: inject event %s has no native event", c, e)
			}
		}
		for _, cell := range cs.Cells {
			if !cs.HasRuleEvent(cell.Event) {
				t.Errorf("%s: cell for unavailable event %s", c, cell.Event)
			}
			if cell.Action == ActionLog || !cell.Action.Valid() {
				t.Errorf("%s: invalid cell action %q", c, cell.Action)
			}
		}
		if cs.MidTurnEvent != "" && !mapped[cs.MidTurnEvent] {
			t.Errorf("%s: mid-turn event %s not mapped", c, cs.MidTurnEvent)
		}
	}
	// Plan B1 matrix spot checks on the reference client.
	cl := Capabilities(ClientClaude)
	if _, ok := cl.Lookup(EventPostToolUse, ActionDeny); ok {
		t.Error("PostToolUse deny must be unsupported")
	}
	if _, ok := cl.Lookup(EventUserPromptSubmit, ActionAsk); ok {
		t.Error("UserPromptSubmit ask must be unsupported")
	}
	if _, ok := Capabilities(ClientCodex).Lookup(EventPreToolUse, ActionAsk); ok {
		t.Error("codex ask must be unsupported")
	}
	if DefaultTarget(EventPostToolUse) != ActionWarn || DefaultTarget(EventPreToolUse) != ActionDeny {
		t.Error("DefaultTarget")
	}
}
