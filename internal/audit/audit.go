package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/redact"
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

// NewWriter returns a Writer. Nil FS and Clock default to the real ones.
func NewWriter(opt Options) *Writer {
	if opt.FS == nil {
		opt.FS = sys.OSFS{}
	}
	if opt.Clock == nil {
		opt.Clock = sys.RealClock{}
	}
	if opt.MaxFileBytes <= 0 {
		opt.MaxFileBytes = MaxFileBytes
	}
	return &Writer{opt: opt}
}

// Append implements ports.AuditSink. Records without a TS get the clock's
// now; TS is written in UTC and a zero V becomes AuditVersion. All lines of
// one month go out in a single write; the rare batch that spans a month
// boundary needs one write per month.
func (w *Writer) Append(recs []model.AuditRecord) error {
	if len(recs) == 0 {
		return nil
	}
	type batch struct {
		month time.Time
		buf   []byte
	}
	var batches []batch
	now := w.opt.Clock.Now()
	for i := range recs {
		rec := recs[i]
		if rec.TS.IsZero() {
			rec.TS = now
		}
		rec.TS = rec.TS.UTC()
		line, err := MarshalLine(&rec)
		if err != nil {
			return err
		}
		month := time.Date(rec.TS.Year(), rec.TS.Month(), 1, 0, 0, 0, 0, time.UTC)
		if n := len(batches); n > 0 && batches[n-1].month.Equal(month) {
			batches[n-1].buf = append(batches[n-1].buf, line...)
			continue
		}
		batches = append(batches, batch{month: month, buf: append([]byte(nil), line...)})
	}
	for _, b := range batches {
		if err := w.writeMonth(b.month, b.buf); err != nil {
			return err
		}
	}
	return nil
}

// writeMonth appends buf to the active file of month, moving to the next
// suffix when the active file is already full. Rotation never renames, so
// concurrent writers can only agree on the next file, never race on it.
func (w *Writer) writeMonth(month time.Time, buf []byte) error {
	n, err := w.activeSuffix(month)
	if err != nil {
		return err
	}
	for {
		f, err := w.open(filepath.Join(w.opt.Dir, FileName(month, n)))
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return err
		}
		if fi.Size() >= w.opt.MaxFileBytes {
			_ = f.Close()
			n++
			continue
		}
		return writeAll(f, buf)
	}
}

// open opens name for appending, creating the directory on first use.
func (w *Writer) open(name string) (sys.File, error) {
	flags := appendFlags | os.O_APPEND | os.O_CREATE
	f, err := w.opt.FS.OpenFile(name, flags, 0o600)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		if merr := w.opt.FS.MkdirAll(w.opt.Dir, 0o700); merr != nil {
			return nil, merr
		}
		f, err = w.opt.FS.OpenFile(name, flags, 0o600)
	}
	return f, err
}

// writeAll performs the single write, checks for a short write, and closes f.
func writeAll(f sys.File, buf []byte) (err error) {
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	unlock, err := lockAppend(f)
	if err != nil {
		return err
	}
	defer unlock()
	n, err := f.Write(buf)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return fmt.Errorf("audit: %w (%d of %d bytes)", sys.ErrShortWrite, n, len(buf))
	}
	return nil
}

// activeSuffix returns the highest existing suffix of month (0 when only the
// plain file or nothing exists).
func (w *Writer) activeSuffix(month time.Time) (int, error) {
	ents, err := w.opt.FS.ReadDir(w.opt.Dir)
	if err != nil {
		if missingDir(w.opt.FS, w.opt.Dir, err) {
			return 0, nil
		}
		return 0, err
	}
	want := month.Format("2006-01")
	highest := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if m, n, ok := parseFileName(e.Name()); ok && m == want && n > highest {
			highest = n
		}
	}
	return highest, nil
}

// MarshalLine encodes one record as a JSONL line within MaxAuditLineBytes,
// applying the shrink order documented in the package doc. Matched and the
// context texts are redacted first; TS is converted to UTC and a zero V
// becomes AuditVersion.
func MarshalLine(rec *model.AuditRecord) ([]byte, error) {
	if rec == nil {
		return nil, errors.New("audit: nil record")
	}
	r := *rec
	r.TS = r.TS.UTC()
	if r.V == 0 {
		r.V = model.AuditVersion
	}
	r.Matched = ClipRunes(Redact(r.Matched), model.MaxMatchedRunes)
	if len(r.Context) > 0 {
		ctx := make([]model.EventSummary, len(r.Context))
		copy(ctx, r.Context)
		for i := range ctx {
			ctx[i].Text = Redact(ctx[i].Text)
		}
		r.Context = ctx
	}
	if len(r.Detail) > 0 && !json.Valid(r.Detail) {
		r.Detail, _ = encodeJSON(string(r.Detail))
	}
	if len(r.Detail) > 0 && redact.MayContainSecret(string(r.Detail)) {
		r.Detail = redactDetail(r.Detail)
	}

	if line, ok, err := fit(&r); ok || err != nil {
		return line, err
	}

	// 1. Detail: clip long strings, then replace.
	if len(r.Detail) > 0 {
		r.Detail = clipDetail(r.Detail, 256)
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
		r.Detail = json.RawMessage(`{"truncated":true}`)
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
	}
	// 2. Context: shorter texts, then only the closest entry, then none.
	if len(r.Context) > 0 {
		for i := range r.Context {
			r.Context[i].Text = ClipRunes(r.Context[i].Text, 60)
			r.Context[i].Hits = nil
		}
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
		r.Context = r.Context[len(r.Context)-1:]
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
		r.Context = nil
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
	}
	// 3. Matched.
	for _, n := range []int{100, 40, 0} {
		if r.Matched == "" {
			break
		}
		r.Matched = ClipRunes(r.Matched, n)
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
	}
	// 4. Evals: keep the largest prefix (by rule id) that fits.
	if len(r.Evals) > 0 {
		full := r.Evals
		keys := make([]string, 0, len(full))
		for k := range full {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lo, hi := 0, len(keys) // lo always fits-or-unknown, find max keep that fits
		var best []byte
		for lo < hi {
			mid := (lo + hi + 1) / 2
			r.Evals = pick(full, keys[:mid])
			line, ok, err := fit(&r)
			if err != nil {
				return nil, err
			}
			if ok {
				lo, best = mid, line
			} else {
				hi = mid - 1
			}
		}
		if best != nil {
			return best, nil
		}
		r.Evals = nil
		if line, ok, err := fit(&r); ok || err != nil {
			return line, err
		}
	}
	return minimalLine(&r)
}

// fit encodes r and reports whether the line is within MaxAuditLineBytes.
func fit(r *model.AuditRecord) (line []byte, ok bool, err error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, false, fmt.Errorf("audit: marshal: %w", err)
	}
	return buf.Bytes(), buf.Len() <= model.MaxAuditLineBytes, nil
}

func minimalLine(r *model.AuditRecord) ([]byte, error) {
	min := struct {
		TS        time.Time       `json:"ts"`
		V         int             `json:"v"`
		Type      model.AuditType `json:"type"`
		Truncated bool            `json:"truncated"`
	}{r.TS, r.V, model.AuditType(clipBytes(string(r.Type), 64)), true}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&min); err != nil {
		return nil, fmt.Errorf("audit: marshal: %w", err)
	}
	return buf.Bytes(), nil
}

func encodeJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func pick(m map[string]model.EvalCount, keys []string) map[string]model.EvalCount {
	out := make(map[string]model.EvalCount, len(keys))
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

// clipDetail shortens every string inside a JSON value to n runes and marks
// objects with "truncated":true. Invalid or non-container input is returned
// as a truncated marker.
func clipDetail(raw json.RawMessage, n int) json.RawMessage {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return json.RawMessage(`{"truncated":true}`)
	}
	v = clipValue(v, n)
	if m, ok := v.(map[string]any); ok {
		m["truncated"] = true
	}
	out, err := encodeJSON(v)
	if err != nil {
		return json.RawMessage(`{"truncated":true}`)
	}
	return out
}

// redactDetail masks secrets in every string value of a JSON detail. It is
// only called when the raw bytes pass the redactor's prefilter, so details
// without anything secret-looking keep their exact bytes.
func redactDetail(raw json.RawMessage) json.RawMessage {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	out, err := encodeJSON(redactValue(v))
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	return out
}

func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return Redact(x)
	case []any:
		for i := range x {
			x[i] = redactValue(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = redactValue(e)
		}
	}
	return v
}

func clipValue(v any, n int) any {
	switch x := v.(type) {
	case string:
		return ClipRunes(x, n)
	case []any:
		for i := range x {
			x[i] = clipValue(x[i], n)
		}
	case map[string]any:
		for k, e := range x {
			x[k] = clipValue(e, n)
		}
	}
	return v
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// FileName returns the base name for month of t and suffix n (0 ⇒ none).
func FileName(t time.Time, n int) string {
	month := t.UTC().Format("2006-01")
	if n <= 0 {
		return month + ".jsonl"
	}
	return month + "." + strconv.Itoa(n) + ".jsonl"
}

// parseFileName splits "YYYY-MM[.N].jsonl" into its month and suffix.
func parseFileName(name string) (month string, n int, ok bool) {
	base, found := strings.CutSuffix(name, ".jsonl")
	if !found || len(base) < 7 {
		return "", 0, false
	}
	month = base[:7]
	if _, err := time.Parse("2006-01", month); err != nil {
		return "", 0, false
	}
	rest := base[7:]
	if rest == "" {
		return month, 0, true
	}
	if rest[0] != '.' || len(rest) == 1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[1:])
	if err != nil || n <= 0 || strconv.Itoa(n) != rest[1:] {
		return "", 0, false
	}
	return month, n, true
}

// ClipRunes shortens s to at most n runes. A cut result ends with "…" and the
// ellipsis counts towards n, so the output never exceeds the limit.
func ClipRunes(s string, n int) string {
	if n <= 0 {
		if s == "" {
			return s
		}
		return ""
	}
	if len(s) <= n || utf8.RuneCountInString(s) <= n {
		return s
	}
	n--
	for i := range s {
		if n == 0 {
			return s[:i] + "…"
		}
		n--
	}
	return s
}
