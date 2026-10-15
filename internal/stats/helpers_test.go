package stats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// sliceSource is an in-memory ports.AuditSource.
type sliceSource struct {
	recs    []model.AuditRecord
	corrupt int
	err     error
}

func (s *sliceSource) Records(since, until time.Time, fn func(*model.AuditRecord) error) (int, error) {
	for i := range s.recs {
		r := &s.recs[i]
		if !since.IsZero() && r.TS.Before(since) {
			continue
		}
		if !until.IsZero() && !r.TS.Before(until) {
			continue
		}
		if err := fn(r); err != nil {
			return s.corrupt, err
		}
	}
	return s.corrupt, s.err
}

// fileSource reads an audit JSONL file like audit.Reader would: chronological,
// corrupt lines skipped and counted.
func fileSource(t testing.TB, path string) *sliceSource {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return parseJSONL(t, data)
}

func parseJSONL(t testing.TB, data []byte) *sliceSource {
	t.Helper()
	src := &sliceSource{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec model.AuditRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			src.corrupt++
			continue
		}
		src.recs = append(src.recs, rec)
	}
	return src
}

// captureSink records appended audit records.
type captureSink struct {
	mu   sync.Mutex
	recs []model.AuditRecord
	err  error
}

func (c *captureSink) Append(recs []model.AuditRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.recs = append(c.recs, recs...)
	return nil
}

// strictCompiler decodes rule files strictly into model.RuleFile; it stands
// in for dsl.Engine.ParseFile (U3).
type strictCompiler struct{}

func (strictCompiler) ParseFile(layer model.Layer, path string, data []byte) (*model.RuleFile, []model.Diagnostic, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var rf model.RuleFile
	if err := dec.Decode(&rf); err != nil {
		return nil, nil, err
	}
	if rf.Version != 1 {
		return &rf, []model.Diagnostic{{Severity: model.SevError, Code: "bad-version", Message: "version must be 1"}}, nil
	}
	return &rf, nil, nil
}

func (strictCompiler) Compile([]model.LoadedRule, model.Settings, string) (*model.CompiledRuleSet, error) {
	return nil, errors.New("not used")
}
