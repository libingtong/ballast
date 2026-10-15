package hook

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/*.json")

// goldenSnapshot is everything observable from one hook invocation.
type goldenSnapshot struct {
	Exit   int                 `json:"exit"`
	Stdout string              `json:"stdout"`
	Stderr string              `json:"stderr"`
	Delta  *model.StateDelta   `json:"state_delta,omitempty"`
	Audit  []model.AuditRecord `json:"audit"`
	Calls  []string            `json:"calls"`
	Spawn  [][]string          `json:"spawned,omitempty"`
	Drops  []model.ScopeKey    `json:"dropped,omitempty"`
}

func TestGolden(t *testing.T) {
	scenarios := []struct {
		name   string
		client model.ClientID
		native string
		stdin  string
		setup  func(h *harness)
	}{
		{
			name: "deny-with-context", client: model.ClientClaude, native: "PreToolUse", stdin: preToolBash,
			setup: func(h *harness) {
				h.engine.verdict = denyVerdict()
				h.state.session = &fakeSession{recent: []model.EventSummary{
					{Seq: 4, TS: testNow.Add(-time.Minute), Event: model.EventUserPromptSubmit},
					{Seq: 5, TS: testNow.Add(-time.Second), Event: model.EventPostToolUse, Tool: "Read", Text: "Read: src/a.go"},
				}}
				h.injector.inj = &model.Injection{Text: "anchor", AnchorEmitted: true}
			},
		},
		{
			name: "log-hit-shadow-mode", client: model.ClientClaude, native: "PreToolUse", stdin: preToolBash,
			setup: func(h *harness) {
				h.engine.verdict = &model.Verdict{
					Action:  model.ActionLog,
					Hits:    []model.RuleHit{{RuleID: "shadow", Layer: model.LayerRepo, Action: model.ActionLog, Target: model.ActionDeny, Effective: model.ActionLog, Matched: "bash node 0: git status"}},
					Results: []model.RuleResult{{RuleID: "shadow", Outcome: model.TriTrue}, {RuleID: "other", Outcome: model.TriUnknown, UnknownCause: "verify_fresh:no_project"}},
				}
			},
		},
		{
			name: "codex-ask-degraded", client: model.ClientCodex, native: "PreToolUse",
			stdin: `{"session_id":"cx","cwd":"/repo","tool_name":"Bash","tool_use_id":"t9","tool_input":{"command":"rm -rf build"}}`,
			setup: func(h *harness) {
				h.engine.verdict = &model.Verdict{
					Action: model.ActionAsk, Reason: "[r] sure?",
					Hits:    []model.RuleHit{{RuleID: "r", Layer: model.LayerUser, Action: model.ActionAsk, Target: model.ActionAsk, Effective: model.ActionAsk, Message: "sure?"}},
					Results: []model.RuleResult{{RuleID: "r", Outcome: model.TriTrue}},
				}
			},
		},
		{
			name: "session-start-full-injection", client: model.ClientClaude, native: "SessionStart",
			stdin: `{"session_id":"s1","cwd":"/repo","source":"startup"}`,
			setup: func(h *harness) {
				// No gc.stamp yet ⇒ a detached gc is spawned.
				h.rules.rs.Notices = []model.ConfigNotice{{Kind: model.NoticeConfigInvalid, Layer: model.LayerRepo, Path: "/repo/.ballast/rules.json", Message: "invalid JSON; using last-known-good"}}
				h.injector.inj = &model.Injection{
					Text: "Ballast rules (generation gen1):\n- [r] be careful", Kinds: []model.InjectKind{model.InjectFull, model.InjectNotices},
					FullEmitted: true, RuleDigest: map[string]model.Action{"r": model.ActionDeny},
				}
			},
		},
		{
			name: "stop-flush-eval-summary", client: model.ClientClaude, native: "Stop",
			stdin: `{"session_id":"s1","cwd":"/repo","last_assistant_message":"DONE"}`,
			setup: func(h *harness) {
				h.state.session = &fakeSession{evals: map[string]model.EvalCount{"r": {Evals: 3, Hits: 1, Unknown: 1}, "q": {Evals: 2, Would: 2}}}
			},
		},
		{
			name: "stop-lock-timeout", client: model.ClientClaude, native: "Stop",
			stdin: `{"session_id":"s1","cwd":"/repo"}`,
			setup: func(h *harness) {
				h.state.session = &fakeSession{evals: map[string]model.EvalCount{"r": {Evals: 3}}}
				h.state.lockTimeout = true
			},
		},
		{
			name: "codex-shell-read-post-tool-use", client: model.ClientCodex, native: "PostToolUse",
			stdin: `{"session_id":"cx","cwd":"/repo","tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"sed -n 1,80p src/a.go"},"tool_response":{}}`,
			setup: func(h *harness) {
				h.deps.Bash = starParser{&model.CommandList{Shell: model.ShellBash, Nodes: []model.CommandNode{node("sed", rd("src/a.go", "sed"))}}}
			},
		},
		{
			name: "not-a-repo-session-start", client: model.ClientClaude, native: "SessionStart",
			stdin: `{"session_id":"s1","cwd":"/tmp/work"}`,
			setup: func(h *harness) {
				h.repo.err = model.ErrNotRepo
				h.deps.Spawner = nil
			},
		},
		{
			name: "subagent-stop-drops-agent", client: model.ClientClaude, native: "SubagentStop",
			stdin: `{"session_id":"s1","cwd":"/repo","agent_id":"a1","agent_type":"Explore","last_assistant_message":"ok"}`,
			setup: func(h *harness) {},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			h := newHarness(t)
			h.deps.Layout.GCStamp = filepath.Join(h.sb.Root, "gc.stamp")
			sc.setup(h)
			code := h.main(sc.client, sc.native, sc.stdin)
			snap := goldenSnapshot{Exit: code, Stdout: h.stdout.String(), Stderr: h.stderr.String(), Audit: h.audit.all(), Calls: h.log.list(), Spawn: h.spawner.spawned()}
			if snap.Audit == nil {
				snap.Audit = []model.AuditRecord{}
			}
			if h.state.commitCount() > 0 {
				d := h.state.lastCommit(t).delta
				snap.Delta = &d
			}
			snap.Drops = append(append(snap.Drops, h.state.droppedAgent...), h.state.droppedSess...)
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(snap); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "golden", sc.name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run `go test ./internal/hook -run TestGolden -update`)", err)
			}
			if !bytes.Equal(buf.Bytes(), want) {
				t.Errorf("golden mismatch for %s (re-run with -update to accept)\n--- got ---\n%s\n--- want ---\n%s", sc.name, buf.Bytes(), want)
			}
		})
	}
}
