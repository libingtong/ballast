package inject

import (
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Non-SessionStart total budget (runes).
const BudgetNonFull = 2500

// Per-part budgets not defined in model.
const (
	BudgetJobResults = 400
	BudgetNotices    = 400
)

// Builder implements ports.Injector.
type Builder struct{}

var _ ports.Injector = Builder{}

// Build implements ports.Injector.
func (Builder) Build(req *model.InjectRequest) (*model.Injection, error) {
	panic("unimplemented: inject.Builder.Build")
}

// FullText renders the SessionStart text for rs within model.BudgetSessionStart.
func FullText(rs *model.CompiledRuleSet) string {
	panic("unimplemented: inject.FullText")
}

// AnchorText renders the one-line-per-rule anchor within model.BudgetAnchor.
func AnchorText(rs *model.CompiledRuleSet) string {
	panic("unimplemented: inject.AnchorText")
}

// GenSummary describes changes from prev (id→action digest stored in state)
// to rs: added, removed, action changed ("promoted"/"demoted"), within
// model.BudgetGenSummary. Empty when nothing changed.
func GenSummary(prev map[string]model.Action, rs *model.CompiledRuleSet) string {
	panic("unimplemented: inject.GenSummary")
}

// JobResults renders finished verify jobs.
func JobResults(recs []model.VerifyRecord) string {
	panic("unimplemented: inject.JobResults")
}

// Digest returns the id→action digest of rs.
func Digest(rs *model.CompiledRuleSet) map[string]model.Action {
	d := make(map[string]model.Action, len(rs.Rules))
	for _, r := range rs.Rules {
		d[r.Rule.ID] = r.Action
	}
	return d
}

// MDCMarker is the first line after the front matter of the managed Cursor
// rules file; install uses it (plus the manifest hash) to recognize ownership.
const MDCMarker = "<!-- ballast-managed: do not edit; regenerate with `ballast install --client cursor` -->"

// RenderMDC renders ~/.cursor/rules/ballast.mdc:
//
//	---
//	description: Ballast rules (generation <g>)
//	alwaysApply: true
//	---
//	<MDCMarker>
//	<FullText(rs)>
func RenderMDC(rs *model.CompiledRuleSet) []byte {
	panic("unimplemented: inject.RenderMDC")
}
