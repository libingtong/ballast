package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

var update = flag.Bool("update", false, "rewrite testdata/report.golden.json")

var fixedNow = time.Date(2026, 10, 16, 9, 41, 17, 123, time.UTC)

// sliceSource is an in-memory ports.AuditSource.
type sliceSource struct {
	recs    []model.AuditRecord
	corrupt int
	err     error
}

func (s *sliceSource) Records(since, until time.Time, fn func(*model.AuditRecord) error) (int, error) {
	for i := range s.recs {
		r := &s.recs[i]
		if !since.IsZero() && r.TS.Before(since) {
			continue
		}
		if !until.IsZero() && !r.TS.Before(until) {
			continue
		}
		if err := fn(r); err != nil {
			return s.corrupt, err
		}
	}
	return s.corrupt, s.err
}

func shippedIDs(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "rules", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	opt, _ := filepath.Glob(filepath.Join("..", "..", "rules", "optional", "*.json"))
	files = append(files, opt...)
	if len(files) < 2 {
		t.Fatalf("rule packs not found: %v", files)
	}
	var ids []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var pack struct {
			Rules []struct {
				ID string `json:"id"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(data, &pack); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, r := range pack.Rules {
			ids = append(ids, r.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func buildOpts(src *sliceSource) Options {
	return Options{
		Audit:          src,
		Clock:          sys.NewFakeClock(fixedNow),
		BallastVersion: "2.0.0-test",
		GOOS:           "linux",
	}
}

func rec(typ model.AuditType, ts string, mod func(*model.AuditRecord)) model.AuditRecord {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	r := model.AuditRecord{TS: t, V: 1, Type: typ}
	if mod != nil {
		mod(&r)
	}
	return r
}

func diagRec(detail string) model.AuditRecord {
	return rec(model.AuditDiag, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) {
		if detail != "" {
			r.Detail = json.RawMessage(detail)
		}
	})
}

func TestGolden(t *testing.T) {
	rd := &audit.Reader{Dir: filepath.Join("testdata", "audit")}
	rep, err := Build(Options{
		Audit:          rd,
		Clock:          sys.NewFakeClock(fixedNow),
		Since:          time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		ShippedRuleIDs: shippedIDs(t),
		BallastVersion: "2.0.0-golden",
		GOOS:           "linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := Validate(rep); len(v) > 0 {
		t.Fatalf("Validate: %v", v)
	}
	got, err := Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "report.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("report differs from %s (run with -update after review)\n--- got ---\n%s", golden, got)
	}

	// The fixture is full of paths, ids, hostnames and a planted token; none
	// of it may be in the golden file.
	for _, leak := range []string{
		"alice", "bob", "/Users", "/home", "<home>", ".ballast/logs", "toolu_", "s-aaaa", "s-bbbb", "s-old", "repo-aaaa", "repo-bbbb",
		"0123456789abcdef", "ghp_", "Ab12Cd34", "internal.example.com", "secret-project", "my-private-note",
		"acme-no-prod-deploy", "brand_new_name", "mystery-client", "settings.json", "job-1", "weird",
		"abc.jsonl", "robot", "docker",
	} {
		if bytes.Contains(got, []byte(leak)) {
			t.Errorf("golden report leaks %q", leak)
		}
	}
	// Spot checks that the golden is not trivially empty.
	if rep.Sessions != 4 || rep.Repos != 2 || rep.Corrupt != 1 {
		t.Errorf("sessions/repos/corrupt = %d/%d/%d, want 4/2/1", rep.Sessions, rep.Repos, rep.Corrupt)
	}
	if rep.Redactions[string(kindBearerOrAuth(rep))] == 0 {
		t.Errorf("no redaction counted: %v", rep.Redactions)
	}
}

// kindBearerOrAuth returns whichever of the kinds the fixture's
// Authorization header is classified as.
func kindBearerOrAuth(r *Report) string {
	for _, k := range []string{"auth_header", "bearer"} {
		if r.Redactions[k] > 0 {
			return k
		}
	}
	return "auth_header"
}

func TestGoldenWindow(t *testing.T) {
	rd := &audit.Reader{Dir: filepath.Join("testdata", "audit")}
	rep, err := Build(Options{Audit: rd, Clock: sys.NewFakeClock(fixedNow), ShippedRuleIDs: shippedIDs(t)})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); !rep.WindowStart.Equal(want) {
		t.Errorf("unbounded window starts %v, want first record's day %v", rep.WindowStart, want)
	}
	if want := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC); !rep.WindowEnd.Equal(want) {
		t.Errorf("window end %v, want %v", rep.WindowEnd, want)
	}
	if want := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC); !rep.GeneratedAt.Equal(want) {
		t.Errorf("generated_at %v, want %v", rep.GeneratedAt, want)
	}
	if rep.Sessions != 5 || rep.Repos != 3 {
		t.Errorf("sessions/repos = %d/%d, want 5/3 (older file included)", rep.Sessions, rep.Repos)
	}
}

func TestDiagNamesRoundTrip(t *testing.T) {
	if len(model.DiagNames) != 18 {
		t.Fatalf("model.DiagNames has %d names; update this test if the contract grew", len(model.DiagNames))
	}
	for _, name := range model.DiagNames {
		t.Run(name, func(t *testing.T) {
			src := &sliceSource{recs: []model.AuditRecord{
				diagRec(fmt.Sprintf(`{"diag":%q,"wait_us":5}`, name)),
				diagRec(fmt.Sprintf(`{"diag":%q}`, name)),
			}}
			rep, err := Build(buildOpts(src))
			if err != nil {
				t.Fatal(err)
			}
			if rep.Diags[name] != 2 || len(rep.Diags) != 1 {
				t.Fatalf("Diags = %v, want {%s:2}", rep.Diags, name)
			}
			if v := Validate(rep); len(v) > 0 {
				t.Fatalf("Validate: %v", v)
			}
		})
	}
}

func TestDiagUnknownGoesToOther(t *testing.T) {
	cases := []struct{ name, detail string }{
		{"unknown name", `{"diag":"totally_new_diag"}`},
		{"unknown with path", `{"diag":"x /home/bob/.ssh/id_rsa"}`},
		{"unknown with secret", `{"diag":"ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78"}`},
		{"case differs", `{"diag":"Lock_Timeout"}`},
		{"trailing space", `{"diag":"lock_timeout "}`},
		{"empty name", `{"diag":""}`},
		{"number", `{"diag":42}`},
		{"bool", `{"diag":true}`},
		{"null", `{"diag":null}`},
		{"array", `{"diag":["lock_timeout"]}`},
		{"object", `{"diag":{"diag":"lock_timeout"}}`},
		{"missing key", `{"other":"lock_timeout"}`},
		{"not an object", `"lock_timeout"`},
		{"malformed", `{"diag":`},
		{"no detail", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := Build(buildOpts(&sliceSource{recs: []model.AuditRecord{diagRec(tc.detail)}}))
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Diags) != 1 || rep.Diags[DiagOther] != 1 {
				t.Fatalf("Diags = %v, want {other:1}", rep.Diags)
			}
			out, _ := Marshal(rep)
			for _, leak := range []string{"totally_new", "bob", "ghp_", "Lock_Timeout", "id_rsa"} {
				if bytes.Contains(out, []byte(leak)) {
					t.Fatalf("report prints %q", leak)
				}
			}
		})
	}
}

func TestRuleIDs(t *testing.T) {
	src := &sliceSource{recs: []model.AuditRecord{
		rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) {
			r.RuleID = "no-long-sleep"
			r.Decision = "deny"
			r.Action = model.ActionDeny
		}),
		rec(model.AuditHit, "2026-10-10T00:00:01Z", func(r *model.AuditRecord) { r.RuleID = "my-secret-rule"; r.Decision = "warn" }),
		rec(model.AuditHit, "2026-10-10T00:00:02Z", func(r *model.AuditRecord) { r.RuleID = "No-Long-Sleep"; r.Decision = "warn" }),
		rec(model.AuditHit, "2026-10-10T00:00:03Z", func(r *model.AuditRecord) { r.RuleID = "evil/../path"; r.Decision = "warn" }),
	}}
	opt := buildOpts(src)
	opt.ShippedRuleIDs = []string{"no-long-sleep", "Bad/Shipped", "ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78"}
	rep, err := Build(opt)
	if err != nil {
		t.Fatal(err)
	}
	h := func(id string) string {
		s := sha256.Sum256([]byte(id))
		return "h:" + hex.EncodeToString(s[:])[:12]
	}
	got := map[string]int{}
	for _, r := range rep.Rules {
		got[r.Rule] = r.Hits
	}
	want := map[string]int{
		"no-long-sleep":     1, // shipped: verbatim
		h("my-secret-rule"): 1,
		h("No-Long-Sleep"):  1, // case differs: not the shipped id
		h("evil/../path"):   1,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	if HashRuleID("my-secret-rule") != h("my-secret-rule") {
		t.Fatal("HashRuleID differs from sha256[:12]")
	}
	out, _ := Marshal(rep)
	for _, leak := range []string{"my-secret-rule", "evil", "No-Long", "Bad/Shipped", "ghp_"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Errorf("report prints %q", leak)
		}
	}
	if !sort.SliceIsSorted(rep.Rules, func(i, j int) bool { return rep.Rules[i].Rule < rep.Rules[j].Rule }) {
		t.Error("rules not sorted")
	}
}

func TestRuleAggregationMatchesStatsSemantics(t *testing.T) {
	src := &sliceSource{recs: []model.AuditRecord{
		rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) {
			r.RuleID, r.SessionID, r.Decision = "no-long-sleep", "s1", "deny"
			r.RuleLayer, r.Action, r.Target = model.LayerUser, model.ActionLog, model.ActionDeny
		}),
		rec(model.AuditHit, "2026-10-10T00:00:01Z", func(r *model.AuditRecord) {
			r.RuleID, r.SessionID, r.Decision = "no-long-sleep", "s2", "would_deny"
			r.RuleLayer, r.Action, r.Target = model.LayerUser, model.ActionDeny, model.ActionDeny
		}),
		rec(model.AuditHit, "2026-10-10T00:00:02Z", func(r *model.AuditRecord) { r.RuleID, r.Decision = "no-long-sleep", "log" }),
		rec(model.AuditEvalSummary, "2026-10-10T00:00:03Z", func(r *model.AuditRecord) {
			r.Evals = map[string]model.EvalCount{"no-long-sleep": {Evals: 10, Unknown: 2}, "": {Evals: 99}}
		}),
		rec(model.AuditEvalSummary, "2026-10-10T00:00:04Z", func(r *model.AuditRecord) {
			r.Evals = map[string]model.EvalCount{"no-long-sleep": {Evals: 5, Unknown: 1}}
		}),
	}}
	opt := buildOpts(src)
	opt.ShippedRuleIDs = []string{"no-long-sleep"}
	rep, err := Build(opt)
	if err != nil {
		t.Fatal(err)
	}
	want := RuleAgg{Rule: "no-long-sleep", Layer: model.LayerUser, Action: model.ActionDeny, Target: model.ActionDeny,
		Evals: 15, Hits: 1, Would: 1, Unknown: 3, Sessions: 2}
	if len(rep.Rules) != 1 || rep.Rules[0] != want {
		t.Fatalf("rules = %+v, want [%+v]", rep.Rules, want)
	}
}

func TestEnumBucketing(t *testing.T) {
	src := &sliceSource{recs: []model.AuditRecord{
		rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) { r.Client = "claude-code"; r.Tier = model.TierT1 }),
		rec(model.AuditHit, "2026-10-10T00:00:01Z", func(r *model.AuditRecord) { r.Client = "bob's laptop"; r.Tier = "T9" }),
		rec(model.AuditHit, "2026-10-10T00:00:02Z", func(r *model.AuditRecord) { r.Client = "bob's laptop2" }),
		rec(model.AuditVerify, "2026-10-10T00:00:03Z", func(r *model.AuditRecord) {
			r.Verify = &model.VerifyRecord{Outcome: "pass", Tier: model.TierT1, Sandbox: "bwrap", Origin: model.OriginHook}
		}),
		rec(model.AuditVerify, "2026-10-10T00:00:04Z", func(r *model.AuditRecord) {
			r.Verify = &model.VerifyRecord{Outcome: "/etc/passwd", Tier: "weird", Sandbox: "sandbox-exec /x", Origin: "alice"}
		}),
		rec(model.AuditVerify, "2026-10-10T00:00:05Z", func(r *model.AuditRecord) {
			r.Verify = &model.VerifyRecord{Outcome: model.OutcomeFail, Tier: model.TierT0} // pre-P2 record
		}),
		rec(model.AuditVerify, "2026-10-10T00:00:06Z", nil), // verify without payload
	}}
	rep, err := Build(buildOpts(src))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Clients["claude-code"] != 1 || rep.Clients[other] != 2 || len(rep.Clients) != 2 {
		t.Errorf("clients = %v", rep.Clients)
	}
	// Verify records without their own tier field count by VerifyRecord.Tier.
	if rep.Tiers[model.TierT1] != 2 || rep.Tiers[other] != 2 || rep.Tiers[model.TierT0] != 1 {
		t.Errorf("tiers = %v", rep.Tiers)
	}
	if rep.Verify.Outcomes[model.OutcomePass] != 1 || rep.Verify.Outcomes[model.OutcomeFail] != 1 || rep.Verify.Outcomes[other] != 1 {
		t.Errorf("outcomes = %v", rep.Verify.Outcomes)
	}
	if fmt.Sprint(rep.Verify.BySandbox) != "map[bwrap:1 none:1 other:1]" {
		t.Errorf("by_sandbox = %v", rep.Verify.BySandbox)
	}
	if fmt.Sprint(rep.Verify.ByOrigin) != "map[cli:1 hook:1 other:1]" {
		t.Errorf("by_origin = %v", rep.Verify.ByOrigin)
	}
	if v := Validate(rep); len(v) > 0 {
		t.Errorf("Validate: %v", v)
	}
}

func TestLatencyHistogram(t *testing.T) {
	var recs []model.AuditRecord
	add := func(us int64, n int) {
		for i := 0; i < n; i++ {
			recs = append(recs, rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) { r.LatencyUS = us }))
		}
	}
	add(0, 5)       // not recorded
	add(1000, 10)   // 1ms edge → bucket 0
	add(1001, 10)   // → 2ms
	add(30000, 60)  // 30ms edge → bucket 5
	add(30001, 10)  // → 50ms
	add(500000, 10) // overflow
	rep, err := Build(buildOpts(&sliceSource{recs: recs}))
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 10, 0, 0, 0, 60, 10, 0, 10}
	if fmt.Sprint(rep.Latency.Counts) != fmt.Sprint(want) {
		t.Fatalf("counts = %v, want %v", rep.Latency.Counts, want)
	}
	// 100 samples, 95th = sample 95 → in the 50ms bucket (cum 90 < 95 ≤ 100? no: 10+10+60+10=90, then overflow)
	if rep.Latency.P95MS != 100 {
		t.Errorf("p95 = %v, want 100 (overflow reports the last bound)", rep.Latency.P95MS)
	}
	if len(rep.Latency.BucketsMS) != 8 || rep.Latency.BucketsMS[7] != 100 {
		t.Errorf("buckets = %v", rep.Latency.BucketsMS)
	}
	rep2, _ := Build(buildOpts(&sliceSource{recs: recs[:35]})) // 5 zero + 10 + 10 + 10 of the 60
	if rep2.Latency.P95MS != 30 {
		t.Errorf("p95 = %v, want 30", rep2.Latency.P95MS)
	}
	empty, _ := Build(buildOpts(&sliceSource{}))
	if empty.Latency.P95MS != 0 || len(empty.Latency.Counts) != 9 {
		t.Errorf("empty latency = %+v", empty.Latency)
	}
}

func TestBuildErrorsAndEmpty(t *testing.T) {
	if _, err := Build(Options{}); err == nil {
		t.Error("nil audit source accepted")
	}
	boom := errors.New("disk on fire /Users/alice")
	if _, err := Build(buildOpts(&sliceSource{recs: []model.AuditRecord{diagRec("")}, err: boom})); !errors.Is(err, boom) {
		t.Errorf("source error not propagated: %v", err)
	}
	rep, err := Build(buildOpts(&sliceSource{corrupt: 7}))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Corrupt != 7 || rep.Sessions != 0 || len(rep.Rules) != 0 {
		t.Errorf("empty report = %+v", rep)
	}
	if v := Validate(rep); len(v) > 0 {
		t.Errorf("empty report invalid: %v", v)
	}
	out, _ := Marshal(rep)
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back["rules"] == nil || back["diags"] == nil {
		t.Errorf("empty collections must serialize as {} / []: %s", out)
	}
}

func TestBuildUntilAndSince(t *testing.T) {
	src := &sliceSource{recs: []model.AuditRecord{
		rec(model.AuditHit, "2026-10-01T00:00:00Z", func(r *model.AuditRecord) { r.SessionID = "a" }),
		rec(model.AuditHit, "2026-10-05T00:00:00Z", func(r *model.AuditRecord) { r.SessionID = "b" }),
		rec(model.AuditHit, "2026-10-09T00:00:00Z", func(r *model.AuditRecord) { r.SessionID = "c" }),
	}}
	opt := buildOpts(src)
	opt.Since = time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	opt.Until = time.Date(2026, 10, 8, 5, 0, 0, 0, time.UTC)
	rep, err := Build(opt)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sessions != 1 {
		t.Errorf("sessions = %d, want 1", rep.Sessions)
	}
	if !rep.WindowStart.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) || !rep.WindowEnd.Equal(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("window = %v .. %v", rep.WindowStart, rep.WindowEnd)
	}
}

func TestRedactionCounts(t *testing.T) {
	tok := "ghp_" + "Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78"
	src := &sliceSource{recs: []model.AuditRecord{
		rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) { r.Matched = "export GITHUB=" + tok }),
		rec(model.AuditHit, "2026-10-10T00:00:01Z", func(r *model.AuditRecord) { r.Matched = "curl --password ***" }),
		rec(model.AuditDiag, "2026-10-10T00:00:02Z", func(r *model.AuditRecord) {
			r.Detail = json.RawMessage(`{"diag":"lock_timeout","note":{"deep":["` + tok + `"]}}`)
		}),
		rec(model.AuditHit, "2026-10-10T00:00:03Z", func(r *model.AuditRecord) { r.Matched = "nothing here" }),
	}}
	rep, err := Build(buildOpts(src))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for k, n := range rep.Redactions {
		if !in(kindSet, k) {
			t.Errorf("unknown kind %q", k)
		}
		total += n
	}
	if total < 3 {
		t.Errorf("redactions = %v, want at least 3 spans", rep.Redactions)
	}
	if rep.Redactions[string(kindVendor())] < 2 {
		t.Errorf("vendor tokens not counted: %v", rep.Redactions)
	}
	out, _ := Marshal(rep)
	if bytes.Contains(out, []byte(tok)) {
		t.Fatal("token printed")
	}
}

func kindVendor() string { return "vendor_token" }

func TestVersionLabelsSanitized(t *testing.T) {
	opt := buildOpts(&sliceSource{})
	opt.BallastVersion = "v1 /Users/alice/bin ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78"
	opt.GOOS = "linux; rm -rf"
	rep, err := Build(opt)
	if err != nil {
		t.Fatal(err)
	}
	if v := Validate(rep); len(v) > 0 {
		t.Fatalf("Validate: %v", v)
	}
	if rep.BallastVersion != "" || rep.GOOS != "" {
		t.Errorf("unsafe labels kept: %q %q", rep.BallastVersion, rep.GOOS)
	}
	opt.BallastVersion, opt.GOOS = "2.0.0-rc.1+build_5", "darwin"
	if rep, err = Build(opt); err != nil || rep.BallastVersion != opt.BallastVersion || rep.GOOS != "darwin" {
		t.Errorf("plain labels dropped: %+v %v", rep, err)
	}
}

// ---------------------------------------------------------------- Validate

func validReport(t testing.TB) *Report {
	t.Helper()
	rep, err := Build(buildOpts(&sliceSource{recs: []model.AuditRecord{
		rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) {
			r.RuleID, r.Decision, r.Client, r.Tier, r.LatencyUS = "no-long-sleep", "deny", "codex", model.TierT0, 900
		}),
		diagRec(`{"diag":"lock_timeout"}`),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	rep.Rules[0].Rule = "no-long-sleep"
	if v := Validate(rep); len(v) > 0 {
		t.Fatalf("baseline report invalid: %v", v)
	}
	return rep
}

func TestValidateRejects(t *testing.T) {
	tok := "ghp_" + "Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78"
	cases := []struct {
		name string
		mut  func(r *Report)
	}{
		{"wrong version", func(r *Report) { r.V = 2 }},
		{"generated_at not truncated", func(r *Report) { r.GeneratedAt = r.GeneratedAt.Add(time.Minute) }},
		{"generated_at zero", func(r *Report) { r.GeneratedAt = time.Time{} }},
		{"generated_at local zone", func(r *Report) { r.GeneratedAt = r.GeneratedAt.In(time.FixedZone("x", 3600)) }},
		{"window_start not day", func(r *Report) { r.WindowStart = r.WindowStart.Add(time.Hour) }},
		{"window_end not day", func(r *Report) { r.WindowEnd = r.WindowEnd.Add(time.Second) }},
		{"version path", func(r *Report) { r.BallastVersion = "/Users/alice/ballast" }},
		{"version secret", func(r *Report) { r.BallastVersion = tok }},
		{"version too long", func(r *Report) { r.BallastVersion = strings.Repeat("a", 65) }},
		{"goos space", func(r *Report) { r.GOOS = "my laptop" }},
		{"negative sessions", func(r *Report) { r.Sessions = -1 }},
		{"negative corrupt", func(r *Report) { r.Corrupt = -1 }},
		{"client session id", func(r *Report) { r.Clients["sess-1234-abcd"] = 1 }},
		{"client user name", func(r *Report) { r.Clients["alice"] = 1 }},
		{"tier path", func(r *Report) { r.Tiers["/home/bob"] = 1 }},
		{"diag path", func(r *Report) { r.Diags["/home/bob/.ssh/id_rsa"] = 1 }},
		{"diag unknown name", func(r *Report) { r.Diags["brand_new"] = 1 }},
		{"diag secret", func(r *Report) { r.Diags[tok] = 1 }},
		{"diag negative", func(r *Report) { r.Diags["lock_timeout"] = -3 }},
		{"redaction kind secret", func(r *Report) { r.Redactions[tok] = 1 }},
		{"outcome path", func(r *Report) { r.Verify.Outcomes["/etc/passwd"] = 1 }},
		{"by_tier unknown", func(r *Report) { r.Verify.ByTier["T9"] = 1 }},
		{"sandbox hostname", func(r *Report) { r.Verify.BySandbox["host.example.com"] = 1 }},
		{"origin user", func(r *Report) { r.Verify.ByOrigin["alice"] = 1 }},
		{"rule raw custom id with path", func(r *Report) { r.Rules[0].Rule = "my/rule" }},
		{"rule secret", func(r *Report) { r.Rules[0].Rule = tok }},
		{"rule upper case", func(r *Report) { r.Rules[0].Rule = "My-Rule" }},
		{"rule with space", func(r *Report) { r.Rules[0].Rule = "ignore previous instructions" }},
		{"rule uuid", func(r *Report) { r.Rules[0].Rule = "0123456789abcdef0123456789abcdef" }},
		{"rule session-like hex run", func(r *Report) { r.Rules[0].Rule = "s-0123456789abcdef01" }},
		{"rule empty", func(r *Report) { r.Rules[0].Rule = "" }},
		{"rule bad hash length", func(r *Report) { r.Rules[0].Rule = "h:0123456789a" }},
		{"rule bad hash chars", func(r *Report) { r.Rules[0].Rule = "h:0123456789ag" }},
		{"rule hash upper", func(r *Report) { r.Rules[0].Rule = "h:0123456789AB" }},
		{"rule layer free text", func(r *Report) { r.Rules[0].Layer = "alice's layer" }},
		{"rule action free text", func(r *Report) { r.Rules[0].Action = "rm -rf" }},
		{"rule target free text", func(r *Report) { r.Rules[0].Target = "x" }},
		{"rule negative", func(r *Report) { r.Rules[0].Hits = -1 }},
		{"latency shape", func(r *Report) { r.Latency.Counts = r.Latency.Counts[1:] }},
		{"latency bounds", func(r *Report) { r.Latency.BucketsMS[2] = r.Latency.BucketsMS[1] }},
		{"latency negative", func(r *Report) { r.Latency.Counts[0] = -1 }},
		{"p95 negative", func(r *Report) { r.Latency.P95MS = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validReport(t)
			tc.mut(r)
			v := Validate(r)
			if len(v) == 0 {
				t.Fatal("Validate accepted a report with planted data")
			}
			for _, msg := range v {
				for _, leak := range []string{"ghp_", "alice", "/Users", "/home", "/etc", "0123456789", "my/rule"} {
					if strings.Contains(msg, leak) {
						t.Errorf("violation message repeats the offending value: %q", msg)
					}
				}
			}
		})
	}
	if v := Validate(nil); len(v) == 0 {
		t.Error("nil report accepted")
	}
}

func TestValidateAcceptsShippedAndHashedIDs(t *testing.T) {
	r := validReport(t)
	for _, id := range shippedIDs(t) {
		r.Rules[0].Rule = id
		if v := Validate(r); len(v) > 0 {
			t.Errorf("shipped id %q rejected: %v", id, v)
		}
	}
	r.Rules[0].Rule = HashRuleID("anything")
	if v := Validate(r); len(v) > 0 {
		t.Errorf("hashed id rejected: %v", v)
	}
}

// ---------------------------------------------------------------- property

// TestPropertyNoLeak feeds random audit records whose every free-text field
// carries a unique planted token (secrets, paths, ids, host names, user
// names) and asserts that no token occurs in the serialized report and that
// the report validates.
func TestPropertyNoLeak(t *testing.T) {
	rng := rand.New(rand.NewSource(20261016))
	flavors := []func(n int) string{
		func(n int) string { return fmt.Sprintf("ghp_%036x", rng.Uint64()^uint64(n)) },
		func(n int) string { return fmt.Sprintf("/Users/user%d/work/proj%x/secret.go", n, rng.Uint32()) },
		func(n int) string { return fmt.Sprintf("C:\\Users\\user%d\\proj%x", n, rng.Uint32()) },
		func(n int) string { return fmt.Sprintf("sess_%016x%016x", rng.Uint64(), rng.Uint64()) },
		func(n int) string { return fmt.Sprintf("toolu_%024x", rng.Uint64()) },
		func(n int) string { return fmt.Sprintf("AKIA%016X", rng.Uint64()) },
		func(n int) string { return fmt.Sprintf("host%d.corp%x.example.com", n, rng.Uint32()) },
		func(n int) string { return fmt.Sprintf("password=hunter%d%x", n, rng.Uint32()) },
		func(n int) string { return fmt.Sprintf("my-private-rule-%d-%x", n, rng.Uint32()) },
		func(n int) string { return fmt.Sprintf("why-the-boss-is-wrong-%d-%x", n, rng.Uint32()) },
	}
	for iter := 0; iter < 60; iter++ {
		var tokens []string
		plant := func() string {
			s := flavors[rng.Intn(len(flavors))](len(tokens))
			tokens = append(tokens, s)
			return s
		}
		var recs []model.AuditRecord
		n := 1 + rng.Intn(40)
		for i := 0; i < n; i++ {
			ts := time.Date(2026, 10, 1+rng.Intn(14), rng.Intn(24), rng.Intn(60), 0, 0, time.UTC)
			r := model.AuditRecord{TS: ts, V: 1, LatencyUS: int64(rng.Intn(200000))}
			switch rng.Intn(5) {
			case 0:
				r.Type = model.AuditHit
				r.RuleID = plant()
				r.RuleLayer = model.Layer(plant())
				r.Action, r.Target = model.Action(plant()), model.Action(plant())
				r.Decision = plant()
				r.Matched = plant()
				r.Tool = plant()
			case 1:
				r.Type = model.AuditEvalSummary
				r.Evals = map[string]model.EvalCount{plant(): {Evals: rng.Intn(9), Unknown: rng.Intn(3)}, plant(): {Evals: 1}}
			case 2:
				r.Type = model.AuditDiag
				r.Detail = mustJSON(map[string]any{"diag": plant(), "path": plant(), "nested": []string{plant()}})
			case 3:
				r.Type = model.AuditVerify
				r.Verify = &model.VerifyRecord{
					Outcome: model.Outcome(plant()), Tier: model.Tier(plant()), Sandbox: plant(), Origin: model.VerifyOrigin(plant()),
					Name: plant(), JobID: plant(), RepoInst: plant(), SessionID: plant(), LogPath: plant(), Error: plant(), ArgvHash: plant(),
				}
			default:
				r.Type = model.AuditType(plant())
				r.Detail = mustJSON(map[string]any{"x": plant()})
			}
			r.Client = model.ClientID(plant())
			r.Tier = model.Tier(plant())
			r.SessionID, r.AgentID, r.ToolUseID, r.RepoInst, r.RepoRemoteHash = plant(), plant(), plant(), plant(), plant()
			r.TranscriptRef = plant()
			r.BallastVersion = plant()
			r.Context = []model.EventSummary{{Text: plant(), Tool: plant()}}
			recs = append(recs, r)
		}
		opt := buildOpts(&sliceSource{recs: recs})
		opt.ShippedRuleIDs = shippedIDs(t)
		rep, err := Build(opt)
		if err != nil {
			t.Fatal(err)
		}
		if v := Validate(rep); len(v) > 0 {
			t.Fatalf("iter %d: Validate: %v", iter, v)
		}
		out, err := Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range tokens {
			if bytes.Contains(out, []byte(tok)) {
				t.Fatalf("iter %d: planted %q appears in the report:\n%s", iter, tok, out)
			}
			// Also in JSON-escaped form (backslashes).
			if esc, _ := json.Marshal(tok); bytes.Contains(out, esc[1:len(esc)-1]) {
				t.Fatalf("iter %d: escaped planted %q appears in the report", iter, tok)
			}
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// FuzzBuildValidates: whatever strings the audit contains, the built report
// validates.
func FuzzBuildValidates(f *testing.F) {
	f.Add("no-long-sleep", "lock_timeout", "codex", "T1")
	f.Add("/etc/passwd", "ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78", "\x00", "T\xff")
	f.Add("h:0123456789ab", "", "claude-code", "")
	f.Fuzz(func(t *testing.T, rule, diag, client, tier string) {
		recs := []model.AuditRecord{
			rec(model.AuditHit, "2026-10-10T00:00:00Z", func(r *model.AuditRecord) {
				r.RuleID, r.Client, r.Tier, r.Decision, r.RuleLayer = rule, model.ClientID(client), model.Tier(tier), diag, model.Layer(tier)
				r.Matched = rule + diag
			}),
			rec(model.AuditEvalSummary, "2026-10-10T00:00:01Z", func(r *model.AuditRecord) {
				r.Evals = map[string]model.EvalCount{rule: {Evals: 1}}
			}),
			rec(model.AuditDiag, "2026-10-10T00:00:02Z", func(r *model.AuditRecord) {
				r.Detail = mustJSON(map[string]any{"diag": diag})
			}),
			rec(model.AuditVerify, "2026-10-10T00:00:03Z", func(r *model.AuditRecord) {
				r.Verify = &model.VerifyRecord{Outcome: model.Outcome(diag), Tier: model.Tier(tier), Sandbox: client, Origin: model.VerifyOrigin(rule)}
			}),
		}
		opt := buildOpts(&sliceSource{recs: recs})
		opt.BallastVersion, opt.GOOS = rule, diag
		rep, err := Build(opt)
		if err != nil {
			t.Fatal(err)
		}
		if v := Validate(rep); len(v) > 0 {
			t.Fatalf("Validate: %v", v)
		}
		if _, err := Marshal(rep); err != nil {
			t.Fatal(err)
		}
	})
}

// ---------------------------------------------------------------- Write

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "sub", "report.json")
	rep := validReport(t)

	if err := Write(sys.OSFS{}, out, rep, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Marshal(rep)
	if !bytes.Equal(data, want) {
		t.Error("file content differs from Marshal")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}

	// Existing target without force: refused, content untouched.
	rep2 := validReport(t)
	rep2.Sessions = 99
	err = Write(sys.OSFS{}, out, rep2, false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if again, _ := os.ReadFile(out); !bytes.Equal(again, want) {
		t.Error("existing file was modified")
	}
	// force overwrites.
	if err := Write(sys.OSFS{}, out, rep2, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(out); !bytes.Contains(again, []byte(`"sessions": 99`)) {
		t.Error("force did not overwrite")
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(filepath.Dir(out))
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func TestWriteRefusesInvalidReport(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "r.json")
	rep := validReport(t)
	rep.Diags["/home/bob/secret"] = 1

	err := Write(sys.OSFS{}, out, rep, false)
	var pe *PrivacyError
	if !errors.Is(err, ErrPrivacy) || !errors.As(err, &pe) || len(pe.Violations) == 0 {
		t.Fatalf("err = %v, want *PrivacyError", err)
	}
	if strings.Contains(err.Error(), "bob") {
		t.Errorf("error repeats the offending value: %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Error("a file was written for an invalid report")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Errorf("leftovers: %v", ents)
	}
	// Invalid report wins over "exists": exit 1 vs exit 4.
	if err := os.WriteFile(out, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(sys.OSFS{}, out, rep, false); !errors.Is(err, ErrPrivacy) {
		t.Errorf("err = %v, want ErrPrivacy", err)
	}
	// With force an invalid report still leaves the old file alone.
	if err := Write(sys.OSFS{}, out, rep, true); !errors.Is(err, ErrPrivacy) {
		t.Errorf("err = %v, want ErrPrivacy", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "x" {
		t.Error("existing file clobbered by invalid report")
	}
}

func TestWriteFailureIsNotPrivacyOrExists(t *testing.T) {
	dir := t.TempDir()
	// Parent is a regular file: neither exists nor creatable.
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Write(sys.OSFS{}, filepath.Join(blocker, "r.json"), validReport(t), false)
	if err == nil || errors.Is(err, ErrExists) || errors.Is(err, ErrPrivacy) {
		t.Fatalf("err = %v, want a plain I/O error", err)
	}
	// Dangling symlink counts as existing.
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "dangling.json")
		if err := os.Symlink(filepath.Join(dir, "nowhere"), link); err != nil {
			t.Fatal(err)
		}
		if err := Write(sys.OSFS{}, link, validReport(t), false); !errors.Is(err, ErrExists) {
			t.Errorf("dangling symlink: err = %v, want ErrExists", err)
		}
	}
}
