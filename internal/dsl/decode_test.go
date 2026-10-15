package dsl

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestParseFile(t *testing.T) {
	e := newEngine(t)
	good := ruleFileJSON(stackRule("good-rule", ""))
	f, ds, err := e.ParseFile(model.LayerRepo, "/repo/.ballast/rules.json", good)
	if err != nil || f == nil || len(f.Rules) != 1 || len(ds) != 0 {
		t.Fatalf("good file: %v %v %v", f, ds, err)
	}

	// Rule-level problems are diagnostics, not an error.
	f, ds, err = e.ParseFile(model.LayerUser, "u.json", ruleFileJSON(stackRule("Bad", "")))
	if err != nil || f == nil || len(ds) != 1 || ds[0].Code != CodeBadID || ds[0].File != "u.json" || ds[0].RuleID != "Bad" {
		t.Fatalf("invalid rule: %v %+v %v", f, ds, err)
	}

	// Undecodable input returns an error and the matching diagnostics.
	f, ds, err = e.ParseFile(model.LayerOrg, "o.json", []byte(`{"version":1,"rules":[{"id":"x","bogus":1}]}`))
	if err == nil || f != nil || len(ds) != 1 || ds[0].Code != CodeUnknownField || ds[0].File != "o.json" || ds[0].Pointer != "/rules/0/bogus" {
		t.Fatalf("undecodable: %v %+v %v", f, ds, err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error text %q", err)
	}
}

func TestDecodeRuleFile(t *testing.T) {
	f, err := DecodeRuleFile([]byte(`{"$schema":"../rules.v1.json","version":1,
		"settings":{"inject":{"anchor_every":0,"batch_after_tools":10}},
		"rules":[{"id":"abc","event":"Stop","examples":[],
			"inject":"short text",
			"match":{"tool":"Edit","path":["a","b"],"text":{"count_regex":{"regex":"x","gte":2}}},
			"when":[{"prior_hit":{"rule":"abc","gte":1}},{"not":{"is_subagent":{}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.Rules[0]
	if f.Schema == "" || *f.Settings.Inject.AnchorEvery != 0 || *f.Settings.Inject.BatchAfterTools != 10 {
		t.Errorf("settings: %+v", f.Settings)
	}
	if r.Inject.Text != "short text" || len(r.Match.Path) != 2 || len(r.Match.Tool) != 1 {
		t.Errorf("rule: %+v", r)
	}
	if c := r.Match.Text.CountRegex; c.Regex != "x" || *c.Gte != 2 {
		t.Errorf("count_regex: %+v", c)
	}
	if r.When[0].PriorHit.Rule != "abc" || *r.When[0].PriorHit.Gte != 1 || r.When[1].Not.IsSubagent == nil {
		t.Errorf("when: %+v", r.When)
	}

	for _, bad := range []struct{ name, in, code string }{
		{"empty", ``, CodeSchema},
		{"truncated", `{"version":1`, CodeSchema},
		{"not an object", `[]`, CodeSchema},
		{"scalar rules", `{"version":1,"rules":3}`, CodeSchema},
		{"unknown inject key", `{"version":1,"rules":[{"id":"x","inject":{"text":"t","bogus":1}}]}`, CodeUnknownField},
		{"unknown in count_regex", `{"version":1,"rules":[{"id":"x","match":{"text":{"count_regex":{"regex":"a","gte":1,"bogus":2}}}}]}`, CodeUnknownField},
		{"unknown in settings", `{"version":1,"settings":{"nope":1},"rules":[]}`, CodeUnknownField},
		{"string list of numbers", `{"version":1,"rules":[{"id":"x","match":{"tool":[1]}}]}`, CodeSchema},
		{"trailing garbage", `{"version":1,"rules":[]} x`, CodeSchema},
	} {
		t.Run(bad.name, func(t *testing.T) {
			_, err := DecodeRuleFile([]byte(bad.in))
			de, ok := err.(*decodeError)
			if !ok || len(de.diags) == 0 || de.diags[0].Code != bad.code || err.Error() == "" {
				t.Fatalf("err = %#v, want code %s", err, bad.code)
			}
		})
	}
}

func TestUnknownFieldReportsEveryKey(t *testing.T) {
	_, err := DecodeRuleFile([]byte(`{"version":1,"zzz":1,"rules":[{"id":"x","aaa":1,"when":[{"exec":{}},{"not":{"shell":{}}}],"inject":{"text":"t","bad":1}}]}`))
	de := err.(*decodeError)
	var got []string
	for _, d := range de.diags {
		got = append(got, d.Code+"@"+d.Pointer)
	}
	want := "unknown-field@/zzz,unknown-field@/rules/0/aaa,unknown-predicate@/rules/0/when/0/exec,unknown-predicate@/rules/0/when/1/not/shell,unknown-field@/rules/0/inject/bad"
	// Order: keys of one object are sorted; the walk is depth first.
	if len(got) != 5 {
		t.Fatalf("got %v", got)
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range strings.Split(want, ",") {
		if !have[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func TestValidateNilAndEmpty(t *testing.T) {
	e := newEngine(t)
	if ds := e.Validate("x.json", nil); len(ds) != 1 || ds[0].Code != CodeSchema {
		t.Errorf("nil file: %+v", ds)
	}
	ds := e.Validate("x.json", &model.RuleFile{Version: 1, Rules: []model.Rule{}})
	if len(ds) != 0 {
		t.Errorf("empty rule list is valid: %+v", ds)
	}
}

func TestCheckCapabilities(t *testing.T) {
	r := func(event model.EventName, action, target model.Action, clients ...model.ClientID) *model.Rule {
		return &model.Rule{ID: "cap-rule", Event: event, Action: action, Target: target, Clients: clients}
	}
	cases := []struct {
		name   string
		rule   *model.Rule
		client model.ClientID
		want   []string
	}{
		{"nil rule", nil, model.ClientClaude, nil},
		{"claude pre deny", r(model.EventPreToolUse, "deny", ""), model.ClientClaude, nil},
		{"claude post deny", r(model.EventPostToolUse, "deny", ""), model.ClientClaude, []string{"error:action-unsupported@/action"}},
		{"claude stop warn", r(model.EventStop, "warn", ""), model.ClientClaude, nil},
		{"claude subagent stop warn is unverified", r(model.EventSubagentStop, "warn", ""), model.ClientClaude, []string{"warning:action-unverified@/action"}},
		{"default action is log", r(model.EventPostToolUse, "", ""), model.ClientHermes, nil},
		{"codex ask", r(model.EventPreToolUse, "ask", ""), model.ClientCodex, []string{"error:action-unsupported@/action"}},
		{"codex stop deny", r(model.EventStop, "deny", ""), model.ClientCodex, []string{"warning:action-unverified@/action"}},
		{"codex subagent stop", r(model.EventSubagentStop, "log", ""), model.ClientCodex, []string{"warning:event-unavailable@/event"}},
		{"cursor pre ask", r(model.EventPreToolUse, "ask", ""), model.ClientCursor, []string{"warning:action-unverified@/action"}},
		{"cursor subagent stop deny", r(model.EventSubagentStop, "deny", ""), model.ClientCursor, []string{"warning:cursor-stop-followup@/action", "warning:action-unverified@/action"}},
		{"hermes post warn", r(model.EventPostToolUse, "warn", ""), model.ClientHermes, []string{"error:action-unsupported@/action"}},
		{"hermes prompt warn", r(model.EventUserPromptSubmit, "warn", ""), model.ClientHermes, []string{"warning:action-unverified@/action"}},
		{"explicit target", r(model.EventPreToolUse, "log", "warn", model.ClientClaude, model.ClientCodex), model.ClientCodex, []string{"warning:target-unsupported@/target"}},
		{"target log is never flagged", r(model.EventPreToolUse, "log", "log"), model.ClientCodex, nil},
		{"rule not for this client", r(model.EventPreToolUse, "ask", "", model.ClientClaude), model.ClientCodex, nil},
		{"bad event is reported elsewhere", r("Nope", "deny", ""), model.ClientClaude, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := CheckCapabilities(c.rule, c.client)
			got := diagKeys(ds)
			want := append([]string(nil), c.want...)
			sortStrings2(want)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("got %v want %v", got, want)
			}
			for _, d := range ds {
				if d.Client != c.client || d.RuleID != "cap-rule" || d.Message == "" {
					t.Errorf("diagnostic metadata: %+v", d)
				}
			}
		})
	}
}

func sortStrings2(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func TestHelpers(t *testing.T) {
	if defaultTextSource(model.EventUserPromptSubmit) != model.TextPrompt || defaultTextSource(model.EventStop) != model.TextLastAssistantMessage ||
		defaultTextSource(model.EventSubagentStop) != model.TextLastAssistantMessage || defaultTextSource(model.EventPostToolUse) != model.TextToolResponse ||
		defaultTextSource(model.EventPostToolUseFailure) != model.TextError {
		t.Error("defaultTextSource")
	}
	if anchorText(nil) != "" || anchorText(&model.Inject{Text: "line one\nline two"}) != "line one" ||
		anchorText(&model.Inject{Text: "t", Anchor: "mine"}) != "mine" || RuneLen(anchorText(&model.Inject{Text: strings.Repeat("字", 100)})) != 80 {
		t.Error("anchorText")
	}
	for id, want := range map[string]bool{"abc": true, "a-b-1": true, "ab": false, "Abc": false, "a_b": false, strings.Repeat("a", 41): false, strings.Repeat("a", 40): true} {
		if validRuleID(id) != want {
			t.Errorf("validRuleID(%q) != %v", id, want)
		}
	}
	if HasErrors(nil) || HasErrors([]model.Diagnostic{{Severity: model.SevWarning}}) || !HasErrors([]model.Diagnostic{{Severity: model.SevError}}) {
		t.Error("HasErrors")
	}
	if comparePointers("/rules/2/x", "/rules/10/x") >= 0 || comparePointers("/a", "/a") != 0 || comparePointers("/a/b", "/a") <= 0 {
		t.Error("comparePointers")
	}
}
