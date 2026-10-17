package sys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFileAtomicLongFileName(t *testing.T) {
	dir := t.TempDir()
	// 240 bytes: valid, but "<name>.tmp.<pid>.<rand>" would exceed NAME_MAX
	// if the whole name were copied into the temp file name.
	name := strings.Repeat("é", 120)
	p := filepath.Join(dir, name)
	if err := WriteFileAtomic(OSFS{}, &SeqID{}, p, []byte("x"), 0o600, false); err != nil {
		t.Fatalf("long destination name: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "x" {
		t.Fatalf("content %q %v", b, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp file left behind: %v", ents)
	}
	if got := tempBase(name); len(got) > maxTempBase || !strings.HasPrefix(name, got) || got == "" {
		t.Errorf("tempBase = %d bytes", len(got))
	}
}

// budgetFS records the budget the final rename was given.
type budgetFS struct {
	OSFS
	got     time.Duration
	renamed bool
}

func (b *budgetFS) RenameWithin(o, n string, budget time.Duration) error {
	b.got = budget
	return b.OSFS.RenameWithin(o, n, budget)
}

func (b *budgetFS) Rename(o, n string) error {
	b.renamed = true
	return b.OSFS.Rename(o, n)
}

func TestWriteFileAtomicWithinPassesTheBudget(t *testing.T) {
	dir := t.TempDir()
	fsys := &budgetFS{}
	if err := WriteFileAtomicWithin(fsys, RandomID{}, filepath.Join(dir, "a"), []byte("x"), 0o600, false, 7*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if fsys.got != 7*time.Millisecond || fsys.renamed {
		t.Errorf("RenameWithin budget = %v, plain Rename used = %v; want 7ms through RenameWithin", fsys.got, fsys.renamed)
	}
	// Without a budget the file system's ordinary Rename is used.
	fsys = &budgetFS{}
	if err := WriteFileAtomic(fsys, RandomID{}, filepath.Join(dir, "b"), []byte("y"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	if fsys.got != 0 || !fsys.renamed {
		t.Errorf("WriteFileAtomic should use Rename: budget %v renamed %v", fsys.got, fsys.renamed)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "b")); err != nil || string(b) != "y" {
		t.Errorf("content = %q, %v", b, err)
	}
}
