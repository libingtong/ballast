//go:build windows

package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// TestWindowsAppendHandleCanBeLocked guards the open flags: a handle opened
// write-only with O_APPEND carries neither GENERIC_READ nor GENERIC_WRITE and
// LockFileEx fails with ERROR_ACCESS_DENIED, which would fail every Append.
func TestWindowsAppendHandleCanBeLocked(t *testing.T) {
	name := filepath.Join(t.TempDir(), "probe.jsonl")
	f, err := os.OpenFile(name, appendFlags|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	unlock, err := lockAppend(sys.File(f))
	if err != nil {
		t.Fatalf("lockAppend on the audit file handle: %v", err)
	}
	unlock()

	w, dir := newWriter(t, 0)
	for i := 0; i < 2; i++ {
		if err := w.Append([]model.AuditRecord{hitRec(i)}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := len(readLines(t, filepath.Join(dir, "2026-10.jsonl"))); got != 2 {
		t.Errorf("lines = %d, want 2", got)
	}
}

// The writers of the multi-process stress tests (8 processes appending in a
// tight loop, up to 3 KB a line, with Defender scanning every append) are far
// hotter than any hook, so their wait in the mutex queue is made patient: what
// they assert is that no record is lost or torn, not how long a turn takes.
// The product default stays below the hook's watchdog (see
// TestQueueWaitsFitTheWatchdog).
//
// Consequence: Go-level tests do NOT verify that no audit record is lost under
// the production waits (60 ms + 50 ms); with the production parameters a very
// hot writer can still give up and lose its record (writer 5 once wrote only
// 448 lines). That property is guarded by scripts/concurrency.sh ("one audit
// hit per denied call", real hooks, N up to 8). See architecture.md section 9.
func init() {
	if os.Getenv(childEnv) != "" {
		queueWait = time.Minute
	}
}

// A hook that cannot get the audit lock must give up, and write its error
// path, before its 150 ms watchdog fires: the mutex wait and the LockFileEx
// fallback together stay well inside it. This is only the waits' own sum: the
// pipeline has already spent about 30-40 ms on Windows (plus up to 20 ms of
// state transaction) before the audit write, so a maximal wait can still lose
// the race with the watchdog; that known gap is in architecture.md section 9.
func TestQueueWaitsFitTheWatchdog(t *testing.T) {
	if defaultQueueWait+rangeWait > 120*time.Millisecond {
		t.Errorf("queue wait %v + range wait %v leave no room under the 150 ms watchdog", defaultQueueWait, rangeWait)
	}
}
