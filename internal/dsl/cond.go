package dsl

import (
	"time"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// evalCond evaluates one `when` condition on a binding (dsl-semantics §7).
func (c *evalCtx) evalCond(cp *condProg, b binding) res {
	switch cp.kind {
	case condAll:
		out := resTrue
		for _, s := range cp.subs {
			out = out.and(c.evalCond(s, b))
			if out.t == model.TriFalse {
				break
			}
		}
		return out
	case condAny:
		out := resFalse
		for _, s := range cp.subs {
			out = out.or(c.evalCond(s, b))
			if out.t == model.TriTrue {
				break
			}
		}
		return out
	case condNot:
		return c.evalCond(cp.subs[0], b).not()
	case condPred:
		return c.evalPred(cp, b)
	}
	return unk(cp.bad)
}

// resolvePath turns a PathArg into a normalized absolute path. Unresolvable
// arguments (no bound target, dynamic target, no PathService) yield an
// unknown result.
func (c *evalCtx) resolvePath(pred, arg string, b binding) (string, res, bool) {
	if c.ps == nil {
		return "", unk("paths:unavailable"), false
	}
	if arg == "" || arg == model.TargetBinding {
		if !b.tgt.ok {
			return "", unk(pred + ":no_target"), false
		}
		if b.tgt.dyn || b.tgt.raw == "" {
			return "", unk(pred + ":dynamic_path"), false
		}
		return c.ps.Normalize(b.tgt.raw, c.pc.Cwd, c.pc), res{}, true
	}
	base := c.pc.ProjectRoot
	if base == "" {
		base = c.pc.Cwd
	}
	return c.ps.Normalize(arg, base, c.pc), res{}, true
}

func (c *evalCtx) evalPred(cp *condProg, b binding) res {
	in := c.in
	switch cp.pred {
	case model.PredReadThisSession:
		if !c.stateOK() {
			return unk("read_this_session:state_unreadable")
		}
		norm, r, ok := c.resolvePath(cp.pred, cp.path, b)
		if !ok {
			return r
		}
		return fromBool(in.Session.HasRead(norm))
	case model.PredPathExists:
		if in.World == nil {
			return unk("path_exists:no_world")
		}
		norm, r, ok := c.resolvePath(cp.pred, cp.path, b)
		if !ok {
			return r
		}
		return fromTri(in.World.PathExists(norm), "path_exists:io_error")
	case model.PredVerifyFresh:
		if in.Evidence == nil {
			return unk("verify_fresh:not_a_repo")
		}
		q := model.FreshQuery{Names: cp.names, Kind: model.FreshWorktree, Cwd: c.ev.Cwd}
		if b.matcher && b.node >= 0 && c.cmds != nil && b.node < len(c.cmds.Nodes) {
			n := &c.cmds.Nodes[b.node]
			if n.Name == "git" && n.Git != nil && n.Git.Sub == "commit" {
				q.Kind, q.Git = model.FreshCommit, n.Git
			}
		}
		fr := in.Evidence.VerifyFresh(q)
		cause := fr.Cause
		if cause == "" {
			cause = "unknown"
		}
		return fromTri(fr.Value, "verify_fresh:"+cause)
	case model.PredVerifyFailedSinceRead:
		if in.Evidence == nil {
			return unk("verify_failed_since_read:not_a_repo")
		}
		if !c.stateOK() {
			return unk("verify_failed_since_read:state_unreadable")
		}
		norm, r, ok := c.resolvePath(cp.pred, cp.path, b)
		if !ok {
			return r
		}
		var after time.Time
		if t, ok := in.Session.LastReadAt(norm); ok {
			after = t
		}
		if st, ok := in.Session.StartedAt(); ok && st.After(after) {
			after = st
		}
		t, _ := in.Evidence.LastFailureAfter(after)
		return fromTri(t, "verify_failed_since_read:unavailable")
	case model.PredToolCountSinceInject:
		if !c.stateOK() {
			return unk("tool_count_since_inject:state_unreadable")
		}
		return fromBool(cp.cmp.Eval(float64(in.Session.ToolCountSinceInject())))
	case model.PredPrevPromptRegex:
		if !c.stateOK() {
			return unk("prev_prompt_regex:state_unreadable")
		}
		tail := lastRunes(in.Session.PrevPromptTail(), 200)
		if tail == "" {
			return resFalse
		}
		re, err := cp.re.get()
		if err != nil {
			return unk("prev_prompt_regex:bad_regex")
		}
		return fromBool(re.MatchString(tail))
	case model.PredPriorHit:
		if !c.stateOK() {
			return unk("prior_hit:state_unreadable")
		}
		return fromBool(cp.cmp.Eval(float64(in.Session.PriorHits(cp.rule))))
	case model.PredEnvEquals:
		if in.World == nil {
			return unk("env_equals:no_world")
		}
		v, ok := in.World.Getenv(cp.env.Name)
		return fromBool(ok && v == cp.env.Value)
	case model.PredBranchGlob:
		if in.World == nil {
			return unk("branch_glob:no_world")
		}
		name, t := in.World.Branch()
		switch t {
		case model.TriTrue:
			return fromBool(cp.globs.match(name))
		case model.TriFalse:
			return resFalse
		}
		return unk("branch_glob:branch_unknown")
	case model.PredIsSubagent:
		return fromBool(c.ev.IsSubagent())
	}
	return unk("cond:unknown_predicate")
}

// lastRunes returns the last n runes of s.
func lastRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)
	return string(rs[len(rs)-n:])
}
