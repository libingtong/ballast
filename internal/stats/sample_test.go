package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

func hitRec(rule string, at time.Time, session, toolUse, decision string) model.AuditRecord {
	return model.AuditRecord{
		TS: at, V: 1, Type: model.AuditHit, SessionID: session, ToolUseID: toolUse,
		RuleID: rule, RuleLayer: model.LayerUser, Generation: "gen-" + toolUse,
		Action: model.ActionLog, Target: model.ActionDeny, Decision: decision,
		Matched: "path: /repo/" + toolUse,
		Context: []model.EventSummary{{Seq: 1, Event: model.EventPreToolUse, Text: "Read: /repo/x"}},
	}
}

func TestSampleWritesReview(t *testing.T) {
	sb := testenv.New(t)
	lay := sb.Layout(t)
	base := ts("2026-10-10T00:00:00Z")
	var recs []model.AuditRecord
	for i := 0; i < 8; i++ {
		recs = append(recs, hitRec("rule-a", base.Add(time.Duration(i)*time.Minute), "s1", fmt.Sprintf("tu%d", i), "would_deny"))
		recs = append(recs, hitRec("rule-b", base.Add(time.Duration(i)*time.Minute), "s1", fmt.Sprintf("other%d", i), "deny"))
	}
	recs = append(recs, model.AuditRecord{TS: base, V: 1, Type: model.AuditDiag, RuleID: "rule-a"}) // not a hit
	clk := sys.NewFakeClock(ts("2026-10-16T00:00:00Z"))

	rv, err := Sample(&sliceSource{recs: recs}, lay, sys.OSFS{}, &sys.SeqID{}, clk, "rule-a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if rv.V != 1 || rv.RuleID != "rule-a" || !rv.ReviewedAt.Equal(clk.Now()) {
		t.Errorf("review header: %+v", rv)
	}
	if len(rv.Samples) != 3 || len(rv.SampleIDs) != 3 {
		t.Fatalf("samples = %d ids = %d", len(rv.Samples), len(rv.SampleIDs))
	}
	// Newest first: tu7, tu6, tu5.
	for i, want := range []string{"tu7", "tu6", "tu5"} {
		s := rv.Samples[i]
		if s.Record.ToolUseID != want || s.ID != rv.SampleIDs[i] || s.ID != SampleIDOf(&s.Record) {
			t.Errorf("sample %d = %s (id %s), want %s", i, s.Record.ToolUseID, s.ID, want)
		}
		if len(s.Record.Context) != 1 || s.Record.Matched == "" {
			t.Errorf("sample %d lost context/matched: %+v", i, s.Record)
		}
	}
	if rv.Generation != "gen-tu7" {
		t.Errorf("generation = %q", rv.Generation)
	}

	// The file on disk equals the returned review.
	data, err := os.ReadFile(filepath.Join(lay.ReviewsDir, "rule-a.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk Review
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.RuleID != "rule-a" || len(disk.Samples) != 3 || disk.SampleIDs[0] != rv.SampleIDs[0] {
		t.Errorf("disk review differs: %+v", disk)
	}
	if data[len(data)-1] != '\n' {
		t.Error("review file lacks trailing newline")
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(lay.ReviewsDir)
	if len(ents) != 1 {
		t.Errorf("reviews dir: %v", ents)
	}
}

func TestSampleFewerThanN(t *testing.T) {
	sb := testenv.New(t)
	lay := sb.Layout(t)
	recs := []model.AuditRecord{hitRec("rule-a", ts("2026-10-10T00:00:00Z"), "s", "tu1", "deny")}
	rv, err := Sample(&sliceSource{recs: recs}, lay, sys.OSFS{}, &sys.SeqID{}, sys.NewFakeClock(ts("2026-10-16T00:00:00Z")), "rule-a", 5)
	if err != nil || len(rv.Samples) != 1 {
		t.Fatalf("rv=%+v err=%v", rv, err)
	}
}

func TestSampleNoHitsWritesNothing(t *testing.T) {
	sb := testenv.New(t)
	lay := sb.Layout(t)
	rv, err := Sample(&sliceSource{}, lay, sys.OSFS{}, &sys.SeqID{}, sys.NewFakeClock(ts("2026-10-16T00:00:00Z")), "rule-a", 5)
	if err != nil || len(rv.Samples) != 0 {
		t.Fatalf("rv=%+v err=%v", rv, err)
	}
	if _, err := os.Stat(filepath.Join(lay.ReviewsDir, "rule-a.json")); !os.IsNotExist(err) {
		t.Errorf("review credential written without samples: %v", err)
	}
}

func TestSampleErrors(t *testing.T) {
	sb := testenv.New(t)
	lay := sb.Layout(t)
	clk := sys.NewFakeClock(ts("2026-10-16T00:00:00Z"))
	src := &sliceSource{recs: []model.AuditRecord{hitRec("rule-a", ts("2026-10-10T00:00:00Z"), "s", "tu1", "deny")}}
	for _, bad := range []string{"", "..", "../evil", "a/b", "UPPER", "ab", "rule_a", "a b c"} {
		if _, err := Sample(src, lay, sys.OSFS{}, &sys.SeqID{}, clk, bad, 1); !errors.Is(err, ErrUsage) {
			t.Errorf("rule %q: err = %v", bad, err)
		}
	}
	if _, err := Sample(src, lay, sys.OSFS{}, &sys.SeqID{}, clk, "rule-a", 0); !errors.Is(err, ErrUsage) {
		t.Errorf("n=0: %v", err)
	}
	if _, err := Sample(src, lay, sys.OSFS{}, &sys.SeqID{}, clk, "rule-a", MaxSample+1); !errors.Is(err, ErrUsage) {
		t.Errorf("n too large: %v", err)
	}
	if _, err := Sample(src, lay, sys.OSFS{}, &sys.SeqID{}, clk, "rule-a", 1<<40); !errors.Is(err, ErrUsage) {
		t.Errorf("n huge: %v", err)
	}
	if _, err := Sample(nil, lay, sys.OSFS{}, &sys.SeqID{}, clk, "rule-a", 1); err == nil {
		t.Error("nil source accepted")
	}
	boom := errors.New("disk on fire")
	if _, err := Sample(&sliceSource{err: boom}, lay, sys.OSFS{}, &sys.SeqID{}, clk, "rule-a", 1); !errors.Is(err, boom) {
		t.Errorf("source error: %v", err)
	}
	fault := sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"Rename": ""}, Err: boom}
	if _, err := Sample(src, lay, fault, &sys.SeqID{}, clk, "rule-a", 1); !errors.Is(err, boom) {
		t.Errorf("rename failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lay.ReviewsDir, "rule-a.json")); !os.IsNotExist(err) {
		t.Error("partial review left behind")
	}
}

func TestSampleIDStable(t *testing.T) {
	r := hitRec("rule-a", ts("2026-10-10T01:02:03.123456789Z"), "s1", "tu1", "deny")
	id := SampleIDOf(&r)
	// Independent computation: printf %s 'ts|s1|tu1|rule-a' | shasum -a 256 | cut -c1-16.
	if id != "c59532e3857203a0" {
		t.Fatalf("id = %q", id)
	}
	r2 := r
	r2.TS = r.TS.In(time.FixedZone("x", 8*3600)) // same instant, other zone
	if SampleIDOf(&r2) != id {
		t.Error("id depends on the zone")
	}
	for _, mut := range []func(*model.AuditRecord){
		func(r *model.AuditRecord) { r.TS = r.TS.Add(time.Nanosecond) },
		func(r *model.AuditRecord) { r.SessionID = "s2" },
		func(r *model.AuditRecord) { r.ToolUseID = "tu2" },
		func(r *model.AuditRecord) { r.RuleID = "rule-b" },
	} {
		r3 := r
		mut(&r3)
		if SampleIDOf(&r3) == id {
			t.Error("id ignores a component")
		}
	}
}

func TestSampleConcurrentWritersKeepFileValid(t *testing.T) {
	sb := testenv.New(t)
	lay := sb.Layout(t)
	base := ts("2026-10-10T00:00:00Z")
	var recs []model.AuditRecord
	for i := 0; i < 20; i++ {
		recs = append(recs, hitRec("rule-a", base.Add(time.Duration(i)*time.Second), "s", fmt.Sprintf("tu%d", i), "deny"))
	}
	src := &sliceSource{recs: recs}
	clk := sys.NewFakeClock(ts("2026-10-16T00:00:00Z"))
	ids := &sys.SeqID{}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := Sample(src, lay, sys.OSFS{}, ids, clk, "rule-a", n); err != nil {
					t.Errorf("sample: %v", err)
					return
				}
			}
		}(w + 1)
	}
	stop := make(chan struct{})
	var rd sync.WaitGroup
	rd.Add(1)
	go func() {
		defer rd.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(filepath.Join(lay.ReviewsDir, "rule-a.json"))
			if err != nil {
				continue // not created yet
			}
			var rv Review
			if err := json.Unmarshal(data, &rv); err != nil {
				t.Errorf("torn review file: %v", err)
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	rd.Wait()
}
