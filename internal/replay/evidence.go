package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// loadEvidenceDir collects the verify records of session from
// <dir>/<repo_inst>/<name>/*.json (layout.EvidenceDir). Unreadable or
// undecodable files are skipped.
func loadEvidenceDir(fsys sys.FS, dir, session string) []model.VerifyRecord {
	if dir == "" || session == "" {
		return nil
	}
	var out []model.VerifyRecord
	insts, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, inst := range insts {
		if !inst.IsDir() {
			continue
		}
		names, err := fsys.ReadDir(filepath.Join(dir, inst.Name()))
		if err != nil {
			continue
		}
		for _, name := range names {
			if !name.IsDir() {
				continue
			}
			files, err := fsys.ReadDir(filepath.Join(dir, inst.Name(), name.Name()))
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				data, err := fsys.ReadFile(filepath.Join(dir, inst.Name(), name.Name(), f.Name()))
				if err != nil {
					continue
				}
				var rec model.VerifyRecord
				if json.Unmarshal(data, &rec) != nil || rec.SessionID != session {
					continue
				}
				out = append(out, rec)
			}
		}
	}
	sortRecords(out)
	return out
}

// loadEvidenceFile reads --evidence <file>: a JSON array of VerifyRecord, a
// single record, or JSON lines. Every record in an explicitly supplied file
// is trusted regardless of its session_id.
func loadEvidenceFile(fsys sys.FS, path string) ([]model.VerifyRecord, error) {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("replay: reading evidence file: %w", err)
	}
	trim := bytes.TrimSpace(data)
	var out []model.VerifyRecord
	switch {
	case len(trim) == 0:
	case trim[0] == '[':
		if err := json.Unmarshal(trim, &out); err != nil {
			return nil, fmt.Errorf("%w: evidence file %s: %v", ErrUsage, path, err)
		}
	default:
		var one model.VerifyRecord
		if json.Unmarshal(trim, &one) == nil {
			out = append(out, one)
			break
		}
		sc := bufio.NewScanner(bytes.NewReader(trim))
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var rec model.VerifyRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				return nil, fmt.Errorf("%w: evidence file %s: %v", ErrUsage, path, err)
			}
			out = append(out, rec)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%w: evidence file %s: %v", ErrUsage, path, err)
		}
	}
	sortRecords(out)
	return out, nil
}

func sortRecords(recs []model.VerifyRecord) {
	sort.SliceStable(recs, func(i, j int) bool {
		if !recs[i].TStart.Equal(recs[j].TStart) {
			return recs[i].TStart.Before(recs[j].TStart)
		}
		return recs[i].JobID < recs[j].JobID
	})
}

// evidenceView answers evidence predicates from trusted records as of a
// moment in the transcript. Freshness cannot be recomputed offline (no tree
// ids of the historical work tree), so a valid pass counts as fresh unless
// the transcript shows a file write after it ended.
type evidenceView struct {
	records   []model.VerifyRecord // chronological by t_start, all visible at now
	lastWrite time.Time
	// required resolves the required names of the repo's project.json and,
	// when unavailable, the cause. nil ⇒ unavailable.
	required func() ([]string, string)
}

var _ model.EvidenceView = (*evidenceView)(nil)

// newEvidenceView keeps the records that had finished by now.
func newEvidenceView(all []model.VerifyRecord, now, lastWrite time.Time) *evidenceView {
	v := &evidenceView{lastWrite: lastWrite}
	for i := range all {
		if now.IsZero() || !all[i].TEnd.After(now) {
			v.records = append(v.records, all[i])
		}
	}
	return v
}

// withRequired sets the project.json required-names resolver.
func (v *evidenceView) withRequired(f func() ([]string, string)) *evidenceView {
	v.required = f
	return v
}

func (v *evidenceView) VerifyFresh(q model.FreshQuery) model.FreshResult {
	names := q.Names
	if len(names) == 0 {
		if v.required == nil {
			return model.FreshResult{Value: model.TriUnknown, Cause: causeRequiredAbsent}
		}
		req, cause := v.required()
		if cause != "" {
			return model.FreshResult{Value: model.TriUnknown, Cause: cause}
		}
		names = req
	}
	res := model.FreshResult{Value: model.TriTrue, Cause: "fresh", Names: names}
	for _, name := range names {
		var latest *model.VerifyRecord
		for i := range v.records {
			if v.records[i].Name == name {
				latest = &v.records[i]
			}
		}
		cause := ""
		switch {
		case latest == nil:
			cause = "no_record"
		case !latest.ValidPass():
			cause = "not_valid_pass"
		case !v.lastWrite.IsZero() && latest.TEnd.Before(v.lastWrite):
			cause = "stale"
		}
		if cause != "" {
			if res.Value == model.TriTrue {
				res.Value, res.Cause = model.TriFalse, cause
			}
			res.Stale = append(res.Stale, name)
		}
	}
	return res
}

func (v *evidenceView) LastFailureAfter(after time.Time) (model.Tri, time.Time) {
	if len(v.records) == 0 {
		return model.TriUnknown, time.Time{}
	}
	var best time.Time
	for i := range v.records {
		r := &v.records[i]
		switch r.Outcome {
		case model.OutcomeFail, model.OutcomeTimeout, model.OutcomeSignal:
			if r.TEnd.After(after) && r.TEnd.After(best) {
				best = r.TEnd
			}
		}
	}
	if best.IsZero() {
		return model.TriFalse, time.Time{}
	}
	return model.TriTrue, best
}

func (v *evidenceView) FinishedSince(since time.Time) []model.VerifyRecord {
	var out []model.VerifyRecord
	for i := range v.records {
		if v.records[i].TEnd.After(since) {
			out = append(out, v.records[i])
		}
	}
	return out
}
