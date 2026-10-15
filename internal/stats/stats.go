package stats

import (
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// GroupBy dimension.
type GroupBy string

const (
	ByRule    GroupBy = "rule"
	BySession GroupBy = "session"
	ByRepo    GroupBy = "repo"
)

// Query selects the stats window.
type Query struct {
	Since time.Time
	By    GroupBy
	Rule  string // optional filter
}

// Row is one output row: rule | action | target | evals | hits | would | unknown | sessions.
type Row struct {
	Key      string       `json:"key"` // rule id, session id or repo_inst
	Rule     string       `json:"rule"`
	Action   model.Action `json:"action"`
	Target   model.Action `json:"target"`
	Evals    int          `json:"evals"`
	Hits     int          `json:"hits"`
	Would    int          `json:"would"`
	Unknown  int          `json:"unknown"`
	Sessions int          `json:"sessions"`
}

// ParseSince parses "7d", "36h", or RFC3339 relative to now.
func ParseSince(s string, now time.Time) (time.Time, error) {
	panic("unimplemented: stats.ParseSince")
}

// Compute aggregates the audit log.
func Compute(src ports.AuditSource, q Query) ([]Row, error) {
	panic("unimplemented: stats.Compute")
}

// SampleHit is one sampled hit.
type SampleHit struct {
	ID     string            `json:"id"`
	Record model.AuditRecord `json:"record"`
}

// Review is reviews/<rule_id>.json.
type Review struct {
	V          int         `json:"v"`
	RuleID     string      `json:"rule_id"`
	Generation string      `json:"generation"`
	ReviewedAt time.Time   `json:"reviewed_at"`
	SampleIDs  []string    `json:"sample_ids"`
	Samples    []SampleHit `json:"samples"`
}

// Sample returns the n most recent hits of rule and writes the review file.
func Sample(src ports.AuditSource, lay layout.Layout, fsys sys.FS, ids sys.IDGen, clk sys.Clock, rule string, n int) (*Review, error) {
	panic("unimplemented: stats.Sample")
}

// PromoteOptions configures Promote.
type PromoteOptions struct {
	RuleID  string
	To      model.Action
	Force   bool
	MinHits int           // default 5
	Window  time.Duration // default 7d
	Clients []model.ClientID
}

// PromoteDeps are the collaborators of Promote.
type PromoteDeps struct {
	Layout   layout.Layout
	FS       sys.FS
	Clock    sys.Clock
	IDs      sys.IDGen
	Audit    ports.AuditSink
	Source   ports.AuditSource
	Compiler ports.RuleCompiler
	// CheckCapabilities is dsl.CheckCapabilities (injected).
	CheckCapabilities func(r *model.Rule, c model.ClientID) []model.Diagnostic
	// RepoRoot locates the repo layer file.
	RepoRoot string
}

// PromoteResult reports what happened.
type PromoteResult struct {
	File     string       `json:"file"`
	Layer    model.Layer  `json:"layer"`
	From     model.Action `json:"from"`
	To       model.Action `json:"to"`
	Forced   bool         `json:"forced"`
	GateHits int          `json:"gate_hits"`
	ReviewAt time.Time    `json:"review_at"`
}

// Promote errors (CLI exit mapping: gate ⇒ 1, not found/org/capability ⇒ 4).
var (
	ErrGate         = errorString("stats: promotion gate not satisfied")
	ErrRuleNotFound = errorString("stats: rule not found in repo or user layer")
	ErrOrgRule      = errorString("stats: org rules cannot be promoted locally")
	ErrCapability   = errorString("stats: target action unsupported by an installed client")
)

type errorString string

func (e errorString) Error() string { return string(e) }

// Promote performs `ballast rules promote`.
func Promote(opt PromoteOptions, deps PromoteDeps) (*PromoteResult, error) {
	panic("unimplemented: stats.Promote")
}
