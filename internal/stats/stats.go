package stats

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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

// DefaultSince is the default --since window.
const DefaultSince = 7 * 24 * time.Hour

// ErrUsage marks invalid arguments (CLI exit 2): bad --since, --by, rule id,
// --to, sample count.
var ErrUsage = errors.New("stats: usage error")

// RuleInfo is the configured action/target of a rule.
type RuleInfo struct {
	Action model.Action
	Target model.Action
}

// Query selects the stats window.
type Query struct {
	Since time.Time
	By    GroupBy
	Rule  string // optional filter
	// Current optionally supplies the configured action/target per rule id
	// (the rules in effect now); it takes precedence over the values found
	// in hit records.
	Current map[string]RuleInfo
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

// ParseSince parses "7d", "36h", or RFC3339 relative to now. The empty
// string means DefaultSince.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-DefaultSince), nil
	}
	if n := len(s); n >= 2 && (s[n-1] == 'd' || s[n-1] == 'h') {
		digits := s[:n-1]
		if !allDigits(digits) {
			return time.Time{}, fmt.Errorf("%w: invalid --since %q", ErrUsage, s)
		}
		v, err := strconv.ParseUint(digits, 10, 63)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: invalid --since %q", ErrUsage, s)
		}
		unit := time.Hour
		if s[n-1] == 'd' {
			unit = 24 * time.Hour
		}
		if v > uint64(math.MaxInt64/int64(unit)) {
			return time.Time{}, fmt.Errorf("%w: --since %q is out of range", ErrUsage, s)
		}
		return now.Add(-time.Duration(v) * unit), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid --since %q (want Nd, Nh or RFC3339)", ErrUsage, s)
	}
	return t, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Compute aggregates the audit log.
func Compute(src ports.AuditSource, q Query) ([]Row, error) {
	rows, _, err := ComputeDetailed(src, q)
	return rows, err
}

type group struct{ key, rule string }

type accum struct {
	row      Row
	sessions map[string]struct{}
}

// ComputeDetailed is Compute that also reports how many corrupt audit lines
// the source skipped.
func ComputeDetailed(src ports.AuditSource, q Query) ([]Row, int, error) {
	by := q.By
	if by == "" {
		by = ByRule
	}
	switch by {
	case ByRule, BySession, ByRepo:
	default:
		return nil, 0, fmt.Errorf("%w: invalid --by %q (want rule, session or repo)", ErrUsage, q.By)
	}
	if src == nil {
		return nil, 0, errors.New("stats: nil audit source")
	}

	accs := map[group]*accum{}
	latest := map[string]struct {
		ts time.Time
		RuleInfo
	}{}
	get := func(g group) *accum {
		a := accs[g]
		if a == nil {
			a = &accum{row: Row{Key: g.key, Rule: g.rule}, sessions: map[string]struct{}{}}
			accs[g] = a
		}
		return a
	}

	corrupt, err := src.Records(q.Since, time.Time{}, func(rec *model.AuditRecord) error {
		switch rec.Type {
		case model.AuditHit:
			if rec.RuleID == "" || (q.Rule != "" && rec.RuleID != q.Rule) {
				return nil
			}
			a := get(group{groupKey(by, rec, rec.RuleID), rec.RuleID})
			switch {
			case strings.HasPrefix(rec.Decision, "would_"):
				a.row.Would++
			case isEffective(rec.Decision):
				a.row.Hits++
			}
			if rec.SessionID != "" {
				a.sessions[rec.SessionID] = struct{}{}
			}
			if l, ok := latest[rec.RuleID]; rec.Action != model.ActionNone && (!ok || !rec.TS.Before(l.ts)) {
				latest[rec.RuleID] = struct {
					ts time.Time
					RuleInfo
				}{rec.TS, RuleInfo{rec.Action, rec.Target}}
			}
		case model.AuditEvalSummary:
			for rule, ec := range rec.Evals {
				if rule == "" || (q.Rule != "" && rule != q.Rule) {
					continue
				}
				a := get(group{groupKey(by, rec, rule), rule})
				a.row.Evals += ec.Evals
				a.row.Unknown += ec.Unknown
			}
		}
		return nil
	})
	if err != nil {
		return nil, corrupt, fmt.Errorf("stats: reading audit: %w", err)
	}

	rows := make([]Row, 0, len(accs))
	for _, a := range accs {
		r := a.row
		r.Sessions = len(a.sessions)
		if info, ok := q.Current[r.Rule]; ok {
			r.Action, r.Target = info.Action, info.Target
		} else if l, ok := latest[r.Rule]; ok {
			r.Action, r.Target = l.Action, l.Target
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Key != rows[j].Key {
			return rows[i].Key < rows[j].Key
		}
		return rows[i].Rule < rows[j].Rule
	})
	return rows, corrupt, nil
}

func isEffective(decision string) bool {
	switch model.Action(decision) {
	case model.ActionWarn, model.ActionAsk, model.ActionDeny:
		return true
	}
	return false
}

func groupKey(by GroupBy, rec *model.AuditRecord, rule string) string {
	switch by {
	case BySession:
		if rec.SessionID != "" {
			return rec.SessionID
		}
		return "(none)"
	case ByRepo:
		if rec.RepoInst != "" {
			return rec.RepoInst
		}
		return "(none)"
	}
	return rule
}

// Sample returns the n most recent hits of rule and writes the review file.
func Sample(src ports.AuditSource, lay layout.Layout, fsys sys.FS, ids sys.IDGen, clk sys.Clock, rule string, n int) (*Review, error) {
	return sample(src, lay, fsys, ids, clk, rule, n)
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
