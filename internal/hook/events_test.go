package hook

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func mustExit0(t *testing.T, h *harness, code int) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, h.stderr.String())
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr on success: %q", h.stderr.String())
	}
}

func TestPipelineOrder(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	want := []string{"parse", "locate", "load", "state.load", "evaluate", "inject", "commit", "audit", "render"}
	if got := h.log.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("call order = %v\nwant %v", got, want)
	}
	if !strings.Contains(h.stdout.String(), denyJSON) || !strings.HasSuffix(h.stdout.String(), "\n") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

func TestSubagentStopDropsAfterAudit(t *testing.T) {
	h := newHarness(t)
	h.state.session = &fakeSession{evals: map[string]model.EvalCount{"r": {Evals: 2}}}
	in := `{"session_id":"s1","cwd":"/repo","agent_id":"a1","agent_type":"Explore","last_assistant_message":"done"}`
	mustExit0(t, h, h.claude("SubagentStop", in))
	log := h.log.list()
	if got := log[len(log)-1]; got != "drop_agent" {
		t.Fatalf("last call = %q, want drop_agent (log %v)", got, log)
	}
	if h.log.index("drop_agent") < h.log.index("audit") || h.log.index("audit") < h.log.index("commit") {
		t.Errorf("cleanup must follow commit and audit: %v", log)
	}
	if h.engine.calls() != 1 {
		t.Error("SubagentStop must evaluate rules")
	}
	if got := h.engine.lastInput().Event.LastAssistantMessage; got != "done" {
		t.Errorf("assistant message = %q", got)
	}
	d := h.state.lastCommit(t).delta
	if !d.FlushEvals || !reflect.DeepEqual(d.FlushedEvals, map[string]model.EvalCount{"r": {Evals: 2}}) {
		t.Errorf("delta: %+v", d)
	}
	if types := h.audit.types(); !reflect.DeepEqual(types, []model.AuditType{model.AuditEvalSummary}) {
		t.Errorf("audit types: %v", types)
	}
	if k := h.state.droppedAgent[0]; k.Agent != "a1" || k.Session != "s1" || k.RepoInst != "0123456789abcdef" {
		t.Errorf("dropped key: %+v", k)
	}
}

func TestSubagentStopWithoutAgentIDNeverDropsMain(t *testing.T) {
	h := newHarness(t)
	mustExit0(t, h, h.main(model.ClientCursor, "subagentStop", `{"conversation_id":"c1","cwd":"/repo"}`))
	if len(h.state.droppedAgent) != 0 {
		t.Errorf("dropped %+v", h.state.droppedAgent)
	}
}

func TestSubagentStopBlockingStillDrops(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = &model.Verdict{
		Action: model.ActionDeny, Reason: "[r] keep going",
		Hits: []model.RuleHit{{RuleID: "r", Action: model.ActionDeny, Effective: model.ActionDeny, Message: "keep going"}},
	}
	in := `{"session_id":"s1","cwd":"/repo","agent_id":"a1","agent_type":"Explore"}`
	mustExit0(t, h, h.claude("SubagentStop", in))
	if !strings.Contains(h.stdout.String(), `"decision":"block"`) {
		t.Errorf("stdout = %q", h.stdout.String())
	}
	if len(h.state.droppedAgent) != 1 {
		t.Error("agent state must be dropped after the decision is committed")
	}
}

func TestCompactAgentIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	in := `{"session_id":"s1","cwd":"/repo","agent_id":"af7a","agent_type":"","last_assistant_message":"summary"}`
	mustExit0(t, h, h.claude("SubagentStop", in))
	if h.stdout.Len() != 0 || h.engine.calls() != 0 || h.injector.lastReq() != nil {
		t.Errorf("compact agent was processed: stdout=%q evals=%d", h.stdout.String(), h.engine.calls())
	}
	if h.state.commitCount() != 0 || h.audit.appendCalls() != 0 {
		t.Error("compact agent must not touch state or audit")
	}
	if len(h.state.droppedAgent) != 1 || h.state.droppedAgent[0].Agent != "af7a" {
		t.Errorf("dropped: %+v", h.state.droppedAgent)
	}
	// A missing agent_type is a normal sub-agent (HasAgentType false).
	h2 := newHarness(t)
	mustExit0(t, h2, h2.claude("SubagentStop", `{"session_id":"s1","cwd":"/repo","agent_id":"a2"}`))
	if h2.engine.calls() != 1 {
		t.Error("SubagentStop without agent_type must be evaluated")
	}
}

func TestSessionStart(t *testing.T) {
	h := newHarness(t)
	h.clock.Set(time.Now())
	h.injector.inj = &model.Injection{
		Text: "Ballast rules", Kinds: []model.InjectKind{model.InjectFull}, FullEmitted: true,
		RuleDigest: map[string]model.Action{"r": model.ActionDeny},
	}
	mustExit0(t, h, h.claude("SessionStart", `{"session_id":"s1","cwd":"/repo","source":"startup"}`))
	if h.engine.calls() != 0 {
		t.Error("SessionStart must not evaluate rules")
	}
	if !strings.Contains(h.stdout.String(), `"additionalContext":"Ballast rules"`) {
		t.Errorf("stdout = %q", h.stdout.String())
	}
	d := h.state.lastCommit(t).delta
	if !d.FullEmitted || d.Generation != "gen1" || d.RuleDigest["r"] != model.ActionDeny || d.ToolCall {
		t.Errorf("delta: %+v", d)
	}
	req := h.injector.lastReq()
	if req.Verdict != nil || req.Event.Name != model.EventSessionStart || req.Rules.Generation != "gen1" {
		t.Errorf("inject request: %+v", req)
	}
	if got := h.spawner.spawned(); len(got) != 1 || !reflect.DeepEqual(got[0], []string{"/opt/ballast/bin/ballast", "gc"}) {
		t.Errorf("spawned %v", got)
	}
}

func TestUserPromptSubmitAndPostToolBatch(t *testing.T) {
	h := newHarness(t)
	h.injector.inj = &model.Injection{Text: "anchor", Kinds: []model.InjectKind{model.InjectAnchor}, AnchorEmitted: true}
	mustExit0(t, h, h.claude("UserPromptSubmit", `{"session_id":"s1","cwd":"/repo","prompt":"do the thing"}`))
	if h.engine.calls() != 1 || h.engine.lastInput().Event.Prompt != "do the thing" {
		t.Error("UserPromptSubmit must evaluate")
	}
	d := h.state.lastCommit(t).delta
	if !d.Prompt || d.PromptTail != "do the thing" || !d.AnchorEmitted || d.ToolCall {
		t.Errorf("delta: %+v", d)
	}
	if !strings.Contains(h.stdout.String(), `"hookEventName":"UserPromptSubmit"`) || !strings.Contains(h.stdout.String(), "anchor") {
		t.Errorf("stdout = %q", h.stdout.String())
	}

	h = newHarness(t)
	h.injector.inj = &model.Injection{Text: "re-anchor", AnchorEmitted: true}
	mustExit0(t, h, h.claude("PostToolBatch", `{"session_id":"s1","cwd":"/repo"}`))
	if h.engine.calls() != 0 {
		t.Error("PostToolBatch is not a rule event")
	}
	if !h.state.lastCommit(t).delta.AnchorEmitted {
		t.Error("anchor emission must be recorded")
	}
	if h.evidence.opens != 1 {
		t.Errorf("mid-turn events open the evidence view for job results (opens=%d)", h.evidence.opens)
	}
}

func TestToolEvents(t *testing.T) {
	for _, tc := range []struct {
		native   string
		toolCall bool
		failure  bool
	}{
		{"PreToolUse", true, false},
		{"PostToolUse", false, false},
		{"PostToolUseFailure", false, true},
	} {
		t.Run(tc.native, func(t *testing.T) {
			h := newHarness(t)
			mustExit0(t, h, h.claude(tc.native, preToolBash))
			d := h.state.lastCommit(t).delta
			if d.ToolCall != tc.toolCall || d.ToolFailure != tc.failure {
				t.Errorf("delta: %+v", d)
			}
			if d.DedupeKey != tc.native+":tu1" {
				t.Errorf("dedupe key = %q", d.DedupeKey)
			}
			in := h.engine.lastInput()
			if in == nil || in.Event.Tool.Command != "git status" || in.Client != model.ClientClaude || in.Commands == nil {
				t.Fatalf("eval input: %+v", in)
			}
			if !in.SessionReadable || in.Session == nil || in.World == nil || in.Evidence == nil {
				t.Errorf("eval input incomplete: %+v", in)
			}
			if !in.Now.Equal(testNow) || in.Event.ReceivedAt != testNow {
				t.Errorf("event time not stamped from the injected clock: %v / %v", in.Now, in.Event.ReceivedAt)
			}
			if in.Paths.Cwd != "/repo" || in.Paths.ProjectRoot != "/repo" || in.Paths.GOOS != h.deps.Env.GOOS() || in.Paths.Home != h.sb.Home {
				t.Errorf("path context: %+v", in.Paths)
			}
		})
	}
}

func TestSessionEnd(t *testing.T) {
	h := newHarness(t)
	h.state.session = &fakeSession{evals: map[string]model.EvalCount{"r": {Evals: 1, Hits: 1}}}
	mustExit0(t, h, h.claude("SessionEnd", `{"session_id":"s1","cwd":"/repo","reason":"clear"}`))
	if h.stdout.Len() != 0 || h.engine.calls() != 0 || h.injector.lastReq() != nil {
		t.Error("SessionEnd must stay silent and skip rules and injection")
	}
	if types := h.audit.types(); !reflect.DeepEqual(types, []model.AuditType{model.AuditEvalSummary}) {
		t.Errorf("audit types: %v", types)
	}
	if h.audit.all()[0].Evals["r"].Hits != 1 {
		t.Errorf("eval_summary: %+v", h.audit.all()[0])
	}
	if len(h.state.droppedSess) != 1 || h.state.droppedSess[0].Session != "s1" {
		t.Errorf("dropped sessions: %+v", h.state.droppedSess)
	}
	if h.log.index("drop_session") < h.log.index("audit") {
		t.Errorf("drop must follow the audit write: %v", h.log.list())
	}
	if h.state.lastCommit(t).delta.Summary != nil {
		t.Error("SessionEnd must not add a ring entry")
	}
}

func TestCursorAgentResponseStoresText(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	mustExit0(t, h, h.main(model.ClientCursor, "afterAgentResponse", `{"conversation_id":"c1","cwd":"/repo","text":"all done"}`))
	if h.stdout.Len() != 0 || h.engine.calls() != 0 || h.injector.lastReq() != nil {
		t.Error("AgentResponse is bookkeeping only")
	}
	d := h.state.lastCommit(t).delta
	if d.LastAssistantText != "all done" || d.Summary != nil || d.ToolCall {
		t.Errorf("delta: %+v", d)
	}
	if k := h.state.lastCommit(t).key; k.Session != "c1" {
		t.Errorf("scope: %+v", k)
	}
}

func TestCursorStopFallsBackToStoredText(t *testing.T) {
	h := newHarness(t)
	h.state.session = &fakeSession{lastText: "stored answer"}
	mustExit0(t, h, h.main(model.ClientCursor, "stop", `{"conversation_id":"c1","cwd":"/repo"}`))
	ev := h.engine.lastInput().Event
	if ev.LastAssistantMessage != "stored answer" || !ev.HasLastAssistantMessage {
		t.Errorf("event: %+v", ev)
	}

	// A payload that carries the text keeps it.
	h = newHarness(t)
	h.state.session = &fakeSession{lastText: "stored answer"}
	mustExit0(t, h, h.main(model.ClientCursor, "stop", `{"conversation_id":"c1","cwd":"/repo","last_assistant_message":"fresh"}`))
	if ev := h.engine.lastInput().Event; ev.LastAssistantMessage != "fresh" {
		t.Errorf("event: %+v", ev)
	}

	// Nothing stored ⇒ text rules stay unknown (flag unset).
	h = newHarness(t)
	mustExit0(t, h, h.main(model.ClientCursor, "stop", `{"conversation_id":"c1","cwd":"/repo"}`))
	if ev := h.engine.lastInput().Event; ev.HasLastAssistantMessage {
		t.Errorf("event: %+v", ev)
	}

	// An unreadable state must not feed stale text either.
	h = newHarness(t)
	h.state.session = &fakeSession{lastText: "stored answer"}
	h.state.loadErr = errTest
	h.state.loadInfo = model.StateLoadInfo{Exists: true}
	mustExit0(t, h, h.main(model.ClientCursor, "stop", `{"conversation_id":"c1","cwd":"/repo"}`))
	if ev := h.engine.lastInput().Event; ev.HasLastAssistantMessage {
		t.Errorf("event: %+v", ev)
	}
}

func TestOtherClients(t *testing.T) {
	cases := []struct {
		client model.ClientID
		native string
		in     string
		name   model.EventName
	}{
		{model.ClientCodex, "PermissionRequest", `{"session_id":"s","cwd":"/repo","tool_name":"Bash","tool_input":{"command":"ls"}}`, model.EventPreToolUse},
		{model.ClientCursor, "beforeShellExecution", `{"conversation_id":"c","cwd":"/repo","command":"ls"}`, model.EventPreToolUse},
		{model.ClientCursor, "beforeSubmitPrompt", `{"conversation_id":"c","cwd":"/repo","prompt":"hi"}`, model.EventUserPromptSubmit},
		{model.ClientHermes, "pre_llm_call", `{"session_id":"s","cwd":"/repo"}`, model.EventUserPromptSubmit},
		{model.ClientHermes, "on_session_end", `{"session_id":"s","cwd":"/repo"}`, model.EventSessionEnd},
	}
	for _, c := range cases {
		t.Run(string(c.client)+"/"+c.native, func(t *testing.T) {
			h := newHarness(t)
			mustExit0(t, h, h.main(c.client, c.native, c.in))
			rec := h.state.lastCommit(t)
			if rec.delta.Summary != nil && rec.delta.Summary.Event != c.name {
				t.Errorf("event %v, want %v", rec.delta.Summary.Event, c.name)
			}
			if c.name.IsRuleEvent() {
				if in := h.engine.lastInput(); in == nil || in.Client != c.client || in.Event.Native != c.native {
					t.Errorf("eval input: %+v", in)
				}
			}
		})
	}
}

func TestRunReturnsResult(t *testing.T) {
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	args := Args{Client: model.ClientClaude, Native: "PreToolUse"}
	res, err := Run(args, []byte(preToolBash), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Stdout), denyJSON) || res.Decision.Action != model.ActionDeny || res.Decision.Reason != "[r] no" {
		t.Errorf("result: %+v / %s", res.Decision, res.Stdout)
	}
	if res.Event == nil || res.Event.Tool.Command != "git status" || len(res.Audit) != 1 || res.Audit[0].Type != model.AuditHit {
		t.Errorf("result: %+v", res)
	}
	if h.stdout.Len() != 0 {
		t.Error("Run must not write stdout")
	}
	if _, err := Run(args, []byte("nope"), h.deps); err == nil {
		t.Error("Run must return the parse error")
	}
	if _, err := Run(args, nil, nil); err == nil {
		t.Error("nil deps")
	}
}

func TestInjectionDroppedWhenDecisionBlocks(t *testing.T) {
	for _, act := range []model.Action{model.ActionDeny, model.ActionAsk} {
		t.Run(string(act), func(t *testing.T) {
			h := newHarness(t)
			h.engine.verdict = &model.Verdict{
				Action: act, Reason: "[r] stop",
				Hits: []model.RuleHit{{RuleID: "r", Action: act, Effective: act, Message: "stop"}},
			}
			h.injector.inj = &model.Injection{
				Text: "anchor", AnchorEmitted: true, FullEmitted: true, ReportedJobs: []string{"j"},
				RuleDigest: map[string]model.Action{"r": act},
			}
			mustExit0(t, h, h.claude("PreToolUse", preToolBash))
			d := h.state.lastCommit(t).delta
			if d.AnchorEmitted || d.FullEmitted || d.ReportedJobs != nil || d.Generation != "" || d.RuleDigest != nil {
				t.Errorf("an injection that was not rendered must not be recorded: %+v", d)
			}
			if strings.Contains(h.stdout.String(), "anchor") {
				t.Errorf("stdout = %q", h.stdout.String())
			}
		})
	}
	// A warn keeps the injection and merges reason and context.
	h := newHarness(t)
	h.engine.verdict = &model.Verdict{
		Action: model.ActionWarn, Reason: "[r] careful",
		Hits: []model.RuleHit{{RuleID: "r", Action: model.ActionWarn, Effective: model.ActionWarn, Message: "careful"}},
	}
	h.injector.inj = &model.Injection{Text: "anchor", AnchorEmitted: true}
	mustExit0(t, h, h.claude("PostToolUse", preToolBash))
	if out := h.stdout.String(); !strings.Contains(out, "[r] careful") || !strings.Contains(out, "anchor") {
		t.Errorf("stdout = %q", out)
	}
	if !h.state.lastCommit(t).delta.AnchorEmitted {
		t.Error("rendered anchor must be recorded")
	}
	if got := h.injector.lastReq().Verdict; got == nil || got.Action != model.ActionWarn {
		t.Errorf("injector must see the verdict: %+v", got)
	}
}

func TestNewJobsForInjector(t *testing.T) {
	jobTime := testNow.Add(-time.Minute)
	recs := []model.VerifyRecord{
		{JobID: "old", Name: "unit", TEnd: testNow.Add(-2 * time.Hour)},
		{JobID: "new", Name: "unit", TEnd: jobTime},
		{JobID: "told", Name: "lint", TEnd: jobTime},
	}
	started := testNow.Add(-time.Hour)

	h := newHarness(t)
	h.evidence.view.finished = recs
	h.state.session = &fakeSession{started: started, hasStart: true, reported: map[string]bool{"told": true}}
	mustExit0(t, h, h.claude("UserPromptSubmit", `{"session_id":"s1","cwd":"/repo","prompt":"p"}`))
	got := h.injector.lastReq().NewJobs
	if len(got) != 1 || got[0].JobID != "new" {
		t.Errorf("new jobs = %+v", got)
	}
	if since := h.evidence.view.since; len(since) != 1 || !since[0].Equal(started) {
		t.Errorf("window must start at the session start: %v", since)
	}

	// A previous report moves the window.
	h = newHarness(t)
	h.evidence.view.finished = recs
	h.state.session = &fakeSession{started: started, hasStart: true, lastJobAt: testNow.Add(-30 * time.Second)}
	mustExit0(t, h, h.claude("UserPromptSubmit", `{"session_id":"s1","cwd":"/repo","prompt":"p"}`))
	if len(h.injector.lastReq().NewJobs) != 0 {
		t.Errorf("new jobs = %+v", h.injector.lastReq().NewJobs)
	}

	// No session start recorded ⇒ nothing is reported (no flood of history).
	h = newHarness(t)
	h.evidence.view.finished = recs
	mustExit0(t, h, h.claude("UserPromptSubmit", `{"session_id":"s1","cwd":"/repo","prompt":"p"}`))
	if len(h.injector.lastReq().NewJobs) != 0 {
		t.Errorf("new jobs = %+v", h.injector.lastReq().NewJobs)
	}

	// Only prompt and mid-turn events carry job results.
	h = newHarness(t)
	h.evidence.view.finished = recs
	h.state.session = &fakeSession{started: started, hasStart: true}
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	if len(h.injector.lastReq().NewJobs) != 0 || len(h.evidence.view.since) != 0 {
		t.Error("PreToolUse must not scan finished jobs")
	}
}

func TestEvidenceAndWorldWiring(t *testing.T) {
	h := newHarness(t)
	h.deps.Watchdog = 150 * time.Millisecond
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	in := h.engine.lastInput()
	wantDeadline := testNow.Add(120 * time.Millisecond)
	if !in.Deadline.Equal(wantDeadline) || !h.evidence.deadline.Equal(wantDeadline) {
		t.Errorf("deadline = %v / %v, want %v", in.Deadline, h.evidence.deadline, wantDeadline)
	}
	if h.evidence.repos[0].Inst != "0123456789abcdef" {
		t.Errorf("evidence repo: %+v", h.evidence.repos[0])
	}
	if b, tri := in.World.Branch(); b != "main" || tri != model.TriTrue {
		t.Errorf("branch = %q %v", b, tri)
	}
	in.World.Branch()
	if h.repo.branches != 1 {
		t.Errorf("Branch must be read once per invocation, got %d", h.repo.branches)
	}
	if v, ok := in.World.Getenv("BALLAST_TEST_SANDBOX"); !ok || v != "1" {
		t.Errorf("Getenv = %q %v", v, ok)
	}

	// Watchdog shorter than the margin ⇒ the deadline collapses to the start.
	h = newHarness(t)
	h.deps.Watchdog = 20 * time.Millisecond
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	if !h.engine.lastInput().Deadline.Equal(testNow) {
		t.Errorf("deadline = %v", h.engine.lastInput().Deadline)
	}

	// Default watchdog applies when the field is zero.
	d := &Deps{}
	if d.watchdog() != DefaultWatchdog {
		t.Error("default watchdog")
	}
}
