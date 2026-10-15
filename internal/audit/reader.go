package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// maxScanLine bounds the memory spent on one line; longer lines are
// consumed but reported as oversize.
const maxScanLine = 1 << 20

// tsSkew is the backwards timestamp jitter Lint tolerates within a file.
const tsSkew = 5 * time.Minute

// Location identifies a line. File is the path of the audit file (Reader.Dir
// joined with its name); Line is 1-based.
type Location struct {
	File string
	Line int
}

// ScanOptions filter a scan. Records are selected when Since <= TS < Until.
type ScanOptions struct {
	Since time.Time // zero ⇒ all
	Until time.Time // zero ⇒ no upper bound
	Types []model.AuditType
}

// ScanStats summarizes a scan. Files counts the files opened, Lines the
// non-blank lines read, Corrupt the lines that are not a JSON object of a
// record (including oversize ones), Skipped the lines with an unknown "v".
// Records dropped by the Since/Until/Types filter are not counted anywhere.
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

// lineRec is a parsed line; Truncated marks the minimal record MarshalLine
// falls back to.
type lineRec struct {
	model.AuditRecord
	Truncated bool `json:"truncated,omitempty"`
}

func (r *Reader) fs() sys.FS {
	if r.FS == nil {
		return sys.OSFS{}
	}
	return r.FS
}

type auditFile struct {
	path  string
	month string
	n     int
}

// files lists the audit files of Dir in chronological order; a missing
// directory is an empty list.
func (r *Reader) files() ([]auditFile, error) {
	ents, err := r.fs().ReadDir(r.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []auditFile
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if m, n, ok := parseFileName(e.Name()); ok {
			out = append(out, auditFile{path: filepath.Join(r.Dir, e.Name()), month: m, n: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].month != out[j].month {
			return out[i].month < out[j].month
		}
		return out[i].n < out[j].n
	})
	return out, nil
}

// inWindow reports whether a file of month can hold records in [since, until).
func inWindow(month string, since, until time.Time) bool {
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return true
	}
	end := start.AddDate(0, 1, 0)
	if !since.IsZero() && !end.After(since) {
		return false
	}
	if !until.IsZero() && !start.Before(until) {
		return false
	}
	return true
}

// walk feeds every line of the selected files to visit. raw excludes the
// newline and is only valid during the call; complete says whether the line
// ended with '\n'; oversize lines carry a truncated raw.
func (r *Reader) walk(since, until time.Time, st *ScanStats, visit func(file string, line int, raw []byte, complete, oversize bool) error) error {
	files, err := r.files()
	if err != nil {
		return err
	}
	for _, af := range files {
		if !inWindow(af.month, since, until) {
			continue
		}
		if err := r.walkFile(af.path, st, visit); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) walkFile(path string, st *ScanStats, visit func(file string, line int, raw []byte, complete, oversize bool) error) error {
	f, err := r.fs().OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) { // removed by a concurrent GC
			return nil
		}
		return err
	}
	defer f.Close()
	st.Files++
	br := bufio.NewReaderSize(f, 64<<10)
	var acc []byte
	for line := 1; ; line++ {
		acc = acc[:0]
		oversize := false
		complete := false
		for {
			chunk, err := br.ReadSlice('\n')
			if !oversize {
				if len(acc)+len(chunk) > maxScanLine {
					oversize = true
					acc = append(acc, chunk[:min(len(chunk), maxScanLine-len(acc))]...)
				} else {
					acc = append(acc, chunk...)
				}
			}
			if err == nil {
				complete = true
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err == io.EOF {
				break
			}
			return err
		}
		if len(acc) == 0 && !complete {
			return nil // clean EOF
		}
		raw := acc
		if complete && len(raw) > 0 {
			raw = raw[:len(raw)-1]
		}
		if oversize {
			// keep the true length visible to Lint: size is only a lower bound
			raw = acc
		}
		if err := visit(path, line, raw, complete, oversize); err != nil {
			return err
		}
		if !complete {
			return nil
		}
	}
}

// Scan calls fn for each record in chronological file order; fn errors abort.
// A line that is not a JSON object is counted as Corrupt and skipped (a
// corrupt trailing line is normal after a crash); a line with an unknown "v"
// is counted as Skipped.
func (r *Reader) Scan(opt ScanOptions, fn func(rec *model.AuditRecord, loc Location) error) (ScanStats, error) {
	var st ScanStats
	types := make(map[model.AuditType]bool, len(opt.Types))
	for _, t := range opt.Types {
		types[t] = true
	}
	err := r.walk(opt.Since, opt.Until, &st, func(file string, line int, raw []byte, _, oversize bool) error {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return nil
		}
		st.Lines++
		if oversize || trimmed[0] != '{' {
			st.Corrupt++
			return nil
		}
		var lr lineRec
		if err := json.Unmarshal(trimmed, &lr); err != nil {
			st.Corrupt++
			return nil
		}
		if lr.V != model.AuditVersion {
			st.Skipped++
			return nil
		}
		rec := &lr.AuditRecord
		if !opt.Since.IsZero() && rec.TS.Before(opt.Since) {
			return nil
		}
		if !opt.Until.IsZero() && !rec.TS.Before(opt.Until) {
			return nil
		}
		if len(types) > 0 && !types[rec.Type] {
			return nil
		}
		return fn(rec, Location{File: file, Line: line})
	})
	return st, err
}

// Finding is an `audit lint` result.
type Finding struct {
	Loc     Location `json:"loc"`
	Message string   `json:"message"`
}

// String formats the finding as "file:line: message".
func (f Finding) String() string { return fmt.Sprintf("%s:%d: %s", f.Loc.File, f.Loc.Line, f.Message) }

// Lint checks every line: valid JSON, v known, required fields per type,
// ts monotonic per file within 5 minutes skew, line ≤ 8 KiB. It is a format
// check only, never a tamper proof.
//
// Required fields (beyond ts, v, type): hit ⇒ rule_id and decision;
// eval_summary ⇒ evals; rule_dropped, rule_shadowed, promote ⇒ rule_id;
// verify ⇒ verify; diag ⇒ detail. The minimal {"truncated":true} fallback
// record is exempt from the per-type checks.
func Lint(r *Reader) ([]Finding, ScanStats, error) {
	var (
		out     []Finding
		st      ScanStats
		curFile string
		maxTS   time.Time
	)
	add := func(file string, line int, format string, args ...any) {
		out = append(out, Finding{Loc: Location{File: file, Line: line}, Message: fmt.Sprintf(format, args...)})
	}
	err := r.walk(time.Time{}, time.Time{}, &st, func(file string, line int, raw []byte, complete, oversize bool) error {
		if file != curFile {
			curFile, maxTS = file, time.Time{}
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			add(file, line, "empty line")
			return nil
		}
		st.Lines++
		if oversize || len(raw)+1 > model.MaxAuditLineBytes {
			add(file, line, "line exceeds %d bytes", model.MaxAuditLineBytes)
			if oversize {
				st.Corrupt++
				return nil
			}
		}
		var lr lineRec
		if err := json.Unmarshal(raw, &lr); err != nil || bytes.TrimSpace(raw)[0] != '{' {
			st.Corrupt++
			if !complete {
				add(file, line, "corrupt trailing line without newline (possible partial write)")
			} else {
				add(file, line, "invalid JSON object")
			}
			return nil
		}
		rec := &lr.AuditRecord
		if rec.V != model.AuditVersion {
			st.Skipped++
			add(file, line, "unknown or missing v: %d", rec.V)
			return nil
		}
		if rec.TS.IsZero() {
			add(file, line, "missing ts")
		} else {
			if !maxTS.IsZero() && rec.TS.Before(maxTS.Add(-tsSkew)) {
				add(file, line, "ts %s is more than %s before an earlier line", rec.TS.UTC().Format(time.RFC3339Nano), tsSkew)
			}
			if rec.TS.After(maxTS) {
				maxTS = rec.TS
			}
		}
		if !knownType(rec.Type) {
			add(file, line, "unknown type %q", rec.Type)
			return nil
		}
		if lr.Truncated {
			return nil
		}
		for _, missing := range missingFields(rec) {
			add(file, line, "type %s: missing %s", rec.Type, missing)
		}
		return nil
	})
	return out, st, err
}

func knownType(t model.AuditType) bool {
	switch t {
	case model.AuditHit, model.AuditEvalSummary, model.AuditRuleDropped, model.AuditRuleShadowed,
		model.AuditConfigInvalid, model.AuditVerify, model.AuditDiag, model.AuditPromote,
		model.AuditInstall, model.AuditTamper:
		return true
	}
	return false
}

func missingFields(rec *model.AuditRecord) (missing []string) {
	need := func(ok bool, name string) {
		if !ok {
			missing = append(missing, name)
		}
	}
	switch rec.Type {
	case model.AuditHit:
		need(rec.RuleID != "", "rule_id")
		need(rec.Decision != "", "decision")
	case model.AuditEvalSummary:
		need(len(rec.Evals) > 0, "evals")
	case model.AuditRuleDropped, model.AuditRuleShadowed, model.AuditPromote:
		need(rec.RuleID != "", "rule_id")
	case model.AuditVerify:
		need(rec.Verify != nil, "verify")
	case model.AuditDiag:
		need(len(rec.Detail) > 0, "detail")
	}
	return missing
}

// GC deletes files older than retention (by month end) relative to now. A
// file is removed when its month ended more than retention ago; retention
// <= 0 means Retention. It returns the full paths removed. A missing
// directory is not an error; removal failures are joined and the run goes on.
func GC(fsys sys.FS, dir string, now time.Time, retention time.Duration) (removed []string, err error) {
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	if retention <= 0 {
		retention = Retention
	}
	ents, rerr := fsys.ReadDir(dir)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, rerr
	}
	var errs []error
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		month, _, ok := parseFileName(e.Name())
		if !ok {
			continue
		}
		start, perr := time.Parse("2006-01", month)
		if perr != nil {
			continue
		}
		end := start.AddDate(0, 1, 0)
		if now.Sub(end) <= retention {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if rmerr := fsys.Remove(p); rmerr != nil {
			if !errors.Is(rmerr, fs.ErrNotExist) {
				errs = append(errs, rmerr)
			}
			continue
		}
		removed = append(removed, p)
	}
	return removed, errors.Join(errs...)
}
