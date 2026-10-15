package inject_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/model"
)

var alphabets = []string{
	"abcdefghijklmnopqrstuvwxyz ",
	"字符预算测试规则注入中文，。",
	"日本語のテキスト🙂🚀é",
	"a\nb\r\nc\n\n d",
}

func randText(r *rand.Rand, n int) string {
	al := []rune(alphabets[r.Intn(len(alphabets))])
	out := make([]rune, n)
	for i := range out {
		out[i] = al[r.Intn(len(al))]
	}
	return string(out)
}

// worstCase builds 12 rules whose texts, anchors and ids are at (or past)
// the documented limits, plus a pile of notices and finished jobs.
func worstCase(r *rand.Rand) (*model.CompiledRuleSet, []model.VerifyRecord) {
	var rules []model.Rule
	for i := 0; i < model.MaxEnabledRules; i++ {
		id := fmt.Sprintf("%s-%02d", strings.Repeat("r", 36), i) // 39 chars
		anchor := ""
		if r.Intn(2) == 0 {
			anchor = randText(r, model.MaxAnchorRunes+r.Intn(60))
		}
		rules = append(rules, ruleWith(id, model.ActionDeny, randText(r, model.MaxInjectTextRunes+r.Intn(3)), anchor))
	}
	rs := ruleSet(gen, rules...)
	for i := 0; i < 8; i++ {
		kind := []model.NoticeKind{model.NoticeConfigInvalid, model.NoticeRuleDropped, model.NoticeRuleShadowed}[r.Intn(3)]
		rs.Notices = append(rs.Notices, model.ConfigNotice{Kind: kind, Layer: model.LayerUser, RuleID: "x", Message: randText(r, 50+r.Intn(500))})
	}
	var jobs []model.VerifyRecord
	for i := 0; i < 10; i++ {
		jobs = append(jobs, model.VerifyRecord{
			JobID: fmt.Sprintf("20261016T010203-%06x", r.Intn(1<<24)), Name: randText(r, 1+r.Intn(60)),
			Outcome: model.OutcomeFail, SEnd: model.TreeID(strings.Repeat("c", 40)),
		})
	}
	return rs, jobs
}

var allEvents = []model.EventName{
	model.EventSessionStart, model.EventUserPromptSubmit, model.EventPostToolBatch, model.EventPostToolUse,
	model.EventPostToolUseFailure, model.EventPreToolUse, model.EventStop,
}

func checkBudgets(t *testing.T, client model.ClientID, event model.EventName, inj *model.Injection, desc string) {
	t.Helper()
	if !utf8.ValidString(inj.Text) {
		t.Fatalf("%s: invalid UTF-8", desc)
	}
	n := utf8.RuneCountInString(inj.Text)
	caps := model.Capabilities(client)
	limit := inject.BudgetNonFull
	if event == caps.FullInjectEvent && event == model.EventSessionStart || containsKind(inj, model.InjectFull) {
		limit = model.BudgetSessionStart
	}
	if n > limit {
		t.Fatalf("%s: %s/%s text is %d runes, limit %d", desc, client, event, n, limit)
	}
	if !caps.CanInject(event) && (inj.Text != "" || inj.AnchorEmitted || inj.FullEmitted || len(inj.Kinds) > 0) {
		t.Fatalf("%s: %s/%s cannot inject but returned %+v", desc, client, event, inj)
	}
	if len(inj.Kinds) == 0 && inj.Text != "" || len(inj.Kinds) > 0 && inj.Text == "" {
		t.Fatalf("%s: kinds %v vs text %q", desc, inj.Kinds, inj.Text)
	}
}

func containsKind(inj *model.Injection, k model.InjectKind) bool {
	for _, x := range inj.Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Property: for any rule set within the documented limits (12 rules × 400
// runes, and well beyond), notices and jobs, no client/event combination
// ever exceeds its budget, and each building block respects its own.
func TestBudgetsNeverExceeded(t *testing.T) {
	r := rand.New(rand.NewSource(20261016))
	for iter := 0; iter < 150; iter++ {
		rs, jobs := worstCase(r)
		if n := rc(inject.FullText(rs)); n > model.BudgetSessionStart {
			t.Fatalf("FullText %d runes", n)
		}
		if n := rc(inject.AnchorText(rs)); n > model.BudgetAnchor {
			t.Fatalf("AnchorText %d runes", n)
		}
		if n := rc(inject.JobResults(jobs)); n > inject.BudgetJobResults {
			t.Fatalf("JobResults %d runes", n)
		}
		prev := map[string]model.Action{}
		for i := 0; i < r.Intn(60); i++ {
			prev[fmt.Sprintf("old-%s-%d", randText(r, 20), i)] = model.ActionWarn
		}
		for i := range rs.Rules {
			if r.Intn(2) == 0 {
				prev[rs.Rules[i].Rule.ID] = model.ActionLog
			}
		}
		if n := rc(inject.GenSummary(prev, rs)); n > model.BudgetGenSummary {
			t.Fatalf("GenSummary %d runes", n)
		}
		if mdc := inject.RenderMDC(rs); !utf8.Valid(mdc) {
			t.Fatal("RenderMDC produced invalid UTF-8")
		}

		for _, client := range model.AllClients {
			for _, event := range allEvents {
				st := &fakeState{
					promptCount: r.Intn(4), toolSince: 25 + r.Intn(50), generation: "OLD", digest: prev,
					fullInjected: r.Intn(2) == 0,
				}
				if r.Intn(5) == 0 {
					st.generation = gen
				}
				e := ev(client, event)
				e.IsFirstTurn = r.Intn(3) == 0
				inj, err := inject.Builder{}.Build(&model.InjectRequest{Event: e, Client: client, Rules: rs, Session: st, NewJobs: jobs})
				if err != nil {
					t.Fatal(err)
				}
				checkBudgets(t, client, event, inj, fmt.Sprintf("iter %d", iter))
			}
		}
	}
}

// The documented worst case exactly: 12 rules × 400 runes each, everything
// else present, on every carrier.
func TestTwelveByFourHundred(t *testing.T) {
	var rules []model.Rule
	for i := 0; i < 12; i++ {
		rules = append(rules, ruleWith(fmt.Sprintf("rule-with-long-id-%02d", i), model.ActionDeny, strings.Repeat("字", 400), strings.Repeat("锚", 80)))
	}
	rs := ruleSet(gen, rules...)
	full := inject.FullText(rs)
	if n := rc(full); n > model.BudgetSessionStart || strings.Contains(full, "omitted") {
		t.Errorf("12×400 must fit the SessionStart budget untruncated: %d runes", n)
	}
	st := &fakeState{generation: "OLD", digest: map[string]model.Action{"gone-rule-aaaaaaaaaaaaaaa": model.ActionDeny}, toolSince: 99}
	rs.Notices = []model.ConfigNotice{{Kind: model.NoticeConfigInvalid, Layer: model.LayerRepo, Message: strings.Repeat("n", 1000)}}
	var jobs []model.VerifyRecord
	for i := 0; i < 9; i++ {
		jobs = append(jobs, model.VerifyRecord{JobID: fmt.Sprintf("20261016T010203-aaaa%02d", i), Name: "a-long-verify-name", Outcome: model.OutcomeTimeout, SEnd: model.TreeID(strings.Repeat("d", 40))})
	}
	inj := build(t, model.ClientClaude, model.EventUserPromptSubmit, rs, st, jobs...)
	if got, want := kindsOf(inj), []string{"notices", "gen_summary", "job_results", "anchor"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v", got)
	}
	if n := rc(inj.Text); n > inject.BudgetNonFull {
		t.Errorf("non-SessionStart text is %d runes", n)
	}
	if !strings.Contains(inj.Text, "[rule-with-long-id-00]") {
		t.Error("anchor lost entirely under pressure")
	}
	inj = build(t, model.ClientClaude, model.EventSessionStart, rs, st, jobs...)
	if n := rc(inj.Text); n > model.BudgetSessionStart || inj.Kinds[len(inj.Kinds)-1] != model.InjectFull {
		t.Errorf("SessionStart: %d runes, kinds %v", n, inj.Kinds)
	}
}

func FuzzBuildBudgets(f *testing.F) {
	f.Add("hello", "anchor", "msg", uint8(0), uint8(3), 25)
	f.Add(strings.Repeat("字", 500), strings.Repeat("a", 200), strings.Repeat("x", 900), uint8(1), uint8(2), 0)
	f.Add("a\n\nb\x00\xff", "", "\n", uint8(3), uint8(0), 99)
	f.Fuzz(func(t *testing.T, text, anchor, notice string, clientIdx, eventIdx uint8, tools int) {
		client := model.AllClients[int(clientIdx)%len(model.AllClients)]
		event := allEvents[int(eventIdx)%len(allEvents)]
		var rules []model.Rule
		for i := 0; i < 12; i++ {
			rules = append(rules, ruleWith(fmt.Sprintf("fuzz-rule-%02d", i), model.ActionWarn, text, anchor))
		}
		rs := ruleSet(gen, rules...)
		rs.Notices = []model.ConfigNotice{{Kind: model.NoticeConfigInvalid, Layer: model.LayerUser, Message: notice}}
		st := &fakeState{toolSince: tools, generation: "OLD", digest: map[string]model.Action{"fuzz-rule-00": model.ActionLog}}
		inj, err := inject.Builder{}.Build(&model.InjectRequest{
			Event: ev(client, event), Client: client, Rules: rs, Session: st,
			NewJobs: []model.VerifyRecord{{JobID: "j", Name: notice, Outcome: model.OutcomePass, SEnd: model.TreeID(text)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		n := utf8.RuneCountInString(inj.Text)
		limit := inject.BudgetNonFull
		if containsKind(inj, model.InjectFull) {
			limit = model.BudgetSessionStart
		}
		if n > limit {
			t.Fatalf("%d runes > %d", n, limit)
		}
	})
}
