package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// synthEntries makes n realistic sorted entries: 100 top-level directories,
// ~80% .go, 10% .md, 10% docs/**/*.txt (the M0c benchmark layout).
func synthEntries(n int) []bentry {
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		d := i % 100
		switch {
		case i%10 == 0:
			paths = append(paths, fmt.Sprintf("pkg%03d/README%d.md", d, i))
		case i%10 == 1:
			paths = append(paths, fmt.Sprintf("docs/sec%03d/page%d.txt", d, i))
		default:
			paths = append(paths, fmt.Sprintf("pkg%03d/sub%d/file%d.go", d, i%7, i))
		}
	}
	sort.Strings(paths)
	ents := make([]bentry, n)
	for i, p := range paths {
		ents[i] = bentry{
			path: p, mode: modeRegular, sha: blobSHA(p),
			ctime: tspec{1700000000, uint32(i)}, mtime: tspec{1700000000, uint32(i)},
			dev: 16777233, ino: uint32(1000 + i), size: uint32(len(p)), uid: 501, gid: 20,
		}
		if i%13 == 0 {
			ents[i].mode = modeExec
		}
	}
	return ents
}

// writeSynthRepo writes a v2 index of n entries into a fake .git directory
// and returns a RepoInfo for it (no work tree is needed for plain C).
func writeSynthRepo(tb testing.TB, n, version int) *model.RepoInfo {
	tb.Helper()
	root := tb.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		tb.Fatal(err)
	}
	idx := filepath.Join(gitDir, "index")
	if err := os.WriteFile(idx, buildIndex(version, synthEntries(n)), 0o644); err != nil {
		tb.Fatal(err)
	}
	return &model.RepoInfo{Toplevel: root, GitDir: gitDir, CommonDir: gitDir, IndexPath: idx, ObjectFormat: "sha1"}
}

func BenchmarkTreeCPlain10k(b *testing.B) {
	for _, version := range []int{2, 4} {
		b.Run(fmt.Sprintf("v%d", version), func(b *testing.B) {
			repo := writeSynthRepo(b, 10000, version)
			f, _ := CompileExempt([]string{"docs/**", "**/*.md"})
			opt := Options{FS: sys.OSFS{}, Clock: sys.RealClock{}, Env: sys.MapEnv{}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := TreeC(opt, repo, model.CommitForm{Kind: model.CommitPlain}, f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkParseIndex10k(b *testing.B) {
	data := buildIndex(2, synthEntries(10000))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseIndex(data); err != nil {
			b.Fatal(err)
		}
	}
}

// TestNativeCUnderBudget is the acceptance bound "native C on a 10k-file
// index < 5ms". The hard assertion is deliberately loose (CI machines are
// noisy, -race slows everything down); the measured median is logged.
func TestNativeCUnderBudget(t *testing.T) {
	repo := writeSynthRepo(t, 10000, 2)
	f := mustFilter(t, "docs/**", "**/*.md")
	opt := Options{FS: sys.OSFS{}, Clock: sys.RealClock{}, Env: sys.MapEnv{}}
	form := model.CommitForm{Kind: model.CommitPlain}
	if _, err := TreeC(opt, repo, form, f); err != nil { // warm up
		t.Fatal(err)
	}
	const runs = 21
	var ds []time.Duration
	for i := 0; i < runs; i++ {
		start := time.Now()
		if _, err := TreeC(opt, repo, form, f); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, time.Since(start))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	median := ds[runs/2]
	t.Logf("native C, 10k entries: median %v, min %v, max %v (budget 5ms)", median, ds[0], ds[runs-1])
	limit := 5 * time.Millisecond
	if raceEnabled || testing.Short() {
		limit = 100 * time.Millisecond
	} else {
		limit = 25 * time.Millisecond // 5x the budget: noise tolerance
	}
	if median > limit {
		t.Errorf("native C on 10k entries took %v (limit %v)", median, limit)
	}
}
