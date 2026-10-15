package audit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/testenv"
)

// Multi-process tests re-exec the test binary (see TestMain): the Go
// equivalent of the scripts/ acceptance runs for "8 concurrent writers x
// 1000 records, every line parses".
const (
	childEnv = "BALLAST_AUDIT_TEST_CHILD" // writer id
	dirEnv   = "BALLAST_AUDIT_TEST_DIR"
	countEnv = "BALLAST_AUDIT_TEST_COUNT"
	maxEnv   = "BALLAST_AUDIT_TEST_MAX"
)

func TestMain(m *testing.M) {
	if id := os.Getenv(childEnv); id != "" {
		os.Exit(runChild(id))
	}
	os.Exit(m.Run())
}

func runChild(idStr string) int {
	id, _ := strconv.Atoi(idStr)
	n, _ := strconv.Atoi(os.Getenv(countEnv))
	max, _ := strconv.ParseInt(os.Getenv(maxEnv), 10, 64)
	w := NewWriter(Options{Dir: os.Getenv(dirEnv), MaxFileBytes: max})
	for i := 0; i < n; {
		// Mix single appends with batches; sizes vary up to ~3KB per line.
		batch := 1
		if i%7 == 0 {
			batch = 4
		}
		if i+batch > n {
			batch = n - i
		}
		recs := make([]model.AuditRecord, batch)
		for j := range recs {
			recs[j] = model.AuditRecord{
				Type: model.AuditHit, RuleID: "r", Decision: "deny",
				SessionID: fmt.Sprintf("w%d", id), EventSeq: int64(i + j),
				Matched: strings.Repeat(string(rune('a'+id)), 10+((i+j)*37)%190),
				Detail:  []byte(`{"pad":"` + strings.Repeat("p", ((i+j)*131)%2800) + `"}`),
			}
		}
		if err := w.Append(recs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		i += batch
	}
	return 0
}

func runWriters(t *testing.T, procs, count int, maxBytes int64) string {
	t.Helper()
	dir := testenv.New(t).Layout(t).AuditDir
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(), childEnv+"="+strconv.Itoa(p), dirEnv+"="+dir,
				countEnv+"="+strconv.Itoa(count), maxEnv+"="+strconv.FormatInt(maxBytes, 10))
			var errb bytes.Buffer
			cmd.Stderr = &errb
			if err := cmd.Run(); err != nil {
				t.Errorf("writer %d: %v\n%s", p, err, errb.String())
			}
		}(p)
	}
	wg.Wait()
	return dir
}

func verifyWriters(t *testing.T, dir string, procs, count int) ScanStats {
	t.Helper()
	next := make([]int64, procs)
	st, err := (&Reader{Dir: dir}).Scan(ScanOptions{}, func(rec *model.AuditRecord, loc Location) error {
		var id int
		if _, err := fmt.Sscanf(rec.SessionID, "w%d", &id); err != nil || id < 0 || id >= procs {
			t.Errorf("%v: bad session %q", loc, rec.SessionID)
			return nil
		}
		// Per-writer order is preserved within a month (append order == write order).
		if rec.EventSeq != next[id] {
			t.Errorf("%v: writer %d has seq %d, want %d", loc, id, rec.EventSeq, next[id])
		}
		next[id] = rec.EventSeq + 1
		if want := strings.Repeat(string(rune('a'+id)), 10+(int(rec.EventSeq)*37)%190); rec.Matched != want {
			t.Errorf("%v: matched field damaged (interleaved write?)", loc)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Corrupt != 0 || st.Skipped != 0 {
		t.Errorf("corrupt=%d skipped=%d, want a clean log", st.Corrupt, st.Skipped)
	}
	for id, n := range next {
		if int(n) != count {
			t.Errorf("writer %d: %d records, want %d", id, n, count)
		}
	}
	findings, _, err := Lint(&Reader{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("lint: %s", f)
	}
	return st
}

// TestMultiProcessEightWritersThousandRecords is the B8 acceptance run.
func TestMultiProcessEightWritersThousandRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test")
	}
	const procs, count = 8, 1000
	dir := runWriters(t, procs, count, 0)
	st := verifyWriters(t, dir, procs, count)
	if st.Lines != procs*count {
		t.Errorf("lines = %d, want %d", st.Lines, procs*count)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("unexpected rotation: %d files", len(ents))
	}
}

// TestMultiProcessRotation runs the same writers against a tiny size limit:
// concurrent rotation must neither lose nor corrupt a line.
func TestMultiProcessRotation(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test")
	}
	const procs, count = 6, 400
	dir := runWriters(t, procs, count, 256<<10)
	st := verifyWriters(t, dir, procs, count)
	if st.Lines != procs*count {
		t.Errorf("lines = %d, want %d", st.Lines, procs*count)
	}
	if st.Files < 3 {
		t.Errorf("only %d files; rotation did not happen", st.Files)
	}
	names, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	t.Logf("rotated into %d files", len(names))
}
