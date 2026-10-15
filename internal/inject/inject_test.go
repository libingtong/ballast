package inject_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/model"
)

const gen = "0123456789abcdef"

func threeRules() *model.CompiledRuleSet {
	return ruleSet(gen,
		ruleWith("read-before-edit", model.ActionDeny, "Read the file before editing it.\nSecond line.", "read first"),
		ruleWith("no-tamper", model.ActionWarn, "Do not touch .ballast.", ""),
		ruleWith("silent-rule", model.ActionLog, "", ""),
	)
}

func TestFullTextFormat(t *testing.T) {
	want := "Ballast rules (generation 0123456789abcdef):\n" +
		"- [read-before-edit] Read the file before editing it.\nSecond line.\n" +
		"- [no-tamper] Do not touch .ballast."
	if got := inject.FullText(threeRules()); got != want {
		t.Errorf("FullText:\n%q\nwant\n%q", got, want)
	}
	if got := inject.FullText(ruleSet(gen, ruleWith("only-silent", model.ActionLog, "", ""))); got != "" {
		t.Errorf("no inject text must give no full text, got %q", got)
	}
	if got := inject.FullText(ruleSet(gen)); got != "" {
		t.Errorf("empty rule set: %q", got)
	}
}

func TestAnchorTextFormat(t *testing.T) {
	want := "Ballast: [read-before-edit] read first; [no-tamper] Do not touch .ballast."
	if got := inject.AnchorText(threeRules()); got != want {
		t.Errorf("AnchorText = %q, want %q", got, want)
	}
	if got := inject.AnchorText(ruleSet(gen)); got != "" {
		t.Errorf("empty rule set: %q", got)
	}
}

func TestAnchorDefaultsAndLimits(t *testing.T) {
	long := repeat("字", 120)
	rs := ruleSet(gen,
		ruleWith("first-line", model.ActionLog, "\n\n  headline here  \nbody", ""),
		ruleWith("long-text", model.ActionLog, long, ""),
		ruleWith("long-anchor", model.ActionLog, "text", long),
		ruleWith("crlf-text", model.ActionLog, "one\r\ntwo", ""),
		ruleWith("exactly-80", model.ActionLog, repeat("a", 80), ""),
	)
	got := inject.AnchorText(rs)
	for _, want := range []string{
		"[first-line] headline here;",
		"[long-text] " + repeat("字", 79) + "…;",
		"[long-anchor] " + repeat("字", 79) + "…;",
		"[crlf-text] one;",
		"[exactly-80] " + repeat("a", 80),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("anchor lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "body") || strings.Contains(got, "two") {
		t.Errorf("anchor must use the first line only:\n%s", got)
	}
}

func TestFullTextTruncation(t *testing.T) {
	var rules []model.Rule
	for i := 0; i < 40; i++ {
		rules = append(rules, ruleWith(fmtID("rule", i), model.ActionLog, repeat("x", 395), ""))
	}
	rs := ruleSet(gen, rules...)
	got := inject.FullText(rs)
	if n := rc(got); n > model.BudgetSessionStart {
		t.Fatalf("full text is %d runes", n)
	}
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	kept := len(lines) - 2 // header + omission line
	if want := "…(+" + itoa(40-kept) + " rules omitted)"; last != want {
		t.Errorf("last line = %q, want %q", last, want)
	}
	for _, l := range lines[1 : len(lines)-1] {
		if !strings.HasPrefix(l, "- [rule-") || rc(l) < 400 {
			t.Errorf("a rule line was cut: %q", l)
		}
	}
	if kept < 15 {
		t.Errorf("only %d lines kept; budget not used", kept)
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Replace(mustJSON(n), `"`, "", -1)) }

func TestAnchorTruncationAndOverlongLine(t *testing.T) {
	var rules []model.Rule
	for i := 0; i < 12; i++ {
		rules = append(rules, ruleWith(repeat("i", 40)[:39]+string(rune('a'+i)), model.ActionLog, repeat("字", 400), ""))
	}
	got := inject.AnchorText(ruleSet(gen, rules...))
	if n := rc(got); n > model.BudgetAnchor {
		t.Fatalf("anchor is %d runes", n)
	}
	if !strings.Contains(got, "\n…(+") || !strings.HasSuffix(got, " rules omitted)") {
		t.Errorf("missing omission line:\n%s", got)
	}

}

// A single line longer than the whole budget is cut at a rune boundary.
func TestOverlongSingleLineIsCut(t *testing.T) {
	rs := ruleSet(gen, ruleWith("huge-rule", model.ActionLog, repeat("字", 9000), ""))
	got := inject.FullText(rs)
	if n := rc(got); n != model.BudgetSessionStart || !strings.HasSuffix(got, "字…") {
		t.Errorf("runes=%d tail=%q", n, got[len(got)-8:])
	}
	if !strings.HasPrefix(got, "Ballast rules (generation "+gen+"):\n- [huge-rule] 字") {
		t.Errorf("head = %q", got[:80])
	}
}

func TestGenSummary(t *testing.T) {
	rs := ruleSet(gen,
		ruleWith("kept-same", model.ActionDeny, "t", ""),
		ruleWith("went-up", model.ActionDeny, "t", ""),
		ruleWith("went-down", model.ActionLog, "t", ""),
		ruleWith("brand-new", model.ActionWarn, "t", ""),
	)
	prev := map[string]model.Action{"kept-same": model.ActionDeny, "went-up": model.ActionWarn, "went-down": model.ActionAsk, "zz-gone": model.ActionDeny, "aa-gone": model.ActionLog}
	want := "Ballast rules changed (generation 0123456789abcdef):\n" +
		"~ went-up: warn → deny (promoted)\n" +
		"~ went-down: ask → log (demoted)\n" +
		"+ brand-new: added (warn)\n" +
		"- aa-gone: removed\n" +
		"- zz-gone: removed"
	if got := inject.GenSummary(prev, rs); got != want {
		t.Errorf("GenSummary:\n%s\nwant\n%s", got, want)
	}
	same := map[string]model.Action{"kept-same": model.ActionDeny, "went-up": model.ActionDeny, "went-down": model.ActionLog, "brand-new": model.ActionWarn}
	if got := inject.GenSummary(same, rs); got != "" {
		t.Errorf("no change must give empty summary, got %q", got)
	}
	if got := inject.GenSummary(nil, ruleSet(gen)); got != "" {
		t.Errorf("empty vs empty: %q", got)
	}
	// Everything is "added" against an empty digest.
	if got := inject.GenSummary(nil, rs); strings.Count(got, "added") != 4 {
		t.Errorf("nil digest: %s", got)
	}
}

func TestGenSummaryBudget(t *testing.T) {
	prev := map[string]model.Action{}
	for i := 0; i < 200; i++ {
		prev[repeat("g", 30)+fmtID("", i)] = model.ActionDeny
	}
	got := inject.GenSummary(prev, ruleSet(gen))
	if n := rc(got); n > model.BudgetGenSummary || !strings.Contains(got, "changes omitted)") {
		t.Errorf("%d runes:\n%s", n, got)
	}
}

func TestJobResultsFormatAndBudget(t *testing.T) {
	recs := []model.VerifyRecord{
		{JobID: "j1", Name: "test", Outcome: model.OutcomePass, SEnd: "cf2116c703d8e60883512ee029b93a3c0b7919e4"},
		{JobID: "j2", Name: "lint", Outcome: model.OutcomeFail, SEnd: "abc"},
		{JobID: "j3", Name: "build", Outcome: model.OutcomeInfra},
	}
	want := "verify test: pass (job=j1, S=cf2116c703d8)\nverify lint: fail (job=j2, S=abc)\nverify build: infra (job=j3)"
	if got := inject.JobResults(recs); got != want {
		t.Errorf("JobResults:\n%s\nwant\n%s", got, want)
	}
	if got := inject.JobResults(nil); got != "" {
		t.Errorf("no jobs: %q", got)
	}
	var many []model.VerifyRecord
	for i := 0; i < 50; i++ {
		many = append(many, model.VerifyRecord{JobID: "20261016T010203-" + fmtID("a", i), Name: "suite", Outcome: model.OutcomePass, SEnd: model.TreeID(repeat("a", 40))})
	}
	got := inject.JobResults(many)
	if n := rc(got); n > inject.BudgetJobResults || !strings.Contains(got, "results omitted)") {
		t.Errorf("%d runes:\n%s", n, got)
	}
}

func TestRenderMDC(t *testing.T) {
	got := string(inject.RenderMDC(threeRules()))
	want, err := os.ReadFile("testdata/cursor.mdc.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("RenderMDC:\n%s\nwant\n%s", got, want)
	}
	lines := strings.Split(got, "\n")
	if lines[0] != "---" || lines[3] != "---" || lines[4] != inject.MDCMarker {
		t.Errorf("front matter / marker misplaced: %q", lines[:5])
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Error("file must end with exactly one newline")
	}
	// Without injectable rules the file is still a valid managed file.
	empty := string(inject.RenderMDC(ruleSet(gen)))
	if !strings.Contains(empty, inject.MDCMarker) || strings.Contains(empty, "\nBallast rules (generation") {
		t.Errorf("empty MDC:\n%s", empty)
	}
	// Rules limited to other clients do not reach Cursor.
	r := ruleWith("claude-only", model.ActionLog, "claude text", "")
	r.Clients = []model.ClientID{model.ClientClaude}
	c := ruleWith("cursor-only", model.ActionLog, "cursor text", "")
	c.Clients = []model.ClientID{model.ClientCursor}
	mdc := string(inject.RenderMDC(ruleSet(gen, r, c)))
	if strings.Contains(mdc, "claude text") || !strings.Contains(mdc, "cursor text") {
		t.Errorf("client filter:\n%s", mdc)
	}
}

func TestDigest(t *testing.T) {
	got := inject.Digest(threeRules())
	want := map[string]model.Action{"read-before-edit": model.ActionDeny, "no-tamper": model.ActionWarn, "silent-rule": model.ActionLog}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Digest = %v", got)
	}
}
