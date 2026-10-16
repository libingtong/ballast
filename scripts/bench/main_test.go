package main

import (
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
	old := budget
	defer func() { budget = old }()
	budget = 60 * time.Millisecond
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
