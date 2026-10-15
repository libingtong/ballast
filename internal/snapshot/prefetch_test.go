package snapshot

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestPrefetchMatchesSerialAndGit: with more tracked entries than
// prefetchMin the leaf Lstat calls are issued in parallel; the result must be
// identical to the serial path and to git, including removals, type changes,
// a symlinked parent and assume-unchanged entries.
func TestPrefetchMatchesSerialAndGit(t *testing.T) {
	requireGit(t)
	f := newFixture(t)
	const dirs, perDir = 30, 90 // 2700 tracked files > prefetchMin
	for d := 0; d < dirs; d++ {
		for i := 0; i < perDir; i++ {
			f.write(fmt.Sprintf("d%02d/f%03d.go", d, i), fmt.Sprintf("package d%d // %d\n", d, i))
		}
	}
	f.write("docs/guide.md", "guide\n")
	f.commitAll("many files")
	if n := len(f.git("ls-files")) / 10; n < prefetchMin/2 {
		t.Fatalf("fixture too small: %d", n)
	}

	f.write("d00/f000.go", "package changed\n")
	f.write("d01/f001.go", "package changed but longer\n")
	f.remove("d02/f002.go")
	f.remove("d03") // whole directory removed
	f.write("docs/guide.md", "changed guide\n")
	f.remove("d05/f005.go")
	f.write("d05/f005.go/inner.txt", "file replaced by directory\n")
	f.git("update-index", "--assume-unchanged", "d06/f006.go")
	f.write("d06/f006.go", "package assumed\n")
	if runtime.GOOS != "windows" {
		f.remove("d07/f007.go")
		f.symlink("f008.go", "d07/f007.go")
		f.chmod("d08/f008.go", 0o755)
	}
	f.relocate()

	filt := mustFilter(t, docsExempt...)
	forms := map[string]func(Options) (model.TreeID, error){
		"S": func(o Options) (model.TreeID, error) { return TreeS(o, f.repo, filt) },
		"C all": func(o Options) (model.TreeID, error) {
			return TreeC(o, f.repo, model.CommitForm{Kind: model.CommitAll}, filt)
		},
		"C paths": func(o Options) (model.TreeID, error) {
			return TreeC(o, f.repo, model.CommitForm{Kind: model.CommitPaths, Paths: []string{"d00", "d05", "d07"}}, filt)
		},
	}
	want := f.reference(true, nil, docsExempt)
	saved := prefetchMin
	defer func() { prefetchMin = saved }()
	for name, call := range forms {
		prefetchMin = 1 << 30
		serial, err := call(f.opts(false))
		if err != nil {
			t.Fatalf("%s serial: %v", name, err)
		}
		prefetchMin = 1
		par, err := call(f.opts(false))
		if err != nil {
			t.Fatalf("%s prefetch: %v", name, err)
		}
		if par != serial {
			t.Errorf("%s: prefetch %s differs from serial %s", name, par, serial)
		}
		if name != "C paths" && par != want {
			t.Errorf("%s: %s", name, describe(par, want))
		}
	}
}
