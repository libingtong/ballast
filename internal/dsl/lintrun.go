package dsl

import (
	"sort"

	"github.com/libingtong/ballast/internal/model"
)

type lintFile struct {
	in     LintInput
	layer  model.Layer
	rf     *model.RuleFile
	failed bool // decode failure or error-level diagnostics
}

func layerRank(l model.Layer) int {
	switch l {
	case model.LayerOrg:
		return 0
	case model.LayerRepo:
		return 1
	case model.LayerUser:
		return 2
	}
	return 3
}

func (e *Engine) lint(inputs []LintInput, opt LintOptions) []model.Diagnostic {
	clients := dedupeClients(opt.Clients)
	var out []model.Diagnostic

	files := make([]*lintFile, len(inputs))
	for i, in := range inputs {
		l := in.Layer
		if (l == "" || l == model.LayerFile) && opt.Layer != "" {
			l = opt.Layer
		}
		if l == "" {
			l = model.LayerFile
		}
		files[i] = &lintFile{in: in, layer: l}
	}

	// Phase 1: decode.
	for _, f := range files {
		rf, err := DecodeRuleFile(f.in.Data)
		if err != nil {
			f.failed = true
			if de, ok := err.(*decodeError); ok {
				for _, d := range de.diags {
					d.File = f.in.Path
					out = append(out, d)
				}
			} else {
				out = append(out, model.Diagnostic{Severity: model.SevError, Code: CodeSchema, File: f.in.Path, Message: err.Error()})
			}
			continue
		}
		f.rf = rf
	}

	// Rule ids prior_hit may refer to: the file itself, or the whole stack.
	var stackKnown map[string]bool
	if opt.Stack {
		stackKnown = map[string]bool{}
		for _, f := range files {
			if f.rf != nil {
				for i := range f.rf.Rules {
					stackKnown[f.rf.Rules[i].ID] = true
				}
			}
		}
	}

	// Phase 2: per-file validation, capabilities, budgets, org cap.
	for _, f := range files {
		if f.rf == nil {
			continue
		}
		v := &validator{e: e, d: &diags{file: f.in.Path}, known: stackKnown, project: opt.Project}
		v.file(f.rf)
		out = append(out, v.d.list...)
		if HasErrors(v.d.list) {
			f.failed = true
		}
		for i := range f.rf.Rules {
			r := &f.rf.Rules[i]
			for _, c := range clients {
				for _, d := range CheckCapabilities(r, c) {
					d.File = f.in.Path
					d.Pointer = ptr("rules", i) + d.Pointer
					out = append(out, d)
				}
			}
		}
		if !opt.Stack {
			d := &diags{file: f.in.Path}
			var rules []*model.Rule
			for i := range f.rf.Rules {
				if r := &f.rf.Rules[i]; r.IsEnabled() {
					rules = append(rules, r)
				}
			}
			checkInjectBudget(d, rules)
			if f.layer == model.LayerOrg {
				n := 0
				for i := range f.rf.Rules {
					if !f.rf.Rules[i].IsEnabled() {
						continue
					}
					if n++; n > model.MaxEnabledRules {
						d.err(CodeTooManyRules, ptr("rules", i), f.rf.Rules[i].ID,
							"org file has more than %d enabled rules; this is enabled rule #%d", model.MaxEnabledRules, n)
					}
				}
			}
			out = append(out, d.list...)
		}
	}

	if opt.Stack {
		out = append(out, lintStack(files)...)
	}

	if opt.RunExamples {
		for _, f := range files {
			if f.rf == nil || f.failed {
				continue
			}
			for _, c := range e.runFile(f.in.Path, f.rf) {
				if c.Pass || c.Skipped {
					continue
				}
				code := CodeExampleFailed
				if c.badInput {
					code = CodeExampleBadInput
				}
				out = append(out, model.Diagnostic{
					Severity: model.SevError, Code: code, File: f.in.Path,
					Pointer: ptr("rules", c.ruleIdx, "examples", c.exIdx), RuleID: c.RuleID,
					Message: c.failureMessage(),
				})
			}
		}
	}

	sortDiagnostics(out)
	return out
}

func dedupeClients(cs []model.ClientID) []model.ClientID {
	if len(cs) == 0 {
		return []model.ClientID{model.ClientClaude}
	}
	seen := map[model.ClientID]bool{}
	var out []model.ClientID
	for _, c := range cs {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// lintStack checks the inputs as one merged stack (org → repo → user).
func lintStack(files []*lintFile) []model.Diagnostic {
	ordered := make([]*lintFile, 0, len(files))
	for _, f := range files {
		if f.rf != nil {
			ordered = append(ordered, f)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return layerRank(ordered[i].layer) < layerRank(ordered[j].layer) })

	type owner struct {
		layer model.Layer
		path  string
	}
	var out []model.Diagnostic
	seen := map[string]owner{}
	var effective []*model.Rule
	enabled := 0
	for _, f := range ordered {
		d := &diags{file: f.in.Path}
		for i := range f.rf.Rules {
			r := &f.rf.Rules[i]
			if prior, dup := seen[r.ID]; dup {
				switch {
				case prior.layer == model.LayerOrg && f.layer != model.LayerOrg:
					d.err(CodeOrgShadowed, ptr("rules", i, "id"), r.ID,
						"rule id %q is defined by the org layer (%s) and cannot be redefined", r.ID, prior.path)
				case prior.layer == f.layer:
					d.err(CodeDuplicateID, ptr("rules", i, "id"), r.ID,
						"rule id %q is already defined in %s", r.ID, prior.path)
				default:
					d.warn(CodeDuplicateID, ptr("rules", i, "id"), r.ID,
						"rule %q is shadowed by the %s layer rule of the same id (%s)", r.ID, prior.layer, prior.path)
				}
				continue
			}
			seen[r.ID] = owner{f.layer, f.in.Path}
			if !r.IsEnabled() {
				continue
			}
			if enabled++; enabled > model.MaxEnabledRules {
				d.err(CodeTooManyRules, ptr("rules", i), r.ID,
					"merged stack has more than %d enabled rules; this rule (#%d) would be dropped", model.MaxEnabledRules, enabled)
				continue
			}
			effective = append(effective, r)
		}
		out = append(out, d.list...)
	}
	bd := &diags{}
	checkInjectBudget(bd, effective)
	return append(out, bd.list...)
}

// AnchorText returns a rule's anchor: the explicit anchor, or the first line
// of the inject text truncated to model.MaxAnchorRunes.
func AnchorText(in *model.Inject) string {
	if in == nil {
		return ""
	}
	if in.Anchor != "" {
		return in.Anchor
	}
	line := in.Text
	for i := 0; i < len(line); i++ {
		if line[i] == '\n' {
			line = line[:i]
			break
		}
	}
	return truncateRunes(line, model.MaxAnchorRunes)
}

func checkInjectBudget(d *diags, rules []*model.Rule) {
	full, anchors := 0, 0
	for _, r := range rules {
		if r.Inject == nil {
			continue
		}
		full += RuneLen(r.Inject.Text)
		anchors += RuneLen(AnchorText(r.Inject))
	}
	if full > model.BudgetSessionStart {
		d.err(CodeInjectBudget, "", "", "inject texts of the enabled rules total %d characters (limit %d)", full, model.BudgetSessionStart)
	}
	if anchors > model.BudgetAnchor {
		d.warn(CodeInjectBudget, "", "", "inject anchors total %d characters (limit %d); they are truncated at run time", anchors, model.BudgetAnchor)
	}
}
