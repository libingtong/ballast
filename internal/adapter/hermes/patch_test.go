package hermes

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

func TestParsePatchGolden(t *testing.T) {
	for _, r := range loadPatchRows(t) {
		t.Run(r.Name, func(t *testing.T) {
			got, err := parsePatch(r.Envelope)
			if (err != nil) != r.Malformed {
				t.Fatalf("err = %v, malformed = %v", err, r.Malformed)
			}
			want := patchSummary{Targets: r.Want.Targets, Adds: r.Want.Adds, Deletes: r.Want.Deletes, Moves: r.Want.Moves, Edits: r.Want.Edits}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func FuzzParsePatch(f *testing.F) {
	for _, r := range loadPatchRows(f) {
		f.Add(r.Envelope)
	}
	f.Add("*** Begin Patch\n*** Update File: \x00\n*** Move to: \n+\n-\n*** End Patch")
	f.Fuzz(func(t *testing.T, s string) {
		sum, _ := parsePatch(s)
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

// TestParsePatchHeaderScan pins the Hermes-specific union: a column-0 header
// is a target wherever it sits in the text, because Hermes' own path guard
// scans the whole text while its applier stops at the first strict End line.
func TestParsePatchHeaderScan(t *testing.T) {
	// "*** End Patchx" is not Hermes' End marker (the applier goes on), but the
	// lenient Codex-style copy of the parser stops there.
	env := "*** Begin Patch\n*** Update File: a.go\n-x\n+y\n*** End Patchx\n*** Update File: ~/.ballast/rules.json\n-a\n+b\n*** End Patch\n"
	sum, _ := parsePatch(env)
	if !reflect.DeepEqual(sum.Targets, []string{"a.go", "~/.ballast/rules.json"}) {
		t.Errorf("targets after a look-alike End line: %v", sum.Targets)
	}
	// Headers after the real End marker are merged in too (superset).
	env = "*** Begin Patch\n*** Update File: a.go\n+y\n*** End Patch\n*** Delete File: later.txt\n"
	sum, err := parsePatch(env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sum.Targets, []string{"a.go", "later.txt"}) || !reflect.DeepEqual(sum.Deletes, []string{"later.txt"}) {
		t.Errorf("header after End: %+v", sum)
	}
	// Indented headers are not Hermes headers but the section parse already
	// treats them leniently; the scan must not duplicate them.
	env = "*** Begin Patch\n  *** Update File: a.go\n+y\n*** Update File: a.go\n+z\n*** End Patch\n"
	sum, _ = parsePatch(env)
	if !reflect.DeepEqual(sum.Targets, []string{"a.go"}) {
		t.Errorf("duplicate targets: %v", sum.Targets)
	}
}

func TestParsePatchMoveFile(t *testing.T) {
	sum, err := parsePatch("*** Begin Patch\n*** Move File: old/a.go -> new/b.go\n*** Add File: c.go\n+x\n*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	want := patchSummary{
		Targets: []string{"old/a.go", "new/b.go", "c.go"},
		Adds:    []string{"c.go"},
		Moves:   map[string]string{"old/a.go": "new/b.go"},
		Edits:   []model.FileEdit{{Path: "new/b.go"}, {Path: "c.go", NewString: "x"}},
	}
	if !reflect.DeepEqual(sum, want) {
		t.Errorf("got  %+v\nwant %+v", sum, want)
	}
	// A move without a destination is reported but loses nothing else.
	sum, err = parsePatch("*** Begin Patch\n*** Move File: a.go\n*** Update File: b.go\n+x\n*** End Patch\n")
	if err == nil || !reflect.DeepEqual(sum.Targets, []string{"b.go"}) {
		t.Errorf("move without arrow: %+v err=%v", sum, err)
	}
}
