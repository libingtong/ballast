package dsl

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestHermesStopContinue covers the P2 warning code hermes-stop-continue:
// Hermes pre_verify (normalized to Stop) answers a deny with "keep going",
// not "refuse to stop" (docs/p2-design.md section 7.1).
func TestHermesStopContinue(t *testing.T) {
	r := func(event model.EventName, action, target model.Action, clients ...model.ClientID) *model.Rule {
		return &model.Rule{ID: "hermes-rule", Event: event, Action: action, Target: target, Clients: clients}
	}
	const warn = "warning:hermes-stop-continue@/action"
	cases := []struct {
		name   string
		rule   *model.Rule
		client model.ClientID
		want   []string
	}{
		{"stop deny warns", r(model.EventStop, "deny", ""), model.ClientHermes, []string{warn}},
		{"stop deny scoped to hermes", r(model.EventStop, "deny", "", model.ClientHermes), model.ClientHermes, []string{warn}},
		{"stop deny is verified, so no action-unverified", r(model.EventStop, "deny", ""), model.ClientHermes, []string{warn}},
		{"stop log is silent", r(model.EventStop, "log", ""), model.ClientHermes, nil},
		{"default action is silent", r(model.EventStop, "", ""), model.ClientHermes, nil},
		{"stop warn is not supported on hermes", r(model.EventStop, "warn", ""), model.ClientHermes, []string{"error:action-unsupported@/action"}},
		{"stop ask is not supported on hermes", r(model.EventStop, "ask", ""), model.ClientHermes, []string{"error:action-unsupported@/action"}},
		{"subagent stop does not exist on hermes", r(model.EventSubagentStop, "deny", ""), model.ClientHermes, []string{"warning:event-unavailable@/event"}},
		{"pre tool deny stays silent", r(model.EventPreToolUse, "deny", ""), model.ClientHermes, nil},
		{"rule for another client is not checked", r(model.EventStop, "deny", "", model.ClientClaude), model.ClientHermes, nil},
		{"claude stop deny has no hermes warning", r(model.EventStop, "deny", ""), model.ClientClaude, nil},
		{"codex stop deny has no hermes warning", r(model.EventStop, "deny", ""), model.ClientCodex, []string{"warning:action-unverified@/action"}},
		{"cursor keeps its own code", r(model.EventStop, "deny", ""), model.ClientCursor, []string{"warning:cursor-stop-followup@/action", "warning:action-unverified@/action"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := CheckCapabilities(c.rule, c.client)
			got := diagKeys(ds)
			want := append([]string(nil), c.want...)
			sortStrings2(want)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("got %v want %v", got, want)
			}
			for _, d := range ds {
				if d.Code != CodeHermesStopContinue {
					continue
				}
				if d.Severity != model.SevWarning || d.Client != model.ClientHermes || d.RuleID != "hermes-rule" {
					t.Errorf("metadata: %+v", d)
				}
				if !strings.Contains(d.Message, "pre_verify") || !strings.Contains(d.Message, "keep going") {
					t.Errorf("message should explain the continue semantics: %q", d.Message)
				}
			}
		})
	}
}

func TestHermesStopContinueCodeValue(t *testing.T) {
	if CodeHermesStopContinue != "hermes-stop-continue" {
		t.Errorf("code = %q", CodeHermesStopContinue)
	}
}

// TestHermesStopContinueThroughLint runs the warning through the full Lint
// path: the pointer is prefixed with /rules/<i>, the file stays valid and the
// warning appears only for clients that ask for hermes.
func TestHermesStopContinueThroughLint(t *testing.T) {
	e := newEngine(t)
	rule := `{"id":"keep-pushing","action":"deny","event":"Stop",` +
		`"match":{"text":{"source":"last_assistant_message","regex":"done"}},` +
		`"message":"m","examples":[` +
		`{"name":"hit","expect":"hit","input":{"last_assistant_message":"done"}},` +
		`{"name":"miss","expect":"miss","input":{"last_assistant_message":"working"}}]}`
	file := `{"version":1,"rules":[` + rule + `]}`
	lint := func(clients ...model.ClientID) []string {
		ds := e.Lint([]LintInput{{Path: "r.json", Layer: model.LayerFile, Data: []byte(file)}}, LintOptions{Clients: clients})
		if HasErrors(ds) {
			t.Fatalf("unexpected errors: %+v", ds)
		}
		return diagKeys(ds)
	}
	got := lint(model.ClientHermes)
	if len(got) != 1 || got[0] != "warning:hermes-stop-continue@/rules/0/action" {
		t.Errorf("hermes lint = %v", got)
	}
	for _, k := range lint(model.ClientClaude) {
		if strings.Contains(k, "hermes-stop-continue") {
			t.Errorf("claude lint leaked the hermes warning: %v", k)
		}
	}
}
