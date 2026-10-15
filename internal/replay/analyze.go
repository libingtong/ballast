package replay

import (
	"github.com/libingtong/ballast/internal/model"
)

// ruleMeta is the static dependency profile of one rule: which kinds of
// facts its verdict rests on. It decides the replay class of an evaluation.
type ruleMeta struct {
	id             string
	event          model.EventName
	usesEnv        bool // env_equals
	usesBranch     bool // branch_glob
	usesPathExists bool // path_exists
	usesVerify     bool // verify_fresh / verify_failed_since_read
	usesState      bool // read_this_session, tool_count_since_inject, prev_prompt_regex, prior_hit
}

func analyzeRule(cr *model.CompiledRule) *ruleMeta {
	m := &ruleMeta{id: cr.Rule.ID, event: cr.Rule.Event}
	walkConds(cr.Rule.When, m)
	return m
}

func walkConds(conds []model.Cond, m *ruleMeta) {
	for i := range conds {
		c := &conds[i]
		walkConds(c.All, m)
		walkConds(c.Any, m)
		if c.Not != nil {
			walkConds([]model.Cond{*c.Not}, m)
		}
		if c.EnvEquals != nil {
			m.usesEnv = true
		}
		if c.BranchGlob != nil {
			m.usesBranch = true
		}
		if c.PathExists != nil {
			m.usesPathExists = true
		}
		if c.VerifyFresh != nil || c.VerifyFailedSinceRead != nil {
			m.usesVerify = true
		}
		if c.ReadThisSession != nil || c.ToolCountSinceInject != nil || c.PrevPromptRegex != nil || c.PriorHit != nil {
			m.usesState = true
		}
	}
}

// Unknown causes added by replay itself.
const (
	causeNoEvidence = "no_trusted_evidence"
	causeEvalError  = "evaluate_error"
)

// classify assigns the replay class (doc.go "Classification").
func classify(m *ruleMeta, res model.RuleResult, hasEvidence bool, client model.ClientID) (Class, string) {
	if m.usesVerify && !hasEvidence {
		return Unknown, causeNoEvidence
	}
	if res.Outcome == model.TriUnknown {
		cause := res.UnknownCause
		if cause == "" {
			cause = "unknown"
		}
		return Unknown, cause
	}
	approx := m.event.IsStopFamily() || // evaluated on the final assistant text, not the hook payload
		m.usesEnv || m.usesBranch || m.usesPathExists ||
		m.usesVerify || // freshness is inferred from records + transcript writes
		(client == model.ClientCursor && m.usesState) // Cursor transcripts do not record tool results
	if approx {
		return Approximate, ""
	}
	if res.Outcome == model.TriTrue {
		return KnownHit, ""
	}
	return KnownMiss, ""
}
