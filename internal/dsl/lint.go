package dsl

import (
	"fmt"

	"github.com/libingtong/ballast/internal/model"
)

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
	CodeHermesStopContinue = "hermes-stop-continue"
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
	return e.lint(inputs, opt)
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
//
// Pointers in the returned diagnostics are relative to the rule
// ("/event", "/action", "/target"); Lint prefixes them with "/rules/<i>".
func CheckCapabilities(r *model.Rule, c model.ClientID) []model.Diagnostic {
	if r == nil || !r.AppliesTo(c) {
		return nil
	}
	mk := func(sev model.Severity, code, pointer, format string, args ...any) model.Diagnostic {
		return model.Diagnostic{
			Severity: sev, Code: code, Pointer: pointer, RuleID: r.ID, Client: c,
			Message: fmt.Sprintf(format, args...),
		}
	}
	if !r.Event.IsRuleEvent() {
		return nil // reported as bad-event by validation
	}
	caps := model.Capabilities(c)
	if !caps.HasRuleEvent(r.Event) {
		return []model.Diagnostic{mk(model.SevWarning, CodeEventUnavailable, "/event",
			"event %s does not exist on %s; the rule never fires there", r.Event, c)}
	}
	var out []model.Diagnostic
	if a := r.EffectiveAction(); a.Valid() && a != model.ActionLog {
		cell, ok := caps.Lookup(r.Event, a)
		switch {
		case !ok:
			out = append(out, mk(model.SevError, CodeActionUnsupported, "/action",
				"action %s is not supported on %s for %s events (strictest supported: %s)", a, c, r.Event, caps.StrictestSupported(r.Event)))
		case !cell.Verified:
			out = append(out, mk(model.SevWarning, CodeActionUnverified, "/action",
				"action %s on %s for %s events has not been verified against the real client", a, c, r.Event))
		}
		if ok && c == model.ClientCursor && r.Event.IsStopFamily() && a == model.ActionDeny {
			out = append(out, mk(model.SevWarning, CodeCursorStopFollowup, "/action",
				"Cursor %s deny sends a follow-up message and continues the run instead of blocking the stop", r.Event))
		}
		if ok && c == model.ClientHermes && r.Event.IsStopFamily() && a == model.ActionDeny {
			out = append(out, mk(model.SevWarning, CodeHermesStopContinue, "/action",
				"Hermes %s deny (pre_verify) asks the agent to keep going instead of blocking the stop; it is bounded by agent.max_verify_nudges", r.Event))
		}
	}
	if t := r.Target; t.Valid() && t != model.ActionLog {
		if _, ok := caps.Lookup(r.Event, t); !ok {
			out = append(out, mk(model.SevWarning, CodeTargetUnsupported, "/target",
				"target %s is not supported on %s for %s events; promoting the rule would fail", t, c, r.Event))
		}
	}
	return out
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
