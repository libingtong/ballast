package hook

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func TestWatchdogFiresOnSlowPipeline(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	h.engine.sleep = 250 * time.Millisecond
	h.deps.Watchdog = 40 * time.Millisecond
	code := h.claude("PreToolUse", preToolBash)
	// The pipeline finished late and must not have written its deny.
	h.wantFailure(code)
	if exits := h.exitCalls(); len(exits) != 1 || exits[0] != 1 {
		t.Errorf("exits = %v", exits)
	}
	if got := h.stderr.String(); got != "ballast: watchdog expired after 40ms\n" {
		t.Errorf("stderr = %q", got)
	}
}

// TestWatchdogAfterCommitExitsZero: once stdout is committed the decision is
// effective; a hung cleanup is cut short with exit 0, never 1.
func TestWatchdogAfterCommitExitsZero(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	h.deps.Watchdog = 150 * time.Millisecond
	h.deps.State = slowDropStore{h.state, 600 * time.Millisecond}
	code := h.claude("SessionEnd", `{"session_id":"s","cwd":"/repo"}`)
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	exits := h.exitCalls()
	if len(exits) != 1 || exits[0] != 0 {
		t.Errorf("exits = %v, want [0]", exits)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

type slowDropStore struct {
	*fakeStore
	d time.Duration
}

func (s slowDropStore) DropSession(k model.ScopeKey) error {
	time.Sleep(s.d)
	return s.fakeStore.DropSession(k)
}

// blockingWriter holds Write until released; it records whether the write was
// complete (never partial) when the watchdog fired.
type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	buf     bytes.Buffer
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	close(b.started)
	<-b.release
	return b.buf.Write(p)
}

// TestWatchdogWaitsForStdoutWrite: the committer holds the commit mutex while
// writing, so the watchdog can neither interleave with nor cut short the write.
func TestWatchdogWaitsForStdoutWrite(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	h.deps.Watchdog = 300 * time.Millisecond
	bw := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, strings.NewReader(preToolBash), bw, &h.stderr, h.deps)
	}()
	<-bw.started
	time.Sleep(500 * time.Millisecond) // the watchdog has fired and is waiting on the mutex
	if exits := h.exitCalls(); len(exits) != 0 {
		t.Fatalf("watchdog exited during the stdout write: %v", exits)
	}
	close(bw.release)
	if code := <-done; code != 0 {
		t.Errorf("code = %d", code)
	}
	if !strings.Contains(bw.buf.String(), denyJSON) || !strings.HasSuffix(bw.buf.String(), "\n") {
		t.Errorf("stdout = %q", bw.buf.String())
	}
	// Once the write finished the late watchdog reports success, not failure.
	time.Sleep(50 * time.Millisecond)
	if exits := h.exitCalls(); len(exits) > 1 || (len(exits) == 1 && exits[0] != 0) {
		t.Errorf("exits = %v", exits)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

// TestWatchdogRace hammers the commit/watchdog boundary: whatever the timing,
// either (exit 0, complete stdout) or (exit 1, empty stdout) — never a mix.
func TestWatchdogRace(t *testing.T) {
	var ok, failed int
	for i := 0; i < 150; i++ {
		h := newHarness(t)
		h.engine.verdict = denyVerdict()
		h.engine.sleep = time.Duration(i%4) * 500 * time.Microsecond
		h.deps.Watchdog = 2 * time.Millisecond
		code := h.claude("PreToolUse", preToolBash)
		exits := h.exitCalls()
		switch code {
		case 0:
			ok++
			if !strings.Contains(h.stdout.String(), denyJSON) || !strings.HasSuffix(h.stdout.String(), "\n") {
				t.Fatalf("exit 0 with stdout %q", h.stdout.String())
			}
			for _, e := range exits {
				if e != 0 {
					t.Fatalf("exit 0 but the watchdog exited %v", exits)
				}
			}
			if h.stderr.Len() != 0 {
				t.Fatalf("exit 0 with stderr %q", h.stderr.String())
			}
		case 1:
			failed++
			h.wantFailure(code)
		default:
			t.Fatalf("code = %d", code)
		}
	}
	t.Logf("race outcomes: %d committed, %d failed open", ok, failed)
}

func TestGuardUnit(t *testing.T) {
	var exits []int
	var mu sync.Mutex
	exit := func(c int) { mu.Lock(); exits = append(exits, c); mu.Unlock() }

	// commit after expiry is refused.
	var errb bytes.Buffer
	g := newGuard(&errb, exit)
	g.onFire(time.Second)
	var out bytes.Buffer
	if err := g.commit(&out, []byte("x")); err == nil || out.Len() != 0 {
		t.Errorf("commit after expiry: %v %q", err, out.String())
	}
	g.fail("late") // already reported by the watchdog
	if strings.Count(errb.String(), "\n") != 1 {
		t.Errorf("stderr = %q", errb.String())
	}
	// fail twice prints once; commit after fail is refused.
	errb.Reset()
	g = newGuard(&errb, exit)
	g.fail("first\nsecond")
	g.fail("third")
	if errb.String() != "ballast: first second\n" {
		t.Errorf("stderr = %q", errb.String())
	}
	if err := g.commit(&out, []byte("x")); err == nil || out.Len() != 0 {
		t.Error("commit after fail must be refused")
	}
	// a failed run makes a later watchdog a no-op.
	mu.Lock()
	before := len(exits)
	mu.Unlock()
	g.onFire(time.Second)
	mu.Lock()
	if len(exits) != before {
		t.Error("watchdog fired after a reported failure")
	}
	mu.Unlock()
	// committed ⇒ exit 0.
	g = newGuard(io.Discard, exit)
	if err := g.commit(&out, nil); err != nil {
		t.Fatal(err)
	}
	g.onFire(time.Second)
	mu.Lock()
	if exits[len(exits)-1] != 0 {
		t.Errorf("exits = %v", exits)
	}
	mu.Unlock()
	// wait returns on the limit without a watchdog.
	g = newGuard(io.Discard, exit)
	start := time.Now()
	g.wait(10 * time.Millisecond)
	if time.Since(start) < 9*time.Millisecond {
		t.Error("wait returned early")
	}
	// disarm without arm is safe; arm + disarm never fires.
	g.disarm()
	var fired atomic.Bool
	g = newGuard(io.Discard, func(int) { fired.Store(true) })
	g.arm(20 * time.Millisecond)
	g.disarm()
	time.Sleep(50 * time.Millisecond)
	if fired.Load() {
		t.Error("disarmed watchdog fired")
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                                 "plain",
		"ballast: unknown native event":         "unknown native event",
		"parse event: ballast: malformed input": "parse event: malformed input",
		"multi\nline\r\ntext ":                  "multi line  text",
		"":                                      "",
	} {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
