package inject_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/model"
)

func build(t *testing.T, client model.ClientID, event model.EventName, rs *model.CompiledRuleSet, st *fakeState, jobs ...model.VerifyRecord) *model.Injection {
	t.Helper()
	if st == nil {
		st = &fakeState{}
	}
	inj, err := inject.Builder{}.Build(&model.InjectRequest{Event: ev(client, event), Client: client, Rules: rs, Session: st, NewJobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	return inj
}

func withSettings(rs *model.CompiledRuleSet, anchorEvery, batch *int) *model.CompiledRuleSet {
	rs.Settings.Inject = &model.InjectSettings{AnchorEvery: anchorEvery, BatchAfterTools: batch}
	return rs
}

func ip(n int) *int { return &n }

func TestAnchorEveryCadence(t *testing.T) {
	cases := []struct {
		every *int
		fires []int // prompt_count values (0-based, before this prompt) that emit
	}{
		{nil, []int{0, 1, 2, 3, 4, 5, 6, 7, 8}},
		{ip(1), []int{0, 1, 2, 3, 4, 5, 6, 7, 8}},
		{ip(3), []int{2, 5, 8}},
		{ip(0), nil},
		{ip(-4), []int{0, 1, 2, 3, 4, 5, 6, 7, 8}}, // negative is invalid: default
	}
	for _, c := range cases {
		name := "default"
		if c.every != nil {
			name = fmt.Sprint(*c.every)
		}
		t.Run("anchor_every="+name, func(t *testing.T) {
			rs := withSettings(threeRules(), c.every, nil)
			var got []int
			for pc := 0; pc <= 8; pc++ {
				inj := build(t, model.ClientClaude, model.EventUserPromptSubmit, rs, &fakeState{promptCount: pc, generation: gen})
				if len(inj.Kinds) == 1 && inj.Kinds[0] == model.InjectAnchor {
					got = append(got, pc)
					if !inj.AnchorEmitted || inj.FullEmitted {
						t.Errorf("flags = %+v", inj)
					}
				} else if inj.Text != "" || inj.AnchorEmitted {
					t.Errorf("pc=%d unexpected injection %+v", pc, inj)
				}
			}
			if !reflect.DeepEqual(got, c.fires) {
				t.Errorf("fired at %v, want %v", got, c.fires)
			}
		})
	}
}

func TestBatchAfterToolsThreshold(t *testing.T) {
	cases := []struct {
		batch *int
		count int
		want  bool
	}{
		{nil, 24, false}, {nil, 25, true}, {nil, 400, true},
		{ip(5), 4, false}, {ip(5), 5, true},
		{ip(0), 1000, false},
	}
	for _, c := range cases {
		rs := withSettings(threeRules(), nil, c.batch)
		inj := build(t, model.ClientClaude, model.EventPostToolBatch, rs, &fakeState{toolSince: c.count, generation: gen})
		if got := inj.Text != ""; got != c.want {
			t.Errorf("batch=%v count=%d: injected=%v want %v", c.batch, c.count, got, c.want)
		}
		if c.want && (!inj.AnchorEmitted || !reflect.DeepEqual(kindsOf(inj), []string{"anchor"})) {
			t.Errorf("flags/kinds = %+v", inj)
		}
	}
}

func TestCarrierPerClient(t *testing.T) {
	rs := threeRules()
	st := func() *fakeState { return &fakeState{toolSince: 99, promptCount: 0, generation: gen} }
	type row struct {
		client model.ClientID
		event  model.EventName
		kinds  []string
	}
	for _, r := range []row{
		{model.ClientClaude, model.EventSessionStart, []string{"full"}},
		{model.ClientClaude, model.EventUserPromptSubmit, []string{"anchor"}},
		{model.ClientClaude, model.EventPostToolBatch, []string{"anchor"}},
		{model.ClientClaude, model.EventPostToolUse, nil},
		{model.ClientClaude, model.EventPreToolUse, nil},
		{model.ClientClaude, model.EventStop, nil},
		{model.ClientCodex, model.EventSessionStart, []string{"full"}},
		{model.ClientCodex, model.EventUserPromptSubmit, []string{"anchor"}},
		{model.ClientCodex, model.EventPostToolUse, []string{"anchor"}},
		{model.ClientCodex, model.EventPostToolBatch, nil},
		{model.ClientCursor, model.EventSessionStart, []string{"full"}},
		{model.ClientCursor, model.EventUserPromptSubmit, []string{"anchor"}},
		{model.ClientCursor, model.EventPostToolUse, []string{"anchor"}},
		{model.ClientHermes, model.EventSessionStart, nil},
		{model.ClientHermes, model.EventUserPromptSubmit, []string{"full"}},
		{model.ClientHermes, model.EventPostToolUse, nil},
		{"unknown-client", model.EventSessionStart, nil},
	} {
		inj := build(t, r.client, r.event, rs, st())
		got := kindsOf(inj)
		if len(r.kinds) == 0 {
			if len(got) != 0 || inj.Text != "" {
				t.Errorf("%s/%s: got %v %q", r.client, r.event, got, inj.Text)
			}
			continue
		}
		if !reflect.DeepEqual(got, r.kinds) {
			t.Errorf("%s/%s: kinds %v, want %v", r.client, r.event, got, r.kinds)
		}
	}
}

func TestNothingMarkedWhenEventCannotCarry(t *testing.T) {
	st := &fakeState{generation: "OLD", digest: map[string]model.Action{}}
	inj := build(t, model.ClientHermes, model.EventPostToolUse, threeRules(), st, model.VerifyRecord{JobID: "j1", Name: "t", Outcome: model.OutcomePass})
	if !reflect.DeepEqual(*inj, model.Injection{}) {
		t.Errorf("injection = %+v, want zero", *inj)
	}
}

func TestFullAndAnchorFlags(t *testing.T) {
	inj := build(t, model.ClientClaude, model.EventSessionStart, threeRules(), nil)
	if !inj.FullEmitted || !inj.AnchorEmitted {
		t.Errorf("full must set FullEmitted and reset the tool counter: %+v", inj)
	}
	if want := inject.Digest(threeRules()); !reflect.DeepEqual(inj.RuleDigest, want) {
		t.Errorf("digest = %v", inj.RuleDigest)
	}
	// Nothing injectable ⇒ nothing emitted, so Hermes retries when rules appear.
	none := ruleSet(gen, ruleWith("silent-rule", model.ActionLog, "", ""))
	inj = build(t, model.ClientHermes, model.EventUserPromptSubmit, none, nil)
	if inj.Text != "" || inj.FullEmitted || inj.AnchorEmitted || inj.RuleDigest == nil {
		t.Errorf("no inject text: %+v", inj)
	}
}

func TestHermesFirstTurnRules(t *testing.T) {
	rs := threeRules()
	cases := []struct {
		desc  string
		st    fakeState
		first bool
		kinds []string
	}{
		{"fresh session", fakeState{}, false, []string{"full"}},
		{"first turn flag", fakeState{fullInjected: true, generation: gen}, true, []string{"full"}},
		{"later turn", fakeState{fullInjected: true, generation: gen, promptCount: 4}, false, []string{"anchor"}},
	}
	for _, c := range cases {
		e := ev(model.ClientHermes, model.EventUserPromptSubmit)
		e.IsFirstTurn = c.first
		st := c.st
		inj, err := inject.Builder{}.Build(&model.InjectRequest{Event: e, Client: model.ClientHermes, Rules: rs, Session: &st})
		if err != nil || !reflect.DeepEqual(kindsOf(inj), c.kinds) {
			t.Errorf("%s: kinds %v err %v, want %v", c.desc, kindsOf(inj), err, c.kinds)
		}
	}
}

func TestGenerationSummaryWhen(t *testing.T) {
	rs := threeRules()
	digest := map[string]model.Action{"read-before-edit": model.ActionLog}
	// First inject-capable event of any kind after the change carries it.
	for _, event := range []model.EventName{model.EventSessionStart, model.EventUserPromptSubmit, model.EventPostToolBatch, model.EventPreToolUse, model.EventPostToolUse} {
		inj := build(t, model.ClientClaude, event, rs, &fakeState{generation: "OLD", digest: digest, toolSince: 0, promptCount: 0})
		if len(inj.Kinds) == 0 || inj.Kinds[0] != model.InjectGenSummary {
			t.Errorf("%s: kinds %v", event, kindsOf(inj))
		}
	}
	// Unset or equal generation: no summary.
	for _, g := range []string{"", gen} {
		inj := build(t, model.ClientClaude, model.EventUserPromptSubmit, rs, &fakeState{generation: g, digest: digest})
		for _, k := range inj.Kinds {
			if k == model.InjectGenSummary {
				t.Errorf("generation %q produced a summary", g)
			}
		}
	}
	// A change that is not summarisable still refreshes the stored digest.
	inj := build(t, model.ClientClaude, model.EventPreToolUse, rs, &fakeState{generation: "OLD", digest: inject.Digest(rs)})
	if inj.Text != "" || !reflect.DeepEqual(inj.RuleDigest, inject.Digest(rs)) {
		t.Errorf("inj = %+v", inj)
	}
}

func TestNoticesPlacement(t *testing.T) {
	rs := threeRules()
	rs.Notices = []model.ConfigNotice{
		{Kind: model.NoticeConfigInvalid, Layer: model.LayerRepo, Message: "repo rule file /r/.ballast/rules.json is invalid; using last-known-good rules"},
		{Kind: model.NoticeRuleShadowed, Layer: model.LayerUser, RuleID: "x-rule", Message: "shadowed noise"},
		{Kind: model.NoticeRuleDropped, Layer: model.LayerUser, RuleID: "u-13", Message: "user rule \"u-13\" dropped: more than 12 enabled rules\nsecond line"},
	}
	inj := build(t, model.ClientClaude, model.EventSessionStart, rs, nil)
	if want := []string{"notices", "full"}; !reflect.DeepEqual(kindsOf(inj), want) {
		t.Fatalf("kinds = %v", kindsOf(inj))
	}
	if !strings.HasPrefix(inj.Text, "Ballast config notices:\n- config_invalid (repo): repo rule file") ||
		!strings.Contains(inj.Text, "- rule_dropped (user): user rule \"u-13\" dropped: more than 12 enabled rules\n\nBallast rules (generation") ||
		strings.Contains(inj.Text, "shadowed noise") || strings.Contains(inj.Text, "second line") {
		t.Errorf("text:\n%s", inj.Text)
	}
	// Not on ordinary prompts ...
	inj = build(t, model.ClientClaude, model.EventUserPromptSubmit, rs, &fakeState{generation: gen})
	if want := []string{"anchor"}; !reflect.DeepEqual(kindsOf(inj), want) {
		t.Errorf("steady state kinds = %v", kindsOf(inj))
	}
	// ... but on the first event after a generation change.
	inj = build(t, model.ClientClaude, model.EventUserPromptSubmit, rs, &fakeState{generation: "OLD", digest: inject.Digest(rs)})
	if want := []string{"notices", "anchor"}; !reflect.DeepEqual(kindsOf(inj), want) {
		t.Errorf("after change kinds = %v", kindsOf(inj))
	}
	// Hermes has no SessionStart carrier: the first full turn gets them.
	inj = build(t, model.ClientHermes, model.EventUserPromptSubmit, rs, nil)
	if want := []string{"notices", "full"}; !reflect.DeepEqual(kindsOf(inj), want) {
		t.Errorf("hermes kinds = %v", kindsOf(inj))
	}
}

func TestJobReporting(t *testing.T) {
	jobs := []model.VerifyRecord{
		{JobID: "job-1", Name: "test", Outcome: model.OutcomePass, SEnd: "abcdef0123456789"},
		{JobID: "job-2", Name: "lint", Outcome: model.OutcomeFail},
	}
	st := &fakeState{generation: gen, reportedJobs: map[string]bool{"job-2": true}}
	inj := build(t, model.ClientClaude, model.EventUserPromptSubmit, threeRules(), st, jobs...)
	if !reflect.DeepEqual(inj.ReportedJobs, []string{"job-1"}) || strings.Contains(inj.Text, "lint") {
		t.Errorf("already-reported job leaked: %v\n%s", inj.ReportedJobs, inj.Text)
	}
	// Only the jobs that actually fit are marked reported.
	var many []model.VerifyRecord
	for i := 0; i < 40; i++ {
		many = append(many, model.VerifyRecord{JobID: fmt.Sprintf("20261016T010203-%06d", i), Name: "suite", Outcome: model.OutcomePass, SEnd: model.TreeID(repeat("b", 40))})
	}
	inj = build(t, model.ClientClaude, model.EventUserPromptSubmit, threeRules(), &fakeState{generation: gen}, many...)
	shown := strings.Count(inj.Text, "verify suite:")
	if shown == 0 || shown >= 40 || len(inj.ReportedJobs) != shown {
		t.Errorf("shown=%d reported=%d", shown, len(inj.ReportedJobs))
	}
	for i, id := range inj.ReportedJobs {
		if id != many[i].JobID {
			t.Fatalf("reported %v is not a prefix of the input", inj.ReportedJobs)
		}
	}
	// SessionStart carries no job results.
	inj = build(t, model.ClientClaude, model.EventSessionStart, threeRules(), nil, jobs...)
	if len(inj.ReportedJobs) != 0 || strings.Contains(inj.Text, "verify ") {
		t.Errorf("SessionStart reported jobs: %v", inj.ReportedJobs)
	}
}

func TestRulesAreFilteredByClient(t *testing.T) {
	a := ruleWith("claude-only", model.ActionLog, "for claude", "")
	a.Clients = []model.ClientID{model.ClientClaude}
	b := ruleWith("everywhere", model.ActionLog, "for all", "")
	rs := ruleSet(gen, a, b)
	if got := build(t, model.ClientCodex, model.EventSessionStart, rs, nil).Text; strings.Contains(got, "for claude") || !strings.Contains(got, "for all") {
		t.Errorf("codex full:\n%s", got)
	}
	if got := build(t, model.ClientClaude, model.EventSessionStart, rs, nil).Text; !strings.Contains(got, "for claude") {
		t.Errorf("claude full:\n%s", got)
	}
}

func TestBuildRejectsIncompleteRequests(t *testing.T) {
	for name, req := range map[string]*model.InjectRequest{
		"nil":      nil,
		"no event": {Rules: threeRules()},
		"no rules": {Event: ev(model.ClientClaude, model.EventSessionStart)},
	} {
		if _, err := (inject.Builder{}).Build(req); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	// A missing session and client are tolerated.
	inj, err := inject.Builder{}.Build(&model.InjectRequest{Event: ev(model.ClientClaude, model.EventSessionStart), Rules: threeRules()})
	if err != nil || len(inj.Kinds) != 1 {
		t.Errorf("inj=%+v err=%v", inj, err)
	}
}

func TestBuildDoesNotMutateRequest(t *testing.T) {
	rs := threeRules()
	rs.Notices = []model.ConfigNotice{{Kind: model.NoticeConfigInvalid, Layer: model.LayerUser, Message: "m"}}
	before := mustJSON(rs)
	build(t, model.ClientClaude, model.EventSessionStart, rs, nil)
	if mustJSON(rs) != before {
		t.Error("rule set mutated")
	}
}
