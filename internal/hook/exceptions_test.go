package hook

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/libingtong/ballast/internal/model"
)

// TestExceptionPath: every error, panic and invariant violation before the
// stdout commit yields empty stdout, "ballast: ..." on stderr and exit 1.
func TestExceptionPath(t *testing.T) {
	type tc struct {
		name   string
		client model.ClientID
		native string
		stdin  string
		setup  func(h *harness)
		stderr string // substring
	}
	var nilPtr *model.Event
	cases := []tc{
		{name: "stdin is not json", stdin: "not json", stderr: "malformed hook input"},
		{name: "stdin is an array", stdin: "[1]", stderr: "malformed hook input"},
		{name: "stdin is null", stdin: "null", stderr: "malformed hook input"},
		{name: "stdin is empty", stdin: "", stderr: "malformed hook input"},
		{name: "unknown event", native: "Bogus", stdin: "{}", stderr: "unknown native event"},
		{name: "event of another client", client: model.ClientCodex, native: "PostToolUseFailure", stdin: "{}", stderr: "unknown native event"},
		{name: "unknown client", client: "bogus", stdin: "{}", stderr: "unknown client"},
		{name: "adapter returns no event", stdin: "{}", stderr: "no event", setup: func(h *harness) {
			h.adapters.ad[model.ClientClaude].parseOverride = func(string, []byte) (*model.Event, error) { return nil, nil }
		}},
		{name: "adapter returns unnamed event", stdin: "{}", stderr: "no event", setup: func(h *harness) {
			h.adapters.ad[model.ClientClaude].parseOverride = func(string, []byte) (*model.Event, error) { return &model.Event{}, nil }
		}},
		{name: "adapter panics", stdin: "{}", stderr: "panic: adapter boom", setup: func(h *harness) {
			h.adapters.ad[model.ClientClaude].parseOverride = func(string, []byte) (*model.Event, error) { panic("adapter boom") }
		}},
		{name: "adapter nil pointer", stdin: "{}", stderr: "panic:", setup: func(h *harness) {
			h.adapters.ad[model.ClientClaude].parseOverride = func(string, []byte) (*model.Event, error) {
				return &model.Event{Name: model.EventPreToolUse, Tool: nilPtr.Tool}, nil
			}
		}},
		{name: "rules load error", stderr: "load rules: injected", setup: func(h *harness) { h.rules.err = errTest }},
		{name: "rules loader returns nothing", stderr: "no rule set", setup: func(h *harness) { h.rules.rs = nil }},
		{name: "evaluate error", stderr: "evaluate: injected", setup: func(h *harness) { h.engine.err = errTest }},
		{name: "evaluate panics with a string", stderr: "panic: engine boom", setup: func(h *harness) { h.engine.panicv = "engine boom" }},
		{name: "evaluate panics with an error", stderr: "panic: injected", setup: func(h *harness) { h.engine.panicv = errTest }},
		{name: "evaluate panics with a runtime error", stderr: "panic:", setup: func(h *harness) {
			h.engine.each = func(*model.EvalInput) *model.Verdict { var m map[string]int; m["x"] = 1; return nil }
		}},
		{name: "injector error", stderr: "build injection: injected", setup: func(h *harness) { h.injector.err = errTest }},
		{name: "state commit error", stderr: "commit state: injected", setup: func(h *harness) { h.state.commitErr = errTest }},
		{name: "audit append error", stderr: "append audit: injected", setup: func(h *harness) {
			h.audit.err = errTest
			h.engine.verdict = denyVerdict()
		}},
		{name: "audit short write", stderr: "short write", setup: func(h *harness) {
			h.audit.err = io.ErrShortWrite
			h.engine.verdict = denyVerdict()
		}},
		{name: "render error", stderr: "render: injected", setup: func(h *harness) {
			h.adapters.ad[model.ClientClaude].renderErr = errTest
			h.engine.verdict = denyVerdict()
		}},
		{name: "missing dependency", stderr: "missing dependencies: Engine", setup: func(h *harness) { h.deps.Engine = nil }},
		{name: "several missing dependencies", stderr: "Audit", setup: func(h *harness) { h.deps.Engine, h.deps.Audit, h.deps.Clock = nil, nil, nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			if c.setup != nil {
				c.setup(h)
			}
			client, native, stdin := c.client, c.native, c.stdin
			if client == "" {
				client = model.ClientClaude
			}
			if native == "" {
				native = "PreToolUse"
			}
			if c.stdin == "" && c.setup != nil && !strings.Contains(c.name, "stdin") {
				stdin = preToolBash
			}
			h.wantFailure(h.main(client, native, stdin))
			if !strings.Contains(h.stderr.String(), c.stderr) {
				t.Errorf("stderr = %q, want substring %q", h.stderr.String(), c.stderr)
			}
			if strings.Count(h.stderr.String(), "ballast: ") != 1 {
				t.Errorf("stderr repeats the prefix: %q", h.stderr.String())
			}
			if len(h.exitCalls()) != 0 {
				t.Errorf("only the watchdog may call Exit, got %v", h.exitCalls())
			}
		})
	}
}

func TestMainArgumentFailures(t *testing.T) {
	for _, argv := range [][]string{
		nil,
		{"--client", "claude-code"},
		{"--event", "PreToolUse"},
		{"--client", "claude-code", "--event", "PreToolUse", "extra"},
		{"--client", "claude-code", "--event", "PreToolUse", "--unknown=1"},
	} {
		h := newHarness(t)
		code := Main(argv, strings.NewReader(preToolBash), &h.stdout, &h.stderr, h.deps)
		h.wantFailure(code)
		if h.log.index("parse") >= 0 {
			t.Errorf("%v: argument errors must stop before parsing the event", argv)
		}
	}
}

func TestMainStdinFailures(t *testing.T) {
	h := newHarness(t)
	code := Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, iotest.ErrReader(errTest), &h.stdout, &h.stderr, h.deps)
	h.wantFailure(code)
	if !strings.Contains(h.stderr.String(), "read stdin") {
		t.Errorf("stderr = %q", h.stderr.String())
	}

	// A nil stdin behaves like an empty one (malformed input).
	h = newHarness(t)
	code = Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, nil, &h.stdout, &h.stderr, h.deps)
	h.wantFailure(code)

	// Exactly MaxStdinBytes+1 bytes are rejected before parsing.
	h = newHarness(t)
	big := io.LimitReader(repeatReader{'x'}, MaxStdinBytes+1)
	code = Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, big, &h.stdout, &h.stderr, h.deps)
	h.wantFailure(code)
	if !strings.Contains(h.stderr.String(), "exceeds") || h.log.index("parse") >= 0 {
		t.Errorf("stderr = %q log=%v", h.stderr.String(), h.log.list())
	}

	// MaxStdinBytes bytes are accepted (and then fail only as malformed JSON).
	h = newHarness(t)
	code = Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, io.LimitReader(repeatReader{'x'}, MaxStdinBytes), &h.stdout, &h.stderr, h.deps)
	h.wantFailure(code)
	if !strings.Contains(h.stderr.String(), "malformed") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

func TestMainNilDeps(t *testing.T) {
	var out, errb strings.Builder
	if code := Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, strings.NewReader("{}"), &out, &errb, nil); code != 1 {
		t.Errorf("code = %d", code)
	}
	if out.Len() != 0 || !strings.HasPrefix(errb.String(), "ballast: ") {
		t.Errorf("stdout=%q stderr=%q", out.String(), errb.String())
	}
	// nil writers are tolerated.
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	if code := Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, strings.NewReader(preToolBash), nil, nil, h.deps); code != 0 {
		t.Errorf("code = %d", code)
	}
}

type failingWriter struct {
	n   int
	err error
}

func (w failingWriter) Write(p []byte) (int, error) {
	if w.n > len(p) {
		return len(p), nil
	}
	return w.n, w.err
}

func TestStdoutWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		w    failingWriter
		want string
	}{
		{"error", failingWriter{0, errTest}, "write stdout: injected"},
		{"short write", failingWriter{3, nil}, "short write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.engine.verdict = denyVerdict()
			var errb strings.Builder
			code := Main([]string{"--client", "claude-code", "--event", "PreToolUse"}, strings.NewReader(preToolBash), tc.w, &errb, h.deps)
			if code != 1 || !strings.HasPrefix(errb.String(), "ballast: ") || !strings.Contains(errb.String(), tc.want) {
				t.Errorf("code=%d stderr=%q", code, errb.String())
			}
			if strings.Count(errb.String(), "\n") != 1 {
				t.Errorf("stderr = %q", errb.String())
			}
		})
	}
}

// TestNotExceptions: conditions that are degraded and audited, never fatal.
func TestNotExceptions(t *testing.T) {
	diagNames := func(h *harness) []string {
		var out []string
		for _, r := range h.audit.all() {
			if r.Type == model.AuditDiag {
				var d struct {
					Diag string `json:"diag"`
				}
				if err := jsonUnmarshal(r.Detail, &d); err != nil {
					panic(err)
				}
				out = append(out, d.Diag)
			}
		}
		return out
	}
	hasDiag := func(h *harness, name string) bool {
		for _, d := range diagNames(h) {
			if d == name {
				return true
			}
		}
		return false
	}

	t.Run("not a repo", func(t *testing.T) {
		h := newHarness(t)
		h.repo.err = fmt.Errorf("locate: %w", model.ErrNotRepo)
		h.engine.verdict = denyVerdict()
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if !strings.Contains(h.stdout.String(), denyJSON) {
			t.Errorf("rules must still apply outside a repo: %q", h.stdout.String())
		}
		in := h.engine.lastInput()
		if in.Evidence != nil {
			t.Error("evidence must be nil outside a repo")
		}
		if b, tri := in.World.Branch(); b != "" || tri != model.TriUnknown {
			t.Errorf("branch = %q %v", b, tri)
		}
		if h.evidence.opens != 0 {
			t.Error("evidence provider must not be opened")
		}
		if h.rules.rs == nil || h.state.lastCommit(t).key.RepoInst != "norepo-"+hash16("/repo") {
			t.Errorf("scope = %+v", h.state.lastCommit(t).key)
		}
		if h.audit.audited(model.AuditDiag) {
			t.Errorf("a per-call not_a_repo diag would flood the audit log: %v", diagNames(h))
		}
		// Once per session, at SessionStart.
		h = newHarness(t)
		h.repo.err = model.ErrNotRepo
		mustExit0(t, h, h.claude("SessionStart", `{"session_id":"s","cwd":"/repo"}`))
		if !hasDiag(h, "not_a_repo") {
			t.Errorf("diags = %v", diagNames(h))
		}
	})

	t.Run("locate fails unexpectedly", func(t *testing.T) {
		h := newHarness(t)
		h.repo.err = errTest
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if !hasDiag(h, "repo_locate_failed") || h.engine.lastInput().Evidence != nil {
			t.Errorf("diags = %v", diagNames(h))
		}
	})

	t.Run("event without cwd", func(t *testing.T) {
		h := newHarness(t)
		mustExit0(t, h, h.claude("PreToolUse", `{"session_id":"s","tool_name":"Bash","tool_input":{"command":"ls"}}`))
		if h.log.index("locate") >= 0 {
			t.Error("Locate must not run without a cwd")
		}
		if in := h.engine.lastInput(); in.Paths.Cwd != "" || in.Evidence != nil {
			t.Errorf("eval input: %+v", in.Paths)
		}
	})

	t.Run("state load I/O error", func(t *testing.T) {
		h := newHarness(t)
		h.state.loadErr = errTest
		h.state.loadInfo = model.StateLoadInfo{Exists: true, Readable: false}
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if h.engine.lastInput().SessionReadable {
			t.Error("SessionReadable must be false")
		}
		if !hasDiag(h, "state_unreadable") || h.state.commitCount() != 1 {
			t.Errorf("diags = %v", diagNames(h))
		}
	})

	t.Run("state reported unreadable without an error", func(t *testing.T) {
		h := newHarness(t)
		h.state.loadInfo = model.StateLoadInfo{Exists: true, Readable: false}
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if h.engine.lastInput().SessionReadable {
			t.Error("SessionReadable must be false")
		}
	})

	t.Run("missing state is readable", func(t *testing.T) {
		h := newHarness(t)
		h.state.loadInfo = model.StateLoadInfo{Exists: false}
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if !h.engine.lastInput().SessionReadable {
			t.Error("a missing state file is an empty readable state")
		}
	})

	t.Run("corrupt state", func(t *testing.T) {
		h := newHarness(t)
		h.state.loadInfo = model.StateLoadInfo{Exists: true, Corrupt: true}
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if !h.engine.lastInput().SessionReadable || !hasDiag(h, "state_corrupt") {
			t.Errorf("diags = %v", diagNames(h))
		}
	})

	t.Run("corrupt state found under the lock", func(t *testing.T) {
		h := newHarness(t)
		h.state.corrupt = true
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if !hasDiag(h, "state_corrupt") {
			t.Errorf("diags = %v", diagNames(h))
		}
	})

	t.Run("store returns nil state", func(t *testing.T) {
		h := newHarness(t)
		h.deps.State = nilStateStore{h.state}
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if h.engine.lastInput().Session == nil {
			t.Error("Session must never be nil")
		}
	})

	t.Run("lock timeout", func(t *testing.T) {
		h := newHarness(t)
		h.state.lockTimeout = true
		h.engine.verdict = denyVerdict()
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if !strings.Contains(h.stdout.String(), denyJSON) || !hasDiag(h, "lock_timeout") {
			t.Errorf("stdout=%q diags=%v", h.stdout.String(), diagNames(h))
		}
	})

	t.Run("evidence open error", func(t *testing.T) {
		h := newHarness(t)
		h.evidence.err = errTest
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if h.engine.lastInput().Evidence != nil || !hasDiag(h, "evidence_open_failed") {
			t.Errorf("diags = %v", diagNames(h))
		}
	})

	t.Run("evaluator returns nothing", func(t *testing.T) {
		h := newHarness(t)
		h.engine.verdict = nil
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if h.stdout.Len() != 0 {
			t.Errorf("stdout = %q", h.stdout.String())
		}
	})

	t.Run("shell parser error makes the command unknown", func(t *testing.T) {
		h := newHarness(t)
		h.bash.err = errTest
		var got error
		h.engine.each = func(in *model.EvalInput) *model.Verdict { _, got = in.Commands.Get(); return &model.Verdict{} }
		mustExit0(t, h, h.claude("PostToolUse", preToolBash))
		if got == nil {
			t.Error("Commands.Get must report the parse error")
		}
		if cc := h.state.lastCommit(t).delta; len(cc.ReadPaths) != 0 {
			t.Errorf("read paths = %v", cc.ReadPaths)
		}
	})

	t.Run("shell parser panic becomes a parse error", func(t *testing.T) {
		h := newHarness(t)
		h.bash.panicv = "mvdan exploded"
		var got error
		h.engine.each = func(in *model.EvalInput) *model.Verdict { _, got = in.Commands.Get(); return &model.Verdict{} }
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if got == nil || !strings.Contains(got.Error(), "mvdan exploded") {
			t.Errorf("parse error = %v", got)
		}
	})

	t.Run("missing parsers", func(t *testing.T) {
		h := newHarness(t)
		h.deps.Bash, h.deps.PS = nil, nil
		var got error
		h.engine.each = func(in *model.EvalInput) *model.Verdict { _, got = in.Commands.Get(); return &model.Verdict{} }
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		if got == nil {
			t.Error("a missing parser must surface as a parse error")
		}
	})

	t.Run("no shell command, no Commands", func(t *testing.T) {
		h := newHarness(t)
		mustExit0(t, h, h.claude("PreToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Edit","tool_input":{"file_path":"a.go"}}`))
		if h.engine.lastInput().Commands != nil {
			t.Error("Commands must be nil for non-shell tools")
		}
		h = newHarness(t)
		mustExit0(t, h, h.claude("PreToolUse", `{"session_id":"s","cwd":"/repo","tool_name":"Bash","tool_input":{}}`))
		if h.engine.lastInput().Commands != nil {
			t.Error("Commands must be nil without a command")
		}
	})

	t.Run("cleanup failures are ignored", func(t *testing.T) {
		h := newHarness(t)
		h.state.dropErr = errTest
		mustExit0(t, h, h.claude("SessionEnd", `{"session_id":"s","cwd":"/repo"}`))
	})

	t.Run("cleanup panics are ignored", func(t *testing.T) {
		h := newHarness(t)
		h.deps.State = panicDropStore{h.state}
		mustExit0(t, h, h.claude("SessionEnd", `{"session_id":"s","cwd":"/repo"}`))
	})

	t.Run("duplicate delivery renders the same decision", func(t *testing.T) {
		h := newHarness(t)
		h.state.duplicate = true
		h.engine.verdict = denyVerdict()
		mustExit0(t, h, h.claude("PreToolUse", preToolBash))
		first := h.stdout.String()
		if !strings.Contains(first, denyJSON) {
			t.Fatalf("stdout = %q", first)
		}
		recs := h.audit.all()
		if len(recs) != 1 || !strings.Contains(string(recs[0].Detail), `"duplicate":true`) {
			t.Errorf("duplicate hits must be marked: %+v", recs)
		}
	})
}

func hash16(s string) string {
	return fmt.Sprintf("%x", sha256Sum(s))[:16]
}

type nilStateStore struct{ *fakeStore }

func (n nilStateStore) Load(k model.ScopeKey) (model.SessionState, model.StateLoadInfo, error) {
	return nil, model.StateLoadInfo{}, nil
}

type panicDropStore struct{ *fakeStore }

func (panicDropStore) DropSession(model.ScopeKey) error { panic("drop boom") }

func (f *fakeAudit) audited(t model.AuditType) bool {
	for _, x := range f.types() {
		if x == t {
			return true
		}
	}
	return false
}

var _ = errors.New
