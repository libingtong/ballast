package dsl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func stackRule(id string, extra string) string {
	return fmt.Sprintf(`{"id":%q,"action":"deny","event":"PreToolUse","match":{"tool":"Edit"},"message":"m",%s
	"examples":[{"expect":"hit","input":{"tool_name":"Edit","tool_input":{"file_path":"/repo/a"}}},{"expect":"miss","input":{"tool_name":"Write","tool_input":{"file_path":"/repo/a"}}}]}`, id, extra)
}

func stackInput(layer model.Layer, rules ...string) LintInput {
	return LintInput{Path: string(layer) + ".json", Layer: layer, Data: ruleFileJSON(rules...)}
}

func TestLintStack(t *testing.T) {
	e := newEngine(t)
	t.Run("org ids cannot be redefined below", func(t *testing.T) {
		ds := e.Lint([]LintInput{
			stackInput(model.LayerUser, stackRule("same-id", "")),
			stackInput(model.LayerOrg, stackRule("same-id", "")),
			stackInput(model.LayerRepo, stackRule("other-id", ""), stackRule("same-id", "")),
		}, LintOptions{Stack: true})
		got := diagKeys(ds)
		want := []string{
			"error:org-id-shadowed@/rules/1/id",
			"error:org-id-shadowed@/rules/0/id",
		}
		if strings.Join(got, ",") != strings.Join(sortedCopy(want), ",") {
			t.Errorf("got %q want %q", got, sortedCopy(want))
		}
		files := map[string]bool{}
		for _, d := range ds {
			files[d.File] = true
		}
		if !files["repo.json"] || !files["user.json"] || files["org.json"] {
			t.Errorf("diagnostics attributed to %v", files)
		}
	})
	t.Run("repo shadows user with a warning", func(t *testing.T) {
		ds := e.Lint([]LintInput{
			stackInput(model.LayerRepo, stackRule("same-id", "")),
			stackInput(model.LayerUser, stackRule("same-id", "")),
		}, LintOptions{Stack: true})
		if got := diagKeys(ds); len(got) != 1 || got[0] != "warning:duplicate-id@/rules/0/id" || ds[0].File != "user.json" {
			t.Errorf("got %+v", ds)
		}
	})
	t.Run("same layer twice is a duplicate", func(t *testing.T) {
		ds := e.Lint([]LintInput{
			{Path: "a.json", Layer: model.LayerRepo, Data: ruleFileJSON(stackRule("same-id", ""))},
			{Path: "b.json", Layer: model.LayerRepo, Data: ruleFileJSON(stackRule("same-id", ""))},
		}, LintOptions{Stack: true})
		if got := diagKeys(ds); len(got) != 1 || got[0] != "error:duplicate-id@/rules/0/id" {
			t.Errorf("got %+v", ds)
		}
	})
	t.Run("at most 12 enabled rules in total", func(t *testing.T) {
		var org, repo, user []string
		for i := 0; i < 5; i++ {
			org = append(org, stackRule(fmt.Sprintf("org-rule-%d", i), ""))
			repo = append(repo, stackRule(fmt.Sprintf("repo-rule-%d", i), ""))
			user = append(user, stackRule(fmt.Sprintf("user-rule-%d", i), ""))
		}
		disabled := stackRule("off-rule", `"enabled":false,`)
		ds := e.Lint([]LintInput{
			stackInput(model.LayerUser, append(user, disabled)...),
			stackInput(model.LayerOrg, org...),
			stackInput(model.LayerRepo, repo...),
		}, LintOptions{Stack: true})
		// 15 enabled rules: org 5, repo 5, then user rules #11 and #12 fit, #13..#15 are dropped.
		var dropped []string
		for _, d := range ds {
			if d.Code == CodeTooManyRules {
				dropped = append(dropped, d.RuleID)
				if d.File != "user.json" {
					t.Errorf("dropped rule attributed to %s", d.File)
				}
			}
		}
		if strings.Join(dropped, ",") != "user-rule-2,user-rule-3,user-rule-4" {
			t.Errorf("dropped = %v (all diagnostics %v)", dropped, diagKeys(ds))
		}
	})
	t.Run("prior_hit may refer to a rule of another layer", func(t *testing.T) {
		ds := e.Lint([]LintInput{
			stackInput(model.LayerOrg, stackRule("org-base", "")),
			stackInput(model.LayerUser, stackRule("user-rule", `"when":[{"prior_hit":{"rule":"org-base","gte":1}}],`)),
		}, LintOptions{Stack: true})
		if len(ds) != 0 {
			t.Errorf("unexpected diagnostics %+v", ds)
		}
		ds = e.Lint([]LintInput{
			stackInput(model.LayerUser, stackRule("user-rule", `"when":[{"prior_hit":{"rule":"org-base","gte":1}}],`)),
		}, LintOptions{Stack: true})
		if got := diagKeys(ds); len(got) != 1 || !strings.HasPrefix(got[0], "warning:prior-hit-unknown-rule") {
			t.Errorf("got %v", got)
		}
	})
	t.Run("an undecodable layer does not hide the others", func(t *testing.T) {
		ds := e.Lint([]LintInput{
			{Path: "org.json", Layer: model.LayerOrg, Data: []byte("{nope")},
			stackInput(model.LayerRepo, stackRule("repo-rule", "")),
		}, LintOptions{Stack: true})
		if got := diagKeys(ds); len(got) != 1 || got[0] != "error:schema-invalid" {
			t.Errorf("got %v", got)
		}
	})
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func TestLintVerifyNames(t *testing.T) {
	e := newEngine(t)
	data := ruleFileJSON(stackRule("verify-rule", `"when":[{"verify_fresh":{"names":["unit","nope"]}}],`))
	project := &model.ProjectConfig{Verify: []model.VerifySpec{{Name: "unit", Argv: []string{"go", "test"}, Required: true}}}
	ds := e.Lint([]LintInput{{Path: "p.json", Data: data}}, LintOptions{Project: project})
	if got := diagKeys(ds); len(got) != 1 || got[0] != "warning:verify-name-unknown@/rules/0/when/0/verify_fresh/names/1" {
		t.Errorf("got %v", got)
	}
	if ds := e.Lint([]LintInput{{Path: "p.json", Data: data}}, LintOptions{}); len(ds) != 0 {
		t.Errorf("without a project nothing is checked: %v", diagKeys(ds))
	}
}

func TestLintSortsByFileThenPointer(t *testing.T) {
	e := newEngine(t)
	var rules []string
	for i := 0; i < 12; i++ {
		rules = append(rules, stackRule(fmt.Sprintf("rule-%02d", i), `"action":"nuke",`))
	}
	// duplicate "action" keys are legal JSON; the decoder keeps the last (nuke).
	ds := e.Lint([]LintInput{
		{Path: "b.json", Data: ruleFileJSON(rules...)},
		{Path: "a.json", Data: ruleFileJSON(stackRule("rule-aa", `"event":"Bogus",`))},
	}, LintOptions{})
	if len(ds) < 13 {
		t.Fatalf("diagnostics: %v", diagKeys(ds))
	}
	if ds[0].File != "a.json" {
		t.Errorf("first file %s", ds[0].File)
	}
	var ptrs []string
	for _, d := range ds {
		if d.File == "b.json" {
			ptrs = append(ptrs, d.Pointer)
		}
	}
	want := []string{"/rules/0/action", "/rules/1/action", "/rules/2/action"}
	if strings.Join(ptrs[:3], ",") != strings.Join(want, ",") || ptrs[len(ptrs)-1] != "/rules/11/action" {
		t.Errorf("pointer order %v", ptrs)
	}
}
