package snapshot

import (
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
)

// corpus is the set of tracked paths every pattern is tried against (no
// file/directory name clashes, so it can be loaded into a git index).
var corpus = []string{
	"a.md", "a/b.md", "a/b/c", "a/b/c.md", "a/b/d.go", "a/b/docs/z", "a/x",
	"ab", "abc", "-dash", "[x]", "a*b", "a?b", "a b", "é/ü.md",
	"docs/x", "docs/xs/y.md", "docs/.hidden", "docs/sub/deep/file.txt", "docs2/x.md",
	".md", "foo.md/x", "README.md", "Makefile", "go.mod", "x.test",
	"src/main.go", "src/main_test.go", "src/m.c", "Src/Main.go",
	"x/y/z", "x/yz", "xyz", "xy/z",
	"tests/a_test.go", "tests/data/in.json", "dir.d/f",
	"vendor/sub/x.txt",
}

// patterns tried in both glob and plain mode.
var patterns = []string{
	"a", "a/", "a/b", "a/*", "a/**", "a/**/c", "a/**/c.md", "**/c.md", "**/*.md", "*.md", "**.md",
	"*", "**", "***", "*/*", "*/**", "**/*", "**/", "docs", "docs/", "docs/*", "docs/**",
	"docs/**/*", "docs/**/*.md", "docs*", "docs?", "docs/?", "d?cs", "d[o]cs", "d[!o]cs", "d[^x]cs",
	"*.go", "src/*.go", "src/*_test.go", "src/m*", "src/m?*", "src/[a-m]*.go", "[a-c]*", "[!a-c]*",
	"[[:alpha:]]", "[[:alpha:]]*", "[[:digit:]]*", "[[:upper:]]rc/*", "[[:bogus:]]", "[a-", "[", "]",
	"[]]", "[]x]", "[x]", "\\[x\\]", "\\*", "a\\*b", "a\\?b", "a?b", "a*b", "\\a", "a/\\b",
	"x/y*", "x/y?", "x/*z", "x/**/z", "x**/z", "**x", "x**", "*x*", "*y*", "x/y/z", "x/y/z/", "./a",
	"a//b", "a/./b", "a/../a/x", "-dash", "é/*", "é/ü.md", "*/ü.md", "**/ü.md", "vendor/sub", "vendor/sub/",
	"vendor/*", "vendor/**", "vendor/sub/**", "vendor/sub/*.txt", "**/test*", "tests/**/*.json", "tests/*/in.json",
	"tests/**/in.json", "**/data/**", "**/data/*", "Makefile", "makefile", "*.mod", "go.*", "*.test", "*.d/f",
	"dir.d", "dir.*/f", "**/docs/z", "a/**/docs/z", "a/b/**/z", "a/b/**", "a/b/**/", "a**b", "a*", "?", "??", "???",
	"?/?", "a/?", "*/b", "**/b", "a/**/b", "a/**/**/c", "[a-z]", "[A-Z]*", "[0-9]", "src/ma[i]n.go", "src/ma[!i]n.go",
	"**/[a-b]", "x/**/**", "docs/.hidden", "docs/.*", ".*", "*.", "*.*", "a.*", "*.md/x", "foo.md/*", ".",
}

func randomPatterns(n int, seed int64) []string {
	toks := []string{"a", "b", "c", "docs", "x", "y", "z", ".md", "*", "**", "/", "/", "/", "?", "[ab]", "[!a]", "[a-c]", "\\*", ".", "-", "src", "main", "go", "_test"}
	rng := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	var out []string
	for len(out) < n {
		var b strings.Builder
		for i, k := 0, 1+rng.Intn(6); i < k; i++ {
			b.WriteString(toks[rng.Intn(len(toks))])
		}
		p := b.String()
		if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "\\") || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func matchedBy(pat string, glob bool) ([]string, bool) {
	n, err := normalizeSpec(pat)
	if err != nil {
		return nil, false
	}
	pp := newPathPattern(n, glob)
	var out []string
	for _, name := range corpus {
		if pp.match(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, true
}

// TestFastPathsAgreeWithWildmatch checks the literal / prefix / suffix fast
// paths of pathPattern against the plain wildmatch port (no git needed).
func TestFastPathsAgreeWithWildmatch(t *testing.T) {
	all := append(append([]string{}, patterns...), randomPatterns(400, 7)...)
	for _, pat := range all {
		n, err := normalizeSpec(pat)
		if err != nil {
			continue
		}
		for _, glob := range []bool{true, false} {
			pp := newPathPattern(n, glob)
			slow := pp
			slow.kind = pkGeneral
			for _, name := range corpus {
				if pp.match(name) != slow.match(name) {
					t.Errorf("pattern %q glob=%v name %q: fast=%v slow=%v (kind %d)", pat, glob, name, pp.match(name), slow.match(name), pp.kind)
				}
			}
		}
	}
}

func TestWildmatchTable(t *testing.T) {
	tests := []struct {
		pat, text string
		pathname  bool
		want      bool
	}{
		{"*.md", "a.md", true, true},
		{"*.md", "d/a.md", true, false},
		{"*.md", "d/a.md", false, true},
		{"**/*.md", "a.md", true, true},
		{"**/*.md", "d/e/a.md", true, true},
		{"docs/**", "docs/a/b", true, true},
		{"docs/**", "docs", true, false},
		{"docs/**/x", "docs/x", true, true},
		{"docs/**/x", "docs/a/b/x", true, true},
		{"a?c", "a/c", true, false},
		{"a?c", "a/c", false, true},
		{"[a-c]x", "bx", true, true},
		{"[!a-c]x", "bx", true, false},
		{"[[:digit:]]*", "9z", true, true},
		{"\\*", "*", true, true},
		{"\\*", "a", true, false},
		{"a[", "a[", true, false},
		{"", "", true, true},
		{"", "a", true, false},
		{"***", "a/b", true, true},
	}
	for _, tc := range tests {
		if got := wildmatch(tc.pat, tc.text, tc.pathname); got != tc.want {
			t.Errorf("wildmatch(%q, %q, pathname=%v) = %v, want %v", tc.pat, tc.text, tc.pathname, got, tc.want)
		}
	}
}

// TestMatcherAgainstGit compares the pathspec matcher with `git ls-files`
// for ':(glob)' (exempt) and plain (commit paths) pathspecs.
func TestMatcherAgainstGit(t *testing.T) {
	requireGit(t)
	f := newFixture(t)
	empty := strings.TrimSpace(f.git("hash-object", "-w", "-t", "blob", "--stdin"))
	var info strings.Builder
	for _, p := range corpus {
		info.WriteString("100644 " + empty + "\t" + p + "\n")
	}
	if out, err := f.gitStdin(f.dir, nil, info.String(), "update-index", "--add", "--index-info"); err != nil {
		t.Fatalf("index-info: %v\n%s", err, out)
	}
	all := append(append([]string{}, patterns...), randomPatterns(250, 42)...)
	type job struct {
		pat  string
		glob bool
	}
	var jobs []job
	for _, p := range all {
		jobs = append(jobs, job{p, true}, job{p, false})
	}
	var mu sync.Mutex
	var failures []string
	skipped := 0
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			spec := j.pat
			if j.glob {
				spec = ":(glob)" + j.pat
			}
			out, err := f.gitErr(f.dir, nil, "ls-files", "--", spec)
			mine, ok := matchedBy(j.pat, j.glob)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || !ok {
				skipped++
				return
			}
			var git []string
			for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				if l != "" {
					git = append(git, unquoteGitPath(l))
				}
			}
			sort.Strings(git)
			if strings.Join(git, "\x00") != strings.Join(mine, "\x00") {
				failures = append(failures, spec+"\n   git:  "+strings.Join(git, " ")+"\n   mine: "+strings.Join(mine, " "))
			}
		}()
	}
	wg.Wait()
	sort.Strings(failures)
	for _, fl := range failures {
		t.Errorf("mismatch for pathspec %s", fl)
	}
	t.Logf("%d pathspecs compared, %d skipped (git rejected them)", len(jobs)-skipped, skipped)
	if skipped > len(jobs)/4 {
		t.Errorf("too many skipped pathspecs: %d of %d", skipped, len(jobs))
	}
}

// unquoteGitPath undoes git's C-style quoting of unusual paths (ls-files
// without -z quotes non-ASCII bytes).
func unquoteGitPath(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch n := s[i]; {
		case n >= '0' && n <= '7' && i+2 < len(s)+0:
			v := 0
			j := 0
			for ; j < 3 && i+j < len(s) && s[i+j] >= '0' && s[i+j] <= '7'; j++ {
				v = v*8 + int(s[i+j]-'0')
			}
			b.WriteByte(byte(v))
			i += j - 1
		case n == 'n':
			b.WriteByte('\n')
		case n == 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(n)
		}
	}
	return b.String()
}

func FuzzWildmatch(f *testing.F) {
	for _, p := range patterns[:40] {
		f.Add(p, "docs/a/b.md")
	}
	f.Add("[[:alpha:]", "a")
	f.Add("a\\", "a")
	f.Add("**/**/**", "")
	f.Fuzz(func(t *testing.T, pat, text string) {
		a := wildmatch(pat, text, true)
		_ = wildmatch(pat, text, false)
		// A pattern without wildcards matches only itself.
		if !hasGlobSpecial(pat) && a != (pat == text) {
			t.Fatalf("literal %q vs %q = %v", pat, text, a)
		}
		// The filter must never panic on arbitrary input.
		if fl, err := CompileExempt([]string{pat}); err == nil {
			_ = fl.Exempt(text)
		}
	})
}
