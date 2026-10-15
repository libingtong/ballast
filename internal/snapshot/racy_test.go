package snapshot

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// craft rewrites the fixture's index with mutated entries and the given
// index file mtime, keeping every stat field git recorded.
func (f *fixture) craft(version int, idxMtime time.Time, mutate func(ents []bentry)) {
	f.t.Helper()
	data, err := os.ReadFile(f.repo.IndexPath)
	if err != nil {
		f.t.Fatal(err)
	}
	idx, err := ParseIndex(data)
	if err != nil {
		f.t.Fatal(err)
	}
	ents := make([]bentry, len(idx.Entries))
	for i, e := range idx.Entries {
		ents[i] = bentry{
			path: e.Path, mode: e.Mode, sha: e.SHA,
			ctime: tspec{e.CtimeS, e.CtimeNS}, mtime: tspec{e.MtimeS, e.MtimeNS},
			dev: e.Dev, ino: e.Ino, size: e.Size, uid: e.UID, gid: e.GID, flags: e.Flags & (flagAssumeValid),
		}
	}
	mutate(ents)
	if err := os.WriteFile(f.repo.IndexPath, buildIndex(version, ents), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chtimes(f.repo.IndexPath, idxMtime, idxMtime); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) entryIndex(path string) int {
	f.t.Helper()
	data, _ := os.ReadFile(f.repo.IndexPath)
	idx, err := ParseIndex(data)
	if err != nil {
		f.t.Fatal(err)
	}
	for i, e := range idx.Entries {
		if e.Path == path {
			return i
		}
	}
	f.t.Fatalf("no entry %s", path)
	return -1
}

func (f *fixture) nativeS(filter *Filter) model.TreeID {
	f.t.Helper()
	got, err := TreeS(f.opts(false), f.repo, filter)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func entryTime(f *fixture, path string) time.Time {
	f.t.Helper()
	data, _ := os.ReadFile(f.repo.IndexPath)
	idx, _ := ParseIndex(data)
	for _, e := range idx.Entries {
		if e.Path == path {
			return time.Unix(int64(e.MtimeS), int64(e.MtimeNS))
		}
	}
	f.t.Fatalf("no entry %s", path)
	return time.Time{}
}

// stale returns the tree of the real work tree with a.txt's blob replaced
// by the stale one (what trusting a stale cache entry produces).
func staleTree(f *fixture, staleSHA [20]byte) model.TreeID {
	f.t.Helper()
	data, _ := os.ReadFile(f.repo.IndexPath)
	idx, _ := ParseIndex(data)
	for i := range idx.Entries {
		if idx.Entries[i].Path == "a.txt" {
			idx.Entries[i].SHA = staleSHA
		}
	}
	return treeID(idx.Entries)
}

func TestStatCacheTrustAndRacy(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := newFixture(t)
		f.write("a.txt", "aaaa\n")
		f.write("b.txt", "bbbb\n")
		f.write("sub/c.txt", "cccc\n")
		f.commitAll("init")
		// The stale blob must exist for git to build trees containing it.
		if out, err := f.gitStdin(f.dir, nil, "STALE\n", "hash-object", "-w", "--stdin"); err != nil {
			t.Fatal(out, err)
		}
		return f
	}
	stale := blobSHA("STALE\n")
	future := time.Now().Add(time.Hour)

	t.Run("stat-clean stale entry is trusted like git does", func(t *testing.T) {
		f := setup(t)
		real := f.nativeS(nil)
		f.craft(2, future, func(e []bentry) { e[0].sha = stale })
		want, err := f.referenceErr(true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := f.nativeS(nil)
		if got != want {
			t.Errorf("%s", describe(got, want))
		}
		if got == real {
			t.Error("the stale blob should have been trusted (test setup broken)")
		}
	})

	t.Run("racy entry is rehashed", func(t *testing.T) {
		f := setup(t)
		real := f.nativeS(nil)
		// Index written in the same second as the entries were modified.
		f.craft(2, entryTime(f, "a.txt"), func(e []bentry) { e[0].sha = stale })
		want, err := f.referenceErr(true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.nativeS(nil); got != want || got != real {
			t.Errorf("%s (real %s)", describe(got, want), real)
		}
	})

	t.Run("index older than the entry is racy too", func(t *testing.T) {
		f := setup(t)
		real := f.nativeS(nil)
		f.craft(2, entryTime(f, "a.txt").Add(-3*time.Second), func(e []bentry) { e[0].sha = stale })
		if got := f.nativeS(nil); got != real {
			t.Errorf("stale entry trusted although racy: %s", got)
		}
	})

	t.Run("same second but earlier nanoseconds is treated as racy", func(t *testing.T) {
		f := setup(t)
		real := f.nativeS(nil)
		mt := entryTime(f, "a.txt")
		f.craft(2, time.Unix(mt.Unix(), 999_999_999), func(e []bentry) { e[0].sha = stale })
		if got := f.nativeS(nil); got != real {
			t.Errorf("same-second entry trusted: %s", got)
		}
	})

	t.Run("smudged entry (size 0) is rehashed", func(t *testing.T) {
		f := setup(t)
		real := f.nativeS(nil)
		f.craft(2, future, func(e []bentry) { e[0].size = 0; e[0].sha = stale })
		want, _ := f.referenceErr(true, nil, nil)
		if got := f.nativeS(nil); got != real || got != want {
			t.Errorf("%s (real %s)", describe(got, want), real)
		}
	})

	t.Run("smudged entry of an empty file is rehashed", func(t *testing.T) {
		f := newFixture(t)
		f.write("a.txt", "aaaa\n")
		f.write("e.txt", "")
		f.commitAll("init")
		if out, err := f.gitStdin(f.dir, nil, "STALE\n", "hash-object", "-w", "--stdin"); err != nil {
			t.Fatal(out, err)
		}
		real := f.nativeS(nil)
		i := f.entryIndex("e.txt")
		// The index is newer than the entry, so only the size-0 rule
		// can reveal that the recorded id is not the empty blob.
		f.craft(2, time.Now().Add(time.Hour), func(e []bentry) {
			if e[i].size != 0 || e[i].sha != emptyBlobID {
				t.Fatalf("setup: e.txt entry size=%d sha=%x", e[i].size, e[i].sha)
			}
			e[i].sha = stale
		})
		want, err := f.referenceErr(true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.nativeS(nil); got != real || got != want {
			t.Errorf("%s (real %s)", describe(got, want), real)
		}
	})

	t.Run("empty blob id constant", func(t *testing.T) {
		if hashBlob(nil) != emptyBlobID {
			t.Errorf("emptyBlobID = %x, want %x", emptyBlobID, hashBlob(nil))
		}
	})

	// Each recorded stat field that differs from the file forces a rehash.
	for _, tc := range []struct {
		name   string
		mutate func(e *bentry)
		git    bool // git compares this field too (so it must agree on the result)
	}{
		{"mtime seconds", func(e *bentry) { e.mtime.sec-- }, true},
		// git ignores sub-second differences (it rehashes only racy
		// entries); rehashing more than git does can never change a result
		// that git computes from correct stat data.
		{"mtime nanoseconds", func(e *bentry) { e.mtime.nsec++ }, false},
		{"ctime seconds", func(e *bentry) { e.ctime.sec-- }, true},
		{"ctime nanoseconds", func(e *bentry) { e.ctime.nsec++ }, false},
		{"size", func(e *bentry) { e.size++ }, true},
		{"inode", func(e *bentry) { e.ino++ }, true},
		{"device", func(e *bentry) { e.dev++ }, false},
	} {
		t.Run("stat mismatch: "+tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("no inode/ctime/dev on windows")
			}
			f := setup(t)
			real := f.nativeS(nil)
			f.craft(2, future, func(e []bentry) { e[0].sha = stale; tc.mutate(&e[0]) })
			if got := f.nativeS(nil); got != real {
				t.Errorf("stale entry trusted despite stat mismatch: got %s, real %s", got, real)
			}
			if tc.git {
				want, _ := f.referenceErr(true, nil, nil)
				if want != real {
					t.Errorf("git disagrees: git %s real %s", want, real)
				}
			}
		})
	}

	t.Run("exec bit mismatch", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no exec bit")
		}
		f := setup(t)
		real := f.nativeS(nil)
		f.craft(2, future, func(e []bentry) { e[0].mode = modeExec; e[0].sha = stale })
		want, _ := f.referenceErr(true, nil, nil)
		if got := f.nativeS(nil); got != want || got != real {
			t.Errorf("%s (real %s)", describe(got, want), real)
		}
	})

	t.Run("exec bit mismatch ignored with core.filemode=false", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no exec bit")
		}
		f := setup(t)
		f.git("config", "core.filemode", "false")
		f.craft(2, future, func(e []bentry) { e[0].mode = modeExec; e[0].sha = stale })
		want, err := f.referenceErr(true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := f.nativeS(nil)
		if got != want {
			t.Errorf("%s", describe(got, want))
		}
	})

	t.Run("assume-unchanged entries are left alone", func(t *testing.T) {
		f := setup(t)
		f.craft(2, future, func(e []bentry) { e[0].flags = flagAssumeValid; e[0].sha = stale; e[0].size = 99 })
		want, err := f.referenceErr(true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := f.nativeS(nil); got != want {
			t.Errorf("%s", describe(got, want))
		}
	})

	t.Run("index without nanoseconds compares seconds only", func(t *testing.T) {
		f := setup(t)
		f.craft(2, future, func(e []bentry) {
			for i := range e {
				e[i].ctime.nsec, e[i].mtime.nsec = 0, 0
			}
			e[0].sha = stale
		})
		if got, want := f.nativeS(nil), staleTree(f, stale); got != want {
			t.Errorf("stale entry should be trusted when only seconds can be compared: %s", describe(got, want))
		}
	})

	t.Run("v3 and v4 crafted indexes", func(t *testing.T) {
		for _, v := range []int{3, 4} {
			f := setup(t)
			real := f.nativeS(nil)
			f.craft(v, future, func(e []bentry) { e[1].sha = stale; e[1].size++ })
			want, err := f.referenceErr(true, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.nativeS(nil); got != want || got != real {
				t.Errorf("v%d: %s (real %s)", v, describe(got, want), real)
			}
		}
	})

	t.Run("unreadable index time means everything is rehashed", func(t *testing.T) {
		f := setup(t)
		real := f.nativeS(nil)
		f.craft(2, future, func(e []bentry) { e[0].sha = stale })
		idx, _ := readIndex(f.opts(false).FS, f.repo.IndexPath)
		idx.FileMtime = time.Time{}
		r := newRefresher(f.opts(false).withDefaults(), f.repo, loadSettings(f.opts(false).FS, nil, f.repo), idx, nil)
		ents, err := r.run(idx.Entries, request{refresh: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := treeID(ents); got != real {
			t.Errorf("zero index mtime must disable the stat cache: %s", got)
		}
	})
}
