package audit

import (
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Frozen limits.
const (
	MaxFileBytes = 64 << 20
	Retention    = 90 * 24 * time.Hour
)

// Options configures a Writer.
type Options struct {
	Dir          string // layout.AuditDir
	FS           sys.FS
	Clock        sys.Clock
	MaxFileBytes int64 // 0 ⇒ MaxFileBytes (tests use small values)
}

// Writer implements ports.AuditSink.
type Writer struct {
	opt Options
}

var _ ports.AuditSink = (*Writer)(nil)

// NewWriter returns a Writer.
func NewWriter(opt Options) *Writer { return &Writer{opt: opt} }

// Append implements ports.AuditSink.
func (w *Writer) Append(recs []model.AuditRecord) error {
	panic("unimplemented: audit.Writer.Append")
}

// MarshalLine encodes one record as a JSONL line within MaxAuditLineBytes,
// applying the shrink order documented in the package doc.
func MarshalLine(rec *model.AuditRecord) ([]byte, error) {
	panic("unimplemented: audit.MarshalLine")
}

// FileName returns the base name for month of t and suffix n (0 ⇒ none).
func FileName(t time.Time, n int) string {
	panic("unimplemented: audit.FileName")
}

// Redact masks secrets in s (AWS keys, private-key blocks, bearer tokens,
// "password=..."/"token=..." values, GitHub/Slack tokens) with "***".
// Minimal core redactor; the P2-a redactor replaces it behind the same signature.
func Redact(s string) string {
	panic("unimplemented: audit.Redact")
}

// ClipRunes truncates s to n runes, appending "…" when cut.
func ClipRunes(s string, n int) string {
	panic("unimplemented: audit.ClipRunes")
}

// Location identifies a line.
type Location struct {
	File string
	Line int
}

// ScanOptions filter a scan.
type ScanOptions struct {
	Since time.Time // zero ⇒ all
	Until time.Time // zero ⇒ no upper bound
	Types []model.AuditType
}

// ScanStats summarizes a scan.
type ScanStats struct {
	Files   int
	Lines   int
	Corrupt int
	Skipped int
}

// Reader streams audit records.
type Reader struct {
	Dir string
	FS  sys.FS
}

var _ ports.AuditSource = (*Reader)(nil)

// Records implements ports.AuditSource (thin wrapper over Scan).
func (r *Reader) Records(since, until time.Time, fn func(rec *model.AuditRecord) error) (int, error) {
	st, err := r.Scan(ScanOptions{Since: since, Until: until}, func(rec *model.AuditRecord, _ Location) error { return fn(rec) })
	return st.Corrupt, err
}

// Scan calls fn for each record in chronological file order; fn errors abort.
func (r *Reader) Scan(opt ScanOptions, fn func(rec *model.AuditRecord, loc Location) error) (ScanStats, error) {
	panic("unimplemented: audit.Reader.Scan")
}

// Finding is an `audit lint` result.
type Finding struct {
	Loc     Location `json:"loc"`
	Message string   `json:"message"`
}

// Lint checks every line: valid JSON, v known, required fields per type,
// ts monotonic per file within 5 minutes skew, line ≤ 8 KiB. It is a format
// check only, never a tamper proof.
func Lint(r *Reader) ([]Finding, ScanStats, error) {
	panic("unimplemented: audit.Lint")
}

// GC deletes files older than retention (by month end) relative to now.
func GC(fsys sys.FS, dir string, now time.Time, retention time.Duration) (removed []string, err error) {
	panic("unimplemented: audit.GC")
}
