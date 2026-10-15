package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func writeLines(t testing.TB, dir, name string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCursorIsApproximateForStatefulRules(t *testing.T) {
	cursorWrite := cr("cursor-write", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Write"}}
		r.When = []model.Cond{{Not: &model.Cond{ReadThisSession: &model.PathArg{Path: model.TargetBinding}}}}
	})
	stateless := cr("any-shell", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Bash: &model.ShellMatch{Cmd: model.StringList{"ls"}}}
	})
	e := newEnv(t, rset("g", cursorWrite, stateless))
	path := writeLines(t, e.dir, "cursor.jsonl", []string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nwrite it\n</user_query>"}]}}`,
		fmt.Sprintf(`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"%s/n.go"}},{"type":"tool_use","name":"Shell","input":{"command":"ls"}}]}}`, e.workdir),
		`{"role":"assistant","message":{"content":[{"type":"text","text":"finished"}]}}`,
	})
	rep := mustRun(t, e, path, Options{Client: model.ClientCursor})
	if got := counts(summary(t, rep, "cursor-write")); got != [4]int{0, 1, 1, 0} {
		t.Errorf("cursor-write = %v", got) // Write (hit) is approximate; the Shell call misses the matcher alone: known-miss
	}
	if got := counts(summary(t, rep, "any-shell")); got != [4]int{1, 1, 0, 0} {
		t.Errorf("any-shell = %v (stateless rules stay known)", got)
	}
	smp := samplesOf(rep, "cursor-write")
	if len(smp) != 1 || smp[0].Class != Approximate {
		t.Errorf("samples = %+v", smp)
	}
	if rep.Client != model.ClientCursor {
		t.Errorf("client = %s", rep.Client)
	}
}

func TestRunCodexStopAndFailureEvents(t *testing.T) {
	stop := cr("codex-stop", model.EventStop, func(r *model.Rule) {
		r.Match = &model.Match{Text: &model.TextMatch{Regex: model.StringList{"done"}}}
	})
	post := cr("saw-output", model.EventPostToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Bash"}}
	})
	e := newEnv(t, rset("g", stop, post))
	path := writeLines(t, e.dir, "rollout.jsonl", []string{
		fmt.Sprintf(`{"timestamp":"2026-10-10T09:00:00Z","type":"session_meta","payload":{"id":"cx","cwd":%q}}`, e.workdir),
		`{"timestamp":"2026-10-10T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]}}`,
		`{"timestamp":"2026-10-10T09:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"false\"}","call_id":"c1"}}`,
		`{"timestamp":"2026-10-10T09:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"{\"output\":\"\",\"metadata\":{\"exit_code\":1}}"}}`,
		`{"timestamp":"2026-10-10T09:00:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"all done"}]}}`,
		`{"timestamp":"2026-10-10T09:00:05Z","type":"event_msg","payload":{"type":"task_complete","last_agent_message":"all done"}}`,
	})
	rep := mustRun(t, e, path, Options{Client: model.ClientCodex})
	if got := counts(summary(t, rep, "saw-output")); got != [4]int{1, 0, 0, 0} {
		t.Errorf("saw-output = %v (Codex has no PostToolUseFailure: the result is a PostToolUse)", got)
	}
	if got := counts(summary(t, rep, "codex-stop")); got != [4]int{0, 0, 1, 0} {
		t.Errorf("codex-stop = %v", got)
	}
}

func TestRunClaudeFailureWithoutFailureRulesStillSkipsReads(t *testing.T) {
	// No rule on PostToolUseFailure: the failed Read must still not count.
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir)
	x.tool("r", "Read", map[string]any{"file_path": e.workdir + "/a.go"}).result("r", "ENOENT", true)
	x.tool("e", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 1, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}

func TestRunWindowsStylePaths(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", `C:\Users\dev\proj`)
	x.tool("r", "Read", map[string]any{"file_path": `C:\Users\dev\proj\a.go`}).result("r", "x", false)
	x.tool("e", "Edit", map[string]any{"file_path": `C:\Users\dev\proj\a.go`})
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "w.jsonl")), Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{0, 2, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}
