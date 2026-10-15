package codex

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

type patchRow struct {
	Name      string `json:"name"`
	Envelope  string `json:"envelope"`
	Malformed bool   `json:"malformed"`
	Want      struct {
		Targets []string          `json:"Targets"`
		Adds    []string          `json:"Adds"`
		Deletes []string          `json:"Deletes"`
		Moves   map[string]string `json:"Moves"`
		Edits   []model.FileEdit  `json:"Edits"`
	} `json:"want"`
}

func loadPatchRows(t testing.TB) []patchRow {
	t.Helper()
	b, err := os.ReadFile("testdata/patches.json")
	if err != nil {
		t.Fatal(err)
	}
	var tbl struct {
		Rows []patchRow `json:"rows"`
	}
	if err := json.Unmarshal(b, &tbl); err != nil {
		t.Fatal(err)
	}
	return tbl.Rows
}

func TestParseApplyPatchGolden(t *testing.T) {
	for _, r := range loadPatchRows(t) {
		t.Run(r.Name, func(t *testing.T) {
			got, err := ParseApplyPatch(r.Envelope)
			if (err != nil) != r.Malformed {
				t.Fatalf("err = %v, malformed = %v", err, r.Malformed)
			}
			want := PatchSummary{Targets: r.Want.Targets, Adds: r.Want.Adds, Deletes: r.Want.Deletes, Moves: r.Want.Moves, Edits: r.Want.Edits}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func FuzzParseApplyPatch(f *testing.F) {
	for _, r := range loadPatchRows(f) {
		f.Add(r.Envelope)
	}
	f.Add("*** Begin Patch\n*** Update File: \x00\n*** Move to: \n+\n-\n*** End Patch")
	f.Fuzz(func(t *testing.T, s string) {
		sum, _ := ParseApplyPatch(s)
		seen := map[string]bool{}
		for _, p := range sum.Targets {
			if p == "" || strings.Contains(p, "\n") || seen[p] {
				t.Fatalf("bad target %q in %v", p, sum.Targets)
			}
			seen[p] = true
		}
		for _, p := range append(append([]string{}, sum.Adds...), sum.Deletes...) {
			if !seen[p] {
				t.Fatalf("%q not in Targets %v", p, sum.Targets)
			}
		}
		for a, b := range sum.Moves {
			if !seen[a] || !seen[b] {
				t.Fatalf("move %q→%q not in Targets", a, b)
			}
		}
	})
}
