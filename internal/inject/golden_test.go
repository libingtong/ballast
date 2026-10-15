package inject_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

var _ ports.Injector = inject.Builder{}

type injectGolden struct {
	Rules []model.Rule `json:"rules"`
	Cases []struct {
		Desc   string          `json:"desc"`
		Client model.ClientID  `json:"client"`
		Event  model.EventName `json:"event"`
		State  struct {
			PromptCount  int                     `json:"prompt_count"`
			ToolSince    int                     `json:"tool_count_since_inject"`
			Generation   string                  `json:"generation"`
			RuleDigest   map[string]model.Action `json:"rule_digest"`
			FullInjected bool                    `json:"full_injected"`
		} `json:"state"`
		IsFirstTurn  bool                 `json:"is_first_turn"`
		NewJobs      []model.VerifyRecord `json:"new_jobs"`
		WantKinds    []string             `json:"want_kinds"`
		WantContains []string             `json:"want_contains"`
		WantEmpty    bool                 `json:"want_empty"`
	} `json:"cases"`
}

func TestInjectGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/inject.json")
	if err != nil {
		t.Fatal(err)
	}
	var g injectGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Cases) == 0 {
		t.Fatal("no golden cases")
	}
	const gen = "0123456789abcdef"
	rs := ruleSet(gen, g.Rules...)
	for _, c := range g.Cases {
		t.Run(c.Desc, func(t *testing.T) {
			st := &fakeState{
				promptCount: c.State.PromptCount, toolSince: c.State.ToolSince,
				generation: c.State.Generation, digest: c.State.RuleDigest, fullInjected: c.State.FullInjected,
			}
			if st.generation == "SAME" {
				st.generation = gen
			}
			e := ev(c.Client, c.Event)
			e.IsFirstTurn = c.IsFirstTurn
			inj, err := inject.Builder{}.Build(&model.InjectRequest{Event: e, Client: c.Client, Rules: rs, Session: st, NewJobs: c.NewJobs})
			if err != nil {
				t.Fatal(err)
			}
			if c.WantEmpty && inj.Text != "" {
				t.Errorf("want empty, got %q", inj.Text)
			}
			if c.WantKinds != nil && !reflect.DeepEqual(kindsOf(inj), c.WantKinds) {
				t.Errorf("kinds = %v, want %v\ntext:\n%s", kindsOf(inj), c.WantKinds, inj.Text)
			}
			for _, sub := range c.WantContains {
				if !strings.Contains(inj.Text, sub) {
					t.Errorf("text lacks %q:\n%s", sub, inj.Text)
				}
			}
			limit := model.BudgetSessionStart
			if c.Event != model.EventSessionStart {
				limit = inject.BudgetNonFull
			}
			if n := rc(inj.Text); n > limit {
				t.Errorf("text is %d runes, limit %d", n, limit)
			}
		})
	}
}
