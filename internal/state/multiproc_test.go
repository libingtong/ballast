package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/testenv"
)

// Multi-process tests re-exec the test binary: TestMain turns the child
// invocation (marked by childEnv) into a worker that commits to a shared key
// and prints its tally as JSON. This is the Go equivalent of
// scripts/concurrency.sh (B6 acceptance).
const (
	childEnv = "BALLAST_STATE_TEST_CHILD" // worker id
	modeEnv  = "BALLAST_STATE_TEST_MODE"  // "unique" | "same"
	countEnv = "BALLAST_STATE_TEST_COUNT"
)

type childResult struct {
	ID       int      `json:"id"`
	Applied  []int    `json:"applied"` // indices whose commit reported Applied && !Duplicate
	Dups     int      `json:"dups"`
	Timeouts int      `json:"timeouts"`
	Errors   []string `json:"errors"`
}

func TestMain(m *testing.M) {
	if id := os.Getenv(childEnv); id != "" {
		os.Exit(runChild(id))
	}
	os.Exit(m.Run())
}

func runChild(idStr string) int {
	id, _ := strconv.Atoi(idStr)
	n, _ := strconv.Atoi(os.Getenv(countEnv))
	mode := os.Getenv(modeEnv)
	lay, err := layout.Resolve(sys.OSEnv{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	store := New(Options{Layout: lay})
	res := childResult{ID: id}
	for i := 0; i < n; i++ {
		d := model.StateDelta{
			DedupeKey: fmt.Sprintf("PreToolUse:p%d-%d", id, i),
			ToolCall:  true,
			ReadPaths: []string{fmt.Sprintf("/proc%d/file%d", id, i)},
			HitRules:  []string{"r"},
			Evals:     map[string]model.EvalCount{"r": {Evals: 1, Hits: 1}},
			Summary:   &model.EventSummary{Event: "PreToolUse", Tool: "Bash", Text: fmt.Sprintf("p%d-%d", id, i)},
		}
		if mode == "same" {
			d.DedupeKey = fmt.Sprintf("PreToolUse:shared-%d", i)
		}
		info, err := store.Commit(model.ScopeKey{RepoInst: "repo", Session: "sess", Agent: "main"}, &d)
		switch {
		case err != nil:
			res.Errors = append(res.Errors, err.Error())
		case info.LockTimeout:
			res.Timeouts++
		case info.Duplicate:
			res.Dups++
		case info.Applied:
			res.Applied = append(res.Applied, i)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		return 2
	}
	return 0
}

func runWorkers(t *testing.T, procs, count int, mode string) []childResult {
	t.Helper()
	sb := testenv.New(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	results := make([]childResult, procs)
	var wg sync.WaitGroup
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(),
				childEnv+"="+strconv.Itoa(p), modeEnv+"="+mode, countEnv+"="+strconv.Itoa(count),
				layout.EnvHome+"="+sb.BallastHome, "HOME="+sb.Home, "USERPROFILE="+sb.Home)
			var out, errb bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errb
			if err := cmd.Run(); err != nil {
				t.Errorf("worker %d: %v\n%s", p, err, errb.String())
				return
			}
			if err := json.Unmarshal(out.Bytes(), &results[p]); err != nil {
				t.Errorf("worker %d output %q: %v", p, out.String(), err)
			}
		}(p)
	}
	wg.Wait()
	// Hand the layout to the caller through the sandbox env the workers used.
	t.Setenv(layout.EnvHome, sb.BallastHome)
	t.Setenv("HOME", sb.Home)
	t.Setenv("USERPROFILE", sb.Home)
	return results
}

func loadShared(t *testing.T) *Record {
	t.Helper()
	lay, err := layout.Resolve(sys.OSEnv{})
	if err != nil {
		t.Fatal(err)
	}
	rec, li := mustLoad(t, New(Options{Layout: lay}), model.ScopeKey{RepoInst: "repo", Session: "sess", Agent: "main"})
	if li.Corrupt || !li.Exists {
		t.Fatalf("shared state: %+v", li)
	}
	return rec
}

// TestMultiProcessNoLostUpdate: 2–8 independent processes commit distinct
// events to one key; the sum of applied commits equals event_seq and every
// applied path/hit/eval is present.
func TestMultiProcessNoLostUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test")
	}
	for _, procs := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("procs=%d", procs), func(t *testing.T) {
			const count = 50 // 8*50 = 400 < SeenCap, so every key stays in the dedupe window
			results := runWorkers(t, procs, count, "unique")
			applied, timeouts := 0, 0
			for _, r := range results {
				if len(r.Errors) > 0 || r.Dups != 0 {
					t.Errorf("worker %d: errors=%v dups=%d", r.ID, r.Errors, r.Dups)
				}
				applied += len(r.Applied)
				timeouts += r.Timeouts
				if len(r.Applied)+r.Timeouts != count {
					t.Errorf("worker %d accounted for %d of %d commits", r.ID, len(r.Applied)+r.Timeouts, count)
				}
			}
			rec := loadShared(t)
			if int(rec.Seq) != applied {
				t.Fatalf("lost or invented updates: applied=%d (timeouts=%d) event_seq=%d", applied, timeouts, rec.Seq)
			}
			if rec.Tools != applied || rec.Hits["r"] != applied || rec.EvalCounts["r"].Evals != applied || rec.EvalCounts["r"].Hits != applied || len(rec.ReadAt) != applied {
				t.Errorf("tools=%d hits=%d evals=%+v reads=%d, want %d", rec.Tools, rec.Hits["r"], rec.EvalCounts["r"], len(rec.ReadAt), applied)
			}
			for _, r := range results {
				for _, i := range r.Applied {
					if !rec.HasRead(fmt.Sprintf("/proc%d/file%d", r.ID, i)) || !rec.Seen(fmt.Sprintf("PreToolUse:p%d-%d", r.ID, i)) {
						t.Errorf("applied update p%d-%d is missing from state", r.ID, i)
					}
				}
			}
			if len(rec.Ring) != RingCap {
				t.Errorf("ring has %d entries", len(rec.Ring))
			}
			for i := 1; i < len(rec.Ring); i++ {
				if rec.Ring[i].Seq != rec.Ring[i-1].Seq+1 {
					t.Errorf("ring seq not consecutive: %+v", rec.Ring)
					break
				}
			}
			t.Logf("procs=%d applied=%d lock_timeouts=%d", procs, applied, timeouts)
		})
	}
}

// TestMultiProcessDuplicateDelivery: every process delivers the SAME
// tool_use_ids; each id must be applied exactly once across all processes.
func TestMultiProcessDuplicateDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test")
	}
	const procs, count = 4, 30
	results := runWorkers(t, procs, count, "same")
	appliedPer := make(map[int]int)
	for _, r := range results {
		if len(r.Errors) > 0 {
			t.Errorf("worker %d errors: %v", r.ID, r.Errors)
		}
		for _, i := range r.Applied {
			appliedPer[i]++
		}
	}
	for i, n := range appliedPer {
		if n != 1 {
			t.Errorf("event %d applied %d times", i, n)
		}
	}
	rec := loadShared(t)
	// An event whose every delivery timed out is simply missing; all others once.
	if int(rec.Seq) != len(appliedPer) || rec.Tools != len(appliedPer) {
		t.Errorf("event_seq=%d tools=%d, distinct applied events=%d", rec.Seq, rec.Tools, len(appliedPer))
	}
}
