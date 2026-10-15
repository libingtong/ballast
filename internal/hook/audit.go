package hook

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// diag is a non-exception condition recorded as an audit "diag" line.
type diag struct {
	name   string
	fields map[string]any
}

func (d diag) detail() json.RawMessage {
	m := make(map[string]any, len(d.fields)+1)
	for k, v := range d.fields {
		m[k] = v
	}
	m["diag"] = d.name
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{"diag":"` + d.name + `"}`)
	}
	return b
}

// auditBuilder turns one invocation's outcome into audit records.
type auditBuilder struct {
	deps     *Deps
	ev       *model.Event
	rs       *model.CompiledRuleSet
	repo     *model.RepoInfo
	key      model.ScopeKey
	caps     model.CapabilitySet
	st       model.SessionState
	readable bool
	cinfo    model.StateCommitInfo
	inj      *model.Injection
	start    time.Time
}

// records returns, in order: one hit record per hit, config notices that were
// surfaced by this injection, diags, and the eval_summary (only after an
// applied, non-duplicate, non-timed-out commit).
func (b *auditBuilder) records(v *model.Verdict, diags []diag, delta *model.StateDelta) []model.AuditRecord {
	var recs []model.AuditRecord

	var context []model.EventSummary
	if b.readable && len(v.Hits) > 0 {
		context = b.st.Recent(2)
		if len(context) > 2 {
			context = context[len(context)-2:]
		}
	}
	latency := b.deps.Clock.Now().Sub(b.start).Microseconds()
	for _, h := range v.Hits {
		r := b.base(model.AuditHit)
		r.RuleID = h.RuleID
		r.RuleLayer = h.Layer
		r.Action = h.Action
		r.Target = h.Target
		r.Decision = h.Label()
		r.Matched = clipRunes(h.Matched, model.MaxMatchedRunes)
		r.Context = append([]model.EventSummary(nil), context...)
		r.LatencyUS = latency
		if b.cinfo.Duplicate {
			r.Detail = json.RawMessage(`{"duplicate":true}`)
		}
		recs = append(recs, r)
	}

	if b.inj != nil && containsKind(b.inj.Kinds, model.InjectNotices) {
		for _, n := range b.rs.Notices {
			r := b.base(noticeType(n.Kind))
			r.RuleID = n.RuleID
			r.RuleLayer = n.Layer
			r.Detail, _ = json.Marshal(map[string]string{"message": n.Message, "path": n.Path})
			recs = append(recs, r)
		}
	}

	for _, rr := range v.Results {
		if rr.Outcome == model.TriUnknown && rr.UnknownCause != "" {
			diags = append(diags, diag{name: "predicate_unknown", fields: map[string]any{"rule_id": rr.RuleID, "cause": rr.UnknownCause}})
		}
	}
	for _, d := range diags {
		r := b.base(model.AuditDiag)
		r.Detail = d.detail()
		recs = append(recs, r)
	}

	if delta.FlushEvals && len(delta.FlushedEvals) > 0 &&
		b.cinfo.Applied && !b.cinfo.Duplicate && !b.cinfo.LockTimeout {
		r := b.base(model.AuditEvalSummary)
		r.Evals = delta.FlushedEvals
		recs = append(recs, r)
	}
	return recs
}

func (b *auditBuilder) base(t model.AuditType) model.AuditRecord {
	ev := b.ev
	r := model.AuditRecord{
		TS:             ev.ReceivedAt.UTC(),
		V:              model.AuditVersion,
		Type:           t,
		EventSeq:       b.cinfo.EventSeq,
		Client:         ev.Client,
		SessionID:      ev.SessionID,
		AgentID:        ev.AgentID,
		AgentType:      ev.AgentType,
		TranscriptRef:  b.transcriptRef(),
		RepoInst:       b.key.RepoInst,
		Event:          ev.Name,
		NativeEvent:    ev.Native,
		Generation:     b.rs.Generation,
		Tier:           model.TierT0,
		BallastVersion: b.deps.version(),
	}
	if b.repo != nil {
		r.RepoRemoteHash = b.repo.RemoteHash
	}
	if ev.Tool != nil {
		r.ToolUseID = ev.Tool.UseID
		r.Tool = ev.Tool.Name
	}
	return r
}

// transcriptRef returns the transcript path "~/"-relative when it lies under
// the home directory.
func (b *auditBuilder) transcriptRef() string {
	p := b.ev.TranscriptPath
	if p == "" || strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := b.deps.Env.HomeDir()
	if err != nil || home == "" {
		return p
	}
	home = strings.TrimRight(strings.ReplaceAll(home, `\`, "/"), "/")
	q := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(q, home+"/") {
		return "~/" + q[len(home)+1:]
	}
	return p
}

func noticeType(k model.NoticeKind) model.AuditType {
	switch k {
	case model.NoticeRuleDropped:
		return model.AuditRuleDropped
	case model.NoticeRuleShadowed:
		return model.AuditRuleShadowed
	}
	return model.AuditConfigInvalid
}

func containsKind(ks []model.InjectKind, k model.InjectKind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}
