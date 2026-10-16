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

// TestParsePatchPythonBlanks pins the blank set: Hermes matches headers with
// Python regexes (\s) and strips paths with str.strip(), both of which treat
// U+001C..U+001F (and the Unicode spaces) as whitespace. Go's strings.Fields /
// TrimSpace do not, so a header written with such a character used to reach
// Hermes unseen (no target, hence no path rule could fire).
func TestParsePatchPythonBlanks(t *testing.T) {
	const p = "/h/.ballast/rules.json"
	blanks := []string{"\x1c", "\x1d", "\x1e", "\x1f", " ", "　", " ", "\u0085"}
	var cases []struct{ name, line string }
	for _, b := range blanks {
		cases = append(cases,
			struct{ name, line string }{"after stars", "***" + b + "Update File: " + p},
			struct{ name, line string }{"between Update and File", "*** Update" + b + "File: " + p},
			struct{ name, line string }{"before path", "*** Update File:" + b + p},
			struct{ name, line string }{"around path", "*** Update File:" + b + p + b},
			struct{ name, line string }{"add header", "***" + b + "Add" + b + "File:" + b + p + b},
			struct{ name, line string }{"delete header", "*** Delete" + b + "File: " + p + b},
		)
	}
	for _, c := range cases {
		for _, wrap := range []string{
			"*** Begin Patch\n%s\n@@\n-a\n+b\n*** End Patch",
			"*** Begin Patch\n*** Update File: x.go\n+y\n*** End Patch\n%s\n+b", // after the real End
			"%s\n+b", // no envelope at all
		} {
			env := strings.Replace(wrap, "%s", c.line, 1)
			sum, _ := parsePatch(env)
			if !contains(sum.Targets, p) {
				t.Errorf("%s: %q: targets %q lack %q", c.name, env, sum.Targets, p)
			}
		}
	}
	// Move File with such blanks around the arrow and the paths.
	sum, err := parsePatch("*** Begin Patch\n***\x1fMove\x1cFile:\x1d/a.txt\x1e->\x1f/h/.ballast/rules.json\x1c\n*** End Patch")
	if err != nil || !reflect.DeepEqual(sum.Targets, []string{"/a.txt", p}) {
		t.Errorf("move with python blanks: %+v err=%v", sum, err)
	}
	// Section-parse path (Move to:) is trimmed the same way.
	sum, _ = parsePatch("*** Begin Patch\n*** Update File: a.go\n***\x1cMove to:\x1f/h/.ballast/rules.json\x1c\n+x\n*** End Patch")
	if !contains(sum.Targets, p) {
		t.Errorf("move to with python blanks: %+v", sum)
	}
}

// TestParsePatchHeaderLookalike: a column-0 line that reads like a file
// header once blanks and control characters are ignored, but yields no path,
// is surfaced as a diagnostic (the targets stay as parsed).
func TestParsePatchHeaderLookalike(t *testing.T) {
	for _, line := range []string{
		"*** Update File:",
		"*** Update File:\x1c",
		"***​Update File: /h/.ballast/rules.json", // zero-width space: not blank for Python either
		"***\x00Delete File: /h/x",
	} {
		_, err := parsePatch("*** Begin Patch\n*** Update File: a.go\n+y\n*** End Patch\n" + line + "\n")
		if err == nil || !strings.Contains(err.Error(), "looks like a file header") {
			t.Errorf("%q: want lookalike diagnostic, got %v", line, err)
		}
	}
	// Plain content that merely mentions the words is not flagged.
	if _, err := parsePatch("*** Begin Patch\n*** Update File: a.go\n+*** Update File:\n*** End Patch\n"); err != nil {
		t.Errorf("content line flagged: %v", err)
	}
}
