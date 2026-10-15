package snapshot

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// goldenEntries renders parsed entries like `git ls-files -s`.
func goldenEntries(idx *Index) string {
	var b strings.Builder
	for _, e := range idx.Entries {
		fmt.Fprintf(&b, "%06o %x 0\t%s\n", e.Mode, e.SHA, e.Path)
	}
	return b.String()
}

func TestParseIndexGolden(t *testing.T) {
	wantEntries, err := os.ReadFile("testdata/index-entries.golden")
	if err != nil {
		t.Fatal(err)
	}
	wantTree, err := os.ReadFile("testdata/index-tree.golden")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file    string
		version int
	}{{"testdata/index-v2.bin", 2}, {"testdata/index-v4.bin", 4}} {
		t.Run(filepath.Base(tc.file), func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			idx, err := ParseIndex(data)
			if err != nil {
				t.Fatal(err)
			}
			if idx.Version != tc.version {
				t.Errorf("version = %d, want %d", idx.Version, tc.version)
			}
			if got := goldenEntries(idx); got != string(wantEntries) {
				t.Errorf("entries differ from git ls-files -s:\n%s\nwant:\n%s", got, wantEntries)
			}
			if got := treeID(idx.Entries); string(got) != strings.TrimSpace(string(wantTree)) {
				t.Errorf("tree = %s, want %s", got, bytes.TrimSpace(wantTree))
			}
		})
	}
}

func sampleEntries() []bentry {
	e := func(path string, mode uint32, content string) bentry {
		return bentry{path: path, mode: mode, sha: blobSHA(content), ctime: tspec{1, 2}, mtime: tspec{3, 4}, dev: 5, ino: 6, size: uint32(len(content)), uid: 7, gid: 8}
	}
	long := strings.Repeat("d/", 2100) + "file" // > 0xfff bytes: name length field saturates
	return []bentry{
		e("README.md", modeRegular, "r"),
		e("a.b", modeRegular, "ab"),
		e("a/b/c.txt", modeRegular, "c"),
		e("a/b/d.txt", modeExec, "d"),
		e("a/link", modeSymlink, "target"),
		e("a0", modeRegular, ""),
		e(long, modeRegular, "long"),
		e("sub", modeGitlink, "gl"),
		e("z/é.txt", modeRegular, "u"),
	}
}

func TestParseIndexSynthesized(t *testing.T) {
	ents := sampleEntries()
	for _, v := range []int{2, 3, 4} {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			in := ents
			if v == 3 {
				in = append([]bentry(nil), ents...)
				in[2].forceExtended = true // extended bit set, flags zero
			}
			idx, err := ParseIndex(buildIndex(v, in, [2]string{"TREE", "garbage"}, [2]string{"REUC", ""}))
			if err != nil {
				t.Fatal(err)
			}
			if idx.Version != v || len(idx.Entries) != len(in) {
				t.Fatalf("version %d entries %d", idx.Version, len(idx.Entries))
			}
			for i, e := range idx.Entries {
				w := in[i]
				if e.Path != w.path || e.Mode != w.mode || e.SHA != w.sha || e.Size != w.size ||
					e.CtimeS != w.ctime.sec || e.CtimeNS != w.ctime.nsec || e.MtimeS != w.mtime.sec || e.MtimeNS != w.mtime.nsec ||
					e.Dev != w.dev || e.Ino != w.ino || e.UID != w.uid || e.GID != w.gid {
					t.Errorf("entry %d = %+v, want %+v", i, e, w)
				}
			}
		})
	}
}

func TestParseIndexRejectsUnsupported(t *testing.T) {
	base := sampleEntries()
	mod := func(f func(e []bentry) []bentry) []bentry { return f(append([]bentry(nil), base...)) }
	good := buildIndex(2, base)
	zeroSum := func() []byte {
		d := buildIndex(2, base)
		copy(d[len(d)-20:], make([]byte, 20))
		return d
	}
	badSum := append([]byte(nil), good...)
	badSum[20] ^= 0xff
	trailing := buildIndex(2, base, [2]string{"TREE", "x"})
	trailing = append(append([]byte(nil), trailing[:len(trailing)-20]...), 1, 2, 3)
	trailing = append(trailing, make([]byte, 20)...)

	tests := []struct {
		name     string
		data     []byte
		fallback bool // want ErrFallback; otherwise success
	}{
		{"valid", good, false},
		{"zero checksum accepted (index.skipHash)", zeroSum(), false},
		{"empty", nil, true},
		{"too short", good[:20], true},
		{"bad magic", append([]byte("XXXX"), good[4:]...), true},
		{"version 1", buildIndex(1, base), true},
		{"version 5", buildIndex(5, base), true},
		{"bad checksum", badSum, true},
		{"unmerged stage 1", buildIndex(2, mod(func(e []bentry) []bentry { e[1].flags = 1 << 12; return e })), true},
		{"unmerged stage 3", buildIndex(2, mod(func(e []bentry) []bentry { e[1].flags = 3 << 12; return e })), true},
		{"skip-worktree", buildIndex(3, mod(func(e []bentry) []bentry { e[1].ext = 0x4000; return e })), true},
		{"intent-to-add", buildIndex(3, mod(func(e []bentry) []bentry { e[1].ext = 0x2000; return e })), true},
		{"unknown extended flag", buildIndex(3, mod(func(e []bentry) []bentry { e[1].ext = 0x0100; return e })), true},
		{"extended bit in v2", buildIndex(2, mod(func(e []bentry) []bentry { e[1].forceExtended = true; return e })), true},
		{"sparse directory entry", buildIndex(2, mod(func(e []bentry) []bentry { e[1].mode = 0o040000; return e })), true},
		{"odd mode", buildIndex(2, mod(func(e []bentry) []bentry { e[1].mode = 0o100664; return e })), true},
		{"out of order", buildIndex(2, mod(func(e []bentry) []bentry { e[0], e[1] = e[1], e[0]; return e })), true},
		{"duplicate path", buildIndex(2, mod(func(e []bentry) []bentry { e[1].path = e[0].path; return e })), true},
		{"split index extension", buildIndex(2, base, [2]string{"link", strings.Repeat("\x00", 40)}), true},
		{"sparse index extension", buildIndex(2, base, [2]string{"sdir", ""}), true},
		{"unknown required extension", buildIndex(2, base, [2]string{"zzzz", "x"}), true},
		{"unknown optional extension", buildIndex(2, base, [2]string{"ZZZZ", "x"}), false},
		{"trailing garbage", trailing, true},
		{"entry count too large", func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint32(d[8:], 1<<30)
			sum := sha1.Sum(d[:len(d)-20])
			copy(d[len(d)-20:], sum[:])
			return d
		}(), true},
		{"truncated body", func() []byte {
			d := append([]byte(nil), good[:100]...)
			sum := sha1.Sum(d)
			return append(d, sum[:]...)
		}(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := ParseIndex(tc.data)
			if tc.fallback {
				if !errors.Is(err, model.ErrFallback) {
					t.Fatalf("err = %v, want ErrFallback", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(idx.Entries) != len(base) {
				t.Errorf("entries = %d", len(idx.Entries))
			}
		})
	}
}

func TestParseIndexV4Corruption(t *testing.T) {
	good := buildIndex(4, sampleEntries())
	// Corrupt the first byte of the first entry's path varint area: the
	// checksum must be recomputed for the parser to get that far.
	fix := func(d []byte) []byte {
		sum := sha1.Sum(d[:len(d)-20])
		copy(d[len(d)-20:], sum[:])
		return d
	}
	d := append([]byte(nil), good...)
	d[12+62] = 5 // strip 5 bytes from an empty previous path
	if _, err := ParseIndex(fix(d)); !errors.Is(err, model.ErrFallback) {
		t.Errorf("bad strip length: %v", err)
	}
}

func TestEncodeDecodeVarint(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 129, 16383, 16384, 1 << 20, 1<<35 + 12345} {
		enc := encodeVarint(v)
		got, n := decodeVarint(enc)
		if got != v || n != len(enc) {
			t.Errorf("roundtrip %d: got %d (n=%d, len=%d)", v, got, n, len(enc))
		}
	}
	if _, n := decodeVarint(nil); n != 0 {
		t.Error("empty input must fail")
	}
	if _, n := decodeVarint([]byte{0x80}); n != 0 {
		t.Error("truncated varint must fail")
	}
}

// TestParseIndexAcceptedByGit makes sure the synthesized indexes are real
// git indexes (so the other tests built on them mean something) and that git
// computes the same tree for them.
func TestParseIndexAcceptedByGit(t *testing.T) {
	requireGit(t)
	f := newFixture(t)
	empty := strings.TrimSpace(f.git("hash-object", "-w", "--stdin"))
	for _, v := range []int{2, 3, 4} {
		ents := sampleEntries()
		for i := range ents {
			ents[i].sha = [20]byte{}
			copy(ents[i].sha[:], mustHex(t, empty))
			ents[i].mode = modeRegular
		}
		if v == 3 {
			ents[3].forceExtended = true
		}
		data := buildIndex(v, ents)
		idxFile := filepath.Join(t.TempDir(), "index")
		if err := os.WriteFile(idxFile, data, 0o644); err != nil {
			t.Fatal(err)
		}
		env := []string{"GIT_INDEX_FILE=" + idxFile}
		out := f.gitIn(f.dir, env, "ls-files", "-s", "-z")
		parsed, err := ParseIndex(data)
		if err != nil {
			t.Fatal(err)
		}
		var want strings.Builder
		for _, e := range parsed.Entries {
			fmt.Fprintf(&want, "%06o %x 0\t%s\x00", e.Mode, e.SHA, e.Path)
		}
		if out != want.String() {
			t.Errorf("v%d: git reads\n%q\nparser reads\n%q", v, out, want.String())
		}
		tree := strings.TrimSpace(f.gitIn(f.dir, env, "write-tree"))
		if got := treeID(parsed.Entries); string(got) != tree {
			t.Errorf("v%d: tree %s, git %s", v, got, tree)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	var out []byte
	for i := 0; i+1 < len(s); i += 2 {
		var b byte
		if _, err := fmt.Sscanf(s[i:i+2], "%02x", &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func FuzzParseIndex(f *testing.F) {
	for _, v := range []int{2, 3, 4} {
		f.Add(buildIndex(v, sampleEntries(), [2]string{"TREE", "x"}))
	}
	for _, name := range []string{"testdata/index-v2.bin", "testdata/index-v4.bin"} {
		if data, err := os.ReadFile(name); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte("DIRC\x00\x00\x00\x02\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		check := func(data []byte) {
			idx, err := ParseIndex(data)
			if err != nil {
				if !errors.Is(err, model.ErrFallback) {
					t.Fatalf("non-fallback error: %v", err)
				}
				return
			}
			for i := 1; i < len(idx.Entries); i++ {
				if idx.Entries[i-1].Path >= idx.Entries[i].Path {
					t.Fatal("entries not strictly sorted")
				}
			}
			_ = treeID(idx.Entries)
		}
		check(data)
		if len(data) >= 32 { // reach past the checksum gate
			fixed := append([]byte(nil), data...)
			sum := sha1.Sum(fixed[:len(fixed)-20])
			copy(fixed[len(fixed)-20:], sum[:])
			check(fixed)
		}
	})
}
