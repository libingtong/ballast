package dsl

// Preprocess applies text preprocess steps in order (dsl-semantics §5.5):
// strip_code_blocks removes ``` / ~~~ fenced blocks (unterminated fence ⇒
// to end of text) and `inline` spans; strip_quotes removes "…", “…”, ‘…’,
// 「…」, 『…』 spans and lines starting with ">". Unknown steps are a
// validation error, never reached here.
func Preprocess(text string, steps []string) string {
	panic("unimplemented: dsl.Preprocess")
}

// WindowText applies head/tail rune windows (head first, then tail; 0 = unlimited).
func WindowText(text string, head, tail int) string {
	panic("unimplemented: dsl.WindowText")
}

// CJKRatio returns CJK runes / (CJK runes + Latin letters), ignoring digits,
// spaces and punctuation; ok=false when the denominator is 0.
// CJK = Unicode Han, Hiragana, Katakana, Hangul.
func CJKRatio(text string) (ratio float64, ok bool) {
	panic("unimplemented: dsl.CJKRatio")
}

// RuneLen counts runes (all DSL budgets are in runes).
func RuneLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
