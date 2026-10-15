package inject_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// fakeState is a SessionState with only the fields the injector reads.
type fakeState struct {
	promptCount  int
	toolSince    int
	generation   string
	digest       map[string]model.Action
	fullInjected bool
	reportedJobs map[string]bool
}

func (s *fakeState) Seen(string) bool                          { return false }
func (s *fakeState) HasRead(string) bool                       { return false }
func (s *fakeState) LastReadAt(string) (t time.Time, ok bool)  { return }
func (s *fakeState) StartedAt() (t time.Time, ok bool)         { return }
func (s *fakeState) ToolCount() int                            { return s.toolSince }
func (s *fakeState) ToolCountSinceInject() int                 { return s.toolSince }
func (s *fakeState) PromptCount() int                          { return s.promptCount }
func (s *fakeState) PrevPromptTail() string                    { return "" }
func (s *fakeState) PriorHits(string) int                      { return 0 }
func (s *fakeState) Generation() string                        { return s.generation }
func (s *fakeState) RuleDigest() map[string]model.Action       { return s.digest }
func (s *fakeState) FullInjected() bool                        { return s.fullInjected }
func (s *fakeState) ReportedJob(id string) bool                { return s.reportedJobs[id] }
func (s *fakeState) LastJobReportAt() (t time.Time)            { return }
func (s *fakeState) LastAssistantText() string                 { return "" }
func (s *fakeState) LastToolFailureAt() (t time.Time, ok bool) { return }
func (s *fakeState) Recent(int) []model.EventSummary           { return nil }
func (s *fakeState) Evals() map[string]model.EvalCount         { return nil }
func (s *fakeState) EventSeq() int64                           { return 0 }

// ruleSet builds a compiled rule set from rules (Action/Target defaults applied).
func ruleSet(generation string, rules ...model.Rule) *model.CompiledRuleSet {
	rs := &model.CompiledRuleSet{Generation: generation}
	for i, r := range rules {
		if r.Event == "" {
			r.Event = model.EventPreToolUse
		}
		rs.Rules = append(rs.Rules, model.CompiledRule{
			LoadedRule: model.LoadedRule{Rule: r, Layer: model.LayerUser, Source: "/u/rules.json", Index: i},
			Action:     r.EffectiveAction(),
			Target:     model.DefaultTarget(r.Event),
		})
	}
	return rs
}

func ruleWith(id string, action model.Action, text, anchor string) model.Rule {
	r := model.Rule{ID: id, Action: action}
	if text != "" || anchor != "" {
		r.Inject = &model.Inject{Text: text, Anchor: anchor}
	}
	return r
}

func ev(client model.ClientID, name model.EventName) *model.Event {
	return &model.Event{Client: client, Name: name, Native: string(name)}
}

func rc(s string) int { return len([]rune(s)) }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func kindsOf(inj *model.Injection) []string {
	out := []string{}
	for _, k := range inj.Kinds {
		out = append(out, string(k))
	}
	return out
}

func repeat(s string, n int) string { return strings.Repeat(s, n) }

func fmtID(prefix string, i int) string { return fmt.Sprintf("%s-%02d", prefix, i) }
