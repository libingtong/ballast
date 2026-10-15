package dsl

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/libingtong/ballast/internal/model"
)

// Preprocess applies text preprocess steps in order (dsl-semantics §5.5):
// strip_code_blocks removes ``` / ~~~ fenced blocks (unterminated fence ⇒
// to end of text) and `inline` spans; strip_quotes removes "…", “…”, ‘…’,
// 「…」, 『…』 spans and lines starting with ">". Unknown steps are a
// validation error, never reached here.
func Preprocess(text string, steps []string) string {
	for _, s := range steps {
		switch s {
		case model.PreStripCodeBlocks:
			text = stripCodeBlocks(text)
		case model.PreStripQuotes:
			text = stripQuotes(text)
		}
	}
	return text
}

// fenceOpen reports the fence character and length when line opens a fenced
// block (three or more ` or ~ after optional indentation).
func fenceOpen(line string) (byte, int) {
	s := strings.TrimLeft(line, " \t")
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	if s[0] == '`' && strings.IndexByte(s[n:], '`') >= 0 {
		return 0, 0 // ```code``` on one line is an inline span, not a fence
	}
	return s[0], n
}

func fenceCloses(line string, ch byte, n int) bool {
	s := strings.TrimSpace(line)
	if len(s) < n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != ch {
			return false
		}
	}
	return true
}

func stripCodeBlocks(text string) string {
	if !strings.ContainsAny(text, "`~") {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	var ch byte
	n := 0
	for _, ln := range lines {
		if n > 0 {
			if fenceCloses(ln, ch, n) {
				ch, n = 0, 0
			}
			continue
		}
		if c, l := fenceOpen(ln); l > 0 {
			ch, n = c, l
			continue
		}
		out = append(out, stripInlineCode(ln))
	}
	return strings.Join(out, "\n")
}

// stripInlineCode removes `code` spans: a run of n backticks closes at the
// next run of exactly n backticks; unmatched runs stay literal.
func stripInlineCode(line string) string {
	if strings.IndexByte(line, '`') < 0 {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		run := j - i
		end := -1
		for k := j; k < len(line); {
			if line[k] != '`' {
				k++
				continue
			}
			l := k
			for l < len(line) && line[l] == '`' {
				l++
			}
			if l-k == run {
				end = l
				break
			}
			k = l
		}
		if end < 0 {
			b.WriteString(line[i:j])
			i = j
			continue
		}
		i = end
	}
	return b.String()
}

var quotePairs = map[rune]rune{'"': '"', '“': '”', '‘': '’', '「': '」', '『': '』'}

func stripQuotes(text string) string {
	lines := strings.Split(text, "\n")
	kept := lines[:0:0]
	for _, ln := range lines {
		if strings.HasPrefix(strings.TrimLeft(ln, " \t"), ">") {
			continue
		}
		kept = append(kept, ln)
	}
	rs := []rune(strings.Join(kept, "\n"))
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		closer, ok := quotePairs[rs[i]]
		if !ok {
			out = append(out, rs[i])
			continue
		}
		end := -1
		for j := i + 1; j < len(rs); j++ {
			if rs[j] == closer {
				end = j
				break
			}
		}
		if end < 0 {
			out = append(out, rs[i])
			continue
		}
		i = end
	}
	return string(out)
}

// WindowText applies head/tail rune windows (head first, then tail; 0 = unlimited).
func WindowText(text string, head, tail int) string {
	if head > 0 {
		text = headRunes(text, head)
	}
	if tail > 0 {
		text = lastRunes(text, tail)
	}
	return text
}

func headRunes(s string, n int) string { return truncateRunes(s, n) }

// CJKRatio returns CJK runes / (CJK runes + Latin letters), ignoring digits,
// spaces and punctuation; ok=false when the denominator is 0.
// CJK = Unicode Han, Hiragana, Katakana, Hangul.
func CJKRatio(text string) (ratio float64, ok bool) {
	cjk, latin := 0, 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r),
			unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			cjk++
		case unicode.Is(unicode.Latin, r) && unicode.IsLetter(r):
			latin++
		}
	}
	if cjk+latin == 0 {
		return 0, false
	}
	return float64(cjk) / float64(cjk+latin), true
}

// RuneLen counts runes (all DSL budgets are in runes).
func RuneLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// FlattenToolResponse renders a tool_response payload as text (the same rule
// the adapters use for Event.ToolResponseText): an object with stdout/stderr
// becomes stdout + "\n" + stderr, a string stays as is, anything else is its
// compact JSON.
func FlattenToolResponse(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		_, hasOut := obj["stdout"]
		_, hasErr := obj["stderr"]
		if hasOut || hasErr {
			var out, errs string
			_ = json.Unmarshal(obj["stdout"], &out)
			_ = json.Unmarshal(obj["stderr"], &errs)
			return out + "\n" + errs
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return string(raw)
	}
	return compact.String()
}
