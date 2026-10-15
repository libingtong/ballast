package paths

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

func TestGlobSpecExamples(t *testing.T) {
	g, err := CompileGlobs([]string{"**/*.go", "!vendor/**"}, GlobOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Match("a/b.go") || g.Match("vendor/x.go") {
		t.Error("list-level negation broken")
	}
	if _, err := CompileGlobs([]string{"!x"}, GlobOptions{}); !errors.Is(err, ErrOnlyNegation) {
		t.Errorf(`{"!x"} must fail with ErrOnlyNegation, got %v`, err)
	}
}

func TestGlobNegationIsOrderIndependent(t *testing.T) {
	a, _ := CompileGlobs([]string{"**/*.go", "!vendor/**"}, GlobOptions{})
	b, _ := CompileGlobs([]string{"!vendor/**", "**/*.go"}, GlobOptions{})
	for _, cands := range [][]string{
		{"vendor/x.go"}, {"a/b.go"}, {"/r/vendor/x.go", "vendor/x.go"}, {"/r/a.go", "a.go"}, {"a.go", "vendor/a.go"}, {},
	} {
		if a.Match(cands...) != b.Match(cands...) {
			t.Errorf("order changed result for %q", cands)
		}
	}
	// Some candidate positive AND no candidate negative: a veto from any form wins.
	if a.Match("link.go", "vendor/real.go") {
		t.Error("negative on any candidate must veto")
	}
	if !a.Match("notgo.txt", "src/x.go") {
		t.Error("positive on any candidate must match")
	}
}

func TestGlobPatternsCopy(t *testing.T) {
	in := []string{"a", "!b"}
	g, err := CompileGlobs(in, GlobOptions{})
	if err != nil {
		t.Fatal(err)
	}
	in[0] = "mutated"
	if !reflect.DeepEqual(g.Patterns(), []string{"a", "!b"}) {
		t.Errorf("Patterns() = %q", g.Patterns())
	}
}

func TestGlobErrorsWrapPattern(t *testing.T) {
	_, err := CompileGlobs([]string{"ok", "[bad"}, GlobOptions{})
	if !errors.Is(err, ErrBadPattern) || err == nil || !strings.Contains(err.Error(), "[bad") {
		t.Errorf("error should wrap ErrBadPattern and name the pattern: %v", err)
	}
	for _, p := range []string{"", "!", "!!a", "[", "{a", "a/[b"} {
		if err := ValidatePattern(p); !errors.Is(err, ErrBadPattern) {
			t.Errorf("ValidatePattern(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"a", "!a", "**/*.go", "{a,b}/c", "~/.ballast/**", "[a-z]?", `\!x`} {
		if err := ValidatePattern(p); err != nil {
			t.Errorf("ValidatePattern(%q) = %v", p, err)
		}
	}
}

func TestServiceCompileGlobsContext(t *testing.T) {
	svc := NewService(sys.OSFS{})

	// Windows: case folding on, home given in a non-normalized spelling.
	ctx := model.PathContext{GOOS: "windows", Home: `C:\Users\X`}
	m, err := svc.CompileGlobs([]string{"~/.ballast/**", "!~/.ballast/tmp/**"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("c:/users/x/.BALLAST/state") || m.Match("c:/Users/X/.ballast/TMP/x") {
		t.Error("windows context: folding / home expansion broken")
	}

	// Linux: case-sensitive.
	m, err = svc.CompileGlobs([]string{"~/.ballast/**"}, model.PathContext{GOOS: "linux", Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("/home/u/.ballast/x") || m.Match("/home/U/.ballast/x") {
		t.Error("linux context must be case-sensitive")
	}

	// The error path never returns a typed-nil inside the interface.
	m, err = svc.CompileGlobs([]string{"!x"}, ctx)
	if err == nil || m != nil {
		t.Errorf("CompileGlobs(!x) = (%v, %v)", m, err)
	}
	var _ ports.GlobMatcher = m
}

func TestGlobNilAndEmpty(t *testing.T) {
	var g *GlobSet
	if g.Match("x") {
		t.Error("nil GlobSet must not match")
	}
	g, _ = CompileGlobs([]string{"**"}, GlobOptions{})
	if g.Match() {
		t.Error("no candidates must not match")
	}
	if g.Match("") {
		t.Error("the empty candidate must not match")
	}
}

func TestConcurrentUse(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.proj+"/src/a.go")
	f.symlink(t, f.ballast, f.proj+"/link")
	g, err := f.svc.CompileGlobs([]string{"~/.ballast/**", "**/*.go", "!vendor/**"}, f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if c := f.svc.Candidates("link/state/x", f.ctx); !g.Match(c...) {
					errs <- fmt.Errorf("goroutine %d: link candidates %q do not match", i, c)
					return
				}
				if c := f.svc.Candidates("vendor/a.go", f.ctx); g.Match(c...) {
					errs <- fmt.Errorf("goroutine %d: vendor candidates %q match", i, c)
					return
				}
				if n := f.svc.Normalize("src/../src/a.go", f.proj, f.ctx); n != f.proj+"/src/a.go" {
					errs <- fmt.Errorf("goroutine %d: normalize = %q", i, n)
					return
				}
				m, err := f.svc.CompileGlobs([]string{"*.go", "!x.go"}, f.ctx)
				if err != nil || !m.Match("a.go") || m.Match("x.go") {
					errs <- fmt.Errorf("goroutine %d: compile: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func BenchmarkNormalize(b *testing.B) {
	ctx := model.PathContext{GOOS: "windows", Home: "c:/Users/x"}
	for i := 0; i < b.N; i++ {
		_ = Normalize(`C:\Users\X\repo\src\..\a.go`, "c:/Users/x/repo", ctx)
	}
}

func BenchmarkGlobMatch(b *testing.B) {
	g, _ := CompileGlobs([]string{"**/*.go", "~/.ballast/**", "!vendor/**"}, GlobOptions{Home: "/home/u", CaseInsensitive: true})
	c := []string{"/Home/u/repo/src/a.go", "src/a.go"}
	for i := 0; i < b.N; i++ {
		_ = g.Match(c...)
	}
}
