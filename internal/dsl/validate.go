package dsl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// Validate performs the per-file semantic checks that do not depend on
// clients or other layers (ids, events, actions, applicability, regex/glob
// compile, Cond arity, Cmp non-empty, text budgets, examples presence).
func (e *Engine) Validate(path string, f *model.RuleFile) []model.Diagnostic {
	v := &validator{e: e, d: &diags{file: path}}
	v.file(f)
	return v.d.list
}

type validator struct {
	e *Engine
	d *diags
	// known are the rule ids prior_hit.rule may refer to; nil ⇒ the ids of
	// the validated file.
	known   map[string]bool
	project *model.ProjectConfig
}

func (v *validator) file(f *model.RuleFile) {
	d := v.d
	if f == nil {
		d.err(CodeSchema, "", "", "empty rule file")
		return
	}
	if f.Version != SchemaVersion {
		d.err(CodeVersion, "/version", "", "unsupported rule file version %d (only version %d is accepted)", f.Version, SchemaVersion)
	}
	if f.Rules == nil {
		d.err(CodeSchema, "/rules", "", "\"rules\" is required")
	}
	if s := f.Settings; s != nil && s.Inject != nil {
		if a := s.Inject.AnchorEvery; a != nil && (*a < 0 || *a > 1000) {
			d.err(CodeSchema, ptr("settings", "inject", "anchor_every"), "", "anchor_every must be between 0 and 1000")
		}
		if b := s.Inject.BatchAfterTools; b != nil && (*b < 0 || *b > 10000) {
			d.err(CodeSchema, ptr("settings", "inject", "batch_after_tools"), "", "batch_after_tools must be between 0 and 10000")
		}
	}
	if v.known == nil {
		v.known = make(map[string]bool, len(f.Rules))
		for i := range f.Rules {
			v.known[f.Rules[i].ID] = true
		}
	}
	seen := make(map[string]int, len(f.Rules))
	for i := range f.Rules {
		r := &f.Rules[i]
		if first, dup := seen[r.ID]; dup && r.ID != "" {
			d.err(CodeDuplicateID, ptr("rules", i, "id"), r.ID, "duplicate rule id %q (first defined at /rules/%d)", r.ID, first)
		} else {
			seen[r.ID] = i
		}
		v.rule(i, r)
	}
}

// validRuleID reports whether id matches ^[a-z0-9-]{3,40}$.
func validRuleID(id string) bool {
	if len(id) < 3 || len(id) > 40 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func (v *validator) rule(i int, r *model.Rule) {
	d := v.d
	base := ptr("rules", i)
	id := r.ID
	if !validRuleID(id) {
		d.err(CodeBadID, base+"/id", id, "rule id %q must match ^[a-z0-9-]{3,40}$", id)
	}
	if RuneLen(r.Description) > 500 {
		d.err(CodeSchema, base+"/description", id, "description is longer than 500 characters")
	}
	eventOK := r.Event.IsRuleEvent()
	if !eventOK {
		d.err(CodeBadEvent, base+"/event", id, "event %q is not one of PreToolUse, UserPromptSubmit, PostToolUse, PostToolUseFailure, Stop, SubagentStop", string(r.Event))
	}
	if r.Action != model.ActionNone && !r.Action.Valid() {
		d.err(CodeBadAction, base+"/action", id, "action %q is not one of log, warn, ask, deny", string(r.Action))
	}
	if r.Target != model.ActionNone && !r.Target.Valid() {
		d.err(CodeBadAction, base+"/target", id, "target %q is not one of log, warn, ask, deny", string(r.Target))
	}
	if r.Target.Valid() && r.EffectiveAction().Valid() && r.Target.Severity() < r.EffectiveAction().Severity() {
		d.warn(CodeTargetWeaker, base+"/target", id, "target %q is weaker than action %q", string(r.Target), string(r.EffectiveAction()))
	}
	v.clients(i, r)
	if RuneLen(r.Message) > model.MaxMessageRunes {
		d.err(CodeMessageTooLong, base+"/message", id, "message has %d characters (limit %d)", RuneLen(r.Message), model.MaxMessageRunes)
	}
	if in := r.Inject; in != nil {
		if in.Text == "" {
			d.err(CodeSchema, base+"/inject", id, "inject needs a non-empty text")
		}
		if n := RuneLen(in.Text); n > model.MaxInjectTextRunes {
			d.err(CodeInjectTooLong, base+"/inject", id, "inject text has %d characters (limit %d)", n, model.MaxInjectTextRunes)
		}
		if n := RuneLen(in.Anchor); n > model.MaxAnchorRunes {
			d.err(CodeAnchorTooLong, base+"/inject/anchor", id, "inject anchor has %d characters (limit %d)", n, model.MaxAnchorRunes)
		}
	}
	m := r.Match
	hasMatch := m != nil && !isEmptyMatch(m)
	if !hasMatch && len(r.When) == 0 {
		d.err(CodeEmptyMatch, base, id, "rule has neither match nor when and would fire on every %s event", string(r.Event))
	}
	if hasMatch && eventOK {
		v.match(base+"/match", r, m)
	}
	for j := range r.When {
		v.cond(ptr("rules", i, "when", j), r, &r.When[j])
	}
	v.examples(base, r)
}

func isEmptyMatch(m *model.Match) bool {
	return len(m.Tool) == 0 && len(m.Path) == 0 && len(m.AgentType) == 0 && len(m.InputRegex) == 0 &&
		m.RunInBackground == nil && m.Bash == nil && m.PS == nil && m.Text == nil
}

func (v *validator) clients(i int, r *model.Rule) {
	if r.Clients == nil {
		return
	}
	if len(r.Clients) == 0 {
		v.d.err(CodeSchema, ptr("rules", i, "clients"), r.ID, "clients must not be empty (omit it to apply to all clients)")
	}
	seen := map[model.ClientID]bool{}
	for j, c := range r.Clients {
		switch {
		case !c.Valid():
			v.d.err(CodeSchema, ptr("rules", i, "clients", j), r.ID, "unknown client %q", string(c))
		case seen[c]:
			v.d.err(CodeSchema, ptr("rules", i, "clients", j), r.ID, "client %q is listed twice", string(c))
		}
		seen[c] = true
	}
}

// toolScope describes which registered tools a rule's tool list can match.
type toolScope struct {
	given      bool
	matched    []string // registered tools matched
	anyShell   bool
	anyTarget  bool
	registered bool // at least one registered tool matched
}

func isTargetTool(name string) bool {
	switch name {
	case model.ToolBash, model.ToolPowerShell, model.ToolEdit, model.ToolMultiEdit,
		model.ToolWrite, model.ToolNotebookEdit, model.ToolRead:
		return true
	}
	return false
}

func scopeOf(m *model.Match) toolScope {
	var s toolScope
	if m == nil || len(m.Tool) == 0 {
		return s
	}
	s.given = true
	g := newNameGlobs(m.Tool)
	for _, name := range sortedKeys(InputFields) {
		if g.match(name) {
			s.matched = append(s.matched, name)
			s.registered = true
			if name == model.ToolBash || name == model.ToolPowerShell {
				s.anyShell = true
			}
			if isTargetTool(name) {
				s.anyTarget = true
			}
		}
	}
	return s
}

// notShell reports that the tool list can only match registered non-shell tools.
func (s toolScope) notShell() bool { return s.given && s.registered && !s.anyShell }

// noTargets reports that the tool list can only match registered tools
// without a path target.
func (s toolScope) noTargets() bool { return s.given && s.registered && !s.anyTarget }

func (v *validator) globList(ptrStr, ruleID string, list []string) {
	if code, msg := checkGlobList(list); code != "" {
		v.d.err(code, ptrStr, ruleID, "%s", msg)
	}
}

func (v *validator) regex(ptrStr, ruleID, src string) {
	if src == "" {
		v.d.err(CodeSchema, ptrStr, ruleID, "empty regular expression")
		return
	}
	if _, err := v.e.lazy(src).get(); err != nil {
		v.d.err(CodeBadRegex, ptrStr, ruleID, "%q is not valid RE2: %v", src, err)
	}
}

func (v *validator) regexList(ptrStr, ruleID string, list []string) {
	for j, s := range list {
		v.regex(ptrStr+"/"+fmt.Sprint(j), ruleID, s)
	}
}

func (v *validator) cmp(ptrStr, ruleID string, c model.Cmp) {
	if c.Empty() {
		v.d.err(CodeBadCmp, ptrStr, ruleID, "comparison needs at least one of eq, ne, gt, gte, lt, lte")
	}
}

func (v *validator) notApplicable(ptrStr, ruleID, format string, args ...any) {
	v.d.err(CodeFieldNotApplicable, ptrStr, ruleID, format, args...)
}

func (v *validator) match(base string, r *model.Rule, m *model.Match) {
	id := r.ID
	ev := r.Event
	toolEvent := ev.IsToolEvent()
	scope := scopeOf(m)

	if !toolEvent {
		na := func(field string, present bool) {
			if present {
				v.notApplicable(base+"/"+field, id, "match.%s only applies to tool events (PreToolUse, PostToolUse, PostToolUseFailure), not %s", field, string(ev))
			}
		}
		na("tool", len(m.Tool) > 0)
		na("path", len(m.Path) > 0)
		na("input_regex", len(m.InputRegex) > 0)
		na("run_in_background", m.RunInBackground != nil)
		na("bash", m.Bash != nil)
		na("ps", m.PS != nil)
	}
	if ev == model.EventPreToolUse && m.Text != nil {
		v.notApplicable(base+"/text", id, "match.text does not apply to PreToolUse (no free text is available)")
	}

	if len(m.Tool) > 0 {
		v.globList(base+"/tool", id, m.Tool)
	}
	if len(m.AgentType) > 0 {
		v.globList(base+"/agent_type", id, m.AgentType)
	}
	if len(m.Path) > 0 {
		v.globList(base+"/path", id, m.Path)
		if toolEvent && scope.noTargets() {
			v.notApplicable(base+"/path", id, "match.path needs a tool with a path target, but match.tool only matches %s", strings.Join(scope.matched, ", "))
		}
	}
	if len(m.InputRegex) > 0 && toolEvent {
		v.inputRegex(base, id, m, scope)
	}
	if m.RunInBackground != nil && toolEvent && scope.notShell() {
		v.notApplicable(base+"/run_in_background", id, "run_in_background only applies to Bash and PowerShell, but match.tool only matches %s", strings.Join(scope.matched, ", "))
	}
	if m.Bash != nil {
		v.shell(base+"/bash", id, "bash", m.Bash, toolEvent, scope)
	}
	if m.PS != nil {
		v.shell(base+"/ps", id, "ps", m.PS, toolEvent, scope)
	}
	if m.Text != nil && ev != model.EventPreToolUse {
		v.text(base+"/text", r, m.Text)
	}
}

func validInputKey(k string) bool {
	if k == "" {
		return false
	}
	for _, seg := range strings.Split(k, ".") {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

func (v *validator) inputRegex(base, id string, m *model.Match, scope toolScope) {
	p := base + "/input_regex"
	if !scope.given {
		v.notApplicable(p, id, "match.input_regex needs match.tool to know which tool_input fields exist")
	}
	for _, key := range sortedKeys(m.InputRegex) {
		kp := p + "/" + strings.ReplaceAll(key, "/", "~1")
		if !validInputKey(key) {
			v.d.err(CodeSchema, kp, id, "input_regex key %q is not a field path", key)
			continue
		}
		if len(m.InputRegex[key]) == 0 {
			v.d.err(CodeSchema, kp, id, "input_regex.%s needs at least one regular expression", key)
		}
		v.regexList(kp, id, m.InputRegex[key])
		var bad []string
		for _, tool := range scope.matched {
			if !containsString(InputFields[tool], key) {
				bad = append(bad, tool)
			}
		}
		if len(bad) > 0 {
			hint := ""
			if len(bad) == 1 {
				hint = " (valid: " + strings.Join(InputFields[bad[0]], ", ") + ")"
			}
			v.d.err(CodeInputFieldUnknown, kp, id, "input_regex.%s is not a tool_input field of %s%s", key, strings.Join(bad, ", "), hint)
			// doc.go's acceptance list calls this field-not-applicable while
			// dsl-semantics §5.3/§11 call it input-field-unknown; report both.
			v.d.err(CodeFieldNotApplicable, kp, id, "input_regex.%s does not apply to %s", key, strings.Join(bad, ", "))
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (v *validator) shell(p, id, name string, s *model.ShellMatch, toolEvent bool, scope toolScope) {
	if toolEvent && scope.notShell() {
		v.notApplicable(p, id, "match.%s only applies to Bash/PowerShell, but match.tool only matches %s", name, strings.Join(scope.matched, ", "))
	}
	if shellMatchEmpty(s) {
		v.d.err(CodeSchema, p, id, "match.%s must have at least one field", name)
	}
	if len(s.Cmd) > 0 {
		v.globList(p+"/cmd", id, s.Cmd)
	}
	v.regexList(p+"/args_regex", id, s.ArgsRegex)
	for j, f := range s.Flags {
		if f == "" || f[0] != '-' {
			v.d.err(CodeSchema, fmt.Sprintf("%s/flags/%d", p, j), id, "flag %q must start with '-'", f)
		}
	}
	for j, g := range s.GitSub {
		if g == "" {
			v.d.err(CodeSchema, fmt.Sprintf("%s/git_sub/%d", p, j), id, "git_sub entries must not be empty")
		}
	}
	if s.SleepS != nil {
		v.cmp(p+"/sleep_s", id, *s.SleepS)
	}
	for _, f := range []struct {
		name string
		list []string
	}{{"writes", s.Writes}, {"pipe_from", s.PipeFrom}, {"pipe_to", s.PipeTo}} {
		if len(f.list) > 0 {
			v.globList(p+"/"+f.name, id, f.list)
		}
	}
}

func shellMatchEmpty(s *model.ShellMatch) bool {
	return len(s.Cmd) == 0 && len(s.ArgsRegex) == 0 && len(s.Flags) == 0 && len(s.GitSub) == 0 &&
		s.SleepS == nil && s.Async == nil && s.InLoop == nil && s.LoopBounded == nil && s.InSubst == nil &&
		s.Sudo == nil && s.Dynamic == nil && len(s.Writes) == 0 && len(s.PipeFrom) == 0 && len(s.PipeTo) == 0
}

func (v *validator) text(p string, r *model.Rule, t *model.TextMatch) {
	id := r.ID
	if want := defaultTextSource(r.Event); t.Source != "" && t.Source != want {
		v.d.err(CodeTextSourceMismatch, p+"/source", id, "text source %q is not available on %s events (use %q)", string(t.Source), string(r.Event), string(want))
	}
	seen := map[string]bool{}
	for j, s := range t.Preprocess {
		if s != model.PreStripCodeBlocks && s != model.PreStripQuotes {
			v.d.err(CodeSchema, fmt.Sprintf("%s/preprocess/%d", p, j), id, "unknown preprocess step %q", s)
		}
		if seen[s] {
			v.d.err(CodeSchema, fmt.Sprintf("%s/preprocess/%d", p, j), id, "preprocess step %q is listed twice", s)
		}
		seen[s] = true
	}
	if w := t.Window; w != nil {
		if w.HeadChars < 0 || w.TailChars < 0 || (w.HeadChars == 0 && w.TailChars == 0) {
			v.d.err(CodeSchema, p+"/window", id, "window needs head_chars and/or tail_chars of at least 1")
		}
	}
	v.regexList(p+"/regex", id, t.Regex)
	v.regexList(p+"/not_regex", id, t.NotRegex)
	if t.CJKRatio != nil {
		v.cmp(p+"/cjk_ratio", id, *t.CJKRatio)
	}
	if c := t.CountRegex; c != nil {
		v.regex(p+"/count_regex/regex", id, c.Regex)
		v.cmp(p+"/count_regex", id, c.Cmp)
	}
	if len(t.Regex) == 0 && len(t.NotRegex) == 0 && t.CJKRatio == nil && t.CountRegex == nil {
		v.d.err(CodeSchema, p, id, "match.text needs at least one of regex, not_regex, cjk_ratio, count_regex")
	}
}

func validVerifyName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && (c == '_' || c == '-'))
		if !ok {
			return false
		}
	}
	return true
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (v *validator) pathArgTarget(p string, r *model.Rule, pred, arg string) {
	if arg != "" && arg != model.TargetBinding {
		return
	}
	scope := scopeOf(r.Match)
	switch {
	case !r.Event.IsToolEvent():
		v.notApplicable(p, r.ID, "%s without a literal path binds to $target, which does not exist on %s events", pred, string(r.Event))
	case scope.noTargets():
		v.notApplicable(p, r.ID, "%s binds to $target, but match.tool only matches tools without a path target (%s)", pred, strings.Join(scope.matched, ", "))
	}
}

func (v *validator) cond(p string, r *model.Rule, c *model.Cond) {
	id := r.ID
	n := 0
	if c.All != nil {
		n++
	}
	if c.Any != nil {
		n++
	}
	if c.Not != nil {
		n++
	}
	preds := []bool{
		c.ReadThisSession != nil, c.PathExists != nil, c.VerifyFresh != nil, c.VerifyFailedSinceRead != nil,
		c.ToolCountSinceInject != nil, c.PrevPromptRegex != nil, c.PriorHit != nil, c.EnvEquals != nil,
		c.BranchGlob != nil, c.IsSubagent != nil,
	}
	for _, b := range preds {
		if b {
			n++
		}
	}
	if n != 1 {
		v.d.err(CodeCondArity, p, id, "a condition must have exactly one key, found %d", n)
		return
	}
	switch {
	case c.All != nil:
		if len(c.All) == 0 {
			v.d.err(CodeSchema, p+"/all", id, "\"all\" needs at least one condition")
		}
		for j := range c.All {
			v.cond(fmt.Sprintf("%s/all/%d", p, j), r, &c.All[j])
		}
	case c.Any != nil:
		if len(c.Any) == 0 {
			v.d.err(CodeSchema, p+"/any", id, "\"any\" needs at least one condition")
		}
		for j := range c.Any {
			v.cond(fmt.Sprintf("%s/any/%d", p, j), r, &c.Any[j])
		}
	case c.Not != nil:
		v.cond(p+"/not", r, c.Not)
	case c.ReadThisSession != nil:
		v.pathArgTarget(p+"/read_this_session", r, model.PredReadThisSession, c.ReadThisSession.Path)
	case c.PathExists != nil:
		v.pathArgTarget(p+"/path_exists", r, model.PredPathExists, c.PathExists.Path)
	case c.VerifyFailedSinceRead != nil:
		v.pathArgTarget(p+"/verify_failed_since_read", r, model.PredVerifyFailedSinceRead, c.VerifyFailedSinceRead.Path)
	case c.VerifyFresh != nil:
		seen := map[string]bool{}
		for j, name := range c.VerifyFresh.Names {
			np := fmt.Sprintf("%s/verify_fresh/names/%d", p, j)
			switch {
			case !validVerifyName(name):
				v.d.err(CodeSchema, np, id, "verify name %q must match ^[a-z0-9][a-z0-9_-]{0,31}$", name)
			case seen[name]:
				v.d.err(CodeSchema, np, id, "verify name %q is listed twice", name)
			}
			seen[name] = true
			if v.project != nil && !hasVerify(v.project, name) {
				v.d.warn(CodeVerifyNameUnknown, np, id, "verify name %q is not defined in .ballast/project.json", name)
			}
		}
	case c.ToolCountSinceInject != nil:
		v.cmp(p+"/tool_count_since_inject", id, *c.ToolCountSinceInject)
	case c.PrevPromptRegex != nil:
		v.regex(p+"/prev_prompt_regex/regex", id, c.PrevPromptRegex.Regex)
	case c.PriorHit != nil:
		ph := c.PriorHit
		if !validRuleID(ph.Rule) {
			v.d.err(CodeSchema, p+"/prior_hit/rule", id, "prior_hit.rule %q is not a valid rule id", ph.Rule)
		} else if !v.known[ph.Rule] {
			v.d.warn(CodePriorHitUnknown, p+"/prior_hit/rule", id, "prior_hit refers to rule %q, which is not in the same rule set", ph.Rule)
		}
		v.cmp(p+"/prior_hit", id, ph.Cmp)
	case c.EnvEquals != nil:
		if !validEnvName(c.EnvEquals.Name) {
			v.d.err(CodeSchema, p+"/env_equals/name", id, "env_equals.name %q is not a valid variable name", c.EnvEquals.Name)
		}
	case c.BranchGlob != nil:
		if len(c.BranchGlob.Patterns) == 0 {
			v.d.err(CodeSchema, p+"/branch_glob/patterns", id, "branch_glob needs at least one pattern")
		} else {
			v.globList(p+"/branch_glob/patterns", id, c.BranchGlob.Patterns)
		}
	}
}

func hasVerify(p *model.ProjectConfig, name string) bool {
	for _, s := range p.Verify {
		if s.Name == name {
			return true
		}
	}
	return false
}

func (v *validator) examples(base string, r *model.Rule) {
	id := r.ID
	hits, nonHits := 0, 0
	for j := range r.Examples {
		ex := &r.Examples[j]
		p := fmt.Sprintf("%s/examples/%d", base, j)
		switch ex.Expect {
		case model.ExpectHit:
			hits++
		case model.ExpectMiss, model.ExpectUnknown:
			nonHits++
		default:
			v.d.err(CodeSchema, p+"/expect", id, "expect %q is not one of hit, miss, unknown", string(ex.Expect))
		}
		if ex.Client != "" && !ex.Client.Valid() {
			v.d.err(CodeSchema, p+"/client", id, "unknown client %q", string(ex.Client))
		}
		if !isJSONObject(ex.Input) {
			v.d.err(CodeExampleBadInput, p+"/input", id, "example input must be a JSON object (a hook stdin payload)")
		}
		if fx := ex.Fixture; fx != nil {
			if fx.VerifyFresh != "" {
				if _, ok := model.ParseTri(fx.VerifyFresh); !ok {
					v.d.err(CodeSchema, p+"/fixture/verify_fresh", id, "verify_fresh must be \"true\", \"false\" or \"unknown\"")
				}
			}
			switch fx.GOOS {
			case "", "linux", "darwin", "windows":
			default:
				v.d.err(CodeSchema, p+"/fixture/goos", id, "goos must be linux, darwin or windows")
			}
			if fx.ToolCountSinceInject != nil && *fx.ToolCountSinceInject < 0 {
				v.d.err(CodeSchema, p+"/fixture/tool_count_since_inject", id, "tool_count_since_inject must not be negative")
			}
			for k, n := range fx.PriorHits {
				if n < 0 {
					v.d.err(CodeSchema, p+"/fixture/prior_hits/"+k, id, "prior_hits must not be negative")
				}
			}
		}
	}
	if hits == 0 {
		v.d.err(CodeExamplesMissing, base+"/examples", id, "rule needs at least one example with expect \"hit\"")
	}
	if nonHits == 0 {
		v.d.err(CodeExamplesMissing, base+"/examples", id, "rule needs at least one example with expect \"miss\" or \"unknown\"")
	}
}

func isJSONObject(raw json.RawMessage) bool {
	b := bytes.TrimSpace(raw)
	return len(b) > 1 && b[0] == '{'
}
