package install

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestUnifiedDiffExact(t *testing.T) {
	cases := []struct {
		name, before, after, want string
	}{
		{"identical", "a\nb\n", "a\nb\n", ""},
		{"change middle", "a\nb\nc\n", "a\nX\nc\n",
			"--- f\n+++ f\n@@ -1,3 +1,3 @@\n a\n-b\n+X\n c\n"},
		{"append", "a\n", "a\nb\n", "--- f\n+++ f\n@@ -1 +1,2 @@\n a\n+b\n"},
		{"delete line", "a\nb\n", "a\n", "--- f\n+++ f\n@@ -1,2 +1 @@\n a\n-b\n"},
		{"no newline at end", "a", "a\n", "--- f\n+++ f\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n"},
		{"far apart hunks", numbered(1, 20, nil), numbered(1, 20, map[int]string{2: "two", 19: "nineteen"}),
			"--- f\n+++ f\n@@ -1,5 +1,5 @@\n 1\n-2\n+two\n 3\n 4\n 5\n@@ -16,5 +16,5 @@\n 16\n 17\n 18\n-19\n+nineteen\n 20\n"},
		{"merge close hunks", numbered(1, 12, nil), numbered(1, 12, map[int]string{3: "three", 8: "eight"}),
			"--- f\n+++ f\n@@ -1,11 +1,11 @@\n 1\n 2\n-3\n+three\n 4\n 5\n 6\n 7\n-8\n+eight\n 9\n 10\n 11\n"},
	}
	for _, c := range cases {
		got := UnifiedDiff("f", []byte(c.before), []byte(c.after))
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func numbered(from, to int, repl map[int]string) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		if r, ok := repl[i]; ok {
			b.WriteString(r)
		} else {
			b.WriteString(strconv.Itoa(i))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestUnifiedDiffCreateAndDelete(t *testing.T) {
	got := UnifiedDiff("/x/new", nil, []byte("a\nb\n"))
	if got != "--- /dev/null\n+++ /x/new\n@@ -0,0 +1,2 @@\n+a\n+b\n" {
		t.Errorf("create: %q", got)
	}
	got = UnifiedDiff("/x/old", []byte("a\nb\n"), nil)
	if got != "--- /x/old\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n" {
		t.Errorf("delete: %q", got)
	}
	if UnifiedDiff("p", nil, nil) != "" {
		t.Error("absent to absent")
	}
	// an existing empty file differs from an absent one
	if UnifiedDiff("p", nil, []byte{}) == "" {
		t.Error("absent -> empty file is a change")
	}
}

// applyUnified applies a unified diff (as produced by UnifiedDiff) to before.
func applyUnified(t *testing.T, before string, diff string) string {
	t.Helper()
	if diff == "" {
		return before
	}
	src := strings.SplitAfter(before, "\n")
	if src[len(src)-1] == "" {
		src = src[:len(src)-1]
	}
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	var out []string
	pos := 0 // index into src
	for i := 2; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "@@"):
			rest := strings.TrimPrefix(l, "@@ ")
			rest = rest[:strings.Index(rest, " @@")]
			parts := strings.Split(rest, " ")
			oldStart, oldCount := parseRange(parts[0][1:])
			start := oldStart - 1
			if oldCount == 0 {
				start = oldStart
			}
			out = append(out, src[pos:start]...)
			pos = start
		case strings.HasPrefix(l, " "):
			out = append(out, src[pos])
			pos++
		case strings.HasPrefix(l, "-"):
			pos++
		case strings.HasPrefix(l, "+"):
			add := l[1:] + "\n"
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\\ No newline") {
				add = l[1:]
			}
			out = append(out, add)
		}
	}
	out = append(out, src[pos:]...)
	return strings.Join(out, "")
}

func parseRange(s string) (start, count int) {
	if i := strings.IndexByte(s, ','); i >= 0 {
		start, _ = strconv.Atoi(s[:i])
		count, _ = strconv.Atoi(s[i+1:])
		return start, count
	}
	start, _ = strconv.Atoi(s)
	return start, 1
}

func TestUnifiedDiffAppliesCleanly(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	randDoc := func() string {
		n := rng.Intn(30)
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "line %d\n", rng.Intn(12))
		}
		return b.String()
	}
	for i := 0; i < 300; i++ {
		a, b := randDoc(), randDoc()
		if a == "" || b == "" {
			continue
		}
		d := UnifiedDiff("f", []byte(a), []byte(b))
		if got := applyUnified(t, a, d); got != b {
			t.Fatalf("diff does not reproduce the target\nbefore %q\nafter  %q\ndiff   %q\ngot    %q", a, b, d, got)
		}
	}
}

func TestUnifiedDiffLargeInputFallsBack(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 2500; i++ {
		fmt.Fprintf(&a, "a%d\n", i)
		fmt.Fprintf(&b, "b%d\n", i)
	}
	d := UnifiedDiff("f", []byte(a.String()), []byte(b.String()))
	if !strings.HasPrefix(d, "--- f\n+++ f\n@@ -1,2500 +1,2500 @@\n-a0\n") {
		t.Errorf("unexpected diff head: %.80q", d)
	}
}
