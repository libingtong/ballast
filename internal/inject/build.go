package inject

import (
	"fmt"
	"strings"
	"unicode/utf8"

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
	if req == nil || req.Event == nil || req.Rules == nil {
		return nil, fmt.Errorf("inject: request needs an event and a rule set")
	}
	client := req.Client
	if client == "" {
		client = req.Event.Client
	}
	caps := model.Capabilities(client)
	ev := req.Event.Name
	if !caps.CanInject(ev) {
		return &model.Injection{}, nil
	}
	sess := req.Session
	if sess == nil {
		sess = emptyState{}
	}
	rs := req.Rules
	rules := rulesFor(rs, client)
	inj := &model.Injection{RuleDigest: Digest(rs)}

	// Which main part (if any) this event carries.
	isFull := ev == caps.FullInjectEvent &&
		(ev == model.EventSessionStart || !sess.FullInjected() || req.Event.IsFirstTurn)
	isAnchor := false
	if !isFull {
		in := rs.Settings.Inject
		switch {
		case ev == model.EventUserPromptSubmit:
			if n := settingOr(in, func(s *model.InjectSettings) *int { return s.AnchorEvery }, model.DefaultAnchorEvery); n > 0 {
				isAnchor = (sess.PromptCount()+1)%n == 0
			}
		case caps.MidTurnEvent != "" && ev == caps.MidTurnEvent:
			if n := settingOr(in, func(s *model.InjectSettings) *int { return s.BatchAfterTools }, model.DefaultBatchAfterTools); n > 0 {
				isAnchor = sess.ToolCountSinceInject() >= n
			}
		}
	}
	genChanged := sess.Generation() != "" && sess.Generation() != rs.Generation
	carriesJobs := ev == model.EventUserPromptSubmit || (caps.MidTurnEvent != "" && ev == caps.MidTurnEvent)

	total := BudgetNonFull
	if isFull {
		total = model.BudgetSessionStart
	}
	type part struct {
		kind model.InjectKind
		text string
	}
	var parts [4]part // text order: notices, gen_summary, job_results, main
	used, count := 0, 0
	// room is what a part may use: its own budget, capped by what is left of
	// the shared Text budget (after the joining blank line when not first).
	room := func(own int) int {
		sep := 0
		if count > 0 {
			sep = len(partSep)
		}
		return min(own, total-used-sep)
	}
	// take records that text was included.
	take := func(text string) {
		if count > 0 {
			used += len(partSep)
		}
		used += utf8.RuneCountInString(text)
		count++
	}

	// Allocation priority: notices, job results, gen summary, then the main
	// part gets what is left; each is also capped by its own budget.
	if isFull || genChanged {
		if t := noticesText(rs.Notices, room(BudgetNotices)); t != "" {
			parts[0] = part{model.InjectNotices, t}
			take(t)
		}
	}
	var reported []string
	if carriesJobs && len(req.NewJobs) > 0 {
		recs := make([]model.VerifyRecord, 0, len(req.NewJobs))
		for _, r := range req.NewJobs {
			if !sess.ReportedJob(r.JobID) {
				recs = append(recs, r)
			}
		}
		if t, n := jobResults(recs, room(BudgetJobResults)); t != "" {
			parts[2] = part{model.InjectJobResults, t}
			take(t)
			for _, r := range recs[:n] {
				reported = append(reported, r.JobID)
			}
		}
	}
	if genChanged {
		if t := genSummary(sess.RuleDigest(), rs, room(model.BudgetGenSummary)); t != "" {
			parts[1] = part{model.InjectGenSummary, t}
			take(t)
		}
	}
	switch {
	case isFull:
		if t := fullText(rules, rs.Generation, room(model.BudgetSessionStart)); t != "" {
			parts[3] = part{model.InjectFull, t}
			take(t)
			inj.FullEmitted, inj.AnchorEmitted = true, true
		}
	case isAnchor:
		if t := anchorText(rules, room(model.BudgetAnchor)); t != "" {
			parts[3] = part{model.InjectAnchor, t}
			take(t)
			inj.AnchorEmitted = true
		}
	}

	var texts []string
	for _, p := range parts {
		if p.text == "" {
			continue
		}
		texts = append(texts, p.text)
		inj.Kinds = append(inj.Kinds, p.kind)
	}
	inj.Text = strings.Join(texts, partSep)
	inj.ReportedJobs = reported
	return inj, nil
}

// partSep joins the parts of one Text.
const partSep = "\n\n"

func settingOr(in *model.InjectSettings, get func(*model.InjectSettings) *int, def int) int {
	if in == nil {
		return def
	}
	if p := get(in); p != nil && *p >= 0 {
		return *p
	}
	return def
}

// rulesFor returns the rules of rs that apply to client c (Rule.Clients), so
// a rule limited to one client is never announced to another.
func rulesFor(rs *model.CompiledRuleSet, c model.ClientID) []model.CompiledRule {
	out := make([]model.CompiledRule, 0, len(rs.Rules))
	for i := range rs.Rules {
		if rs.Rules[i].Rule.AppliesTo(c) {
			out = append(out, rs.Rules[i])
		}
	}
	return out
}

// FullText renders the SessionStart text for rs within model.BudgetSessionStart.
func FullText(rs *model.CompiledRuleSet) string {
	return fullText(rs.Rules, rs.Generation, model.BudgetSessionStart)
}

// AnchorText renders the one-line-per-rule anchor within model.BudgetAnchor.
func AnchorText(rs *model.CompiledRuleSet) string {
	return anchorText(rs.Rules, model.BudgetAnchor)
}

// GenSummary describes changes from prev (id→action digest stored in state)
// to rs: added, removed, action changed ("promoted"/"demoted"), within
// model.BudgetGenSummary. Empty when nothing changed.
func GenSummary(prev map[string]model.Action, rs *model.CompiledRuleSet) string {
	return genSummary(prev, rs, model.BudgetGenSummary)
}

// JobResults renders finished verify jobs.
func JobResults(recs []model.VerifyRecord) string {
	t, _ := jobResults(recs, BudgetJobResults)
	return t
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
	var b strings.Builder
	b.WriteString("---\ndescription: Ballast rules (generation ")
	b.WriteString(rs.Generation)
	b.WriteString(")\nalwaysApply: true\n---\n")
	b.WriteString(MDCMarker)
	b.WriteByte('\n')
	if t := fullText(rulesFor(rs, model.ClientCursor), rs.Generation, model.BudgetSessionStart); t != "" {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
