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

import (
	"errors"
	"strings"

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
			if dest := strings.TrimSpace(mk[len(hdrMove):]); dest != "" && cur.op == hdrUpdate {
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
	for _, h := range scanHeaders(envelope) {
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
// restricted to one line (blanks, not newlines, between the words).
func scanHeaders(text string) []header {
	var out []header
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "***") {
			continue
		}
		rest := strings.TrimLeft(line[3:], " \t")
		word, tail, found := strings.Cut(rest, ":")
		if !found {
			continue
		}
		kw := strings.Fields(word)
		if len(kw) != 2 || kw[1] != "File" {
			continue
		}
		switch kw[0] {
		case "Update", "Add", "Delete":
			p := strings.TrimSpace(tail)
			if p == "" {
				continue
			}
			op := hdrUpdate
			if kw[0] == "Add" {
				op = hdrAdd
			} else if kw[0] == "Delete" {
				op = hdrDelete
			}
			out = append(out, header{op: op, path: p})
		case "Move":
			if src, dst, ok := splitMoveFile(tail); ok {
				out = append(out, header{op: hdrMoveFile, path: src, dest: dst})
			}
		}
	}
	return out
}

// splitMoveFile splits "a -> b" (blanks around the arrow optional).
func splitMoveFile(s string) (src, dst string, ok bool) {
	a, b, found := strings.Cut(s, "->")
	if !found {
		return "", "", false
	}
	src, dst = strings.TrimSpace(a), strings.TrimSpace(b)
	return src, dst, src != "" && dst != ""
}

// normMarker returns line unchanged unless, after trimming surrounding
// blanks, it starts with "***": then the keyword part (up to the first ':'
// or the whole line) has its inner blanks collapsed to single spaces, so
// "  ***  Update  File: a" reads as "*** Update File: a". Lines starting
// with '+' or '-' are content and are never markers.
func normMarker(line string) string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "***") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
		return line
	}
	rest := t[3:]
	kw, tail, colon := strings.Cut(rest, ":")
	n := "*** " + strings.Join(strings.Fields(kw), " ")
	if colon {
		n += ":" + tail
	}
	return n
}

// fileHeader recognizes "*** Update|Add|Delete File: path".
func fileHeader(line string) (hdr, path string, ok bool) {
	for _, h := range [...]string{hdrUpdate, hdrAdd, hdrDelete} {
		if strings.HasPrefix(line, h) {
			return h, strings.TrimSpace(line[len(h):]), true
		}
	}
	return "", "", false
}
