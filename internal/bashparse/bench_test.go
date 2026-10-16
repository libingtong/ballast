package bashparse

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// bigCommand builds a realistic ~size-byte shell script mixing the constructs
// agents emit: pipelines, quoting, substitutions, loops, heredocs, redirects,
// wrappers and `bash -c`.
func bigCommand(size int) string {
	lines := []string{
		`cd /workspace/project && git -C /workspace/project status --short | head -n 20`,
		`for f in src/*.go internal/*/*.go; do echo "checking $f"; wc -l "$f" >> /tmp/counts.txt; done`,
		`grep -rn --include='*.go' -e "TODO" -e 'FIXME' src internal | sed -n '1,40p' | tee /tmp/todo.txt`,
		`cat <<'EOF' > notes.md
# notes
some text with $(not a command) and 'quotes'
EOF`,
		`result=$(curl -fsS "https://example.invalid/api?q=$QUERY" | jq -r '.items[] | .name')`,
		`if [ -f "$CONFIG" ] && grep -q enabled "$CONFIG"; then echo on; else echo off >&2; fi`,
		`sudo -u build env CI=1 timeout 600 make -j8 all > build.log 2>&1 &`,
		`bash -c 'sleep 1; cat a.go | head -5; echo "done" > /tmp/done'`,
		`while read -r line; do printf '%s\n' "$line" | awk -F: '{print $1}' x=1 /etc/passwd; done < input.txt`,
		`cp -r build/output dist/ && rsync -a --exclude .git dist/ host:/srv/www/ && rm -rf build/tmp`,
		`case "$OS" in linux) tar xzf a.tgz;; darwin) unzip -o a.zip;; *) echo unknown;; esac`,
		`echo "line with ${VAR:-default} and $((1+2)) and $(date +%s)" # trailing comment`,
	}
	var sb strings.Builder
	for i := 0; sb.Len() < size; i++ {
		fmt.Fprintf(&sb, "%s\n", lines[i%len(lines)])
	}
	return sb.String()
}

func BenchmarkParse100KB(b *testing.B) {
	src := bigCommand(100 * 1024)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(src, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseSmall(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse("for i in 1 2 3; do curl -f localhost:8080 && break; sleep 1; done", Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

// TestParse100KBBudget enforces the acceptance budget (a 100KB command parses
// in under 5ms) on the best of several runs, so scheduler noise does not fail
// it. The race detector slows everything several-fold, and shared CI runners
// are noisy, so the bound is relaxed in those two cases.
func TestParse100KBBudget(t *testing.T) {
	src := bigCommand(100 * 1024)
	if len(src) < 100*1024 {
		t.Fatalf("generator produced %d bytes", len(src))
	}
	if _, err := Parse(src, Options{}); err != nil {
		t.Fatal(err)
	}
	limit := 5 * time.Millisecond
	if raceEnabled {
		limit = 60 * time.Millisecond
	}
	if os.Getenv("CI") != "" {
		// Shared CI runners are noisy (a Windows runner measured 79 ms best-of-30
		// under -race while other packages were testing in parallel); the
		// benchmark and scripts/bench.sh (scenario 5) are the real gates there.
		limit *= 3
	}
	best := time.Hour
	for i := 0; i < 30; i++ {
		start := time.Now()
		if _, err := Parse(src, Options{}); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d < best {
			best = d
		}
	}
	runtime.GC()
	t.Logf("100KB parse best of 30: %v (limit %v)", best, limit)
	if best > limit {
		t.Errorf("100KB parse took %v, budget %v", best, limit)
	}
}

// TestAdversarialBudget checks that pathological but syntactically valid
// inputs stay linear: a quadratic scan would blow the 150ms watchdog and turn
// the matcher fail-open.
func TestAdversarialBudget(t *testing.T) {
	const n = 100 * 1024
	cases := map[string]string{
		"open braces":     "echo " + strings.Repeat("{x", n/2),
		"nested braces":   "cat " + strings.Repeat("{", n/4) + strings.Repeat(",}", n/4),
		"brace commas":    "cat {" + strings.Repeat("a,", n/2),
		"open brackets":   "cat " + strings.Repeat("[", n),
		"bracket pairs":   "cat " + strings.Repeat("[a", n/2) + "]",
		"closed brackets": "cat " + strings.Repeat("[a]", n/3),
		"escapes":         "cat " + strings.Repeat(`\{`, n/2),
		"many words":      "cat " + strings.Repeat("{a,b} ", n/6),
		"many redirects":  "cat " + strings.Repeat("a >b ", n/5),
	}
	limit := 20 * time.Millisecond
	switch {
	case raceEnabled:
		limit = 200 * time.Millisecond
	case os.Getenv("CI") != "":
		limit *= 3
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			best := time.Hour
			for i := 0; i < 5; i++ {
				start := time.Now()
				// A parse error is an acceptable outcome; the time is what matters.
				_, _ = Parse(src, Options{})
				if d := time.Since(start); d < best {
					best = d
				}
			}
			if best > limit {
				t.Errorf("%d-byte input took %v, budget %v", len(src), best, limit)
			}
		})
	}
}

// refHasBraceExpansion is the straightforward quadratic definition that the
// linear scan in unquotedLit must agree with.
func refHasBraceExpansion(s string) bool {
	depth := 0
	sep := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return sep
			}
		case ',':
			if depth == 1 {
				sep = true
			}
		case '.':
			if depth == 1 && i+1 < len(s) && s[i+1] == '.' {
				sep = true
			}
		}
	}
	return false
}

func refUnquotedDyn(v string) bool {
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		case '[':
			if strings.IndexByte(v[i+1:], ']') >= 0 {
				return true
			}
		case '{':
			if refHasBraceExpansion(v[i:]) {
				return true
			}
		}
	}
	return false
}

func TestUnquotedLitMatchesReference(t *testing.T) {
	alphabet := []byte(`{}[],.\a*`)
	var gen func(prefix []byte, left int)
	gen = func(prefix []byte, left int) {
		if _, dyn := unquotedLit(string(prefix)); dyn != refUnquotedDyn(string(prefix)) {
			t.Fatalf("%q: dyn=%v, reference=%v", prefix, dyn, refUnquotedDyn(string(prefix)))
		}
		if left == 0 {
			return
		}
		for _, c := range alphabet {
			gen(append(prefix, c), left-1)
		}
	}
	gen(nil, 6)
}
