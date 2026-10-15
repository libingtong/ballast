package bashparse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

const childEnv = "BALLAST_BASHPARSE_CHILD"

// TestMain lets the test binary act as a child process that parses the golden
// corpus and prints the result (see TestParseAcrossProcesses).
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		out := make([]*model.CommandList, 0, len(goldenSources))
		for _, src := range goldenSources {
			cl, err := Parse(src, Options{})
			if err != nil {
				cl = nil
			}
			out = append(out, cl)
		}
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestParseConcurrent hammers Parse from many goroutines (run with -race): the
// pooled syntax parsers and shared tables must not leak state between calls.
func TestParseConcurrent(t *testing.T) {
	want := make([]*model.CommandList, len(goldenSources))
	for i, src := range goldenSources {
		cl, err := Parse(src, Options{})
		if err != nil {
			cl = nil
		}
		want[i] = cl
	}
	const workers, rounds = 16, 40
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				i := (w*7 + r*3) % len(goldenSources)
				cl, err := Parser{}.ParseBash(goldenSources[i])
				if err != nil {
					cl = nil
				}
				if !reflect.DeepEqual(cl, want[i]) {
					errs <- fmt.Sprintf("worker %d: %q differs", w, goldenSources[i])
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestParseAcrossProcesses re-executes the test binary and checks that a fresh
// process produces byte-identical output (no dependence on process state such
// as map iteration order).
func TestParseAcrossProcesses(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	var first []byte
	for i := 0; i < 2; i++ {
		cmd := exec.Command(exe, "-test.run=NONE")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("child: %v: %s", err, stderr.String())
		}
		if first == nil {
			first = out
		} else if !bytes.Equal(first, out) {
			t.Fatal("two child processes disagree")
		}
	}
	want := make([]*model.CommandList, 0, len(goldenSources))
	for _, src := range goldenSources {
		cl, err := Parse(src, Options{})
		if err != nil {
			cl = nil
		}
		want = append(want, cl)
	}
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, b.Bytes()) {
		t.Fatal("child process output differs from in-process parse")
	}
}
