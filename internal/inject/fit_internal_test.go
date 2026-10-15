package inject

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestFitCutNeverCutsAFittingLine(t *testing.T) {
	ly := layout{sep: "\n", what: "results"}
	// The first line fits alone but not with the omitted line: keep it whole.
	items := []string{strings.Repeat("a", 390), "short"}
	s, n, cut := ly.fitCut(items, 395)
	if cut || n != 1 || s != items[0] {
		t.Errorf("fitting first line: len=%d n=%d cut=%v", runeLen(s), n, cut)
	}
	r30 := strings.Repeat("r", 30)
	// Whole items are dropped from the end behind an omitted line when it fits.
	s, n, cut = ly.fitCut([]string{r30, r30, r30}, 2*30+1+runeLen("\n…(+1 results omitted)"))
	if cut || n != 2 || !strings.HasSuffix(s, "\n…(+1 results omitted)") {
		t.Errorf("drop tail: s=%q n=%d cut=%v", s, n, cut)
	}
	// A first line longer than the budget is cut at a rune boundary.
	long := []string{strings.Repeat("a", 60), "second"}
	s, n, cut = ly.fitCut(long, 50)
	if !cut || n != 1 || runeLen(s) != 50 || !strings.HasSuffix(s, "…") || strings.Contains(s, "omitted") {
		t.Errorf("over-long: s=%q n=%d cut=%v", s, n, cut)
	}
	// A single item has no omitted line.
	s, n, cut = ly.fitCut(long[:1], 20)
	if !cut || n != 1 || runeLen(s) != 20 || !strings.HasSuffix(s, "…") {
		t.Errorf("single: s=%q n=%d cut=%v", s, n, cut)
	}
	// Nothing was cut when everything fits.
	if _, n, cut = ly.fitCut(append(long, "third"), 1000); cut || n != 3 {
		t.Errorf("fit: n=%d cut=%v", n, cut)
	}
}

func TestJobResultsKeepsFittingLineWhole(t *testing.T) {
	recs := []model.VerifyRecord{
		{JobID: "j1", Name: "first", Outcome: model.OutcomeFail},
		{JobID: "j2", Name: "second", Outcome: model.OutcomeFail},
	}
	line := "verify first: " + string(model.OutcomeFail) + " (job=j1)"
	// Room is smaller than the first line plus the omitted line: the first
	// line is reported whole and the second is left for next time.
	budget := runeLen(line) + 5
	text, n := jobResults(recs, budget)
	if n != 1 || text != line {
		t.Errorf("text=%q n=%d", text, n)
	}
	// Plenty of room: both are reported in full.
	if _, n = jobResults(recs, BudgetJobResults); n != 2 {
		t.Errorf("n = %d", n)
	}
	// A 378-rune line next to a short one is never truncated.
	wide := []model.VerifyRecord{
		{JobID: "j4", Name: strings.Repeat("n", 360), Outcome: model.OutcomeFail},
		{JobID: "j5", Name: "x", Outcome: model.OutcomeFail},
	}
	text, n = jobResults(wide, 400)
	if n != 1 || strings.Contains(text, "…") || !strings.Contains(text, "job=j4)") {
		t.Errorf("wide: len=%d n=%d", runeLen(text), n)
	}
	// A line that cannot fit even the whole job budget counts as reported.
	long := []model.VerifyRecord{{JobID: "j3", Name: strings.Repeat("n", 600), Outcome: model.OutcomeFail}}
	if text, n = jobResults(long, BudgetJobResults); n != 1 || runeLen(text) != BudgetJobResults {
		t.Errorf("long: len=%d n=%d", runeLen(text), n)
	}
}
