package common

import (
	"encoding/json"

	"github.com/libingtong/ballast/internal/model"
)

// Fields is a tolerant view over a JSON object's top-level keys.
type Fields map[string]json.RawMessage

// DecodeObject decodes stdin into Fields; non-object ⇒ model.ErrBadInput.
func DecodeObject(stdin []byte) (Fields, error) {
	panic("unimplemented: common.DecodeObject")
}

// Str returns the string at key; mistyped/missing ⇒ ("", false).
func (f Fields) Str(key string) (string, bool) {
	panic("unimplemented: common.Fields.Str")
}

// FirstStr returns the first present string among keys.
func (f Fields) FirstStr(keys ...string) string {
	panic("unimplemented: common.Fields.FirstStr")
}

// Bool returns the bool at key; mistyped/missing ⇒ false.
func (f Fields) Bool(key string) bool {
	panic("unimplemented: common.Fields.Bool")
}

// Int64 returns the integer at key; mistyped/missing ⇒ (0,false).
func (f Fields) Int64(key string) (int64, bool) {
	panic("unimplemented: common.Fields.Int64")
}

// Effort extracts effort.level (object) or effort (string).
func (f Fields) Effort() string {
	panic("unimplemented: common.Fields.Effort")
}

// FillCommon populates the common Event fields (session/agent/cwd/...).
func FillCommon(ev *model.Event, f Fields) {
	panic("unimplemented: common.FillCommon")
}

// FlattenStrings returns every string leaf of a JSON value keyed by dotted
// path with array indices dropped (see adapter package doc).
func FlattenStrings(raw json.RawMessage) map[string][]string {
	panic("unimplemented: common.FlattenStrings")
}

// ParseExitCode parses a leading "Exit code N" line; nil when absent.
func ParseExitCode(errText string) *int {
	panic("unimplemented: common.ParseExitCode")
}

// ResponseText flattens tool_response for the text matcher (model.Event.ToolResponseText).
func ResponseText(raw json.RawMessage) string {
	panic("unimplemented: common.ResponseText")
}

// MarshalCompact encodes v as compact JSON without HTML escaping + "\n".
func MarshalCompact(v any) ([]byte, error) {
	panic("unimplemented: common.MarshalCompact")
}

// JoinReasonContext joins reason and context with a blank line, skipping empties.
func JoinReasonContext(reason, context string) string {
	switch {
	case reason == "":
		return context
	case context == "":
		return reason
	}
	return reason + "\n\n" + context
}
