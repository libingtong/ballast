package config_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/model"
)

type mergeGolden struct {
	Cases []struct {
		Desc        string     `json:"desc"`
		Org         [][]any    `json:"org"`
		Repo        [][]any    `json:"repo"`
		User        [][]any    `json:"user"`
		WantActive  []string   `json:"want_active"`
		WantNotices [][]string `json:"want_notices"`
	} `json:"cases"`
	Settings []struct {
		Org  model.Settings `json:"org"`
		Repo model.Settings `json:"repo"`
		User model.Settings `json:"user"`
		Want model.Settings `json:"want"`
	} `json:"settings"`
}

func layerFrom(layer model.Layer, rows [][]any) config.LayerRules {
	f := &model.RuleFile{Version: 1}
	for _, row := range rows {
		en := row[1].(bool)
		f.Rules = append(f.Rules, model.Rule{ID: row[0].(string), Event: model.EventPreToolUse, Enabled: &en})
	}
	return config.LayerRules{Source: model.LayerSource{Layer: layer, Path: "/p/" + string(layer) + ".json", Status: model.LayerOK}, File: f}
}

func activeIDs(res config.MergeResult) []string {
	out := []string{}
	for _, r := range res.Rules {
		out = append(out, r.Rule.ID)
	}
	return out
}

func TestMergeGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/merge.json")
	if err != nil {
		t.Fatal(err)
	}
	var g mergeGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Cases) == 0 || len(g.Settings) == 0 {
		t.Fatal("golden file is empty")
	}
	for _, c := range g.Cases {
		t.Run(c.Desc, func(t *testing.T) {
			res := config.Merge([]config.LayerRules{
				layerFrom(model.LayerOrg, c.Org), layerFrom(model.LayerRepo, c.Repo), layerFrom(model.LayerUser, c.User),
			})
			if got := activeIDs(res); !reflect.DeepEqual(got, c.WantActive) {
				t.Errorf("active = %v, want %v", got, c.WantActive)
			}
			var got [][]string
			for _, n := range res.Notices {
				got = append(got, []string{string(n.Kind), string(n.Layer), n.RuleID})
			}
			if !reflect.DeepEqual(got, c.WantNotices) {
				t.Errorf("notices = %v, want %v", got, c.WantNotices)
			}
			if len(res.Rules) > model.MaxEnabledRules {
				t.Errorf("%d active rules exceed the cap", len(res.Rules))
			}
		})
	}
	for i, c := range g.Settings {
		mk := func(layer model.Layer, s model.Settings) config.LayerRules {
			lr := layerFrom(layer, nil)
			lr.File.Settings = &s
			return lr
		}
		res := config.Merge([]config.LayerRules{mk(model.LayerOrg, c.Org), mk(model.LayerRepo, c.Repo), mk(model.LayerUser, c.User)})
		got, _ := json.Marshal(res.Settings)
		want, _ := json.Marshal(c.Want)
		if string(got) != string(want) {
			t.Errorf("settings case %d: got %s, want %s", i, got, want)
		}
	}
}

func TestMergeFates(t *testing.T) {
	org := layerFrom(model.LayerOrg, [][]any{{"org-on", true}, {"org-off", false}})
	repo := layerFrom(model.LayerRepo, [][]any{{"org-off", true}, {"shared-id", false}, {"repo-id", true}})
	user := layerFrom(model.LayerUser, [][]any{{"shared-id", true}, {"org-on", true}, {"user-id", true}})
	res := config.Merge([]config.LayerRules{org, repo, user})

	type row struct {
		layer model.Layer
		id    string
		fate  config.Fate
		by    model.Layer
	}
	var got []row
	for _, f := range res.All {
		got = append(got, row{f.Rule.Layer, f.Rule.Rule.ID, f.Fate, f.ShadowBy})
	}
	want := []row{
		{model.LayerOrg, "org-on", config.FateActive, ""},
		{model.LayerOrg, "org-off", config.FateDisabled, ""},
		{model.LayerRepo, "org-off", config.FateShadowed, model.LayerOrg},
		{model.LayerRepo, "shared-id", config.FateDisabled, ""},
		{model.LayerRepo, "repo-id", config.FateActive, ""},
		{model.LayerUser, "shared-id", config.FateShadowed, model.LayerRepo},
		{model.LayerUser, "org-on", config.FateShadowed, model.LayerOrg},
		{model.LayerUser, "user-id", config.FateActive, ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fates:\n got %v\nwant %v", got, want)
	}
	if ids := activeIDs(res); !reflect.DeepEqual(ids, []string{"org-on", "repo-id", "user-id"}) {
		t.Errorf("active = %v", ids)
	}
	for _, lr := range res.Rules {
		if lr.Source != "/p/"+string(lr.Layer)+".json" {
			t.Errorf("rule %s has source %q", lr.Rule.ID, lr.Source)
		}
	}
	if res.Rules[1].Index != 2 {
		t.Errorf("repo-id index = %d, want 2", res.Rules[1].Index)
	}
}

func TestMergeSameLayerDuplicateFirstWins(t *testing.T) {
	repo := layerFrom(model.LayerRepo, [][]any{{"dup-rule", true}, {"dup-rule", true}})
	res := config.Merge([]config.LayerRules{repo})
	if len(res.Rules) != 1 || len(res.Notices) != 1 || res.Notices[0].Kind != model.NoticeRuleShadowed {
		t.Fatalf("rules=%d notices=%+v", len(res.Rules), res.Notices)
	}
	if res.All[1].ShadowBy != model.LayerRepo {
		t.Errorf("ShadowBy = %q", res.All[1].ShadowBy)
	}
}

func TestMergeDroppedRulesDoNotCount(t *testing.T) {
	var rows [][]any
	for i := 0; i < 15; i++ {
		rows = append(rows, []any{fmt.Sprintf("rule-%02d", i), i%5 != 4}) // 3 disabled
	}
	res := config.Merge([]config.LayerRules{layerFrom(model.LayerOrg, rows)})
	if len(res.Rules) != 12 {
		t.Fatalf("active = %d, want 12", len(res.Rules))
	}
	if len(res.Notices) != 0 {
		t.Errorf("12 enabled of 15 rows must not drop anything: %+v", res.Notices)
	}
	rows = append(rows, []any{"rule-extra", true})
	res = config.Merge([]config.LayerRules{layerFrom(model.LayerOrg, rows)})
	if len(res.Notices) != 1 || res.Notices[0].RuleID != "rule-extra" || res.Notices[0].Kind != model.NoticeRuleDropped {
		t.Errorf("notices = %+v", res.Notices)
	}
	last := res.All[len(res.All)-1]
	if last.Fate != config.FateDropped {
		t.Errorf("fate = %q", last.Fate)
	}
}

func TestMergeConfigInvalidNotices(t *testing.T) {
	repoLKG := layerFrom(model.LayerRepo, [][]any{{"repo-rule", true}})
	repoLKG.Source.Status, repoLKG.Source.Error = model.LayerLKG, "bad-regex: oops"
	userBad := config.LayerRules{
		Source: model.LayerSource{Layer: model.LayerUser, Path: "/p/user.json", Status: model.LayerInvalid},
		Diags:  []model.Diagnostic{{Severity: model.SevError, Code: "bad-id", Message: "bad id X"}},
	}
	res := config.Merge([]config.LayerRules{layerFrom(model.LayerOrg, [][]any{{"org-rule", true}}), repoLKG, userBad})
	if got := activeIDs(res); !reflect.DeepEqual(got, []string{"org-rule", "repo-rule"}) {
		t.Errorf("active = %v", got)
	}
	if len(res.Notices) != 2 {
		t.Fatalf("notices = %+v", res.Notices)
	}
	for i, wantLayer := range []model.Layer{model.LayerRepo, model.LayerUser} {
		n := res.Notices[i]
		if n.Kind != model.NoticeConfigInvalid || n.Layer != wantLayer || n.Path == "" || n.Message == "" {
			t.Errorf("notice %d = %+v", i, n)
		}
	}
}

func TestMergeSettingsPerKeyPrecedence(t *testing.T) {
	i := func(n int) *int { return &n }
	mk := func(layer model.Layer, in *model.InjectSettings) config.LayerRules {
		lr := layerFrom(layer, nil)
		lr.File.Settings = &model.Settings{Inject: in}
		return lr
	}
	res := config.Merge([]config.LayerRules{
		mk(model.LayerOrg, &model.InjectSettings{}),
		mk(model.LayerRepo, &model.InjectSettings{BatchAfterTools: i(0)}),
		mk(model.LayerUser, &model.InjectSettings{AnchorEvery: i(4), BatchAfterTools: i(9)}),
	})
	in := res.Settings.Inject
	if in == nil || in.AnchorEvery == nil || *in.AnchorEvery != 4 || in.BatchAfterTools == nil || *in.BatchAfterTools != 0 {
		t.Errorf("settings = %+v", in)
	}
	// Mutating the result must not reach the layer input.
	*in.AnchorEvery = 99
	if got := config.Merge([]config.LayerRules{mk(model.LayerUser, &model.InjectSettings{AnchorEvery: i(4)})}); *got.Settings.Inject.AnchorEvery != 4 {
		t.Error("merge aliases input settings")
	}
	if none := config.Merge(nil); none.Settings.Inject != nil || none.Rules != nil {
		t.Errorf("empty merge = %+v", none)
	}
}
