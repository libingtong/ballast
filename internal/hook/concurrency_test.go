package hook

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// TestConcurrentInvocations runs many hook invocations in parallel over
// shared (thread-safe) ports; with -race this checks that Main keeps all of
// its state per invocation.
func TestConcurrentInvocations(t *testing.T) {
	const n = 64
	h := newHarness(t)
	h.engine.each = func(in *model.EvalInput) *model.Verdict {
		if strings.Contains(in.Event.Tool.Command, "deny") {
			return denyVerdict()
		}
		return &model.Verdict{}
	}
	h.injector.inj = &model.Injection{Text: "ctx", AnchorEmitted: true}
	h.deps.Watchdog = 5 * time.Second

	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			deny := i%2 == 0
			cmd := "ls"
			if deny {
				cmd = "deny me"
			}
			in := fmt.Sprintf(`{"session_id":"s%d","cwd":"/repo","tool_name":"Bash","tool_use_id":"tu%d","tool_input":{"command":%q}}`, i, i, cmd)
			var out, errb strings.Builder
			code := Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, strings.NewReader(in), &out, &errb, h.deps)
			switch {
			case code != 0 || errb.Len() != 0:
				errs <- fmt.Sprintf("worker %d: code=%d stderr=%q", i, code, errb.String())
			case deny && !strings.Contains(out.String(), denyJSON):
				errs <- fmt.Sprintf("worker %d: missing deny in %q", i, out.String())
			case !deny && strings.Contains(out.String(), "permissionDecision"):
				errs <- fmt.Sprintf("worker %d: spurious decision in %q", i, out.String())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if got := h.state.commitCount(); got != n {
		t.Errorf("commits = %d, want %d", got, n)
	}
	if got := len(h.audit.all()); got != n/2 {
		t.Errorf("audit records = %d, want %d", got, n/2)
	}
	seen := map[string]bool{}
	h.state.mu.Lock()
	for _, c := range h.state.commits {
		seen[c.key.Session+"/"+c.delta.DedupeKey] = true
	}
	h.state.mu.Unlock()
	if len(seen) != n {
		t.Errorf("distinct (session, key) commits = %d, want %d", len(seen), n)
	}
}

// TestConcurrentSameScope: many invocations for the SAME scope (parallel
// PostToolUse of one session); the hook sends one delta per invocation and
// loses none of them.
func TestConcurrentSameScope(t *testing.T) {
	const n = 32
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := fmt.Sprintf(`{"session_id":"same","cwd":"/repo","tool_name":"Read","tool_use_id":"tu%d","tool_input":{"file_path":"f%d.go"}}`, i, i)
			var out, errb strings.Builder
			if code := Main([]string{"--client", "claude-code", "--event", "PostToolUse"}, strings.NewReader(in), &out, &errb, h.deps); code != 0 {
				t.Errorf("worker %d: code=%d stderr=%q", i, code, errb.String())
			}
		}()
	}
	wg.Wait()
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	if len(h.state.commits) != n {
		t.Fatalf("commits = %d", len(h.state.commits))
	}
	reads := map[string]bool{}
	for _, c := range h.state.commits {
		if c.key != (model.ScopeKey{RepoInst: "0123456789abcdef", Session: "same", Agent: "main"}) {
			t.Errorf("key = %+v", c.key)
		}
		for _, p := range c.delta.ReadPaths {
			reads[p] = true
		}
	}
	if len(reads) != n {
		t.Errorf("distinct read paths = %d, want %d", len(reads), n)
	}
}
