// Package report (P2 unit P2-REPORT; architecture v2.0, docs/p2-design.md
// §4.4) builds `ballast report --out <file>`: a LOCAL file of redacted,
// aggregated statistics that a user may choose to share. It never sends
// anything anywhere.
//
// Layer L4 (imports model, ports, sys, layout, redact, stats). NOT hot path.
// It must not import net/* (archtest TestBinaryNoNet covers the whole
// cmd/ballast binary). Network upload, if ever built, lives in a separate
// binary (docs/p2-design.md §4.5) and is out of P2 scope.
//
// Privacy contract (frozen):
//   - Only counts, durations (bucketed) and enumerations leave the audit
//     log. Never: session/agent/tool_use ids, repo_inst, remote hashes,
//     transcript refs, paths, matched text, context summaries, reasons,
//     messages, prompts, hostnames, user names.
//   - Rule ids of the shipped packs (rules/default.json, rules/optional/*)
//     appear verbatim; every other rule id appears as "h:" +
//     hex(sha256(id))[:12] so custom rule names do not leak intent.
//   - Every string field is passed through redact.String and then checked
//     by Validate; a report that fails Validate is not written.
//   - Distinct sessions/repos are reported as counts only.
//
// Acceptance (P2-REPORT): golden test from a fixture audit dir
// (testdata/audit → testdata/report.golden.json); Validate rejects
// planted paths/ids/secrets (table test); a property test feeds random audit
// records with planted secrets and paths and asserts none appears in the
// serialized report; `ballast report --out f` exit codes per
// docs/p2-design.md §4.4.
package report

import (
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Version is Report.V.
const Version = 1

// Options configures Build.
type Options struct {
	Audit ports.AuditSource
	Clock sys.Clock
	// Since/Until bound the window (zero ⇒ unbounded / now).
	Since time.Time
	Until time.Time
	// ShippedRuleIDs are the ids printed verbatim (the CLI passes the ids of
	// the embedded default/optional packs).
	ShippedRuleIDs []string
	BallastVersion string
	GOOS           string
}

// Report is the file written by `ballast report --out`.
type Report struct {
	V              int       `json:"v"`
	GeneratedAt    time.Time `json:"generated_at"` // truncated to the hour
	WindowStart    time.Time `json:"window_start"` // truncated to the day
	WindowEnd      time.Time `json:"window_end"`   // truncated to the day
	BallastVersion string    `json:"ballast_version"`
	GOOS           string    `json:"goos"`

	Sessions int `json:"sessions"` // distinct session ids (count only)
	Repos    int `json:"repos"`    // distinct repo_inst (count only)

	Clients map[model.ClientID]int `json:"clients"` // records per client
	Tiers   map[model.Tier]int     `json:"tiers"`   // records per tier
	Rules   []RuleAgg              `json:"rules"`
	Diags   map[string]int         `json:"diags"` // detail.diag name → count (names are a closed enum)
	Verify  VerifyAgg              `json:"verify"`
	Latency LatencyAgg             `json:"latency"`
	// Redactions counts audit strings that contained a redacted secret, by
	// redact.Kind (lets maintainers see detector coverage without content).
	Redactions map[string]int `json:"redactions"`
	Corrupt    int            `json:"corrupt_lines"`
}

// RuleAgg is one rule's aggregate (same semantics as `ballast stats`).
type RuleAgg struct {
	Rule     string       `json:"rule"` // shipped id or "h:<12 hex>"
	Layer    model.Layer  `json:"layer"`
	Action   model.Action `json:"action"`
	Target   model.Action `json:"target,omitempty"`
	Evals    int          `json:"evals"`
	Hits     int          `json:"hits"`
	Would    int          `json:"would"`
	Unknown  int          `json:"unknown"`
	Sessions int          `json:"sessions"`
}

// VerifyAgg counts verify outcomes.
type VerifyAgg struct {
	Outcomes  map[model.Outcome]int `json:"outcomes"`
	ByTier    map[model.Tier]int    `json:"by_tier"`
	BySandbox map[string]int        `json:"by_sandbox"`
	ByOrigin  map[string]int        `json:"by_origin"`
}

// LatencyAgg is a fixed-bucket histogram of hook latency_us.
type LatencyAgg struct {
	BucketsMS []float64 `json:"buckets_ms"` // upper bounds: 1,2,5,10,20,30,50,100,+Inf
	Counts    []int     `json:"counts"`
	P95MS     float64   `json:"p95_ms"`
}

// Build aggregates the audit log.
func Build(opt Options) (*Report, error) {
	panic("unimplemented: report.Build (unit P2-REPORT)")
}

// Validate returns privacy violations (empty ⇒ OK).
func Validate(r *Report) []string {
	panic("unimplemented: report.Validate (unit P2-REPORT)")
}

// Write validates r and writes it atomically (0600) to path; it refuses to
// overwrite an existing file unless force is true.
func Write(fsys sys.FS, path string, r *Report, force bool) error {
	panic("unimplemented: report.Write (unit P2-REPORT)")
}
