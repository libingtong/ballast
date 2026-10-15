package snapshot

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const (
	helperRepoEnv   = "BALLAST_SNAPSHOT_HELPER_REPO"
	helperRoundsEnv = "BALLAST_SNAPSHOT_HELPER_ROUNDS"
	helperModeEnv   = "BALLAST_SNAPSHOT_HELPER_MODE"
)

// TestMain doubles as the worker of the multi-process tests: the test binary
// re-executes itself with helperRepoEnv set and computes snapshots in a
// separate process (no shared memory, separate file descriptors).
func TestMain(m *testing.M) {
	if dir := os.Getenv(helperRepoEnv); dir != "" {
		os.Exit(helperMain(dir))
	}
	os.Exit(m.Run())
}

func helperMain(dir string) int {
	rounds, _ := strconv.Atoi(os.Getenv(helperRoundsEnv))
	repo, err := (&Locator{FS: sys.OSFS{}}).Locate(dir)
	if err != nil {
		fmt.Println("ERR locate:", err)
		return 2
	}
	filter, err := CompileExempt([]string{"**/*.md", "docs/**"})
	if err != nil {
		fmt.Println("ERR filter:", err)
		return 2
	}
	opt := Options{} // all defaults: OSFS, real clock, OSEnv
	for i := 0; i < rounds; i++ {
		switch os.Getenv(helperModeEnv) {
		case "plain":
			id, err := TreeC(opt, repo, model.CommitForm{Kind: model.CommitPlain}, filter)
			if err != nil {
				fmt.Println("ERR plain:", err)
				return 1
			}
			fmt.Println("PLAIN", id)
		default:
			s, err := TreeS(opt, repo, filter)
			if err != nil {
				fmt.Println("ERR S:", err)
				return 1
			}
			c, err := TreeC(opt, repo, model.CommitForm{Kind: model.CommitAll}, filter)
			if err != nil {
				fmt.Println("ERR C:", err)
				return 1
			}
			fmt.Println("S", s)
			fmt.Println("CALL", c)
		}
	}
	return 0
}

type workerResult struct {
	lines []string
	err   error
}

func runWorkers(t *testing.T, f *fixture, n, rounds int, mode string) []workerResult {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	results := make([]workerResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(exe)
			cmd.Env = append(f.env.Environ(),
				helperRepoEnv+"="+f.dir, helperRoundsEnv+"="+strconv.Itoa(rounds), helperModeEnv+"="+mode)
			out, err := cmd.StdoutPipe()
			if err != nil {
				results[i].err = err
				return
			}
			if err := cmd.Start(); err != nil {
				results[i].err = err
				return
			}
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				results[i].lines = append(results[i].lines, sc.Text())
			}
			results[i].err = cmd.Wait()
		}(i)
	}
	wg.Wait()
	return results
}

// TestProcessesAgreeWhileExemptFilesChange: several processes compute S and
// C while another party rewrites exempt files at full speed; every answer is
// the one `git add -u && git rm --cached exempt && git write-tree` gives.
func TestProcessesAgreeWhileExemptFilesChange(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.write("src/a.go", "package multi\n")
	exempt := []string{"**/*.md", "docs/**"}
	want := f.reference(true, nil, exempt)

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			f.write("docs/guide.md", fmt.Sprintf("rev %d\n", i))
			f.write("README.md", fmt.Sprintf("# rev %d\n", i))
			time.Sleep(2 * time.Millisecond)
		}
	}()
	results := runWorkers(t, f, 6, 25, "all")
	close(stop)
	writer.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Errorf("worker %d: %v\n%s", i, r.err, strings.Join(r.lines, "\n"))
			continue
		}
		if len(r.lines) != 50 {
			t.Errorf("worker %d produced %d lines", i, len(r.lines))
		}
		for _, l := range r.lines {
			if _, id, _ := strings.Cut(l, " "); id != string(want) {
				t.Errorf("worker %d: %q, want tree %s", i, l, want)
				break
			}
		}
	}
}

// TestProcessesReadIndexWhileGitRewritesIt: git replaces the index (lock +
// rename) in a loop; readers must always see one complete index.
func TestProcessesReadIndexWhileGitRewritesIt(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	exempt := []string{"**/*.md", "docs/**"}
	contents := []string{"package one\n", "package two, a bit longer\n"}
	trees := map[string]bool{}
	for _, c := range contents {
		f.write("src/a.go", c)
		f.git("add", "src/a.go")
		trees[string(f.reference(false, nil, exempt))] = true
	}
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			f.write("src/a.go", contents[i%2])
			f.git("add", "src/a.go")
		}
	}()
	results := runWorkers(t, f, 6, 150, "plain")
	close(stop)
	writer.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Errorf("worker %d: %v\n%s", i, r.err, strings.Join(r.lines, "\n"))
			continue
		}
		for _, l := range r.lines {
			if _, id, _ := strings.Cut(l, " "); !trees[id] {
				t.Errorf("worker %d saw a tree that never existed: %q", i, l)
				break
			}
		}
	}
}
