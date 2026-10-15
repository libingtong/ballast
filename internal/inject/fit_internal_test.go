package inject

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestFitCutKeepsOmittedLine(t *testing.T) {
	ly := layout{sep: "\n", what: "results"}
	items := []string{strings.Repeat("a", 60), "second", "third"}
	omit := "\n…(+2 results omitted)"
	// build(1) = 60 + len(omit) runes does not fit in 50.
	s, n, cut := ly.fitCut(items, 50)
	if !cut || n != 1 || runeLen(s) > 50 || !strings.HasSuffix(s, omit) || !strings.Contains(s, "…\n") {
		t.Errorf("s=%q n=%d cut=%v", s, n, cut)
	}
	// No room for the item next to the omitted line: cut the bare item.
	s, n, cut = ly.fitCut(items, runeLen(omit)+1)
	if !cut || n != 1 || runeLen(s) > runeLen(omit)+1 || strings.Contains(s, "omitted") {
		t.Errorf("tiny budget: s=%q n=%d cut=%v", s, n, cut)
	}
	// A single item has no omitted line.
	s, n, cut = ly.fitCut(items[:1], 20)
	if !cut || n != 1 || runeLen(s) != 20 || !strings.HasSuffix(s, "…") {
		t.Errorf("single: s=%q n=%d cut=%v", s, n, cut)
	}
	// Nothing was cut when everything fits.
	if _, n, cut = ly.fitCut(items, 1000); cut || n != 3 {
		t.Errorf("fit: n=%d cut=%v", n, cut)
	}
}

func TestJobResultsCutLineIsNotReported(t *testing.T) {
	recs := []model.VerifyRecord{
		{JobID: "j1", Name: "first", Outcome: model.OutcomeFail},
		{JobID: "j2", Name: "second", Outcome: model.OutcomeFail},
	}
	line := "verify first: " + string(model.OutcomeFail) + " (job=j1)"
	// Room is smaller than the first line plus the omitted line.
	budget := runeLen(line) + 5
	text, n := jobResults(recs, budget)
	if n != 0 || text == "" || runeLen(text) > budget || !strings.Contains(text, "omitted") {
		t.Errorf("text=%q n=%d", text, n)
	}
	// Plenty of room: both are reported in full.
	if _, n = jobResults(recs, BudgetJobResults); n != 2 {
		t.Errorf("n = %d", n)
	}
	// A line that cannot fit even the whole job budget counts as reported.
	long := []model.VerifyRecord{{JobID: "j3", Name: strings.Repeat("n", 600), Outcome: model.OutcomeFail}}
	if text, n = jobResults(long, BudgetJobResults); n != 1 || runeLen(text) != BudgetJobResults {
		t.Errorf("long: len=%d n=%d", runeLen(text), n)
	}
}
