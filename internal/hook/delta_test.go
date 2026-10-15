package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func TestScopeFor(t *testing.T) {
	sum := sha256.Sum256([]byte("/work/x"))
	norepo := "norepo-" + hex.EncodeToString(sum[:])[:16]
	cases := []struct {
		name string
		ev   *model.Event
		repo *model.RepoInfo
		cwd  string
		want model.ScopeKey
	}{
		{"repo main", &model.Event{SessionID: "s"}, &model.RepoInfo{Inst: "abc"}, "/r", model.ScopeKey{RepoInst: "abc", Session: "s", Agent: "main"}},
		{"repo agent", &model.Event{SessionID: "s", AgentID: "a1"}, &model.RepoInfo{Inst: "abc"}, "/r", model.ScopeKey{RepoInst: "abc", Session: "s", Agent: "a1"}},
		{"no repo", &model.Event{SessionID: "s"}, nil, "/work/x", model.ScopeKey{RepoInst: norepo, Session: "s", Agent: "main"}},
		{"repo without inst falls back", &model.Event{SessionID: "s"}, &model.RepoInfo{}, "/work/x", model.ScopeKey{RepoInst: norepo, Session: "s", Agent: "main"}},
		{"empty cwd", &model.Event{}, nil, "", model.ScopeKey{RepoInst: "norepo-e3b0c44298fc1c14", Session: "", Agent: "main"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ScopeFor(c.ev, c.repo, c.cwd); got != c.want {
				t.Errorf("ScopeFor = %+v, want %+v", got, c.want)
			}
		})
	}
	if k := ScopeFor(&model.Event{}, nil, "/a"); len(k.RepoInst) != len("norepo-")+16 {
		t.Errorf("norepo key length: %q", k.RepoInst)
	}
	if ScopeFor(&model.Event{}, nil, "/a") == ScopeFor(&model.Event{}, nil, "/b") {
		t.Error("different cwd must give different norepo scopes")
	}
}

func bashTool(id, cmd string) *model.ToolCall {
	return &model.ToolCall{Name: "Bash", UseID: id, Shell: model.ShellBash, Command: cmd}
}

func TestBuildDeltaByEvent(t *testing.T) {
	now := testNow
	rs := &model.CompiledRuleSet{Generation: "g7"}
	long := strings.Repeat("é", 300)
	cases := []struct {
		name  string
		ev    *model.Event
		check func(t *testing.T, d *model.StateDelta)
	}{
		{"PreToolUse", &model.Event{Name: model.EventPreToolUse, Tool: bashTool("tu1", "git status\nsecond line")}, func(t *testing.T, d *model.StateDelta) {
			if !d.ToolCall || d.Prompt || d.FlushEvals || d.ToolFailure {
				t.Errorf("flags: %+v", d)
			}
			if d.DedupeKey != "PreToolUse:tu1" {
				t.Errorf("dedupe key %q", d.DedupeKey)
			}
			want := &model.EventSummary{TS: now, Event: model.EventPreToolUse, Tool: "Bash", Text: "Bash: git status"}
			if !reflect.DeepEqual(d.Summary, want) {
				t.Errorf("summary = %+v, want %+v", d.Summary, want)
			}
			if !d.Now.Equal(now) {
				t.Errorf("Now = %v", d.Now)
			}
		}},
		{"PostToolUse shares tool_use_id but not the key", &model.Event{Name: model.EventPostToolUse, Tool: bashTool("tu1", "ls")}, func(t *testing.T, d *model.StateDelta) {
			if d.ToolCall {
				t.Error("PostToolUse must not count a tool call")
			}
			if d.DedupeKey != "PostToolUse:tu1" {
				t.Errorf("dedupe key %q", d.DedupeKey)
			}
		}},
		{"tool without id has no dedupe key", &model.Event{Name: model.EventPreToolUse, Tool: bashTool("", "ls")}, func(t *testing.T, d *model.StateDelta) {
			if d.DedupeKey != "" {
				t.Errorf("dedupe key %q", d.DedupeKey)
			}
		}},
		{"file tool summary uses first target", &model.Event{Name: model.EventPostToolUse, Tool: &model.ToolCall{Name: "Edit", Targets: []string{"src/a.go", "src/b.go"}}}, func(t *testing.T, d *model.StateDelta) {
			if d.Summary.Text != "Edit: src/a.go" {
				t.Errorf("text %q", d.Summary.Text)
			}
		}},
		{"tool without command or targets", &model.Event{Name: model.EventPostToolUse, Tool: &model.ToolCall{Name: "mcp__x__y"}}, func(t *testing.T, d *model.StateDelta) {
			if d.Summary.Text != "mcp__x__y" || d.Summary.Tool != "mcp__x__y" {
				t.Errorf("summary %+v", d.Summary)
			}
		}},
		{"summary text is redacted and clipped", &model.Event{Name: model.EventPreToolUse, Tool: bashTool("a", "curl -H 'Authorization: Bearer abcdef' --password=hunter2 "+strings.Repeat("x", 300))}, func(t *testing.T, d *model.StateDelta) {
			if strings.Contains(d.Summary.Text, "hunter2") || strings.Contains(d.Summary.Text, "abcdef") {
				t.Errorf("secret leaked: %q", d.Summary.Text)
			}
			if n := len([]rune(d.Summary.Text)); n > model.MaxEventSummaryRunes {
				t.Errorf("summary has %d runes", n)
			}
		}},
		{"UserPromptSubmit", &model.Event{Name: model.EventUserPromptSubmit, Prompt: "  " + long + "  "}, func(t *testing.T, d *model.StateDelta) {
			if !d.Prompt || d.ToolCall {
				t.Errorf("flags: %+v", d)
			}
			if got := []rune(d.PromptTail); len(got) != 200 || string(got) != strings.Repeat("é", 200) {
				t.Errorf("prompt tail has %d runes", len(got))
			}
			if d.Summary == nil || d.Summary.Text != "" {
				t.Errorf("prompt text must not be recorded in the ring: %+v", d.Summary)
			}
		}},
		{"short prompt kept whole", &model.Event{Name: model.EventUserPromptSubmit, Prompt: "hello"}, func(t *testing.T, d *model.StateDelta) {
			if d.PromptTail != "hello" {
				t.Errorf("tail %q", d.PromptTail)
			}
		}},
		{"PostToolUseFailure", &model.Event{Name: model.EventPostToolUseFailure, Tool: bashTool("f1", "false")}, func(t *testing.T, d *model.StateDelta) {
			if !d.ToolFailure || d.ToolCall {
				t.Errorf("flags: %+v", d)
			}
		}},
		{"Stop flushes", &model.Event{Name: model.EventStop}, func(t *testing.T, d *model.StateDelta) {
			if !d.FlushEvals {
				t.Error("Stop must flush evals")
			}
		}},
		{"SubagentStop flushes", &model.Event{Name: model.EventSubagentStop}, func(t *testing.T, d *model.StateDelta) {
			if !d.FlushEvals {
				t.Error("SubagentStop must flush evals")
			}
		}},
		{"SessionEnd flushes without a ring entry", &model.Event{Name: model.EventSessionEnd}, func(t *testing.T, d *model.StateDelta) {
			if !d.FlushEvals || d.Summary != nil {
				t.Errorf("delta: %+v", d)
			}
		}},
		{"SessionStart does not flush", &model.Event{Name: model.EventSessionStart}, func(t *testing.T, d *model.StateDelta) {
			if d.FlushEvals || d.Summary == nil {
				t.Errorf("delta: %+v", d)
			}
		}},
		{"AgentResponse stores text only", &model.Event{Name: model.EventAgentResponse, LastAssistantMessage: "head " + strings.Repeat("a", 5000) + " 已完成"}, func(t *testing.T, d *model.StateDelta) {
			// The tail is kept (contract v1.3): tail-window text rules must
			// still see the end of the message.
			if len([]rune(d.LastAssistantText)) != 4000 || !strings.HasSuffix(d.LastAssistantText, " 已完成") || strings.Contains(d.LastAssistantText, "head") {
				t.Errorf("assistant text has %d runes", len([]rune(d.LastAssistantText)))
			}
			if d.Summary != nil || d.ToolCall || d.FlushEvals {
				t.Errorf("delta: %+v", d)
			}
		}},
		{"PostToolBatch", &model.Event{Name: model.EventPostToolBatch}, func(t *testing.T, d *model.StateDelta) {
			if d.ToolCall || d.Prompt || d.FlushEvals {
				t.Errorf("delta: %+v", d)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.check(t, BuildDelta(c.ev, rs, nil, nil, nil, now))
		})
	}
}

func TestBuildDeltaVerdictCounts(t *testing.T) {
	v := &model.Verdict{
		Results: []model.RuleResult{
			{RuleID: "warned", Outcome: model.TriTrue},
			{RuleID: "shadow", Outcome: model.TriTrue},
			{RuleID: "miss", Outcome: model.TriFalse},
			{RuleID: "unk", Outcome: model.TriUnknown, UnknownCause: "x"},
			{RuleID: "denied", Outcome: model.TriTrue},
		},
		Hits: []model.RuleHit{
			{RuleID: "warned", Action: model.ActionWarn, Effective: model.ActionWarn},
			{RuleID: "shadow", Action: model.ActionLog, Target: model.ActionDeny, Effective: model.ActionLog},
			{RuleID: "denied", Action: model.ActionDeny, Effective: model.ActionDeny},
		},
	}
	ev := &model.Event{Name: model.EventPreToolUse, Tool: bashTool("x", "ls")}
	d := BuildDelta(ev, nil, v, nil, nil, testNow)
	wantEvals := map[string]model.EvalCount{
		"warned": {Evals: 1, Hits: 1},
		"shadow": {Evals: 1, Would: 1},
		"miss":   {Evals: 1},
		"unk":    {Evals: 1, Unknown: 1},
		"denied": {Evals: 1, Hits: 1},
	}
	if !reflect.DeepEqual(d.Evals, wantEvals) {
		t.Errorf("evals = %+v, want %+v", d.Evals, wantEvals)
	}
	if want := []string{"warned", "shadow", "denied"}; !reflect.DeepEqual(d.HitRules, want) {
		t.Errorf("hit rules = %v, want %v", d.HitRules, want)
	}
	if !reflect.DeepEqual(d.Summary.Hits, d.HitRules) {
		t.Errorf("summary hits = %v", d.Summary.Hits)
	}
	// The same rule hit twice is counted once in HitRules; counts per result.
	v2 := &model.Verdict{
		Results: []model.RuleResult{{RuleID: "r", Outcome: model.TriTrue}, {RuleID: "r", Outcome: model.TriFalse}},
		Hits: []model.RuleHit{
			{RuleID: "r", Effective: model.ActionLog}, {RuleID: "r", Effective: model.ActionWarn},
		},
	}
	d2 := BuildDelta(ev, nil, v2, nil, nil, testNow)
	if !reflect.DeepEqual(d2.HitRules, []string{"r"}) {
		t.Errorf("hit rules = %v", d2.HitRules)
	}
	if d2.Evals["r"].Evals != 2 {
		t.Errorf("evals = %+v", d2.Evals)
	}
	// No results (fake evaluator) ⇒ no counters, but hits still feed prior_hits.
	d3 := BuildDelta(ev, nil, &model.Verdict{Hits: v.Hits}, nil, nil, testNow)
	if d3.Evals != nil || len(d3.HitRules) != 3 {
		t.Errorf("delta = %+v", d3)
	}
}

func TestBuildDeltaInjectionAndPaths(t *testing.T) {
	rs := &model.CompiledRuleSet{Generation: "g7"}
	ev := &model.Event{Name: model.EventUserPromptSubmit, Prompt: "p"}
	digest := map[string]model.Action{"a": model.ActionDeny}
	inj := &model.Injection{
		Text: "t", AnchorEmitted: true, FullEmitted: true,
		ReportedJobs: []string{"j1", "j2"}, RuleDigest: digest,
	}
	d := BuildDelta(ev, rs, nil, inj, []string{"/r/a", "/r/b"}, testNow)
	if !d.AnchorEmitted || !d.FullEmitted {
		t.Errorf("emission flags: %+v", d)
	}
	if !reflect.DeepEqual(d.ReportedJobs, []string{"j1", "j2"}) || !d.JobReportAt.Equal(testNow) {
		t.Errorf("jobs: %+v", d)
	}
	if d.Generation != "g7" || !reflect.DeepEqual(d.RuleDigest, digest) {
		t.Errorf("generation/digest: %q %v", d.Generation, d.RuleDigest)
	}
	if !reflect.DeepEqual(d.ReadPaths, []string{"/r/a", "/r/b"}) {
		t.Errorf("read paths: %v", d.ReadPaths)
	}
	// The delta must not alias the inputs.
	digest["b"] = model.ActionWarn
	inj.ReportedJobs[0] = "zzz"
	if len(d.RuleDigest) != 1 || d.ReportedJobs[0] != "j1" {
		t.Error("delta aliases the injection")
	}

	// Without a digest the generation is not recorded (the event could not
	// carry a generation summary).
	d = BuildDelta(ev, rs, nil, &model.Injection{Text: "t"}, nil, testNow)
	if d.Generation != "" || d.RuleDigest != nil || d.JobReportAt != (time.Time{}) {
		t.Errorf("delta = %+v", d)
	}
	// nil rule set, injection or verdict are tolerated.
	d = BuildDelta(ev, nil, nil, &model.Injection{RuleDigest: map[string]model.Action{}}, nil, testNow)
	if d.Generation != "" {
		t.Errorf("generation without rule set: %q", d.Generation)
	}
	if d := BuildDelta(nil, rs, nil, nil, nil, testNow); d == nil || !d.Now.Equal(testNow) || d.ToolCall {
		t.Errorf("nil event delta: %+v", d)
	}
}
