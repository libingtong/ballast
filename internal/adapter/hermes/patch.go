package hermes

// Source: internal/adapter/codex/patch.go, blob last changed in commit
// b084d95cf79af695cbb08d3c91f027ba50f096bf, copied at repository commit
// cee8a4c963ce152a24e8da7b38cb289fa1b21ecc (docs/p2-design.md section 7.1:
// the parser is COPIED, adapter/codex and adapter/common stay untouched; the
// de-duplication of the two copies is left for a refactor after P2).
//
// Differences from the Codex original (everything else is unchanged, so that
// the golden rows copied into testdata/patches.json keep passing):
//
//   - Type and function names are package-private (patchSummary, parsePatch)
//     and the error prefix is "hermes:".
//   - Hermes' V4A dialect has "*** Move File: a -> b" next to the Codex
//     "*** Move to: b" line; both are understood (source and destination are
//     targets, Moves[a] = b).
//   - After the section parse, every column-0 file header anywhere in the
//     text (also after "*** End Patch", also outside any Begin/End pair) is
//     merged in. Hermes' own path guard collects headers with a whole-text
//     line regex (tools/file_tools.py _V4A_*_HEADER_RE), while its applier
//     stops at the first strict "*** End Patch" line; the union is the safe
//     superset of what Hermes may touch (fail towards detecting more
//     targets, never fewer).
//   - "Blank" means Python's notion, not Go's: Hermes recognizes headers with
//     the regexes `\*\*\*\s*Update\s+File:\s*(.+)` and strips the path with
//     str.strip(), and Python's \s / str.isspace() also count U+001C..U+001F
//     as whitespace, which strings.Fields / TrimSpace do not. Every blank
//     test in this file therefore goes through isPySpace; a line that looks
//     like a file header once such characters are ignored but cannot be
//     parsed is reported in the error returned by NormalizeTool only. The
//     production path (common.FillTools) currently discards that error, so
//     it does not reach the audit; Hermes itself does not treat such a line
//     as a header, so nothing is bypassed.

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/libingtong/ballast/internal/model"
)

// patchSummary is the parse of a V4A envelope ("*** Begin Patch" ...
// "*** End Patch") found in the patch tool's `patch` argument.
type patchSummary struct {
	Targets []string          // every file touched, in order of appearance
	Adds    []string          // "*** Add File: p"
	Deletes []string          // "*** Delete File: p"
	Moves   map[string]string // "*** Update File: a" + "*** Move to: b", or "*** Move File: a -> b": a→b (b also in Targets)
	Edits   []model.FileEdit  // per file: removed lines ⇒ OldString, added lines ⇒ NewString ("\n"-joined)
}

const (
	patchBegin = "*** Begin Patch"
	patchEnd   = "*** End Patch"

	hdrUpdate   = "*** Update File:"
	hdrAdd      = "*** Add File:"
	hdrDelete   = "*** Delete File:"
	hdrMove     = "*** Move to:"
	hdrMoveFile = "*** Move File:" // Hermes dialect: "*** Move File: a -> b"
)

// parsePatch parses the envelope found in the patch tool's `patch` argument.
// Malformed envelopes return what could be parsed plus an error (the hook
// still evaluates with the partial targets; an empty target list makes path
// matchers false and read-before-edit cannot fire — documented limitation).
func parsePatch(envelope string) (patchSummary, error) {
	var sum patchSummary
	var errs []string
	if !strings.Contains(envelope, patchBegin) {
		errs = append(errs, "missing "+patchBegin)
	}

	type section struct {
		op       string // hdrUpdate | hdrAdd | hdrDelete | hdrMoveFile
		path     string
		dest     string
		old, new []string
	}
	var cur *section
	var sections []*section
	ended := false

	flush := func() {
		if cur != nil {
			sections = append(sections, cur)
			cur = nil
		}
	}

	for _, line := range strings.Split(envelope, "\n") {
		line = strings.TrimSuffix(line, "\r")
		// Inside an Update section a line starting with a space is a context
		// line, never a marker (even if its text reads "*** End Patch"):
		// accepting it would let file content end the envelope early or
		// forge extra targets. Lenient markers are only honoured at section
		// boundaries (no open section, Add/Delete section, or a first
		// character other than a space such as a tab).
		if cur != nil && cur.op == hdrUpdate && strings.HasPrefix(line, " ") {
			continue
		}
		// Markers are recognized after trimming surrounding blanks and
		// collapsing blanks inside the marker keyword; +/- content lines keep
		// the raw line.
		mk := normMarker(line)
		if strings.HasPrefix(mk, patchEnd) {
			flush()
			ended = true
			break
		}
		if strings.HasPrefix(mk, hdrMoveFile) {
			flush()
			src, dst, ok := splitMoveFile(mk[len(hdrMoveFile):])
			if !ok {
				errs = append(errs, "bad path pair after "+hdrMoveFile)
				continue
			}
			sections = append(sections, &section{op: hdrMoveFile, path: src, dest: dst})
			continue
		}
		if hdr, rest, ok := fileHeader(mk); ok {
			flush()
			if rest == "" {
				errs = append(errs, "empty path after "+hdr)
				continue
			}
			cur = &section{op: hdr, path: rest}
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(mk, hdrMove) {
			if dest := trimPy(mk[len(hdrMove):]); dest != "" && cur.op == hdrUpdate {
				cur.dest = dest
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			cur.new = append(cur.new, line[1:])
		case strings.HasPrefix(line, "-") && cur.op != hdrAdd:
			cur.old = append(cur.old, line[1:])
		}
	}
	flush()
	if !ended {
		errs = append(errs, "missing "+patchEnd)
	}

	seen := map[string]bool{}
	addTarget := func(p string) {
		if !seen[p] {
			seen[p] = true
			sum.Targets = append(sum.Targets, p)
		}
	}
	addMove := func(a, b string) {
		if sum.Moves == nil {
			sum.Moves = map[string]string{}
		}
		sum.Moves[a] = b
		addTarget(b)
	}
	for _, s := range sections {
		addTarget(s.path)
		edit := model.FileEdit{Path: s.path, OldString: strings.Join(s.old, "\n"), NewString: strings.Join(s.new, "\n")}
		switch s.op {
		case hdrAdd:
			sum.Adds = append(sum.Adds, s.path)
		case hdrDelete:
			sum.Deletes = append(sum.Deletes, s.path)
		case hdrUpdate, hdrMoveFile:
			if s.dest != "" {
				addMove(s.path, s.dest)
				edit.Path = s.dest
			}
		}
		sum.Edits = append(sum.Edits, edit)
	}

	// Merge in headers the section parse did not reach (see the file header).
	hdrs, odd := scanHeaders(envelope)
	for _, n := range odd {
		errs = append(errs, "line "+strconv.Itoa(n)+" looks like a file header but its path cannot be parsed")
	}
	for _, h := range hdrs {
		if h.op == hdrMoveFile {
			if seen[h.path] && seen[h.dest] {
				continue
			}
			addTarget(h.path)
			addMove(h.path, h.dest)
			sum.Edits = append(sum.Edits, model.FileEdit{Path: h.dest})
			continue
		}
		if seen[h.path] {
			continue
		}
		addTarget(h.path)
		switch h.op {
		case hdrAdd:
			sum.Adds = append(sum.Adds, h.path)
		case hdrDelete:
			sum.Deletes = append(sum.Deletes, h.path)
		}
		sum.Edits = append(sum.Edits, model.FileEdit{Path: h.path})
	}

	if len(sum.Targets) == 0 {
		errs = append(errs, "no file targets")
	}
	if len(errs) > 0 {
		return sum, errors.New("hermes: malformed patch envelope: " + strings.Join(errs, "; "))
	}
	return sum, nil
}

// header is one column-0 file header found by scanHeaders.
type header struct {
	op   string // hdrUpdate | hdrAdd | hdrDelete | hdrMoveFile
	path string
	dest string // hdrMoveFile only
}

// scanHeaders lists every file header that starts in column 0 of a line,
// wherever it is in the text. It mirrors Hermes' whole-text header regexes:
//
//	^\*\*\*\s*(Update|Add|Delete)\s+File:\s*(.+)$
//	^\*\*\*\s*Move\s+File:\s*(.+?)\s*->\s*(.+)$
//
// restricted to one line (blanks, not newlines, between the words), with
// "blank" meaning Python's \s (see isPySpace). The second result lists the
// 1-based numbers of column-0 lines that read like a file header once blanks,
// control and format characters are ignored but that could not be parsed
// into a path: a diagnostic for what the parser cannot attribute to a target.
func scanHeaders(text string) (out []header, odd []int) {
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "***") {
			continue
		}
		before := len(out)
		rest := strings.TrimLeftFunc(line[3:], isPySpace)
		word, tail, found := strings.Cut(rest, ":")
		if found {
			kw := strings.FieldsFunc(word, isPySpace)
			if len(kw) == 2 && kw[1] == "File" {
				switch kw[0] {
				case "Update", "Add", "Delete":
					if p := trimPy(tail); p != "" {
						op := hdrUpdate
						if kw[0] == "Add" {
							op = hdrAdd
						} else if kw[0] == "Delete" {
							op = hdrDelete
						}
						out = append(out, header{op: op, path: p})
					}
				case "Move":
					if src, dst, ok := splitMoveFile(tail); ok {
						out = append(out, header{op: hdrMoveFile, path: src, dest: dst})
					}
				}
			}
		}
		if len(out) == before && lookalikeHeader(line) {
			odd = append(odd, i+1)
		}
	}
	return out, odd
}

// lookalikeHeader reports whether line, with every blank, control and format
// character removed, reads "***Update|Add|Delete|MoveFile:".
func lookalikeHeader(line string) bool {
	compact := strings.Map(func(r rune) rune {
		if isPySpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, line)
	for _, kw := range [...]string{"Update", "Add", "Delete", "Move"} {
		if strings.HasPrefix(compact, "***"+kw+"File:") {
			return true
		}
	}
	return false
}

// isPySpace is Python's str.isspace() / regex \s for str patterns: Unicode
// white space plus the separator controls U+001C..U+001F, which Go's
// unicode.IsSpace does not include.
func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// trimPy is Python's str.strip().
func trimPy(s string) string { return strings.TrimFunc(s, isPySpace) }

// splitMoveFile splits "a -> b" (blanks around the arrow optional).
func splitMoveFile(s string) (src, dst string, ok bool) {
	a, b, found := strings.Cut(s, "->")
	if !found {
		return "", "", false
	}
	src, dst = trimPy(a), trimPy(b)
	return src, dst, src != "" && dst != ""
}

// normMarker returns line unchanged unless, after trimming surrounding
// blanks, it starts with "***": then the keyword part (up to the first ':'
// or the whole line) has its inner blanks collapsed to single spaces, so
// "  ***  Update  File: a" reads as "*** Update File: a". Lines starting
// with '+' or '-' are content and are never markers.
func normMarker(line string) string {
	t := trimPy(line)
	if !strings.HasPrefix(t, "***") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
		return line
	}
	rest := t[3:]
	kw, tail, colon := strings.Cut(rest, ":")
	n := "*** " + strings.Join(strings.FieldsFunc(kw, isPySpace), " ")
	if colon {
		n += ":" + tail
	}
	return n
}

// fileHeader recognizes "*** Update|Add|Delete File: path".
func fileHeader(line string) (hdr, path string, ok bool) {
	for _, h := range [...]string{hdrUpdate, hdrAdd, hdrDelete} {
		if strings.HasPrefix(line, h) {
			return h, trimPy(line[len(h):]), true
		}
	}
	return "", "", false
}
