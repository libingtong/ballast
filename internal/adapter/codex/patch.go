package codex

import "github.com/libingtong/ballast/internal/model"

// PatchSummary is the parse of an apply_patch envelope
// ("*** Begin Patch" … "*** End Patch").
type PatchSummary struct {
	Targets []string          // every file touched, in order of appearance
	Adds    []string          // "*** Add File: p"
	Deletes []string          // "*** Delete File: p"
	Moves   map[string]string // "*** Update File: a" + "*** Move to: b" ⇒ a→b (b also in Targets)
	Edits   []model.FileEdit  // per file: removed lines ⇒ OldString, added lines ⇒ NewString ("\n"-joined)
}

// ParseApplyPatch parses the envelope found in tool_input.input (string) or
// tool_input.patch, or the raw string argument. Malformed envelopes return
// what could be parsed plus an error (the hook still evaluates with the
// partial targets; an empty target list makes path matchers false and
// read-before-edit cannot fire — documented limitation).
func ParseApplyPatch(envelope string) (PatchSummary, error) {
	panic("unimplemented: codex.ParseApplyPatch")
}
