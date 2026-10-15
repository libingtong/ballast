package codex

import (
	"errors"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// PatchSummary is the parse of an apply_patch envelope
// ("*** Begin Patch" … "*** End Patch").
type PatchSummary struct {
	Targets []string          // every file touched, in order of appearance
	Adds    []string          // "*** Add File: p"
	Deletes []string          // "*** Delete File: p"
	Moves   map[string]string // "*** Update File: a" + "*** Move to: b" ⇒ a→b (b also in Targets)
	Edits   []model.FileEdit  // per file: removed lines ⇒ OldString, added lines ⇒ NewString ("\n"-joined)
}

const (
	patchBegin = "*** Begin Patch"
	patchEnd   = "*** End Patch"

	hdrUpdate = "*** Update File:"
	hdrAdd    = "*** Add File:"
	hdrDelete = "*** Delete File:"
	hdrMove   = "*** Move to:"
)

// ParseApplyPatch parses the envelope found in tool_input.input (string) or
// tool_input.patch, or the raw string argument. Malformed envelopes return
// what could be parsed plus an error (the hook still evaluates with the
// partial targets; an empty target list makes path matchers false and
// read-before-edit cannot fire — documented limitation).
func ParseApplyPatch(envelope string) (PatchSummary, error) {
	var sum PatchSummary
	var errs []string
	if !strings.Contains(envelope, patchBegin) {
		errs = append(errs, "missing "+patchBegin)
	}

	type section struct {
		op       string // hdrUpdate | hdrAdd | hdrDelete
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
		if strings.HasPrefix(line, patchEnd) {
			flush()
			ended = true
			break
		}
		if hdr, rest, ok := fileHeader(line); ok {
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
		if strings.HasPrefix(line, hdrMove) {
			if dest := strings.TrimSpace(line[len(hdrMove):]); dest != "" && cur.op == hdrUpdate {
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
	for _, s := range sections {
		addTarget(s.path)
		edit := model.FileEdit{Path: s.path, OldString: strings.Join(s.old, "\n"), NewString: strings.Join(s.new, "\n")}
		switch s.op {
		case hdrAdd:
			sum.Adds = append(sum.Adds, s.path)
		case hdrDelete:
			sum.Deletes = append(sum.Deletes, s.path)
		case hdrUpdate:
			if s.dest != "" {
				if sum.Moves == nil {
					sum.Moves = map[string]string{}
				}
				sum.Moves[s.path] = s.dest
				addTarget(s.dest)
				edit.Path = s.dest
			}
		}
		sum.Edits = append(sum.Edits, edit)
	}
	if len(sum.Targets) == 0 {
		errs = append(errs, "no file targets")
	}
	if len(errs) > 0 {
		return sum, errors.New("codex: malformed apply_patch envelope: " + strings.Join(errs, "; "))
	}
	return sum, nil
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
