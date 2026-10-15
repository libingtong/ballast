package sys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
