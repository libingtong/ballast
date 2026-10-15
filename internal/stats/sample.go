package stats

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// ReviewVersion is the "v" of reviews/<rule_id>.json.
const ReviewVersion = 1

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

// SampleIDOf returns the stable id of a hit record:
// hex(sha256(ts|session_id|tool_use_id|rule_id))[:16], ts in UTC RFC3339Nano.
func SampleIDOf(rec *model.AuditRecord) string {
	h := sha256.Sum256([]byte(rec.TS.UTC().Format(time.RFC3339Nano) + "|" + rec.SessionID + "|" + rec.ToolUseID + "|" + rec.RuleID))
	return hex.EncodeToString(h[:])[:16]
}

// validRuleID guards the rule id before it becomes a file name
// (^[a-z0-9-]{3,40}$, dsl-semantics §3).
func validRuleID(id string) bool {
	if len(id) < 3 || len(id) > 40 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// MaxSample bounds the sample count accepted by Sample.
const MaxSample = 10000

// ReviewPath returns reviews/<rule_id>.json.
func ReviewPath(lay layout.Layout, ruleID string) string {
	return filepath.Join(lay.ReviewsDir, ruleID+".json")
}

func sample(src ports.AuditSource, lay layout.Layout, fsys sys.FS, ids sys.IDGen, clk sys.Clock, rule string, n int) (*Review, error) {
	if !validRuleID(rule) {
		return nil, fmt.Errorf("%w: invalid rule id %q", ErrUsage, rule)
	}
	if n <= 0 {
		return nil, fmt.Errorf("%w: sample count must be positive, got %d", ErrUsage, n)
	}
	if n > MaxSample {
		return nil, fmt.Errorf("%w: sample count %d exceeds the maximum %d", ErrUsage, n, MaxSample)
	}
	if src == nil {
		return nil, fmt.Errorf("stats: nil audit source")
	}

	// Keep the n most recent hits in a ring (records arrive chronologically).
	ring := make([]model.AuditRecord, 0, min(n, 256))
	next := 0
	_, err := src.Records(time.Time{}, time.Time{}, func(rec *model.AuditRecord) error {
		if rec.Type != model.AuditHit || rec.RuleID != rule {
			return nil
		}
		if len(ring) < n {
			ring = append(ring, *rec)
		} else {
			ring[next] = *rec
			next = (next + 1) % n
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stats: reading audit: %w", err)
	}

	// Oldest-first from the ring, then reverse to newest-first.
	ordered := make([]model.AuditRecord, 0, len(ring))
	for i := 0; i < len(ring); i++ {
		ordered = append(ordered, ring[(next+i)%len(ring)])
	}
	rv := &Review{
		V:          ReviewVersion,
		RuleID:     rule,
		ReviewedAt: clk.Now().UTC(),
		SampleIDs:  []string{},
		Samples:    []SampleHit{},
	}
	for i := len(ordered) - 1; i >= 0; i-- {
		rec := ordered[i]
		id := SampleIDOf(&rec)
		rv.SampleIDs = append(rv.SampleIDs, id)
		rv.Samples = append(rv.Samples, SampleHit{ID: id, Record: rec})
	}
	if len(ordered) > 0 {
		rv.Generation = ordered[len(ordered)-1].Generation
	}
	if len(rv.Samples) == 0 {
		// Nothing was looked at, so no credential is issued.
		return rv, nil
	}

	data, err := json.MarshalIndent(rv, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("stats: encoding review: %w", err)
	}
	data = append(data, '\n')
	if err := sys.WriteFileAtomic(fsys, ids, ReviewPath(lay, rule), data, 0o600, false); err != nil {
		return nil, fmt.Errorf("stats: writing review: %w", err)
	}
	return rv, nil
}
