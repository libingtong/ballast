package config_test

import (
	"sort"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func twelveRules() []spec {
	var s []spec
	for i := 0; i < 12; i++ {
		s = append(s, on("rule-"+string(rune('a'+i))+"-id"))
	}
	return s
}

// A cold load (no cache) of 12 rules must stay under 1ms; the budget is not
// asserted under -race, which slows everything several-fold.
func TestColdLoadTwelveRulesUnder1ms(t *testing.T) {
	e := newEnv(t)
	e.opt.NoCache = true
	e.write(model.LayerUser, rulesJSON(`{"inject":{"anchor_every":2}}`, twelveRules()...))
	l := e.loader()
	var d []time.Duration
	for i := 0; i < 300; i++ {
		start := time.Now()
		rs, err := l.Load(e.req())
		d = append(d, time.Since(start))
		if err != nil || len(rs.Rules) != 12 {
			t.Fatalf("err=%v rules=%d", err, len(rs.Rules))
		}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	median := d[len(d)/2]
	t.Logf("cold Load of 12 rules: median %v, p95 %v", median, d[len(d)*95/100])
	if !raceEnabled && median > time.Millisecond {
		t.Errorf("median %v exceeds 1ms", median)
	}
}

func BenchmarkLoadCold12(b *testing.B) {
	e := newEnv(b)
	e.opt.NoCache = true
	e.write(model.LayerUser, rulesJSON("", twelveRules()...))
	l := e.loader()
	req := e.req()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Load(req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLoadCached12(b *testing.B) {
	e := newEnv(b)
	e.write(model.LayerUser, rulesJSON("", twelveRules()...))
	l := e.loader()
	req := e.req()
	if _, err := l.Load(req); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Load(req); err != nil {
			b.Fatal(err)
		}
	}
}
