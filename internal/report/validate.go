package report

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/redact"
	"github.com/libingtong/ballast/internal/sys"
)

// Errors returned by Write (the CLI maps them to exit codes, §4.4).
var (
	// ErrPrivacy: the report failed Validate; nothing was written (exit 1).
	// The error wraps the violation list.
	ErrPrivacy = errors.New("report: privacy check failed")
	// ErrExists: the target exists and force is false (exit 4).
	ErrExists = errors.New("report: output file already exists")
)

// PrivacyError carries the Validate violations.
type PrivacyError struct{ Violations []string }

// Error implements error.
func (e *PrivacyError) Error() string {
	return fmt.Sprintf("%v: %s", ErrPrivacy, strings.Join(e.Violations, "; "))
}

// Is makes errors.Is(err, ErrPrivacy) true.
func (e *PrivacyError) Is(target error) bool { return target == ErrPrivacy }

// Validate returns privacy violations (empty ⇒ OK).
//
// It is deliberately independent of Build: it re-checks the finished value
// against closed enumerations and shape rules, so a bug in the aggregation
// (or a hand-edited report) cannot smuggle free text into the output.
// Violation messages never repeat the offending value.
func Validate(r *Report) []string {
	if r == nil {
		return []string{"nil report"}
	}
	var v []string
	bad := func(format string, a ...any) { v = append(v, fmt.Sprintf(format, a...)) }

	if r.V != Version {
		bad("v: unsupported version %d", r.V)
	}
	checkTime := func(name string, t time.Time, unit time.Duration, allowZero bool) {
		if t.IsZero() {
			if !allowZero {
				bad("%s: zero time", name)
			}
			return
		}
		if t.Location() != time.UTC || !t.Equal(t.Truncate(unit)) {
			bad("%s: not a UTC time truncated to %s", name, unit)
		}
	}
	checkTime("generated_at", r.GeneratedAt, time.Hour, false)
	checkTime("window_start", r.WindowStart, 24*time.Hour, true)
	checkTime("window_end", r.WindowEnd, 24*time.Hour, true)

	label := func(name, s string) {
		if s == "" {
			return
		}
		if len(s) > 64 || !labelChars(s) || redact.String(s) != s {
			bad("%s: not a plain label", name)
		}
	}
	label("ballast_version", r.BallastVersion)
	label("goos", r.GOOS)

	nonNeg := func(name string, n int) {
		if n < 0 {
			bad("%s: negative count", name)
		}
	}
	nonNeg("sessions", r.Sessions)
	nonNeg("repos", r.Repos)
	nonNeg("corrupt_lines", r.Corrupt)

	clients := append(clientNames(), other)
	for k, n := range r.Clients {
		enum("clients", string(k), clients, bad)
		nonNeg("clients", n)
	}
	for k, n := range r.Tiers {
		enum("tiers", string(k), tierSet, bad)
		nonNeg("tiers", n)
	}
	diags := append(append([]string(nil), model.DiagNames...), DiagOther)
	for k, n := range r.Diags {
		enum("diags", k, diags, bad)
		nonNeg("diags", n)
	}
	for k, n := range r.Redactions {
		enum("redactions", k, kindSet, bad)
		nonNeg("redactions", n)
	}
	for k, n := range r.Verify.Outcomes {
		enum("verify.outcomes", string(k), outcomeSet, bad)
		nonNeg("verify.outcomes", n)
	}
	for k, n := range r.Verify.ByTier {
		enum("verify.by_tier", string(k), tierSet, bad)
		nonNeg("verify.by_tier", n)
	}
	for k, n := range r.Verify.BySandbox {
		enum("verify.by_sandbox", k, sandboxSet, bad)
		nonNeg("verify.by_sandbox", n)
	}
	for k, n := range r.Verify.ByOrigin {
		enum("verify.by_origin", k, originSet, bad)
		nonNeg("verify.by_origin", n)
	}

	for i, a := range r.Rules {
		if !validRuleName(a.Rule) {
			bad("rules[%d].rule: neither a shipped-style id nor h:<12 hex>", i)
		}
		if !in(layerSet, string(a.Layer)) {
			bad("rules[%d].layer: not an enumerated layer", i)
		}
		if !in(actionSet, string(a.Action)) {
			bad("rules[%d].action: not an enumerated action", i)
		}
		if !in(actionSet, string(a.Target)) {
			bad("rules[%d].target: not an enumerated action", i)
		}
		for _, n := range []int{a.Evals, a.Hits, a.Would, a.Unknown, a.Sessions} {
			if n < 0 {
				bad("rules[%d]: negative count", i)
				break
			}
		}
	}

	l := r.Latency
	if len(l.Counts) != len(l.BucketsMS)+1 {
		bad("latency: counts must have one more entry than buckets_ms")
	}
	for i, b := range l.BucketsMS {
		if b <= 0 || (i > 0 && b <= l.BucketsMS[i-1]) {
			bad("latency.buckets_ms: not strictly increasing positive bounds")
			break
		}
	}
	for _, c := range l.Counts {
		if c < 0 {
			bad("latency.counts: negative count")
			break
		}
	}
	if l.P95MS < 0 {
		bad("latency.p95_ms: negative")
	}
	sort.Strings(v)
	return v
}

func enum(field, key string, set []string, bad func(string, ...any)) {
	if !in(set, key) {
		bad("%s: key outside the closed enumeration", field)
	}
}

func clientNames() []string {
	out := make([]string, 0, len(model.AllClients)+1)
	for _, c := range model.AllClients {
		out = append(out, string(c))
	}
	return out
}

func labelChars(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_', c == '+':
		default:
			return false
		}
	}
	return true
}

// validRuleName accepts "h:" + 12 lower-case hex digits, or a slug of the
// shipped-id shape that carries no secret, no path/space and no long
// identifier-looking hex run.
func validRuleName(s string) bool {
	if rest, ok := strings.CutPrefix(s, "h:"); ok {
		return len(rest) == 12 && isHex(rest)
	}
	if !validSlug(s) || redact.String(s) != s {
		return false
	}
	run := 0
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			run++
			if run >= 16 {
				return false
			}
		} else {
			run = 0
		}
	}
	return true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Write validates r and writes it atomically (0600) to path; it refuses to
// overwrite an existing file unless force is true. A failed Validate returns
// a *PrivacyError (errors.Is ErrPrivacy) and touches nothing; an existing
// target returns ErrExists. The existence check (Lstat, so a dangling
// symlink counts) and the rename are not one atomic step.
func Write(fsys sys.FS, path string, r *Report, force bool) error {
	if v := Validate(r); len(v) > 0 {
		return &PrivacyError{Violations: v}
	}
	data, err := marshal(r)
	if err != nil {
		return err
	}
	// Last line of defence (docs/p2-design.md §4.1): the serialized bytes
	// must be a fixed point of the redactor, whatever the field checks said.
	if redact.String(string(data)) != string(data) {
		return &PrivacyError{Violations: []string{"serialized report: the redactor would change the output"}}
	}
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	if !force {
		if _, err := fsys.Lstat(path); err == nil {
			return fmt.Errorf("%w: %s", ErrExists, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return sys.WriteFileAtomic(fsys, sys.RandomID{}, path, data, 0o600, true)
}
