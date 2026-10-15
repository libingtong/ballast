package install

import (
	"fmt"
	"strings"
)

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
	nonl bool // line has no trailing newline
}

func diffSplit(b []byte) []diffOp {
	var out []diffOp
	s := string(b)
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, diffOp{text: s, nonl: true})
			break
		}
		out = append(out, diffOp{text: s[:i]})
		s = s[i+1:]
	}
	return out
}

func sameLine(a, b diffOp) bool { return a.text == b.text && a.nonl == b.nonl }

// diffLines returns the edit script turning a into b.
func diffLines(a, b []diffOp) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && sameLine(a[pre], b[pre]) {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && sameLine(a[len(a)-1-suf], b[len(b)-1-suf]) {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var out []diffOp
	for _, l := range a[:pre] {
		l.kind = ' '
		out = append(out, l)
	}
	if len(ma)*len(mb) > 4_000_000 {
		for _, l := range ma {
			l.kind = '-'
			out = append(out, l)
		}
		for _, l := range mb {
			l.kind = '+'
			out = append(out, l)
		}
	} else {
		out = append(out, lcsDiff(ma, mb)...)
	}
	for _, l := range a[len(a)-suf:] {
		l.kind = ' '
		out = append(out, l)
	}
	return out
}

func lcsDiff(a, b []diffOp) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if sameLine(a[i], b[j]) {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case sameLine(a[i], b[j]):
			l := a[i]
			l.kind = ' '
			out = append(out, l)
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			l := a[i]
			l.kind = '-'
			out = append(out, l)
			i++
		default:
			l := b[j]
			l.kind = '+'
			out = append(out, l)
			j++
		}
	}
	for ; i < n; i++ {
		l := a[i]
		l.kind = '-'
		out = append(out, l)
	}
	for ; j < m; j++ {
		l := b[j]
		l.kind = '+'
		out = append(out, l)
	}
	return out
}

const diffContext = 3

// UnifiedDiff renders a unified diff of before/after for --dry-run. A nil
// before is a created file, a nil after a deleted one; identical inputs give "".
func UnifiedDiff(path string, before, after []byte) string {
	if string(before) == string(after) && (before == nil) == (after == nil) {
		return ""
	}
	ops := diffLines(diffSplit(before), diffSplit(after))
	var b strings.Builder
	from, to := path, path
	if before == nil {
		from = "/dev/null"
	}
	if after == nil {
		to = "/dev/null"
	}
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", from, to)

	changed := func(i int) bool { return ops[i].kind != ' ' }
	i := 0
	oldLine, newLine := 0, 0 // lines consumed before ops[i]
	for i < len(ops) {
		if !changed(i) {
			oldLine++
			newLine++
			i++
			continue
		}
		// hunk start: back up over context
		start := i
		for start > 0 && i-start < diffContext && ops[start-1].kind == ' ' {
			start--
		}
		ctxBefore := i - start
		// find the end: extend while changes are within 2*context of each other
		end := i
		lastChange := i
		for k := i; k < len(ops); k++ {
			if changed(k) {
				lastChange = k
				end = k
			} else if k-lastChange > 2*diffContext {
				break
			}
		}
		stop := end + 1
		for stop < len(ops) && stop-end <= diffContext && ops[stop].kind == ' ' {
			stop++
		}
		oldStart, newStart := oldLine-ctxBefore, newLine-ctxBefore
		oldCount, newCount := 0, 0
		for k := start; k < stop; k++ {
			if ops[k].kind != '+' {
				oldCount++
			}
			if ops[k].kind != '-' {
				newCount++
			}
		}
		fmt.Fprintf(&b, "@@ -%s +%s @@\n", rangeStr(oldStart, oldCount), rangeStr(newStart, newCount))
		for k := start; k < stop; k++ {
			b.WriteByte(ops[k].kind)
			b.WriteString(ops[k].text)
			b.WriteByte('\n')
			if ops[k].nonl {
				b.WriteString("\\ No newline at end of file\n")
			}
		}
		// advance counters over [i, stop)
		for k := i; k < stop; k++ {
			if ops[k].kind != '+' {
				oldLine++
			}
			if ops[k].kind != '-' {
				newLine++
			}
		}
		i = stop
	}
	return b.String()
}

func rangeStr(start, count int) string {
	switch count {
	case 0:
		return fmt.Sprintf("%d,0", start)
	case 1:
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
