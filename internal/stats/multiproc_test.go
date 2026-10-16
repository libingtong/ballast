package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

const childEnv = "BALLAST_STATS_TEST_CHILD"

// TestMain lets the test binary re-exec itself as a review writer.
func TestMain(m *testing.M) {
	if home := os.Getenv(childEnv); home != "" {
		os.Exit(childSampleLoop(home))
	}
	os.Exit(m.Run())
}

func childAuditRecords() []model.AuditRecord {
	base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	var recs []model.AuditRecord
	for i := 0; i < 30; i++ {
		recs = append(recs, hitRec("rule-a", base.Add(time.Duration(i)*time.Second), "s", fmt.Sprintf("tu%d", i), "deny"))
	}
	return recs
}

func childSampleLoop(ballastHome string) int {
	env := sys.MapEnv{Vars: map[string]string{"HOME": filepath.Dir(ballastHome), layout.EnvHome: ballastHome}}
	lay, err := layout.Resolve(env)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	clk := sys.RealClock{}
	for i := 0; i < 40; i++ {
		if _, err := Sample(&sliceSource{recs: childAuditRecords()}, lay, sys.OSFS{}, sys.RandomID{}, clk, "rule-a", 1+i%10); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

// TestSampleAcrossProcesses: several processes rewrite the same review file
// while this one reads it; a reader must never see a torn file.
func TestSampleAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped with -short")
	}
	sb := testenv.New(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var cmds []*exec.Cmd
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		cmd := exec.Command(exe, "-test.run=^$")
		cmd.Env = append(os.Environ(), childEnv+"="+sb.BallastHome)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
		go func() { done <- cmd.Wait() }()
	}
	reviewFile := filepath.Join(sb.BallastHome, "reviews", "rule-a.json")
	finished := 0
	reads := 0
	for finished < len(cmds) {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("child failed: %v", err)
			}
			finished++
		default:
			// Read the way the product does (FILE_SHARE_DELETE on Windows), so the
			// reader never blocks the children's rename-over.
			data, err := sys.OSFS{}.ReadFile(reviewFile)
			if err != nil {
				continue
			}
			reads++
			var rv Review
			if err := json.Unmarshal(data, &rv); err != nil {
				t.Fatalf("torn review file after %d reads: %v", reads, err)
			}
			if rv.V != 1 || rv.RuleID != "rule-a" || len(rv.Samples) == 0 || len(rv.Samples) != len(rv.SampleIDs) {
				t.Fatalf("inconsistent review: %+v", rv)
			}
		}
	}
	ents, _ := os.ReadDir(filepath.Dir(reviewFile))
	if len(ents) != 1 {
		t.Errorf("leftover files in reviews dir: %v", ents)
	}
	t.Logf("%d consistent reads while 4 processes wrote", reads)
}
