package hook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var errWatchdog = errors.New("watchdog expired before stdout commit")

// guard owns the process-level output protocol: the single stdout commit,
// the single stderr failure line and the watchdog. All three serialize on mu,
// so a partial stdout write can never race the watchdog and the failure line
// is printed at most once.
type guard struct {
	mu        sync.Mutex
	stderr    io.Writer
	exit      func(int)
	timer     *time.Timer
	committed bool
	failed    bool
	expired   bool
	fired     chan struct{} // closed after the watchdog fired and Exit returned
}

func newGuard(stderr io.Writer, exit func(int)) *guard {
	if exit == nil {
		exit = os.Exit
	}
	return &guard{stderr: stderr, exit: exit, fired: make(chan struct{})}
}

func (g *guard) arm(d time.Duration) {
	g.timer = time.AfterFunc(d, func() { g.onFire(d) })
}

func (g *guard) disarm() {
	if g.timer != nil {
		g.timer.Stop()
	}
}

// onFire runs on the watchdog timer. Before the stdout commit it reports and
// exits 1; after the commit the decision is already effective, so a late
// expiry (hung cleanup) exits 0.
func (g *guard) onFire(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed {
		return
	}
	if g.committed {
		g.exit(0)
		return
	}
	g.expired = true
	fmt.Fprintf(g.stderr, "ballast: watchdog expired after %s\n", d)
	g.exit(1)
	close(g.fired)
}

// fail prints the exception line unless the watchdog (or an earlier failure)
// already did.
func (g *guard) fail(msg string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failed || g.expired {
		return
	}
	g.failed = true
	fmt.Fprintf(g.stderr, "ballast: %s\n", oneLine(msg))
}

// commit performs THE single stdout write. It refuses once the watchdog fired
// or a failure was reported, which keeps stdout empty on every exception path.
func (g *guard) commit(stdout io.Writer, out []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.expired || g.failed {
		return errWatchdog
	}
	g.committed = true
	if len(out) == 0 {
		return nil
	}
	n, err := stdout.Write(out)
	if err == nil && n < len(out) {
		err = io.ErrShortWrite
	}
	if err != nil {
		g.failed = true
		fmt.Fprintf(g.stderr, "ballast: write stdout: %s\n", oneLine(err.Error()))
		return err
	}
	return nil
}

// wait blocks until the watchdog fired (its Exit returned, which only happens
// in tests) or limit elapsed (Run without a watchdog).
func (g *guard) wait(limit time.Duration) {
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-g.fired:
	case <-t.C:
	}
}

// oneLine makes an error text fit the single "ballast: <msg>" stderr line;
// wrapped sentinels already carry the prefix, which is stripped.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "ballast: ", "")
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}
