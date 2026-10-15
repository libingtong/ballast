package dsl

import "github.com/libingtong/ballast/internal/model"

// Diagnostic codes (stable; documented in docs/dsl-semantics.md §11).
const (
	CodeSchema             = "schema-invalid"
	CodeUnknownField       = "unknown-field"
	CodeVersion            = "version-unsupported"
	CodeBadID              = "bad-id"
	CodeDuplicateID        = "duplicate-id"
	CodeBadEvent           = "bad-event"
	CodeBadAction          = "bad-action"
	CodeTargetWeaker       = "target-weaker-than-action"
	CodeActionUnsupported  = "action-unsupported"
	CodeActionUnverified   = "action-unverified"
	CodeTargetUnsupported  = "target-unsupported"
	CodeEventUnavailable   = "event-unavailable"
	CodeCursorStopFollowup = "cursor-stop-followup"
	CodeFieldNotApplicable = "field-not-applicable"
	CodeInputFieldUnknown  = "input-field-unknown"
	CodeTextSourceMismatch = "text-source-mismatch"
	CodeBadRegex           = "bad-regex"
	CodeBadGlob            = "bad-glob"
	CodeGlobOnlyNegation   = "glob-only-negation"
	CodeBadCmp             = "bad-cmp"
	CodeCondArity          = "cond-arity"
	CodeUnknownPredicate   = "unknown-predicate"
	CodePriorHitUnknown    = "prior-hit-unknown-rule"
	CodeVerifyNameUnknown  = "verify-name-unknown"
	CodeMessageTooLong     = "message-too-long"
	CodeInjectTooLong      = "inject-too-long"
	CodeAnchorTooLong      = "anchor-too-long"
	CodeInjectBudget       = "inject-budget-exceeded"
	CodeExamplesMissing    = "examples-missing"
	CodeExampleBadInput    = "example-bad-input"
	CodeExampleFailed      = "example-failed"
	CodeTooManyRules       = "too-many-rules"
	CodeOrgShadowed        = "org-id-shadowed"
	CodeEmptyMatch         = "empty-rule"
)

// LintInput is one file to lint.
type LintInput struct {
	Path  string
	Layer model.Layer // LayerFile when linted standalone
	Data  []byte
}

// LintOptions controls cross-file and capability checks.
type LintOptions struct {
	// Clients to check capabilities against. Empty ⇒ claude-code only.
	// The CLI (U12) fills this with the clients recorded in
	// install-manifest.json (plus claude-code) unless --client is given; dsl
	// itself performs no I/O.
	Clients []model.ClientID
	// Layer: when LayerOrg, enforce ≤12 enabled rules in the org file alone.
	Layer model.Layer
	// Stack lints the inputs as one merged stack (org → repo → user order):
	// duplicate ids across layers, org shadowing, ≤12 total, total budget.
	Stack bool
	// RunExamples also runs TestRules and reports example-failed.
	RunExamples bool
	// Project, when set, enables verify-name-unknown checks.
	Project *model.ProjectConfig
}

// Lint validates inputs and returns all diagnostics (errors and warnings),
// sorted by file, pointer.
func (e *Engine) Lint(inputs []LintInput, opt LintOptions) []model.Diagnostic {
	panic("unimplemented: dsl.Engine.Lint")
}

// HasErrors reports whether any diagnostic is an error.
func HasErrors(ds []model.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == model.SevError {
			return true
		}
	}
	return false
}

// CheckCapabilities checks one rule against one client's matrix (exported
// for `rules promote`, which must re-check the new action).
func CheckCapabilities(r *model.Rule, c model.ClientID) []model.Diagnostic {
	panic("unimplemented: dsl.CheckCapabilities")
}

// InputFields is the frozen registry of tool_input fields per canonical
// tool (dsl-semantics §5.3). input_regex keys must be listed for EVERY tool
// the rule's `tool` list can match (unknown tools such as mcp__* accept any
// key). The virtual keys VirtualNewText / VirtualOldText read
// ToolCall.Edits[].NewString / OldString and are valid for every file-edit
// tool (Edit, MultiEdit, Write, NotebookEdit; apply_patch normalizes to Edit).
var InputFields = map[string][]string{
	model.ToolBash:         {"command", "description", "timeout", "run_in_background"},
	model.ToolPowerShell:   {"command", "description", "timeout", "run_in_background"},
	model.ToolEdit:         {"file_path", "old_string", "new_string", "replace_all", VirtualNewText, VirtualOldText},
	model.ToolMultiEdit:    {"file_path", "edits.old_string", "edits.new_string", VirtualNewText, VirtualOldText},
	model.ToolWrite:        {"file_path", "content", VirtualNewText, VirtualOldText},
	model.ToolNotebookEdit: {"notebook_path", "new_source", "cell_id", "cell_type", "edit_mode", VirtualNewText, VirtualOldText},
	model.ToolRead:         {"file_path", "offset", "limit"},
	"Grep":                 {"pattern", "path", "glob", "type", "output_mode"},
	"Glob":                 {"pattern", "path"},
	"WebFetch":             {"url", "prompt"},
	"WebSearch":            {"query"},
	model.ToolAgent:        {"prompt", "description", "subagent_type"},
}

// Virtual input_regex keys over ToolCall.Edits.
const (
	VirtualNewText = "new_text"
	VirtualOldText = "old_text"
)
