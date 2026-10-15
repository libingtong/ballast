package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestVerifyRunningHint: a warn-or-stricter rule whose verify_fresh predicate
// is unknown because the verify job is still running does not hit, but the
// agent gets a warn-level notice (plan B3). A log-tier rule stays silent.
func TestVerifyRunningHint(t *testing.T) {
	stdin, err := os.ReadFile(filepath.Join("..", "..", "docs", "facts", "payloads", "bash-ok", "03-PreToolUse.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		action model.Action
		cause  string
		want   bool
	}{
		{"deny rule, job running", model.ActionDeny, "verify_fresh:job_running", true},
		{"warn rule, job running", model.ActionWarn, "verify_fresh:job_running", true},
		{"log rule, job running", model.ActionLog, "verify_fresh:job_running", false},
		{"deny rule, other cause", model.ActionDeny, "verify_fresh:no_record", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.rules.rs = &model.CompiledRuleSet{Generation: "gen1", Rules: []model.CompiledRule{
				{LoadedRule: model.LoadedRule{Rule: model.Rule{ID: "evidence-before-commit"}}, Action: c.action},
			}}
			h.engine.verdict = &model.Verdict{Results: []model.RuleResult{
				{RuleID: "evidence-before-commit", Outcome: model.TriUnknown, UnknownCause: c.cause},
			}}
			if code := h.main(model.ClientClaude, "PreToolUse", string(stdin)); code != 0 {
				t.Fatalf("exit %d: %s", code, h.stderr.String())
			}
			out := h.stdout.String()
			got := strings.Contains(out, "verify 进行中")
			if got != c.want {
				t.Errorf("hint present = %v, want %v (stdout %q)", got, c.want, out)
			}
			if strings.Contains(out, `"permissionDecision":"deny"`) {
				t.Errorf("unknown must never block: %q", out)
			}
		})
	}
}
