package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// baseRepo commits a small but varied tree: nested directories, an
// executable, a symlink, an empty file, names with spaces and non-ASCII
// characters, and the exempt candidates (*.md, docs/**).
func baseRepo(f *fixture) {
	f.t.Helper()
	f.write("README.md", "# readme\n")
	f.write("docs/guide.md", "guide\n")
	f.write("docs/deep/x.txt", "deep\n")
	f.write("src/a.go", "package a\n")
	f.write("src/b.go", "package b\n")
	f.write("src/sub/c.go", "package c\n")
	f.write("src/sub/inner/d.go", "package d\n")
	f.write("bin/run.sh", "#!/bin/sh\necho hi\n")
	f.chmod("bin/run.sh", 0o755)
	f.write("empty.txt", "")
	f.write("a b/space name.txt", "space\n")
	f.write("uni/é.txt", "unicode\n")
	f.write("a.b", "dot before slash\n")
	f.write("a0", "zero after slash\n")
	f.write("a/z.go", "package z\n")
	if runtime.GOOS != "windows" {
		f.symlink("src/a.go", "link")
	}
	f.commitAll("base")
}

var (
	noExempt     []string
	docsExempt   = []string{"**/*.md", "docs/**"}
	onlyDocs     = []string{"docs/**"}
	onlyMarkdown = []string{"*.md"}
)

type scenario struct {
	name   string
	setup  func(f *fixture) // runs after baseRepo, before mutate (committed state tweaks)
	mutate func(f *fixture) // work tree / index changes under test
	exempt []string         // exempt globs
	// fallback: refresh forms must report ErrFallback natively and still
	// equal git through the fallback. plainNative: the plain C form must
	// still be native (default true).
	fallback   bool
	plainFalls bool // plain C also needs the fallback
	unixOnly   bool
	cfg        [][2]string // git config k v applied before mutate
}

func scenarios() []scenario {
	return []scenario{
		{name: "clean"},
		{name: "clean exempt", exempt: docsExempt},
		{name: "modify same size", mutate: func(f *fixture) { f.write("src/a.go", "package X\n") }},
		{name: "modify different size", mutate: func(f *fixture) { f.write("src/a.go", "package a // longer\n") }},
		{name: "modify to empty", mutate: func(f *fixture) { f.write("src/a.go", "") }},
		{name: "modify empty file", mutate: func(f *fixture) { f.write("empty.txt", "now not empty\n") }},
		{name: "modify exempt only", exempt: docsExempt, mutate: func(f *fixture) {
			f.write("README.md", "# changed\n")
			f.write("docs/deep/x.txt", "changed deep\n")
		}},
		{name: "modify code and exempt", exempt: docsExempt, mutate: func(f *fixture) {
			f.write("README.md", "# changed\n")
			f.write("src/b.go", "package B\n")
		}},
		{name: "exec bit on", unixOnly: true, mutate: func(f *fixture) { f.chmod("src/b.go", 0o755) }},
		{name: "exec bit off", unixOnly: true, mutate: func(f *fixture) { f.chmod("bin/run.sh", 0o644) }},
		{name: "exec bit and content", unixOnly: true, mutate: func(f *fixture) {
			f.write("bin/run.sh", "#!/bin/sh\necho changed\n")
			f.chmod("bin/run.sh", 0o644)
		}},
		{name: "filemode false ignores chmod", unixOnly: true, cfg: [][2]string{{"core.filemode", "false"}},
			mutate: func(f *fixture) { f.chmod("src/b.go", 0o755); f.chmod("bin/run.sh", 0o644) }},
		{name: "filemode false with content", unixOnly: true, cfg: [][2]string{{"core.filemode", "false"}},
			mutate: func(f *fixture) {
				f.write("bin/run.sh", "changed\n")
				f.write("src/b.go", "changed\n")
				f.chmod("src/b.go", 0o755)
			}},
		{name: "delete file", mutate: func(f *fixture) { f.remove("src/b.go") }},
		{name: "delete directory", mutate: func(f *fixture) { f.remove("src/sub") }},
		{name: "delete everything", mutate: func(f *fixture) {
			for _, p := range []string{"README.md", "docs", "src", "bin", "empty.txt", "a b", "uni", "a.b", "a0", "a", "link"} {
				f.remove(p)
			}
		}},
		{name: "delete exempt", exempt: docsExempt, mutate: func(f *fixture) { f.remove("docs"); f.remove("README.md") }},
		{name: "untracked file is ignored", mutate: func(f *fixture) { f.write("src/new.go", "package new\n"); f.write("newdir/x", "x") }},
		{name: "symlink retargeted", unixOnly: true, mutate: func(f *fixture) { f.remove("link"); f.symlink("src/b.go", "link") }},
		{name: "symlink to regular", unixOnly: true, mutate: func(f *fixture) { f.remove("link"); f.write("link", "now a file\n") }},
		{name: "regular to symlink", unixOnly: true, mutate: func(f *fixture) { f.remove("src/b.go"); f.symlink("a.go", "src/b.go") }},
		{name: "symlink deleted", unixOnly: true, mutate: func(f *fixture) { f.remove("link") }},
		{name: "file replaced by directory", mutate: func(f *fixture) { f.remove("src/b.go"); f.write("src/b.go/inner.txt", "x") }},
		{name: "directory replaced by file", mutate: func(f *fixture) { f.remove("src/sub"); f.write("src/sub", "now a file") }},
		{name: "directory replaced by symlink", unixOnly: true, mutate: func(f *fixture) {
			f.remove("src/sub")
			f.symlink("../a b", "src/sub")
		}},
		{name: "parent replaced by symlink to a copy", unixOnly: true, mutate: func(f *fixture) {
			if err := os.Rename(f.path("src"), f.path("src-copy")); err != nil {
				f.t.Fatal(err)
			}
			f.symlink("src-copy", "src")
		}},
		{name: "space and unicode names", mutate: func(f *fixture) { f.write("a b/space name.txt", "changed\n"); f.write("uni/é.txt", "changed\n") }},
		{name: "dot slash ordering", mutate: func(f *fixture) { f.write("a.b", "x\n"); f.write("a0", "y\n"); f.write("a/z.go", "package Z\n") }},
		{name: "crlf content without conversion", mutate: func(f *fixture) { f.write("src/a.go", "package a\r\nfunc x() {}\r\n") }},
		{name: "binary content", mutate: func(f *fixture) { f.write("src/a.go", "\x00\x01\x02\xff\r\n") }},
		{name: "staged and unstaged", mutate: func(f *fixture) {
			f.write("src/a.go", "package staged\n")
			f.git("add", "src/a.go")
			f.write("src/a.go", "package staged and more\n")
			f.write("src/b.go", "package b2\n")
		}},
		{name: "staged new file", mutate: func(f *fixture) {
			f.write("src/new.go", "new\n")
			f.git("add", "src/new.go")
			f.write("src/new.go", "newer\n")
		}},
		{name: "staged deletion", mutate: func(f *fixture) { f.git("rm", "-q", "src/b.go") }},
		{name: "staged exempt differing from head and worktree", exempt: docsExempt, mutate: func(f *fixture) {
			f.write("docs/guide.md", "staged\n")
			f.git("add", "docs/guide.md")
			f.write("docs/guide.md", "worktree\n")
		}},
		{name: "assume unchanged", mutate: func(f *fixture) {
			f.git("update-index", "--assume-unchanged", "src/a.go", "src/sub/c.go")
			f.write("src/a.go", "package assumed\n")
			f.remove("src/sub/c.go")
			f.write("src/b.go", "package b3\n")
		}},
		{name: "index version 4", mutate: func(f *fixture) {
			f.git("update-index", "--index-version", "4")
			f.write("src/sub/inner/d.go", "package D\n")
			f.remove("a/z.go")
		}},
		{name: "index version 4 exempt", exempt: docsExempt, mutate: func(f *fixture) {
			f.git("update-index", "--index-version", "4")
			f.write("docs/deep/x.txt", "package D\n")
		}},
		{name: "crlf with autocrlf false and text attr absent", mutate: func(f *fixture) { f.write("README.md", "a\r\nb\r\n") }},

		// ---- cases that must use the git fallback ----
		{name: "split index", fallback: true, plainFalls: true, mutate: func(f *fixture) {
			f.git("update-index", "--split-index")
			f.write("src/a.go", "package split\n")
		}},
		{name: "skip-worktree", fallback: true, plainFalls: true, mutate: func(f *fixture) {
			f.git("update-index", "--skip-worktree", "src/a.go")
			f.write("src/b.go", "package b\n// edit\n")
		}},
		{name: "intent to add", fallback: true, plainFalls: true, mutate: func(f *fixture) {
			f.write("src/new.go", "package n\n")
			f.git("add", "-N", "src/new.go")
		}},
		{name: "unmerged entries", fallback: true, plainFalls: true, mutate: func(f *fixture) { makeConflict(f) }},
		{name: "gitlink removed by deeper exempt pattern", exempt: []string{"vendor/sub/**"}, mutate: addGitlink},
		{name: "gitlink removed by deeper literal exempt", exempt: []string{"vendor/sub/x.txt"}, mutate: addGitlink},
		{name: "gitlink removed by exact exempt", exempt: []string{"vendor/sub"}, mutate: addGitlink},
		{name: "gitlink removed by star exempt", exempt: []string{"vendor/*"}, mutate: addGitlink},
		{name: "gitlink kept by unrelated exempt", exempt: []string{"vendor/other/**", "docs/**"}, mutate: addGitlink},
		{name: "gitlink kept by sibling prefix exempt", exempt: []string{"vendor/sub2/**"}, mutate: addGitlink},
		{name: "file replaced by nested repository", fallback: true, mutate: func(f *fixture) {
			f.remove("src/b.go")
			nestedRepo(f, "src/b.go")
		}},
		{name: "symlink replaced by nested repository", unixOnly: true, fallback: true, mutate: func(f *fixture) {
			f.remove("link")
			nestedRepo(f, "link")
		}},
		{name: "file replaced by directory holding a gitfile", fallback: true, mutate: func(f *fixture) {
			f.remove("src/b.go")
			f.write("src/b.go/.git", "gitdir: ../.git\n")
		}},
		{name: "gitlink with missing directory", mutate: func(f *fixture) {
			addGitlinks(f, "vendor/sub")
			f.write("vendor/keep.txt", "k\n")
			f.git("add", "vendor/keep.txt")
		}},
		{name: "gitlink with uninitialised directory", mutate: func(f *fixture) {
			addGitlinks(f, "vendor/sub")
			if err := os.MkdirAll(f.path("vendor/sub"), 0o755); err != nil {
				f.t.Fatal(err)
			}
			f.write("src/a.go", "package ui\n")
		}},
		{name: "gitlink with uninitialised directory and files", mutate: func(f *fixture) {
			addGitlinks(f, "vendor/sub")
			f.write("vendor/sub/untracked.txt", "u\n")
			f.write("src/a.go", "package ui\n")
		}},
		{name: "gitlink with uninitialised directory exempt", exempt: []string{"vendor/**"}, mutate: func(f *fixture) {
			addGitlinks(f, "vendor/sub")
			f.write("vendor/sub/untracked.txt", "u\n")
		}},
		{name: "checked-out submodule", mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			f.gitIn(f.path("vendor/sub"), nil, "commit", "-q", "--allow-empty", "-m", "moved on")
		}},
		{name: "checked-out submodule unchanged", mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
		}},
		{name: "checked-out submodule with packed refs", mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			f.gitIn(f.path("vendor/sub"), nil, "commit", "-q", "--allow-empty", "-m", "moved on")
			f.gitIn(f.path("vendor/sub"), nil, "pack-refs", "--all")
		}},
		{name: "checked-out submodule on a detached head", mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			f.gitIn(f.path("vendor/sub"), nil, "commit", "-q", "--allow-empty", "-m", "moved on")
			f.gitIn(f.path("vendor/sub"), nil, "checkout", "-q", "--detach", "HEAD~1")
			f.write("src/a.go", "package ui\n")
		}},
		{name: "checked-out submodule with a gitfile", mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			f.gitIn(f.path("vendor/sub"), nil, "commit", "-q", "--allow-empty", "-m", "moved on")
			moved := f.path(".git/modules/sub")
			if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
				f.t.Fatal(err)
			}
			if err := os.Rename(f.path("vendor/sub/.git"), moved); err != nil {
				f.t.Fatal(err)
			}
			f.write("vendor/sub/.git", "gitdir: "+moved+"\n")
		}},
		{name: "checked-out submodule with unborn head", fallback: true, mutate: func(f *fixture) {
			addGitlinks(f, "vendor/sub")
			f.gitIn(f.dir, nil, "init", "-q", "-b", "main", f.path("vendor/sub"))
			f.write("src/a.go", "package ui\n")
		}},
		{name: "checked-out submodule with dirty content", mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			f.write("vendor/sub/inner.txt", "modified\n")
			f.write("vendor/sub/untracked.txt", "u\n")
		}},
		{name: "checked-out submodule exempt", exempt: []string{"vendor/**"}, mutate: func(f *fixture) {
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			f.gitIn(f.path("vendor/sub"), nil, "commit", "-q", "--allow-empty", "-m", "moved on")
		}},
		{name: "gitlink replaced by file", fallback: true, mutate: func(f *fixture) {
			addGitlinks(f, "vendor/sub")
			f.write("vendor/sub", "now a file\n")
		}},
		{name: "ordering around slash", mutate: func(f *fixture) {
			for _, n := range []string{"a-b", "a_b", "a+b", "a!b", "a~b", "a,b", "a=b", "a@b"} {
				f.write(n, "x\n")
			}
			f.git("add", "-A")
			f.write("a-b", "changed\n")
			f.write("a/z.go", "package Z2\n")
		}},
		{name: "dangling symlink changed", unixOnly: true, mutate: func(f *fixture) {
			f.remove("link")
			f.symlink("does/not/exist", "link")
		}},
		{name: "symlink to directory", unixOnly: true, mutate: func(f *fixture) {
			f.remove("link")
			f.symlink("src", "link")
		}},
		{name: "symlink with unicode and long target", unixOnly: true, mutate: func(f *fixture) {
			f.remove("link")
			f.symlink(strings.Repeat("长目录/", 60)+"é", "link")
		}},
		{name: "many untouched dirs", mutate: func(f *fixture) {
			for i := 0; i < 30; i++ {
				f.write(fmt.Sprintf("gen/d%02d/e%d/f.txt", i, i%4), "x\n")
			}
			f.git("add", "-A")
			f.git("commit", "-qm", "gen")
			f.write("gen/d07/e3/f.txt", "changed\n")
			f.remove("gen/d13")
		}},
		{name: "gitlink entry", exempt: docsExempt, mutate: func(f *fixture) {
			f.git("update-index", "--add", "--cacheinfo", "160000,0123456789012345678901234567890123456789,vendor/sub")
			f.write("src/a.go", "package g\n")
		}},
		{name: "filter attribute on dirty file", fallback: true, setup: func(f *fixture) {
			f.write(".gitattributes", "*.dat filter=nop\n")
			f.write("data/x.dat", "dat\n")
			f.git("config", "filter.nop.clean", "cat")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("data/x.dat", "dat2\n") }},
		{name: "ident attribute on dirty file", fallback: true, setup: func(f *fixture) {
			f.write(".gitattributes", "*.id ident\n")
			f.write("data/x.id", "plain\n")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("data/x.id", "$Id$\n") }},
		{name: "tracked gitattributes deleted from the work tree", fallback: true, setup: func(f *fixture) {
			f.write(".gitattributes", "*.dat filter=nop\n")
			f.write("data/x.dat", "dat\n")
			f.git("config", "filter.nop.clean", "cat")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.remove(".gitattributes"); f.write("data/x.dat", "dat2\n") }},
		{name: "tracked subdir gitattributes deleted but nothing dirty below", setup: func(f *fixture) {
			f.write("data/.gitattributes", "*.dat filter=nop\n")
			f.write("data/x.dat", "dat\n")
			f.git("config", "filter.nop.clean", "cat")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
			f.settleSlow()
		}, mutate: func(f *fixture) { f.remove("data/.gitattributes"); f.write("src/a.go", "package changed elsewhere\n") }},
		{name: "text=auto lf only dirty stays native", setup: func(f *fixture) {
			f.write(".gitattributes", "* text=auto\n")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("src/a.go", "package lf\n") }},
		{name: "text=auto crlf dirty falls back", fallback: true, setup: func(f *fixture) {
			f.write(".gitattributes", "* text=auto\n")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("src/a.go", "package crlf\r\nline\r\n") }},
		{name: "eol=crlf attribute crlf dirty falls back", fallback: true, setup: func(f *fixture) {
			f.write(".gitattributes", "*.go text eol=crlf\n")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("src/a.go", "package x\r\nline\r\n") }},
		{name: "subdir attributes crlf dirty falls back", fallback: true, setup: func(f *fixture) {
			f.write("src/.gitattributes", "*.go text\n")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("src/sub/c.go", "package x\r\nline\r\n") }},
		{name: "subdir attributes elsewhere ignored", setup: func(f *fixture) {
			f.write("src/.gitattributes", "*.go text\n")
			f.git("add", "-A")
			f.git("commit", "-qm", "attrs")
		}, mutate: func(f *fixture) { f.write("bin/run.sh", "echo \r\n") }},
		{name: "autocrlf true lf only dirty stays native", cfg: [][2]string{{"core.autocrlf", "true"}},
			mutate: func(f *fixture) { f.write("src/a.go", "package lf2\n") }},
		{name: "autocrlf true crlf dirty falls back", fallback: true, cfg: [][2]string{{"core.autocrlf", "true"}},
			mutate: func(f *fixture) { f.write("src/a.go", "package x\r\nfoo\r\n") }},
		{name: "autocrlf input crlf dirty falls back", fallback: true, cfg: [][2]string{{"core.autocrlf", "input"}},
			mutate: func(f *fixture) { f.write("src/a.go", "package x\r\nfoo\r\n") }},
		{name: "core.eol set crlf dirty falls back", fallback: true, cfg: [][2]string{{"core.eol", "crlf"}, {"core.autocrlf", "true"}},
			mutate: func(f *fixture) { f.write("README.md", "a\r\nb\r\n") }},
		{name: "safecrlf true with autocrlf falls back", fallback: true, cfg: [][2]string{{"core.autocrlf", "true"}, {"core.safecrlf", "true"}},
			mutate: func(f *fixture) { f.write("src/a.go", "package lf3\n") }},
		{name: "autocrlf true clean worktree", cfg: [][2]string{{"core.autocrlf", "true"}}},
		{name: "core.symlinks false with symlink entry", unixOnly: true, fallback: true, cfg: [][2]string{{"core.symlinks", "false"}},
			mutate: func(f *fixture) { f.write("src/a.go", "package ns\n") }},
		{name: "core.symlinks false without symlinks stays native", unixOnly: true, cfg: [][2]string{{"core.symlinks", "false"}},
			setup:  func(f *fixture) { f.git("rm", "-q", "link"); f.git("commit", "-qm", "no link") },
			mutate: func(f *fixture) { f.write("src/a.go", "package ns\n"); f.remove("bin/run.sh") }},
		{name: "core.symlinks false with new symlink in work tree", unixOnly: true, fallback: true, cfg: [][2]string{{"core.symlinks", "false"}},
			setup:  func(f *fixture) { f.git("rm", "-q", "link"); f.git("commit", "-qm", "no link") },
			mutate: func(f *fixture) { f.remove("src/b.go"); f.symlink("a.go", "src/b.go") }},
	}
}

func addGitlink(f *fixture) {
	f.git("update-index", "--add", "--cacheinfo", "160000,0123456789012345678901234567890123456789,vendor/sub")
	f.git("update-index", "--add", "--cacheinfo", "160000,0123456789012345678901234567890123456789,vendor/sub2")
	f.write("src/a.go", "package g2\n")
}

// addGitlinks records gitlinks (without any work tree directory) at paths.
func addGitlinks(f *fixture, paths ...string) {
	f.t.Helper()
	for _, p := range paths {
		f.git("update-index", "--add", "--cacheinfo", "160000,0123456789012345678901234567890123456789,"+p)
	}
}

// nestedRepo creates a git repository with one commit at rel.
func nestedRepo(f *fixture, rel string) {
	f.t.Helper()
	if err := os.MkdirAll(f.path(rel), 0o755); err != nil {
		f.t.Fatal(err)
	}
	dir := f.path(rel)
	f.gitIn(dir, nil, "init", "-q", "-b", "main")
	f.write(rel+"/inner.txt", "inner\n")
	f.gitIn(dir, nil, "add", "inner.txt")
	f.gitIn(dir, nil, "commit", "-qm", "inner")
}

func makeConflict(f *fixture) {
	f.t.Helper()
	f.git("checkout", "-q", "-b", "other")
	f.write("src/a.go", "package other\n")
	f.git("commit", "-qam", "other")
	f.git("checkout", "-q", "main")
	f.write("src/a.go", "package main2\n")
	f.git("commit", "-qam", "main2")
	if _, err := f.gitErr(f.dir, nil, "merge", "other"); err == nil {
		f.t.Fatal("expected a merge conflict")
	}
}

func TestMatchesGitOnFixtures(t *testing.T) {
	requireGit(t)
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			if sc.unixOnly && runtime.GOOS == "windows" {
				t.Skip("unix only")
			}
			f := newFixture(t)
			baseRepo(f)
			if sc.setup != nil {
				sc.setup(f)
			}
			for _, kv := range sc.cfg {
				f.git("config", kv[0], kv[1])
			}
			if sc.mutate != nil {
				sc.mutate(f)
			}
			f.relocate()
			checkAllForms(t, f, sc)
		})
	}
}

func checkAllForms(t *testing.T, f *fixture, sc scenario) {
	t.Helper()
	filt := mustFilter(t, sc.exempt...)
	type form struct {
		name    string
		refresh bool
		paths   []string
		call    func(opt Options) (model.TreeID, error)
	}
	forms := []form{
		{"S", true, nil, func(o Options) (model.TreeID, error) { return TreeS(o, f.repo, filt) }},
		{"C all", true, nil, func(o Options) (model.TreeID, error) {
			return TreeC(o, f.repo, model.CommitForm{Kind: model.CommitAll}, filt)
		}},
		{"C plain", false, nil, func(o Options) (model.TreeID, error) {
			return TreeC(o, f.repo, model.CommitForm{Kind: model.CommitPlain}, filt)
		}},
	}
	for _, ps := range [][]string{{"src/a.go"}, {"src"}, {"src/sub", "docs"}, {"README.md", "bin/run.sh", "link"}, {"*.go"}, {"src/sub/"}, {"."}} {
		ps := ps
		forms = append(forms, form{"C paths " + strings.Join(ps, ","), true, ps, func(o Options) (model.TreeID, error) {
			return TreeC(o, f.repo, model.CommitForm{Kind: model.CommitPaths, Paths: ps}, filt)
		}})
	}
	for _, fm := range forms {
		want, rerr := f.referenceErr(fm.refresh, fm.paths, sc.exempt)
		if rerr != nil {
			// git cannot produce this snapshot itself (unmerged index,
			// path beyond a symlink, ...): nothing to compare with.
			t.Logf("%s: git fails: %.120s", fm.name, rerr)
			fm.call(f.opts(true)) // must not panic or hang
			continue
		}
		needsFallback := sc.fallback && fm.refresh || sc.plainFalls
		got, err := fm.call(f.opts(false))
		if needsFallback {
			// Only forms whose evaluation actually trips the condition
			// must report ErrFallback; others may legitimately stay
			// native, but they must always equal git.
			if err == nil {
				if got != want {
					t.Errorf("%s native: %s", fm.name, describe(got, want))
				}
			} else if !errors.Is(err, model.ErrFallback) {
				t.Errorf("%s native: unexpected error %v", fm.name, err)
			}
			got2, err2 := fm.call(f.opts(true))
			if err2 != nil {
				t.Errorf("%s with fallback: %v", fm.name, err2)
			} else if got2 != want {
				t.Errorf("%s with fallback: %s", fm.name, describe(got2, want))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v (want native result %s)", fm.name, err, want)
			continue
		}
		if got != want {
			t.Errorf("%s: %s", fm.name, describe(got, want))
		}
	}
	// The user's index must be untouched and no stray files may appear in .git.
	entries, _ := os.ReadDir(f.repo.GitDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "index.") || strings.HasSuffix(e.Name(), ".lock") {
			t.Errorf("stray file in .git: %s", e.Name())
		}
	}
}

// TestDamagedSubmoduleFallsBack: a checked-out submodule whose git
// directory is not a repository for git (no objects/ or refs/, e.g. a .git
// holding only HEAD) must not get a native gitlink id; git itself fails on
// it, so the native path falls back (contract v1.3, review u8a minor).
func TestDamagedSubmoduleFallsBack(t *testing.T) {
	damage := map[string]func(f *fixture){
		"objects removed": func(f *fixture) {
			if err := os.RemoveAll(f.path("vendor/sub/.git/objects")); err != nil {
				t.Fatal(err)
			}
		},
		"refs removed": func(f *fixture) {
			if err := os.RemoveAll(f.path("vendor/sub/.git/refs")); err != nil {
				t.Fatal(err)
			}
		},
		"HEAD only": func(f *fixture) {
			head := f.gitIn(f.path("vendor/sub"), nil, "rev-parse", "HEAD")
			if err := os.RemoveAll(f.path("vendor/sub/.git")); err != nil {
				t.Fatal(err)
			}
			f.write("vendor/sub/.git/HEAD", strings.TrimSpace(head)+"\n")
		},
	}
	for name, mutate := range damage {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			baseRepo(f)
			nestedRepo(f, "vendor/sub")
			f.git("add", "vendor/sub")
			f.git("commit", "-qm", "add submodule")
			mutate(f)
			f.relocate()
			filt := mustFilter(t)
			for _, form := range []struct {
				name string
				call func(o Options) (model.TreeID, error)
			}{
				{"S", func(o Options) (model.TreeID, error) { return TreeS(o, f.repo, filt) }},
				{"C all", func(o Options) (model.TreeID, error) {
					return TreeC(o, f.repo, model.CommitForm{Kind: model.CommitAll}, filt)
				}},
			} {
				if id, err := form.call(f.opts(false)); !errors.Is(err, model.ErrFallback) {
					t.Errorf("%s native: id %s, err %v; want ErrFallback", form.name, id, err)
				}
				form.call(f.opts(true)) // git reports the damage; must not panic or hang
			}
		})
	}
}
