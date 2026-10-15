package evidence

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs:\n got: %s\nwant: %s", name, got, want)
	}
}

// The on-disk formats are an interface (other tools and releases read them).
func TestOnDiskFormatGolden(t *testing.T) {
	st, _, _ := newStore(t)
	exit := 0
	r := &model.VerifyRecord{
		JobID: "20261016T120000-0a0b0c", Name: "test", RepoInst: "0123456789abcdef",
		ArgvHash: "aa", ProjectJSONHash: "bb", BallastVersion: "0.0.0-dev", Tier: model.TierT0,
		SStart: "1111111111111111111111111111111111111111", SEnd: "1111111111111111111111111111111111111111",
		TStart: time.Date(2026, 10, 16, 12, 0, 0, 5, time.UTC), TEnd: time.Date(2026, 10, 16, 12, 1, 0, 0, time.UTC),
		Outcome: model.OutcomePass, ExitCode: &exit, SessionID: "s", LogPath: "/x/y.log",
	}
	if err := st.WriteRecord(r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(st.RecordPath(r))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "record.golden.json", data)
	if name := filepath.Base(st.RecordPath(r)); name != fmt.Sprintf("%020d-20261016T120000-0a0b0c.json", r.TStart.UnixNano()) {
		t.Errorf("file name %s", name)
	}

	js := &model.JobStatus{JobID: "20261016T120000-0a0b0c", Name: "test", RepoInst: "0123456789abcdef", State: model.JobRunning, PID: 77,
		StartedAt: r.TStart, Deadline: r.TStart.Add(30 * time.Minute)}
	if err := st.WriteJob(js); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(st.JobPath(js.RepoInst, js.Name, js.JobID))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "job.golden.json", data)
}

func FuzzReadRecord(f *testing.F) {
	f.Add([]byte(`{"v":1,"job_id":"j","name":"test","s_start":"a","s_end":"a","outcome":"pass"}`))
	f.Add([]byte(`{"v":1,`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte("\x00\xff"))
	f.Add([]byte(`{"t_start":"not a time"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		st, _, _ := newStore(t)
		dir := filepath.Join(st.opt.Layout.EvidenceDir, "inst", "test")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d-fuzz.json", epoch.UnixNano())), data, 0o600); err != nil {
			t.Fatal(err)
		}
		rec, err := st.Latest("inst", "test")
		if err != nil {
			if !errors.Is(err, errCorrupt) || rec != nil {
				t.Fatalf("Latest = %+v, %v", rec, err)
			}
			return
		}
		if rec == nil {
			t.Fatal("nil record without error")
		}
		_ = rec.ValidPass()
		// The view must reach a verdict whatever the file holds.
		recs, err := st.FinishedSince("inst", epoch.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		_ = recs
	})
}

func FuzzRecordNS(f *testing.F) {
	f.Add("00000000000000000001-job.json")
	f.Add("x")
	f.Add("99999999999999999999-j.json")
	f.Add("-1234567890123456789-j.json")
	f.Fuzz(func(t *testing.T, name string) {
		ns, ok := recordNS(name)
		if ok && (ns < 0 || len(name) < tsDigits+1) {
			t.Fatalf("recordNS(%q) = %d", name, ns)
		}
		if tm, ok := jobIDTime(name); ok && tm.IsZero() {
			t.Fatalf("jobIDTime(%q) zero", name)
		}
	})
}
