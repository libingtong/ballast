package dsl

import (
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

func TestPredicates(t *testing.T) {
	edit := ruleJSON
	when := func(w string) string { return edit("PreToolUse", `{"tool":"Edit"}`, w) }
	file := editScn("/repo/src/a.go")
	runRows(t, newEngine(t), []row{
		{"read_this_session read", when(`[{"read_this_session":{}}]`), file.with(model.Fixture{Read: []string{"src/a.go"}}), T, ""},
		{"read_this_session unread", when(`[{"read_this_session":{}}]`), file, F, ""},
		{"read_this_session explicit $target", when(`[{"read_this_session":{"path":"$target"}}]`), file.with(model.Fixture{Read: []string{"/repo/src/a.go"}}), T, ""},
		{"read_this_session literal path", when(`[{"read_this_session":{"path":"README.md"}}]`), file.with(model.Fixture{Read: []string{"README.md"}}), T, ""},
		{"read_this_session literal home path", when(`[{"read_this_session":{"path":"~/notes.txt"}}]`), file.with(model.Fixture{Read: []string{"/home/user/notes.txt"}}), T, ""},

		{"path_exists true", when(`[{"path_exists":{}}]`), file.with(model.Fixture{Exists: []string{"src/a.go"}}), T, ""},
		{"path_exists false", when(`[{"path_exists":{}}]`), file, F, ""},
		{"not path_exists", when(`[{"not":{"path_exists":{}}}]`), file, T, ""},

		{"empty target is no target", when(`[{"read_this_session":{}}]`), scenario{payload: toolPayload("Edit", `{"file_path":""}`)}, U, "read_this_session:no_target"},
		{"verify_fresh true", when(`[{"verify_fresh":{}}]`), file.with(model.Fixture{VerifyFresh: "true"}), T, ""},
		{"verify_fresh false", when(`[{"verify_fresh":{}}]`), file.with(model.Fixture{VerifyFresh: "false"}), F, ""},
		{"verify_fresh unknown", when(`[{"verify_fresh":{}}]`), file, U, "verify_fresh:fixture_unknown"},
		{"verify_fresh outside a repo", when(`[{"verify_fresh":{}}]`), file.with(model.Fixture{NotARepo: true}), U, "verify_fresh:not_a_repo"},
		{"not verify_fresh unknown stays unknown", when(`[{"not":{"verify_fresh":{}}}]`), file, U, "verify_fresh:"},

		{"verify_failed_since_read: failure after the read", when(`[{"verify_failed_since_read":{}}]`),
			file.with(model.Fixture{Read: []string{"src/a.go"}, FailedSinceRead: []string{"src/a.go"}}), T, ""},
		{"verify_failed_since_read: read after the failure", when(`[{"verify_failed_since_read":{}}]`),
			file.with(model.Fixture{Read: []string{"src/a.go"}, FailedSinceRead: []string{"other.go"}}), F, ""},
		{"verify_failed_since_read: no failures", when(`[{"verify_failed_since_read":{}}]`), file.with(model.Fixture{Read: []string{"src/a.go"}}), F, ""},
		{"verify_failed_since_read: outside a repo", when(`[{"verify_failed_since_read":{}}]`), file.with(model.Fixture{NotARepo: true}), U, "verify_failed_since_read:not_a_repo"},

		{"tool_count_since_inject gte", when(`[{"tool_count_since_inject":{"gte":80}}]`), file.with(model.Fixture{ToolCountSinceInject: intp(81)}), T, ""},
		{"tool_count_since_inject below", when(`[{"tool_count_since_inject":{"gte":80}}]`), file.with(model.Fixture{ToolCountSinceInject: intp(3)}), F, ""},
		{"prev_prompt_regex", when(`[{"prev_prompt_regex":{"regex":"\\p{Han}"}}]`), file.with(model.Fixture{PrevPrompt: "修一下"}), T, ""},
		{"prev_prompt_regex no record", when(`[{"prev_prompt_regex":{"regex":"x"}}]`), file, F, ""},
		{"prev_prompt_regex looks at the last 200 runes", when(`[{"prev_prompt_regex":{"regex":"^head"}}]`),
			file.with(model.Fixture{PrevPrompt: "head" + repeat("x", 250)}), F, ""},
		{"prior_hit eq", when(`[{"prior_hit":{"rule":"other-rule","eq":2}}]`), file.with(model.Fixture{PriorHits: map[string]int{"other-rule": 2}}), T, ""},
		{"prior_hit missing rule counts zero", when(`[{"prior_hit":{"rule":"other-rule","gte":1}}]`), file, F, ""},

		{"env_equals", when(`[{"env_equals":{"name":"CI","value":"1"}}]`), file.with(model.Fixture{Env: map[string]string{"CI": "1"}}), T, ""},
		{"env_equals differs", when(`[{"env_equals":{"name":"CI","value":"1"}}]`), file.with(model.Fixture{Env: map[string]string{"CI": "0"}}), F, ""},
		{"env_equals unset", when(`[{"env_equals":{"name":"CI","value":"1"}}]`), file, F, ""},

		{"branch_glob match", when(`[{"branch_glob":{"patterns":["main","release/*"]}}]`), file.with(model.Fixture{Branch: strp("release/1.0")}), T, ""},
		{"branch_glob multi level", when(`[{"branch_glob":{"patterns":"feature/**"}}]`), file.with(model.Fixture{Branch: strp("feature/a/b")}), T, ""},
		{"branch_glob star does not cross slash", when(`[{"branch_glob":{"patterns":"feature/*"}}]`), file.with(model.Fixture{Branch: strp("feature/a/b")}), F, ""},
		{"branch_glob case sensitive", when(`[{"branch_glob":{"patterns":"Main"}}]`), file.with(model.Fixture{Branch: strp("main")}), F, ""},
		{"branch_glob negation", when(`[{"branch_glob":{"patterns":["*","!main"]}}]`), file.with(model.Fixture{Branch: strp("main")}), F, ""},
		{"branch_glob detached", when(`[{"branch_glob":{"patterns":"*"}}]`), file.with(model.Fixture{Branch: strp("")}), F, ""},
		{"branch_glob unknown branch", when(`[{"branch_glob":{"patterns":"*"}}]`), file, U, "branch_glob:"},

		{"is_subagent", edit("PreToolUse", `{"tool":"Read"}`, `[{"is_subagent":{}}]`),
			scenario{payload: `{"agent_id":"a1","agent_type":"x","tool_name":"Read","tool_input":{"file_path":"/repo/a"}}`}, T, ""},
		{"is_subagent main", edit("PreToolUse", `{"tool":"Edit"}`, `[{"is_subagent":{}}]`), file, F, ""},

		{"when entries are ANDed", when(`[{"is_subagent":{}},{"path_exists":{}}]`), file, F, ""},
		{"false beats unknown in AND", when(`[{"verify_fresh":{}},{"is_subagent":{}}]`), file, F, ""},
		{"unknown and true is unknown", when(`[{"verify_fresh":{}},{"not":{"is_subagent":{}}}]`), file, U, "verify_fresh:"},
		{"all", when(`[{"all":[{"not":{"is_subagent":{}}},{"not":{"path_exists":{}}}]}]`), file, T, ""},
		{"any", when(`[{"any":[{"is_subagent":{}},{"not":{"path_exists":{}}}]}]`), file, T, ""},
		{"any with unknown and true", when(`[{"any":[{"verify_fresh":{}},{"not":{"is_subagent":{}}}]}]`), file, T, ""},
		{"any with unknown and false", when(`[{"any":[{"verify_fresh":{}},{"is_subagent":{}}]}]`), file, U, "verify_fresh:"},
		{"empty all is true", when(`[{"all":[]}]`), file, T, ""},
		{"empty any is false", when(`[{"any":[]}]`), file, F, ""},
		{"condition without keys is unknown", when(`[{}]`), file, U, "cond:arity"},
		{"condition with two keys is unknown", when(`[{"is_subagent":{},"path_exists":{}}]`), file, U, "cond:arity"},
	})
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestPredicatesNoTargetAndState(t *testing.T) {
	e := newEngine(t)
	prompt := scenario{native: "UserPromptSubmit", payload: `{"prompt":"hi"}`}
	runRows(t, e, []row{
		{"$target on an event without targets is unknown", ruleJSON("UserPromptSubmit", `{"agent_type":"main"}`, `[{"path_exists":{}}]`), prompt, U, "path_exists:no_target"},
		{"literal path works on any event", ruleJSON("UserPromptSubmit", `{"agent_type":"main"}`, `[{"path_exists":{"path":"go.mod"}}]`), prompt.with(model.Fixture{Exists: []string{"go.mod"}}), T, ""},
		{"tool without targets leaves $target unbound", ruleJSON("PreToolUse", `{"tool":"WebFetch"}`, `[{"read_this_session":{}}]`),
			scenario{payload: toolPayload("WebFetch", `{"url":"http://x"}`)}, U, "read_this_session:no_target"},
	})

	unreadable := func(in *model.EvalInput) { in.SessionReadable = false }
	for _, c := range []struct{ name, w, cause string }{
		{"read_this_session", `{"read_this_session":{"path":"a"}}`, "read_this_session:state_unreadable"},
		{"tool_count_since_inject", `{"tool_count_since_inject":{"gte":1}}`, "tool_count_since_inject:state_unreadable"},
		{"prev_prompt_regex", `{"prev_prompt_regex":{"regex":"x"}}`, "prev_prompt_regex:state_unreadable"},
		{"prior_hit", `{"prior_hit":{"rule":"abc","gte":1}}`, "prior_hit:state_unreadable"},
		{"verify_failed_since_read", `{"verify_failed_since_read":{"path":"a"}}`, "verify_failed_since_read:state_unreadable"},
	} {
		t.Run("unreadable state: "+c.name, func(t *testing.T) {
			rr, _ := evalWith(t, e, ruleJSON("UserPromptSubmit", `{"agent_type":"main"}`, `[`+c.w+`]`), prompt, unreadable)
			if rr.Outcome != U || rr.UnknownCause != c.cause {
				t.Errorf("got %v %q, want unknown %q", rr.Outcome, rr.UnknownCause, c.cause)
			}
		})
	}

	t.Run("nil session and world do not panic", func(t *testing.T) {
		for _, w := range []string{
			`{"read_this_session":{"path":"a"}}`, `{"path_exists":{"path":"a"}}`, `{"env_equals":{"name":"A","value":"b"}}`,
			`{"branch_glob":{"patterns":"x"}}`, `{"verify_fresh":{}}`, `{"prior_hit":{"rule":"abc","gte":1}}`,
		} {
			rr, _ := evalWith(t, e, ruleJSON("UserPromptSubmit", `{"agent_type":"main"}`, `[`+w+`]`), prompt, func(in *model.EvalInput) {
				in.Session, in.World, in.Evidence = nil, nil, nil
			})
			if rr.Outcome != U {
				t.Errorf("%s: outcome %v, want unknown", w, rr.Outcome)
			}
		}
	})
}

type spyEvidence struct{ got []model.FreshQuery }

func (s *spyEvidence) VerifyFresh(q model.FreshQuery) model.FreshResult {
	s.got = append(s.got, q)
	return model.FreshResult{Value: model.TriFalse, Cause: "stale"}
}
func (s *spyEvidence) LastFailureAfter(time.Time) (model.Tri, time.Time) {
	return model.TriFalse, time.Time{}
}
func (s *spyEvidence) FinishedSince(time.Time) []model.VerifyRecord { return nil }

func TestVerifyFreshQueryKind(t *testing.T) {
	e := newEngine(t)
	commit := withGit(nd("git", "commit", "-m", "x"), "commit")
	rule := ruleJSON("PreToolUse", `{"bash":{"cmd":"git","git_sub":"commit"}}`, `[{"verify_fresh":{"names":["unit","lint"]}}]`)
	spy := &spyEvidence{}
	rr, _ := evalWith(t, e, rule, bashScn("git add . && git commit -m x", bashList(withGit(nd("git", "add", "."), "add"), commit)),
		func(in *model.EvalInput) { in.Evidence = spy })
	if rr.Outcome != F {
		t.Fatalf("outcome %v", rr.Outcome)
	}
	if len(spy.got) != 1 {
		t.Fatalf("queries: %+v", spy.got)
	}
	q := spy.got[0]
	if q.Kind != model.FreshCommit || q.Git == nil || q.Git.Sub != "commit" || q.Cwd != "/repo" || len(q.Names) != 2 {
		t.Errorf("commit query = %+v", q)
	}

	spy = &spyEvidence{}
	stop := ruleJSON("Stop", `{"text":{"regex":"done"}}`, `[{"verify_fresh":{}}]`)
	evalWith(t, e, stop, scenario{native: "Stop", payload: `{"last_assistant_message":"done"}`}, func(in *model.EvalInput) { in.Evidence = spy })
	if len(spy.got) != 1 || spy.got[0].Kind != model.FreshWorktree || spy.got[0].Git != nil || spy.got[0].Names != nil {
		t.Errorf("worktree query = %+v", spy.got)
	}

	// A bash rule whose binding node is not a commit asks about the worktree.
	spy = &spyEvidence{}
	other := ruleJSON("PreToolUse", `{"bash":{"cmd":"make"}}`, `[{"verify_fresh":{}}]`)
	evalWith(t, e, other, bashScn("make", bashList(nd("make"))), func(in *model.EvalInput) { in.Evidence = spy })
	if len(spy.got) != 1 || spy.got[0].Kind != model.FreshWorktree {
		t.Errorf("non-commit node query = %+v", spy.got)
	}
}

type fallbackSession struct {
	*fixSession
	text string
}

func (s fallbackSession) LastAssistantText() string { return s.text }

func TestCursorStopTextFallback(t *testing.T) {
	e := newEngine(t)
	rule := ruleJSON("Stop", `{"text":{"regex":"done"}}`, "")
	s := scenario{native: "Stop", payload: `{}`}
	rr, _ := evalWith(t, e, rule, s, func(in *model.EvalInput) { in.Session = fallbackSession{&fixSession{}, "all done"} })
	if rr.Outcome != T {
		t.Errorf("fallback text: outcome %v (%s)", rr.Outcome, rr.UnknownCause)
	}
	rr, _ = evalWith(t, e, rule, s, func(in *model.EvalInput) { in.Session = fallbackSession{&fixSession{}, ""} })
	if rr.Outcome != U {
		t.Errorf("no text at all: outcome %v", rr.Outcome)
	}
	// A present-but-empty message is just empty text, not a missing source.
	rr, _ = evalWith(t, e, rule, scenario{native: "Stop", payload: `{"last_assistant_message":""}`}, nil)
	if rr.Outcome != F {
		t.Errorf("empty message: outcome %v", rr.Outcome)
	}
}
