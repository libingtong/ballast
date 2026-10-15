package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// parityFile mirrors testdata/pinrule-parity.json: one entry per pinrule test
// (tests/test_checks.py, tests/test_bypass_pinrule.py) mapped to the Ballast
// rules that replace the pinrule check, with the expected result of each rule.
type parityFile struct {
	Version       int      `json:"version"`
	PinruleTests  []string `json:"pinrule_tests"`
	NotApplicable []struct {
		Test   string `json:"test"`
		Reason string `json:"reason"`
	} `json:"not_applicable"`
	Cases []parityCase `json:"cases"`
}

type parityCase struct {
	ID      string          `json:"id"`
	Test    string          `json:"test"`
	Name    string          `json:"name"`
	Pinrule string          `json:"pinrule"`
	Input   json.RawMessage `json:"input"`
	Fixture json.RawMessage `json:"fixture"`
	Checks  []struct {
		Rule   string `json:"rule"`
		Expect string `json:"expect"`
	} `json:"checks"`
	Ballast    string `json:"ballast"`
	Divergence string `json:"divergence"`
}

// TestPinruleParity is the plan's first completion condition: the comparison
// cases mapped by hand from pinrule's tests run through the real DSL engine,
// and a result differing from pinrule's must cite a registered "有意改变" or
// "舍弃" row of docs/pinrule-mapping.md.
//
// The pinrule sources are not part of this repository. When PINRULE_SRC points
// at a pinrule checkout the list of test names is cross-checked against it;
// without it the test only checks the file against itself.
func TestPinruleParity(t *testing.T) {
	var pf parityFile
	data, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "pinrule-parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("pinrule-parity.json: %v", err)
	}
	if pf.Version != 1 || len(pf.PinruleTests) < 100 || len(pf.Cases) < 150 {
		t.Fatalf("pinrule-parity.json looks truncated: version %d, %d tests, %d cases", pf.Version, len(pf.PinruleTests), len(pf.Cases))
	}

	// 1. Every pinrule test is mapped (a case) or explicitly not applicable.
	covered := map[string]bool{}
	ids := map[string]bool{}
	for _, c := range pf.Cases {
		if ids[c.ID] {
			t.Errorf("duplicate case id %s", c.ID)
		}
		ids[c.ID] = true
		covered[c.Test] = true
	}
	for _, n := range pf.NotApplicable {
		if n.Reason == "" {
			t.Errorf("%s: not_applicable needs a reason", n.Test)
		}
		if covered[n.Test] {
			t.Errorf("%s is both mapped and not_applicable", n.Test)
		}
		covered[n.Test] = true
	}
	listed := map[string]bool{}
	for _, name := range pf.PinruleTests {
		if listed[name] {
			t.Errorf("pinrule_tests lists %s twice", name)
		}
		listed[name] = true
		if !covered[name] {
			t.Errorf("pinrule test %s is neither mapped nor not_applicable", name)
		}
	}
	for name := range covered {
		if !listed[name] {
			t.Errorf("%s is mapped but not in pinrule_tests", name)
		}
	}
	if src := os.Getenv("PINRULE_SRC"); src != "" {
		checkAgainstPinruleSource(t, src, listed)
	}

	// 2. Divergences cite a section of pinrule-mapping.md that exists.
	mapping, err := os.ReadFile(filepath.Join(repoRoot, "docs", "pinrule-mapping.md"))
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^#{2,3} (\d+(?:\.\d+)?) `).FindAllStringSubmatch(string(mapping), -1) {
		sections[m[1]] = true
	}
	refRE := regexp.MustCompile(`§(\d+(?:\.\d+)?)`)
	for _, c := range pf.Cases {
		if len(c.Checks) == 0 {
			t.Errorf("%s: no Ballast rule is checked", c.ID)
		}
		sum := "miss"
		for _, k := range c.Checks {
			if k.Expect == "hit" {
				sum = "hit"
			}
		}
		if sum != c.Ballast {
			t.Errorf("%s: ballast=%s but the checks add up to %s", c.ID, c.Ballast, sum)
		}
		switch {
		case c.Pinrule == c.Ballast && c.Divergence != "":
			t.Errorf("%s: pinrule and Ballast agree, the divergence text is stale", c.ID)
		case c.Pinrule != c.Ballast:
			m := refRE.FindStringSubmatch(c.Divergence)
			if !strings.Contains(c.Divergence, "pinrule-mapping.md") || m == nil {
				t.Errorf("%s (%s): Ballast differs from pinrule but cites no pinrule-mapping.md section: %q", c.ID, c.Name, c.Divergence)
			} else if !sections[m[1]] {
				t.Errorf("%s: pinrule-mapping.md has no section %s", c.ID, m[1])
			}
		}
	}

	// 3. Run every case through the engine, one temporary rule file per rule:
	// the rule itself (with its own examples, which satisfy the hit/miss
	// minimum) plus the parity cases as extra examples. test-rules forces the
	// action to deny, so the result is the pure match outcome.
	byRule := map[string][]int{}
	for i, c := range pf.Cases {
		for _, k := range c.Checks {
			byRule[k.Rule] = append(byRule[k.Rule], i)
		}
	}
	rules := make([]string, 0, len(byRule))
	for r := range byRule {
		rules = append(rules, r)
	}
	sort.Strings(rules)

	s := newSandbox(t)
	got := map[string]string{} // case id + rule -> hit|miss|unknown
	for _, ref := range rules {
		file, id, ok := strings.Cut(ref, "#")
		if !ok {
			t.Fatalf("bad rule reference %q", ref)
		}
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Rules []map[string]json.RawMessage `json:"rules"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var rule map[string]json.RawMessage
		for _, r := range doc.Rules {
			var rid string
			_ = json.Unmarshal(r["id"], &rid)
			if rid == id {
				rule = r
			}
		}
		if rule == nil {
			t.Fatalf("%s has no rule %s", file, id)
		}
		var examples []json.RawMessage
		if err := json.Unmarshal(rule["examples"], &examples); err != nil {
			t.Fatal(err)
		}
		for _, i := range byRule[ref] {
			c := pf.Cases[i]
			ex := map[string]any{"name": c.ID, "expect": "hit", "input": c.Input}
			if len(c.Fixture) > 0 {
				ex["fixture"] = c.Fixture
			}
			b, err := json.Marshal(ex)
			if err != nil {
				t.Fatal(err)
			}
			examples = append(examples, b)
		}
		rule["examples"], _ = json.Marshal(examples)
		out, err := json.Marshal(map[string]any{"version": 1, "rules": []any{rule}})
		if err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(s.sb.Root, "parity-"+strings.NewReplacer("/", "_", "#", "_").Replace(ref)+".json")
		writeFile(t, tmp, string(out))
		r := s.run("", "", "test-rules", "--json", tmp)
		var rep struct {
			Cases []struct {
				Example string `json:"example"`
				Got     int    `json:"got"`
				Pass    bool   `json:"pass"`
			} `json:"cases"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
			t.Fatalf("%s: test-rules output: %v\n%s%s", ref, err, r.stdout, r.stderr)
		}
		ran := 0
		for _, tc := range rep.Cases {
			if !ids[tc.Example] {
				// One of the rule's own examples: it must keep passing.
				if !tc.Pass {
					t.Errorf("%s: own example %q fails", ref, tc.Example)
				}
				continue
			}
			ran++
			// Tri: 0 unknown, 1 false, 2 true.
			got[tc.Example+" "+ref] = map[int]string{0: "unknown", 1: "miss", 2: "hit"}[tc.Got]
		}
		if ran != len(byRule[ref]) {
			t.Errorf("%s: %d parity examples ran, want %d\n%s", ref, ran, len(byRule[ref]), r.stdout)
		}
	}

	fail := 0
	for _, c := range pf.Cases {
		for _, k := range c.Checks {
			if g := got[c.ID+" "+k.Rule]; g != k.Expect {
				fail++
				t.Errorf("%s (%s) [%s] %s: engine says %s, parity file expects %s", c.ID, c.Test, c.Name, k.Rule, g, k.Expect)
			}
		}
	}
	if fail == 0 {
		t.Logf("pinrule parity: %d cases (%d with a registered divergence), %d tests not applicable", len(pf.Cases), countDivergent(pf.Cases), len(pf.NotApplicable))
	}
}

func countDivergent(cs []parityCase) int {
	n := 0
	for _, c := range cs {
		if c.Pinrule != c.Ballast {
			n++
		}
	}
	return n
}

// checkAgainstPinruleSource compares the recorded test names with a pinrule
// checkout (PINRULE_SRC), so the file cannot silently fall behind.
func checkAgainstPinruleSource(t *testing.T, src string, listed map[string]bool) {
	t.Helper()
	want := map[string]bool{}
	re := regexp.MustCompile(`(?m)^def (test_\w+)`)
	for _, f := range []string{"test_checks.py", "test_bypass_pinrule.py"} {
		b, err := os.ReadFile(filepath.Join(src, "tests", f))
		if err != nil {
			t.Fatalf("PINRULE_SRC: %v", err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			want[f+"::"+m[1]] = true
		}
	}
	for n := range want {
		if !listed[n] {
			t.Errorf("PINRULE_SRC has %s, missing from pinrule_tests", n)
		}
	}
	for n := range listed {
		if !want[n] {
			t.Errorf("pinrule_tests lists %s, absent from PINRULE_SRC", n)
		}
	}
}
