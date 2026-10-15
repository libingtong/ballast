package dsl

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func TestPreprocess(t *testing.T) {
	codes := []string{model.PreStripCodeBlocks}
	quotes := []string{model.PreStripQuotes}
	cases := []struct {
		name  string
		steps []string
		in    string
		want  string
	}{
		{"no steps", nil, "a `b` \"c\"", "a `b` \"c\""},
		{"fenced block", codes, "a\n```go\ncode\n```\nb", "a\nb"},
		{"tilde fence", codes, "a\n~~~\nx\n~~~\nb", "a\nb"},
		{"fence only", codes, "```\ngo test ./...\n```", ""},
		{"unterminated fence runs to the end", codes, "a\n```\nrest\nmore", "a"},
		{"longer fence needs a long closer", codes, "````\n```\nstill code\n````\nafter", "after"},
		{"indented fence", codes, "x\n  ```\ncode\n  ```\ny", "x\ny"},
		{"fence info string", codes, "```python title=x\nprint(1)\n```\nok", "ok"},
		{"mismatched fence char does not close", codes, "```\nx\n~~~\ny\n```\nz", "z"},
		{"inline span", codes, "use `x` here", "use  here"},
		{"inline span with doubled backticks", codes, "use ``a`b`` here", "use  here"},
		{"unmatched backtick stays", codes, "a ` b", "a ` b"},
		{"inline triple on one line", codes, "```code``` text", " text"},
		{"several inline spans", codes, "`a` and `b`", " and "},
		{"double quotes", quotes, `say "done" now`, "say  now"},
		{"curly double quotes", quotes, "say “done” now", "say  now"},
		{"curly single quotes", quotes, "say ‘done’ now", "say  now"},
		{"corner brackets", quotes, "说「完成」了", "说了"},
		{"white corner brackets", quotes, "说『完成』了", "说了"},
		{"quote pairs do not cross lines", quotes, "\"a\nb\" c", "\"a\nb\" c"},
		{"stray inch mark keeps later lines", quotes, "5\" screen\nAll tests passed. \"ok\"", "5\" screen\nAll tests passed. "},
		{"stray curly opener keeps later lines", quotes, "“open\nAll tests passed. “ok”", "“open\nAll tests passed. "},
		{"quoted lines", quotes, "> quoted\nkept\n  > indented quote\nalso", "kept\nalso"},
		{"unmatched quote stays", quotes, `5" pipe`, `5" pipe`},
		{"apostrophes are not quotes", quotes, "don't stop, it's fine", "don't stop, it's fine"},
		{"mixed pairs do not close each other", quotes, "“a\"b”", ""},
		{"steps compose", []string{model.PreStripCodeBlocks, model.PreStripQuotes}, "```\n\"x\n```\n\"y\"", ""},
		{"unknown steps are ignored", []string{"nope"}, "a `b`", "a `b`"},
		{"empty input", []string{model.PreStripCodeBlocks, model.PreStripQuotes}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Preprocess(c.in, c.steps); got != c.want {
				t.Errorf("Preprocess(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestWindowText(t *testing.T) {
	cases := []struct {
		in         string
		head, tail int
		want       string
	}{
		{"abcdef", 0, 0, "abcdef"},
		{"abcdef", 3, 0, "abc"},
		{"abcdef", 0, 3, "def"},
		{"abcdef", 4, 2, "cd"},
		{"abc", 10, 10, "abc"},
		{"你好世界和平", 4, 2, "世界"},
		{"héllo", 0, 3, "llo"},
		{"", 3, 3, ""},
		{"abc", -1, -1, "abc"},
	}
	for _, c := range cases {
		if got := WindowText(c.in, c.head, c.tail); got != c.want {
			t.Errorf("WindowText(%q,%d,%d) = %q, want %q", c.in, c.head, c.tail, got, c.want)
		}
	}
}

func TestCJKRatio(t *testing.T) {
	cases := []struct {
		in    string
		ratio float64
		ok    bool
	}{
		{"", 0, false},
		{"123 ... !?", 0, false},
		{"hello", 0, true},
		{"你好", 1, true},
		{"ab你好", 0.5, true},
		{"abc 123 你", 0.25, true},
		{"ひらがなカタカナ", 1, true},
		{"한국어 abc", 3.0 / 6.0, true},
		{"привет", 0, false}, // Cyrillic is neither CJK nor Latin
		{"привет ab", 0, true},
		{"，。！你", 1, true}, // CJK punctuation is ignored
		{"ÀÉ你", 1.0 / 3.0, true},
	}
	for _, c := range cases {
		got, ok := CJKRatio(c.in)
		if ok != c.ok || math.Abs(got-c.ratio) > 1e-9 {
			t.Errorf("CJKRatio(%q) = %v,%v want %v,%v", c.in, got, ok, c.ratio, c.ok)
		}
	}
}

func TestRuneLen(t *testing.T) {
	for s, want := range map[string]int{"": 0, "abc": 3, "你好": 2, "é": 1, "😀a": 2} {
		if got := RuneLen(s); got != want {
			t.Errorf("RuneLen(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestFlattenToolResponse(t *testing.T) {
	cases := []struct{ in, want string }{
		{``, ""},
		{`"plain"`, "plain"},
		{`{"stdout":"out","stderr":"err","interrupted":false}`, "out\nerr"},
		{`{"stdout":"out"}`, "out\n"},
		{`{"k": 1, "z": [1, 2]}`, `{"k":1,"z":[1,2]}`},
		{`[1, 2]`, `[1,2]`},
		{`not json`, "not json"},
	}
	for _, c := range cases {
		if got := flattenToolResponse(json.RawMessage(c.in)); got != c.want {
			t.Errorf("flattenToolResponse(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if truncateRunes("你好世界", 2) != "你好" || truncateRunes("ab", 5) != "ab" || truncateRunes("ab", 0) != "" {
		t.Error("truncateRunes")
	}
	if got := lastRunes(strings.Repeat("x", 300), 200); len(got) != 200 {
		t.Errorf("lastRunes len %d", len(got))
	}
}

// TestPreprocessIsLinear feeds adversarial inputs (many unmatched openers,
// backtick runs of every length) that would be quadratic with a naive scan.
func TestPreprocessIsLinear(t *testing.T) {
	var runs strings.Builder
	for n := 1; n < 700; n++ {
		runs.WriteString(strings.Repeat("`", n))
		runs.WriteString("x")
	}
	inputs := []string{
		strings.Repeat("「", 1<<20),
		strings.Repeat("\"a", 1<<19),
		strings.Repeat("“", 1<<19) + "”",
		runs.String(),
		strings.Repeat("` ", 1<<19),
		strings.Repeat("```\n", 1<<18),
	}
	for i, in := range inputs {
		start := time.Now()
		_ = Preprocess(in, []string{model.PreStripCodeBlocks, model.PreStripQuotes})
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("input %d took %v", i, d)
		}
	}
}
