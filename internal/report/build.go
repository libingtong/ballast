package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/redact"
	"github.com/libingtong/ballast/internal/sys"
)

// other is the bucket for every enumeration value outside its closed set
// (clients, tiers, verify outcomes, sandbox/origin names, redaction kinds).
// The offending value itself is never printed.
const other = "other"

// latencyBoundsMS are the finite upper bounds of the latency histogram.
// LatencyAgg.Counts has one more entry than LatencyAgg.BucketsMS: the last
// counts everything above the final bound (+Inf is not representable in
// JSON, so it is implicit).
var latencyBoundsMS = []float64{1, 2, 5, 10, 20, 30, 50, 100}

// Closed enumerations accepted in a report (used by Build to bucket and by
// Validate to reject).
var (
	outcomeSet = []string{
		string(model.OutcomePass), string(model.OutcomeFail), string(model.OutcomeTimeout),
		string(model.OutcomeSignal), string(model.OutcomeInfra), string(model.OutcomeSuperseded), other,
	}
	sandboxSet = []string{"sandbox-exec", "bwrap", "none", other}
	originSet  = []string{string(model.OriginCLI), string(model.OriginHook), other}
	tierSet    = []string{string(model.TierT0), string(model.TierT1), other}
	layerSet   = []string{"", string(model.LayerOrg), string(model.LayerRepo), string(model.LayerUser), string(model.LayerFile)}
	actionSet  = []string{"", string(model.ActionLog), string(model.ActionWarn), string(model.ActionAsk), string(model.ActionDeny)}
	kindSet    = []string{
		string(redact.KindPrivateKey), string(redact.KindURLPassword), string(redact.KindAuthHeader),
		string(redact.KindBearer), string(redact.KindKeyValue), string(redact.KindFlag),
		string(redact.KindAWS), string(redact.KindVendorToken), string(redact.KindJWT),
		string(redact.KindHighEntropy), other,
	}
)

func in(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// bucket returns s when it is a member of set, else other.
func bucket(set []string, s string) string {
	if in(set, s) {
		return s
	}
	return other
}

// HashRuleID is the report form of a non-shipped rule id: "h:" followed by
// the first 12 hex digits of sha256(id).
func HashRuleID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "h:" + hex.EncodeToString(sum[:])[:12]
}

type ruleAcc struct {
	RuleAgg
	sessions map[string]struct{}
	layerTS  time.Time
	actionTS time.Time
}

// Build aggregates the audit log. It reads the log once. An unreadable log
// is an error (CLI exit 5); corrupt lines are only counted.
func Build(opt Options) (*Report, error) {
	if opt.Audit == nil {
		return nil, errors.New("report: nil audit source")
	}
	clk := opt.Clock
	if clk == nil {
		clk = sys.RealClock{}
	}
	now := clk.Now().UTC()
	until := opt.Until
	if until.IsZero() {
		until = now
	}

	shipped := map[string]struct{}{}
	for _, id := range opt.ShippedRuleIDs {
		if validSlug(id) && redact.String(id) == id {
			shipped[id] = struct{}{}
		}
	}
	ruleName := func(id string) string {
		if _, ok := shipped[id]; ok {
			return id
		}
		return HashRuleID(id)
	}

	r := &Report{
		V:              Version,
		GeneratedAt:    now.Truncate(time.Hour),
		WindowEnd:      until.UTC().Truncate(24 * time.Hour),
		BallastVersion: cleanField(opt.BallastVersion),
		GOOS:           cleanField(opt.GOOS),
		Clients:        map[model.ClientID]int{},
		Tiers:          map[model.Tier]int{},
		Rules:          []RuleAgg{},
		Diags:          map[string]int{},
		Verify: VerifyAgg{
			Outcomes:  map[model.Outcome]int{},
			ByTier:    map[model.Tier]int{},
			BySandbox: map[string]int{},
			ByOrigin:  map[string]int{},
		},
		Latency: LatencyAgg{
			BucketsMS: append([]float64(nil), latencyBoundsMS...),
			Counts:    make([]int, len(latencyBoundsMS)+1),
		},
		Redactions: map[string]int{},
	}
	if !opt.Since.IsZero() {
		r.WindowStart = opt.Since.UTC().Truncate(24 * time.Hour)
	}

	sessions := map[string]struct{}{}
	repos := map[string]struct{}{}
	rules := map[string]*ruleAcc{}
	var first time.Time
	get := func(id string) *ruleAcc {
		name := ruleName(id)
		a := rules[name]
		if a == nil {
			a = &ruleAcc{RuleAgg: RuleAgg{Rule: name}, sessions: map[string]struct{}{}}
			rules[name] = a
		}
		return a
	}

	corrupt, err := opt.Audit.Records(opt.Since, opt.Until, func(rec *model.AuditRecord) error {
		if first.IsZero() || rec.TS.Before(first) {
			first = rec.TS
		}
		if rec.SessionID != "" {
			sessions[rec.SessionID] = struct{}{}
		}
		if rec.RepoInst != "" {
			repos[rec.RepoInst] = struct{}{}
		}
		if rec.Client != "" {
			r.Clients[clientKey(rec.Client)]++
		}
		tier := rec.Tier
		if tier == "" && rec.Verify != nil {
			tier = rec.Verify.Tier
		}
		if tier != "" {
			r.Tiers[model.Tier(bucket(tierSet, string(tier)))]++
		}
		if rec.LatencyUS > 0 {
			r.Latency.Counts[latencyBucket(rec.LatencyUS)]++
		}
		countRedactions(r.Redactions, rec)

		switch rec.Type {
		case model.AuditHit:
			if rec.RuleID == "" {
				return nil
			}
			a := get(rec.RuleID)
			switch {
			case strings.HasPrefix(rec.Decision, "would_"):
				a.Would++
			case isEffective(rec.Decision):
				a.Hits++
			}
			if rec.SessionID != "" {
				a.sessions[rec.SessionID] = struct{}{}
			}
			if in(layerSet, string(rec.RuleLayer)) && rec.RuleLayer != "" && !rec.TS.Before(a.layerTS) {
				a.Layer, a.layerTS = rec.RuleLayer, rec.TS
			}
			if rec.Action != model.ActionNone && !rec.TS.Before(a.actionTS) {
				a.actionTS = rec.TS
				a.Action = model.Action(bucketAction(rec.Action))
				a.Target = model.Action(bucketAction(rec.Target))
			}
		case model.AuditEvalSummary:
			for id, ec := range rec.Evals {
				if id == "" {
					continue
				}
				a := get(id)
				a.Evals += ec.Evals
				a.Unknown += ec.Unknown
			}
		case model.AuditDiag:
			r.Diags[diagKey(rec.Detail)]++
		case model.AuditVerify:
			if v := rec.Verify; v != nil {
				r.Verify.Outcomes[model.Outcome(bucket(outcomeSet, string(v.Outcome)))]++
				r.Verify.ByTier[model.Tier(bucket(tierSet, string(v.Tier)))]++
				sb := v.Sandbox
				if sb == "" {
					sb = "none" // pre-P2 records ran unsandboxed
				}
				r.Verify.BySandbox[bucket(sandboxSet, sb)]++
				org := string(v.Origin)
				if org == "" {
					org = string(model.OriginCLI) // pre-P2 records: only `ballast verify` existed
				}
				r.Verify.ByOrigin[bucket(originSet, org)]++
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("report: reading audit: %w", err)
	}

	if r.WindowStart.IsZero() && !first.IsZero() {
		r.WindowStart = first.UTC().Truncate(24 * time.Hour)
	}
	r.Sessions, r.Repos, r.Corrupt = len(sessions), len(repos), corrupt
	for _, a := range rules {
		a.Sessions = len(a.sessions)
		r.Rules = append(r.Rules, a.RuleAgg)
	}
	sort.Slice(r.Rules, func(i, j int) bool { return r.Rules[i].Rule < r.Rules[j].Rule })
	r.Latency.P95MS = p95(r.Latency)
	return r, nil
}

func clientKey(c model.ClientID) model.ClientID {
	if c.Valid() {
		return c
	}
	return other
}

func bucketAction(a model.Action) string {
	if in(actionSet, string(a)) {
		return string(a)
	}
	return ""
}

func isEffective(decision string) bool {
	switch model.Action(decision) {
	case model.ActionWarn, model.ActionAsk, model.ActionDeny:
		return true
	}
	return false
}

// diagKey returns detail.diag when it is a string naming a member of
// model.DiagNames, otherwise DiagOther (missing, non-string, malformed or
// unknown).
func diagKey(detail json.RawMessage) string {
	var d struct {
		Diag any `json:"diag"`
	}
	if len(detail) == 0 || json.Unmarshal(detail, &d) != nil {
		return DiagOther
	}
	name, ok := d.Diag.(string)
	if !ok {
		return DiagOther
	}
	i := sort.SearchStrings(model.DiagNames, name)
	if i < len(model.DiagNames) && model.DiagNames[i] == name {
		return name
	}
	return DiagOther
}

func latencyBucket(us int64) int {
	ms := float64(us) / 1000
	for i, b := range latencyBoundsMS {
		if ms <= b {
			return i
		}
	}
	return len(latencyBoundsMS)
}

// p95 is the upper bound of the bucket holding the 95th percentile. The
// overflow bucket reports the last finite bound (the true value is larger).
func p95(l LatencyAgg) float64 {
	total := 0
	for _, c := range l.Counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	need := (total*95 + 99) / 100 // ceil(0.95*total)
	cum := 0
	for i, c := range l.Counts {
		cum += c
		if cum >= need {
			if i >= len(l.BucketsMS) {
				return l.BucketsMS[len(l.BucketsMS)-1]
			}
			return l.BucketsMS[i]
		}
	}
	return l.BucketsMS[len(l.BucketsMS)-1]
}

// countRedactions adds, per redact.Kind, the number of secret spans found in
// the strings of rec (already-masked values count too). Only counts leave.
func countRedactions(into map[string]int, rec *model.AuditRecord) {
	add := func(s string) {
		if s == "" || !redact.MayContainSecret(s) {
			return
		}
		for _, m := range redact.Find(s) {
			into[bucket(kindSet, string(m.Kind))]++
		}
	}
	add(rec.Matched)
	add(rec.TranscriptRef)
	for _, c := range rec.Context {
		add(c.Text)
	}
	if v := rec.Verify; v != nil {
		add(v.Error)
		add(v.LogPath)
	}
	if len(rec.Detail) > 0 {
		var d any
		if json.Unmarshal(rec.Detail, &d) == nil {
			walkStrings(d, add)
		}
	}
}

func walkStrings(v any, fn func(string)) {
	switch x := v.(type) {
	case string:
		fn(x)
	case []any:
		for _, e := range x {
			walkStrings(e, fn)
		}
	case map[string]any:
		for k, e := range x {
			fn(k)
			walkStrings(e, fn)
		}
	}
}

// cleanField accepts a caller-supplied label (version, GOOS) only when it is
// entirely made of label characters, short and secret-free; anything else
// is dropped (empty), not repaired.
func cleanField(s string) string {
	if len(s) > 64 || !labelChars(s) || redact.String(s) != s {
		return ""
	}
	return s
}

// validSlug reports whether s looks like a shipped rule id: lower-case
// letters, digits, '.', '_', '-', at most 64 bytes, starting alphanumeric.
func validSlug(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// Marshal is the canonical serialization of a report (indented JSON plus a
// trailing newline; map keys sorted by encoding/json).
func Marshal(r *Report) ([]byte, error) { return marshal(r) }

func marshal(r *Report) ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
