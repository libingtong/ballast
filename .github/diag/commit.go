//go:build ignore

// Diagnostic: lock contention of internal/state.Store.Commit across processes.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/state"
	"github.com/libingtong/ballast/internal/sys"
)

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

func child() {
	home := os.Getenv("DIAG_HOME")
	id := os.Getenv("DIAG_ID")
	n, _ := strconv.Atoi(os.Getenv("DIAG_N"))
	gapMS, _ := strconv.Atoi(os.Getenv("DIAG_GAP_MS"))
	lay, err := layout.Resolve(sys.MapEnv{Vars: map[string]string{"HOME": home, "USERPROFILE": home, "BALLAST_HOME": home + "/.ballast"}})
	if err != nil {
		panic(err)
	}
	s := state.New(state.Options{Layout: lay, Clock: sys.RealClock{}})
	key := model.ScopeKey{RepoInst: "diag", Session: "s", Agent: "main"}
	var hold, wait []time.Duration
	timeouts := 0
	for i := 0; i < n; i++ {
		// simulate the rest of a hook: ~gap ms of CPU
		end := time.Now().Add(time.Duration(gapMS) * time.Millisecond)
		for time.Now().Before(end) {
		}
		d := model.StateDelta{DedupeKey: fmt.Sprintf("Pre:%s-%d", id, i), ToolCall: true, ReadPaths: []string{"/repo/src/a.go"}}
		t0 := time.Now()
		info, err := s.Commit(key, &d)
		el := time.Since(t0)
		if err != nil {
			fmt.Println("commit error:", err)
			continue
		}
		if info.LockTimeout {
			timeouts++
			wait = append(wait, info.LockWait)
			continue
		}
		wait = append(wait, info.LockWait)
		hold = append(hold, el-info.LockWait)
	}
	fmt.Printf("child %s: commits=%d timeouts=%d hold p50=%v p95=%v max=%v | wait p50=%v p95=%v max=%v\n", id, n, timeouts,
		pct(hold, .5), pct(hold, .95), pct(hold, 1), pct(wait, .5), pct(wait, .95), pct(wait, 1))
}

func main() {
	if os.Getenv("DIAG_ID") != "" {
		child()
		return
	}
	// sleep granularity
	var sl []time.Duration
	for i := 0; i < 200; i++ {
		t0 := time.Now()
		time.Sleep(200 * time.Microsecond)
		sl = append(sl, time.Since(t0))
	}
	fmt.Printf("Sleep(200us): p50=%v p95=%v max=%v\n", pct(sl, .5), pct(sl, .95), pct(sl, 1))
	exe, _ := os.Executable()
	for _, procs := range []int{2, 4, 8} {
		for _, gap := range []int{10} {
			home, _ := os.MkdirTemp("", "diagstate")
			var wg sync.WaitGroup
			var mu sync.Mutex
			var outs []string
			for p := 0; p < procs; p++ {
				wg.Add(1)
				go func(p int) {
					defer wg.Done()
					cmd := exec.Command(exe)
					cmd.Env = append(os.Environ(), "DIAG_HOME="+home, "DIAG_ID="+strconv.Itoa(p), "DIAG_N=60", "DIAG_GAP_MS="+strconv.Itoa(gap))
					out, _ := cmd.CombinedOutput()
					mu.Lock()
					outs = append(outs, string(out))
					mu.Unlock()
				}(p)
			}
			wg.Wait()
			fmt.Printf("== procs=%d gap=%dms\n", procs, gap)
			sort.Strings(outs)
			for _, o := range outs {
				fmt.Print(o)
			}
			os.RemoveAll(home)
		}
	}
}
