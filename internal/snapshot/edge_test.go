package snapshot

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// hookFS lets a test observe or fail Lstat calls.
type hookFS struct {
	sys.FS
	onLstat func(name string) error
}

func (h hookFS) Lstat(name string) (fs.FileInfo, error) {
	if h.onLstat != nil {
		if err := h.onLstat(name); err != nil {
			return nil, err
		}
	}
	return h.FS.Lstat(name)
}

func TestEmptyTreeConstant(t *testing.T) {
	if got := treeID(nil); got != emptyTree {
		t.Errorf("treeID(nil) = %s", got)
	}
}

func TestEmptyAndMissingIndex(t *testing.T) {
	f := newFixture(t) // no commit, no index file yet
	if _, err := os.Stat(f.repo.IndexPath); err == nil {
		t.Fatal("fresh repo already has an index")
	}
	filt := mustFilter(t, "docs/**")
	for name, call := range map[string]func() (model.TreeID, error){
		"S": func() (model.TreeID, error) { return TreeS(f.opts(false), f.repo, filt) },
		"plain": func() (model.TreeID, error) {
			return TreeC(f.opts(false), f.repo, model.CommitForm{Kind: model.CommitPlain}, nil)
		},
		"all": func() (model.TreeID, error) {
			return TreeC(f.opts(false), f.repo, model.CommitForm{Kind: model.CommitAll}, nil)
		},
	} {
		got, err := call()
		if err != nil || got != emptyTree {
			t.Errorf("%s: %s err=%v", name, got, err)
		}
	}
	if want := f.reference(false, nil, nil); want != emptyTree {
		t.Errorf("git says %s for the empty index", want)
	}
	// Staged-only repository (no commit yet) still works.
	f.write("a.txt", "a\n")
	f.git("add", "a.txt")
	f.write("a.txt", "changed\n")
	f.relocate()
	want := f.reference(true, nil, nil)
	if got, err := TreeS(f.opts(false), f.repo, nil); err != nil || got != want {
		t.Errorf("unborn branch: %s err=%v", describe(got, want), err)
	}
}

func TestHashBlobAgainstGit(t *testing.T) {
	f := newFixture(t)
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 54, 55, 56, 57, 63, 64, 65, 119, 120, 127, 128, 1000, 4096, 1 << 20} {
		buf := make([]byte, n)
		rng.Read(buf)
		out, err := f.gitStdin(f.dir, nil, string(buf), "hash-object", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", hashBlob(buf)); got != strings.TrimSpace(out) {
			t.Errorf("size %d: %s, git %s", n, got, strings.TrimSpace(out))
		}
	}
}

func TestUnreadableFileFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs permission checks")
	}
	f := newFixture(t)
	baseRepo(f)
	f.write("src/a.go", "package changed\n")
	f.chmod("src/a.go", 0)
	t.Cleanup(func() { _ = os.Chmod(f.path("src/a.go"), 0o644) })
	if _, err := TreeS(f.opts(false), f.repo, nil); !errors.Is(err, model.ErrFallback) {
		t.Errorf("err = %v, want ErrFallback", err)
	}
	// An unreadable file that is clean needs no read.
	f.chmod("src/a.go", 0o644)
	f.git("add", "-A")
	f.git("commit", "-qm", "x")
	f.settle()
	f.chmod("src/a.go", 0)
	f.relocate()
	// chmod changes ctime and the exec bit stays: ctime mismatch forces a read.
	if _, err := TreeS(f.opts(false), f.repo, nil); err != nil && !errors.Is(err, model.ErrFallback) {
		t.Errorf("err = %v", err)
	}
}

func TestParentNotSearchable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs permission checks")
	}
	f := newFixture(t)
	baseRepo(f)
	if err := os.Chmod(f.path("src"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.path("src"), 0o755) })
	if _, err := TreeS(f.opts(false), f.repo, nil); !errors.Is(err, model.ErrFallback) {
		t.Errorf("err = %v, want ErrFallback", err)
	}
}

func TestLstatErrorFallsBack(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	opt := f.opts(false)
	opt.FS = hookFS{FS: sys.OSFS{}, onLstat: func(name string) error {
		if strings.HasSuffix(name, filepath.FromSlash("src/b.go")) {
			return &fs.PathError{Op: "lstat", Path: name, Err: syscall.EIO}
		}
		return nil
	}}
	if _, err := TreeS(opt, f.repo, nil); !errors.Is(err, model.ErrFallback) {
		t.Errorf("err = %v, want ErrFallback", err)
	}
	// plain C never touches the work tree.
	if _, err := TreeC(opt, f.repo, model.CommitForm{Kind: model.CommitPlain}, nil); err != nil {
		t.Errorf("plain C: %v", err)
	}
}

func TestNativeHonoursDeadline(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 700; i++ {
		f.write(fmt.Sprintf("d%02d/f%03d.txt", i%20, i), "x\n")
	}
	f.commitAll("many")
	f.relocate()
	clock := sys.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	opt := f.opts(false)
	opt.Clock = clock
	opt.Deadline = clock.Now().Add(time.Minute)
	if _, err := TreeS(opt, f.repo, nil); err != nil {
		t.Fatalf("before the deadline: %v", err)
	}
	calls := 0
	opt.FS = hookFS{FS: sys.OSFS{}, onLstat: func(string) error {
		if calls++; calls == 300 {
			clock.Advance(time.Hour)
		}
		return nil
	}}
	if _, err := TreeS(opt, f.repo, nil); !errors.Is(err, model.ErrDeadline) {
		t.Errorf("err = %v, want ErrDeadline", err)
	}
	if calls >= 700 {
		t.Errorf("loop ran to completion (%d lstat calls) after the deadline", calls)
	}
}

func TestStreamedLargeFiles(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.write("src/a.go", strings.Repeat("0123456789abcdef", 100)) // 1600 bytes
	opt := f.opts(false)
	opt.inMemoryLimit = 64
	want := f.reference(true, nil, nil)
	got, err := TreeS(opt, f.repo, nil)
	if err != nil || got != want {
		t.Errorf("streamed: %s err=%v", describe(got, want), err)
	}
	// Under a text conversion rule a large file cannot be scanned: fallback.
	f.git("config", "core.autocrlf", "true")
	if _, err := TreeS(opt, f.repo, nil); !errors.Is(err, model.ErrFallback) {
		t.Errorf("err = %v, want ErrFallback", err)
	}
}

func TestPathsFormDirAndPathspecs(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.write("src/a.go", "package a2\n")
	f.write("src/sub/c.go", "package c2\n")
	f.write("src/sub/inner/d.go", "package d2\n")
	f.write("docs/guide.md", "g2\n")
	call := func(form model.CommitForm) (model.TreeID, error) { return TreeC(f.opts(false), f.repo, form, nil) }
	sub := filepath.Join(f.dir, "src", "sub")

	t.Run("relative to Dir", func(t *testing.T) {
		got, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{"c.go", "inner"}, Dir: sub})
		want := f.reference(true, []string{"src/sub/c.go", "src/sub/inner"}, nil)
		if err != nil || got != want {
			t.Errorf("%s err=%v", describe(got, want), err)
		}
		if s, _ := TreeS(f.opts(false), f.repo, nil); s == got {
			t.Error("paths form must not equal S when other files changed")
		}
	})
	t.Run("dot and parent", func(t *testing.T) {
		got, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{".", "../a.go"}, Dir: sub})
		want := f.reference(true, []string{"src/sub", "src/a.go"}, nil)
		if err != nil || got != want {
			t.Errorf("%s err=%v", describe(got, want), err)
		}
	})
	t.Run("absolute path", func(t *testing.T) {
		got, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{filepath.Join(f.dir, "docs", "guide.md")}})
		want := f.reference(true, []string{"docs/guide.md"}, nil)
		if err != nil || got != want {
			t.Errorf("%s err=%v", describe(got, want), err)
		}
	})
	t.Run("Dir outside the repository", func(t *testing.T) {
		_, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{"x"}, Dir: f.sb.Root})
		if err == nil || errors.Is(err, model.ErrFallback) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("pathspec escaping the repository", func(t *testing.T) {
		if _, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{"../../x"}}); err == nil {
			t.Error("expected an error")
		}
		if _, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{filepath.Join(f.sb.Root, "x")}}); err == nil {
			t.Error("expected an error for an absolute path outside")
		}
	})
	t.Run("empty pathspec", func(t *testing.T) {
		if _, err := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{""}}); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("magic pathspec needs git", func(t *testing.T) {
		form := model.CommitForm{Kind: model.CommitPaths, Paths: []string{":(top)src/a.go"}}
		if _, err := call(form); !errors.Is(err, model.ErrFallback) {
			t.Fatalf("native: %v", err)
		}
		got, err := TreeC(f.opts(true), f.repo, form, nil)
		want := f.reference(true, []string{":(top)src/a.go"}, nil)
		if err != nil || got != want {
			t.Errorf("fallback: %s err=%v", describe(got, want), err)
		}
	})
	t.Run("amend and include do not change C", func(t *testing.T) {
		base, _ := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{"src/a.go"}})
		got, _ := call(model.CommitForm{Kind: model.CommitPaths, Paths: []string{"src/a.go"}, Include: true, Amend: true})
		if got != base {
			t.Errorf("%s != %s", got, base)
		}
		plain, _ := call(model.CommitForm{Kind: model.CommitPlain, Amend: true})
		if plain != f.reference(false, nil, nil) {
			t.Error("amend plain")
		}
	})
}

func TestUnparseableAndBadInput(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	if _, err := TreeC(f.opts(true), f.repo, model.CommitForm{Kind: model.CommitUnparseable, Reason: "dynamic arg"}, nil); err == nil || !strings.Contains(err.Error(), "dynamic arg") {
		t.Errorf("unparseable: %v", err)
	}
	if _, err := TreeC(f.opts(true), f.repo, model.CommitForm{Kind: "weird"}, nil); err == nil {
		t.Error("unknown kind")
	}
	if _, err := TreeS(f.opts(true), nil, nil); err == nil {
		t.Error("nil repo")
	}
	if _, err := TreeC(f.opts(true), nil, model.CommitForm{Kind: model.CommitPaths, Paths: []string{"x"}}, nil); err == nil {
		t.Error("nil repo, paths")
	}
}

func TestExemptEntriesAreNotExamined(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	// A file below a symlinked directory would be "removed" for git, but if
	// it is exempt the filter drops it first in both implementations.
	f.write("docs/guide.md", "changed\n")
	f.remove("docs/deep")
	opt := f.opts(false)
	seen := map[string]bool{}
	opt.FS = hookFS{FS: sys.OSFS{}, onLstat: func(name string) error { seen[name] = true; return nil }}
	filt := mustFilter(t, "docs/**")
	got, err := TreeS(opt, f.repo, filt)
	want := f.reference(true, nil, []string{"docs/**"})
	if err != nil || got != want {
		t.Fatalf("%s err=%v", describe(got, want), err)
	}
	for name := range seen {
		if strings.Contains(name, string(filepath.Separator)+"docs") {
			t.Errorf("exempt path %s was examined", name)
		}
	}
}

func TestInvalidUTF8Name(t *testing.T) {
	f := newFixture(t)
	name := "bad\xffname.txt"
	if err := os.WriteFile(f.path(name), []byte("x\n"), 0o644); err != nil {
		t.Skipf("file system rejects the name: %v", err)
	}
	f.write("ok.txt", "ok\n")
	f.commitAll("odd")
	if err := os.WriteFile(f.path(name), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.relocate()
	want := f.reference(true, nil, nil)
	if got, err := TreeS(f.opts(false), f.repo, nil); err != nil || got != want {
		t.Errorf("%s err=%v", describe(got, want), err)
	}
}

// TestSEqualsCConsistency is the acceptance row "S == C(all) ==
// C(plain after git add -u) == git add -u && git rm --cached exempt &&
// git write-tree".
func TestSEqualsCConsistency(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	filt := mustFilter(t, "**/*.md", "docs/**")
	exempt := []string{"**/*.md", "docs/**"}
	f.write("src/a.go", "package edited\n")
	f.write("README.md", "# edited\n")
	f.write("src/b.go", "package b edited\n")
	f.write("docs/guide.md", "edited\n")
	f.remove("src/sub/c.go")

	s, err := TreeS(f.opts(false), f.repo, filt)
	if err != nil {
		t.Fatal(err)
	}
	call, err := TreeC(f.opts(false), f.repo, model.CommitForm{Kind: model.CommitAll}, filt)
	if err != nil {
		t.Fatal(err)
	}
	plainBefore, _ := TreeC(f.opts(false), f.repo, model.CommitForm{Kind: model.CommitPlain}, filt)
	want := f.reference(true, nil, exempt)
	if s != call || s != want {
		t.Errorf("S=%s C(all)=%s git=%s", s, call, want)
	}
	if plainBefore == s {
		t.Error("partial staging: plain C must differ from S while tracked edits are unstaged")
	}
	f.git("add", "-u")
	plainAfter, err := TreeC(f.opts(false), f.repo, model.CommitForm{Kind: model.CommitPlain}, filt)
	if err != nil || plainAfter != s {
		t.Errorf("C(plain after add -u) = %s, S = %s (err %v)", plainAfter, s, err)
	}
	// Only exempt files changing leaves S alone.
	f.write("README.md", "# edited again\n")
	f.write("docs/deep/x.txt", "again\n")
	if s2, _ := TreeS(f.opts(false), f.repo, filt); s2 != s {
		t.Errorf("exempt-only edit changed S: %s -> %s", s, s2)
	}
	// The same filter instance serves both: an edit that is exempt in one
	// place and not in another.
	f.write("lib.md.go", "package x\n")
	f.git("add", "lib.md.go")
	if s3, _ := TreeS(f.opts(false), f.repo, filt); s3 == s {
		t.Error("lib.md.go is not exempt and must change S")
	}
}

func TestConcurrentComputation(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.write("src/a.go", "package concurrent\n")
	f.write("docs/guide.md", "x\n")
	filt := mustFilter(t, "docs/**")
	wantS := f.reference(true, nil, []string{"docs/**"})
	wantC := f.reference(false, nil, []string{"docs/**"})
	opt := f.opts(false)
	errs := make(chan string, 64)
	concurrentDo(16, func(i int) {
		for r := 0; r < 10; r++ {
			if got, err := TreeS(opt, f.repo, filt); err != nil || got != wantS {
				errs <- fmt.Sprintf("S: %s err=%v", describe(got, wantS), err)
			}
			if got, err := TreeC(opt, f.repo, model.CommitForm{Kind: model.CommitPlain}, filt); err != nil || got != wantC {
				errs <- fmt.Sprintf("C: %s err=%v", describe(got, wantC), err)
			}
			if _, err := (&Locator{FS: sys.OSFS{}}).Locate(f.dir); err != nil {
				errs <- err.Error()
			}
		}
	})
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestFilterCompileAndMatch(t *testing.T) {
	for _, bad := range [][]string{{""}, {"a\x00b"}, {"/abs"}, {"../x"}, {"a/../../x"}} {
		if _, err := CompileExempt(bad); err == nil {
			t.Errorf("CompileExempt(%q) should fail", bad)
		}
	}
	if f, err := CompileExempt(nil); err != nil || f.Exempt("anything") || !f.empty() {
		t.Errorf("empty filter: %v", err)
	}
	var nilFilter *Filter
	if nilFilter.Exempt("x") || !nilFilter.empty() {
		t.Error("nil filter must exempt nothing")
	}
	f := mustFilter(t, "**/*.md", "docs/**", "Makefile", "./build/", "gen//out/*.o", "*.LOG")
	tests := map[string]bool{
		"README.md": true, "a/b/c.md": true, "x.md/y": false, "docs/a": true, "docs": false, "docs/a/b/c": true,
		"Makefile": true, "sub/Makefile": false, "build/x": true, "build": false, "gen/out/a.o": true, "gen/out/d/a.o": false,
		"a.log": false, "a.LOG": true, "src/a.go": false, "a.markdown": false, ".md": true,
	}
	for path, want := range tests {
		if got := f.Exempt(path); got != want {
			t.Errorf("Exempt(%q) = %v, want %v", path, got, want)
		}
	}
	if got := strings.Join(f.pathspecArgs(), " "); got != ":(glob)**/*.md :(glob)docs/** :(glob)Makefile :(glob)./build/ :(glob)gen//out/*.o :(glob)*.LOG" {
		t.Errorf("pathspecArgs = %s", got)
	}
}

func FuzzExemptNeverPanics(f *testing.F) {
	for _, p := range patterns[:30] {
		f.Add(p, "docs/a/b.md")
	}
	f.Fuzz(func(t *testing.T, pat, name string) {
		fl, err := CompileExempt([]string{pat, "**/*.md", "docs/**"})
		if err != nil {
			return
		}
		_ = fl.Exempt(name)
		ents := []IndexEntry{{Path: name, Mode: modeRegular}}
		_ = fl.removeExempt(ents)
	})
}
