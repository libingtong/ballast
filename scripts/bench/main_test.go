package main

import (
	"strings"
	"testing"
	"time"
)

func TestRankNearest(t *testing.T) {
	var s []time.Duration
	for i := 1; i <= 300; i++ {
		s = append(s, time.Duration(i)*time.Millisecond)
	}
	// ceil(0.95*300) = 285 -> the 285th smallest sample.
	if got := rank(s, 0.95); got != 285*time.Millisecond {
		t.Errorf("p95 of 1..300ms = %v, want 285ms", got)
	}
	if got := rank(s, 0.5); got != 150*time.Millisecond {
		t.Errorf("p50 = %v, want 150ms", got)
	}
	if got := rank(nil, 0.95); got != 0 {
		t.Errorf("empty = %v", got)
	}
	// n=20: ceil(19.0)=19 -> second largest.
	var s20 []time.Duration
	for i := 1; i <= 20; i++ {
		s20 = append(s20, time.Duration(i)*time.Millisecond)
	}
	if got := rank(s20, 0.95); got != 19*time.Millisecond {
		t.Errorf("n=20 p95 = %v, want 19ms", got)
	}
}

func good() result {
	return result{ID: "x", N: 300, Procs: 1, Want: 300, Samples: 300, P95: 12}
}

// TestGateRequiresAllThree is the M0c audit finding: Bad==0, complete
// samples and p95<=30 must ALL hold; a run whose spawns all failed (no or
// too few samples, or only Bad ones) can never pass.
func TestGateRequiresAllThree(t *testing.T) {
	r := good()
	gate(&r)
	if !r.Pass {
		t.Fatalf("healthy result failed: %v", r.Failures)
	}
	cases := map[string]func(*result){
		"bad run":          func(r *result) { r.Bad = 1 },
		"warm-up bad":      func(r *result) { r.WarmBad = 1 },
		"missing sample":   func(r *result) { r.Samples = 299 },
		"extra sample":     func(r *result) { r.Samples = 301 },
		"no samples":       func(r *result) { r.Samples, r.P95 = 0, 0 },
		"nothing expected": func(r *result) { r.Want, r.Samples, r.P95 = 0, 0, 0 },
		"p95 over budget":  func(r *result) { r.P95 = 30.01 },
		"all spawns fail":  func(r *result) { r.Bad, r.Samples, r.P95 = 300, 0, 0 },
	}
	for name, mut := range cases {
		r := good()
		mut(&r)
		gate(&r)
		if r.Pass {
			t.Errorf("%s: gate passed, want failure", name)
		}
	}
	r = good()
	r.P95 = 30
	gate(&r)
	if !r.Pass {
		t.Errorf("p95 exactly 30ms must pass: %v", r.Failures)
	}
}

func TestScenarioTable(t *testing.T) {
	ids := map[string]bool{}
	clients := map[string]bool{}
	for _, sc := range scenarios() {
		if ids[sc.id] {
			t.Errorf("duplicate scenario id %q", sc.id)
		}
		ids[sc.id] = true
		if sc.payload == nil || sc.client == "" || sc.event == "" {
			t.Errorf("scenario %q is incomplete", sc.id)
		}
		if sc.preTool {
			clients[sc.client] = true
		}
	}
	for _, id := range []string{"1", "2", "3", "4", "5", "6a", "6b", "7", "8", "9", "10", "11"} {
		if !ids[id] {
			t.Errorf("plan B10 scenario %s missing", id)
		}
	}
	for _, c := range requiredClients {
		if !clients[c] {
			t.Errorf("client %s has no PreToolUse scenario", c)
		}
	}
}

func TestCoverage(t *testing.T) {
	all := scenarios()
	var full []result
	for _, sc := range all {
		full = append(full, result{ID: sc.id})
	}
	if p := coverage(all, full, false); len(p) != 0 {
		t.Errorf("complete run reported problems: %v", p)
	}
	if p := coverage(all, full[1:], false); len(p) == 0 {
		t.Error("a run without scenario 1 passed the coverage check")
	}
	var noHermes []result
	for _, r := range full {
		if r.ID != "C-hermes" {
			noHermes = append(noHermes, r)
		}
	}
	if p := coverage(all, noHermes, false); len(p) == 0 {
		t.Error("a run without a hermes PreToolUse scenario passed the coverage check")
	}
	if p := coverage(all, full[:1], true); len(p) != 0 {
		t.Errorf("partial runs are not judged on coverage: %v", p)
	}
}

func TestTwelveRules(t *testing.T) {
	b, err := twelveRules("../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty rule file")
	}
}

func TestOkOutput(t *testing.T) {
	if !okOutput("empty", nil) || okOutput("empty", []byte("x")) {
		t.Error("empty expectation")
	}
	if !okOutput(`"permission":"deny"`, []byte(`{"permission":"deny"}`)) || okOutput("deny", nil) {
		t.Error("substring expectation")
	}
}

// BALLAST_BENCH_P95_MS can only raise the bound; anything else is an error.
func TestParseBudget(t *testing.T) {
	ok := map[string]time.Duration{"": 30 * time.Millisecond, " ": 30 * time.Millisecond, "30": 30 * time.Millisecond, "60": 60 * time.Millisecond, " 200 ": 200 * time.Millisecond, "1000": time.Second}
	for in, want := range ok {
		got, err := parseBudget(in)
		if err != nil || got != want {
			t.Errorf("parseBudget(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"29", "0", "-1", "1001", "report", "30.5", "enforce"} {
		if got, err := parseBudget(in); err == nil {
			t.Errorf("parseBudget(%q) = %v, want an error", in, got)
		}
	}
}

// With a raised bound the gate follows it, and the correctness checks still
// gate the scenario.
func TestGateRaisedBudget(t *testing.T) {
	oldB, oldH := budget, heavyBudget
	defer func() { budget, heavyBudget = oldB, oldH }()
	budget, heavyBudget = 60*time.Millisecond, 60*time.Millisecond
	r := good()
	r.P95 = 51
	gate(&r)
	if !r.Pass {
		t.Errorf("p95 inside the raised bound failed: %v", r.Failures)
	}
	r = good()
	r.P95 = 60.5
	gate(&r)
	if r.Pass {
		t.Error("p95 over the raised bound must fail")
	}
	r = good()
	r.P95, r.Bad = 20, 1
	gate(&r)
	if r.Pass {
		t.Error("Bad runs must still fail")
	}
}

// The whole-work-tree scenarios have their own (higher) bound; every other
// scenario keeps the tight one.
func TestGateHeavyBudget(t *testing.T) {
	oldB, oldH := budget, heavyBudget
	defer func() { budget, heavyBudget = oldB, oldH }()
	budget, heavyBudget = 150*time.Millisecond, 400*time.Millisecond
	r := good()
	r.P95, r.Heavy = 306, true
	gate(&r)
	if !r.Pass {
		t.Errorf("a heavy scenario inside the heavy bound failed: %v", r.Failures)
	}
	r = good()
	r.P95 = 306
	gate(&r)
	if r.Pass {
		t.Error("a light scenario at 306 ms must fail against the 150 ms bound")
	}
	r = good()
	r.P95, r.Heavy = 401, true
	gate(&r)
	if r.Pass {
		t.Error("a heavy scenario over the heavy bound must fail")
	}
	for _, id := range []string{"6c", "6d", "8b", "8c"} {
		if !heavyScenarios[id] {
			t.Errorf("%s should be a heavy scenario", id)
		}
	}
	for _, id := range []string{"1", "5", "6a", "6b", "10"} {
		if heavyScenarios[id] {
			t.Errorf("%s must not be a heavy scenario", id)
		}
	}
}

func TestParseHeavyBound(t *testing.T) {
	got, err := parseBound(BenchP95HeavyMsEnv, "", 80*time.Millisecond)
	if err != nil || got != 80*time.Millisecond {
		t.Errorf("default = %v, %v", got, err)
	}
	if _, err := parseBound(BenchP95HeavyMsEnv, "79", 80*time.Millisecond); err == nil {
		t.Error("a heavy bound below the general one must be an error")
	}
	if got, err := parseBound(BenchP95HeavyMsEnv, "400", 80*time.Millisecond); err != nil || got != 400*time.Millisecond {
		t.Errorf("400 = %v, %v", got, err)
	}
}

func TestParseDegradedPct(t *testing.T) {
	for in, want := range map[string]int{"": 0, " 0 ": 0, "5": 5, "50": 50} {
		got, err := parseDegradedPct(in)
		if err != nil || got != want {
			t.Errorf("parseDegradedPct(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-1", "51", "x", "2.5"} {
		if _, err := parseDegradedPct(in); err == nil {
			t.Errorf("parseDegradedPct(%q) accepted", in)
		}
	}
}

// A run that missed the verdict because the 120ms deadline expired is a
// "degraded" run only in a heavy scenario, only when it exited 0 cleanly and
// was at least as slow as the deadline; anything else stays a plain Bad.
func TestDegradedRun(t *testing.T) {
	slow := oneRun{d: 130 * time.Millisecond}
	if !degradedRun(scenario{id: "6d"}, slow) {
		t.Error("slow clean run of a heavy scenario must count as degraded")
	}
	if degradedRun(scenario{id: "4"}, slow) {
		t.Error("a light scenario never degrades through the deadline")
	}
	if degradedRun(scenario{id: "6d"}, oneRun{d: 20 * time.Millisecond}) {
		t.Error("a fast wrong verdict is a real failure")
	}
	if degradedRun(scenario{id: "6d"}, oneRun{d: 200 * time.Millisecond, code: 1}) || degradedRun(scenario{id: "6d"}, oneRun{d: 200 * time.Millisecond, err: "boom"}) {
		t.Error("a crash or an error exit is a real failure")
	}
}

func TestGateDegradedBound(t *testing.T) {
	old := maxDegradedPct
	defer func() { maxDegradedPct = old }()
	r := good()
	r.Degraded = 1
	maxDegradedPct = 0
	gate(&r)
	if r.Pass {
		t.Error("a degraded run must fail the gate when no share is allowed")
	}
	maxDegradedPct = 5 // 5% of 300 = 15 runs
	r = good()
	r.Degraded = 15
	gate(&r)
	if !r.Pass {
		t.Errorf("15/300 degraded within 5%% failed: %v", r.Failures)
	}
	r = good()
	r.Degraded = 16
	gate(&r)
	if r.Pass {
		t.Error("16/300 degraded exceeds 5%")
	}
}

// A run that passes only under a platform bound must say so and count the
// scenarios above the product gate; a run within 30 ms keeps the plain pass.
func TestConclusionNamesPlatformBound(t *testing.T) {
	oldB, oldH := budget, heavyBudget
	defer func() { budget, heavyBudget = oldB, oldH }()
	budget, heavyBudget = 60*time.Millisecond, 60*time.Millisecond

	a, b, c := good(), good(), good()
	a.ID, b.ID, c.ID = "1", "2", "3"
	b.P95, c.P95 = 45.5, 30.01
	for _, r := range []*result{&a, &b, &c} {
		gate(r)
		if !r.Pass {
			t.Fatalf("scenario %s must pass under the platform bound: %v", r.ID, r.Failures)
		}
	}
	got := conclusion([]result{a, b, c}, true)
	if !strings.Contains(got, "通过平台上限,但 2 个场景未达 30ms 产品门槛") {
		t.Errorf("conclusion = %q", got)
	}
	if ids := overIDs([]result{a, b, c}); len(ids) != 2 || ids[0] != "2" || ids[1] != "3" {
		t.Errorf("overIDs = %v", ids)
	}
	md := markdown(map[string]string{}, []result{a, b, c}, nil, true, true)
	if !strings.Contains(md, "通过平台上限,但 2 个场景未达 30ms 产品门槛") || !strings.Contains(md, "- 2 ") {
		t.Errorf("markdown lacks the platform-bound wording:\n%s", md)
	}

	// Exactly 30 ms is still within the product gate.
	d := good()
	d.P95 = 30
	if len(overProductBudget([]result{d})) != 0 {
		t.Error("p95 == 30ms must not count as over the product gate")
	}
	if got := conclusion([]result{a}, true); !strings.HasPrefix(got, "通过(") {
		t.Errorf("clean run must keep the plain pass: %q", got)
	}
	if got := conclusion([]result{b}, false); got != "未通过" {
		t.Errorf("failed run conclusion = %q", got)
	}
}

func TestOverBudgetWarningOnlyOnGitHubActions(t *testing.T) {
	oldB, oldH := budget, heavyBudget
	defer func() { budget, heavyBudget = oldB, oldH }()
	budget, heavyBudget = 60*time.Millisecond, 60*time.Millisecond

	slow, fast := good(), good()
	slow.ID, slow.Name, slow.P95 = "7", "slow", 41.25
	fast.ID = "8"
	rs := []result{slow, fast}

	t.Setenv("GITHUB_ACTIONS", "")
	if w := overBudgetWarning(rs); w != "" {
		t.Errorf("warning outside GitHub Actions: %q", w)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	w := overBudgetWarning(rs)
	if !strings.HasPrefix(w, "::warning::") || !strings.Contains(w, "7 slow p95=41.25ms") ||
		strings.Contains(w, "8 ") || !strings.Contains(w, "1 个场景未达 30ms 产品门槛") {
		t.Errorf("warning = %q", w)
	}
	if w := overBudgetWarning([]result{fast}); w != "" {
		t.Errorf("warning with nothing over the gate: %q", w)
	}
}
