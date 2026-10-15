package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/testenv"
)

func TestParseArgsInterspersed(t *testing.T) {
	tests := []struct {
		name string
		args []string
		pos  []string
		json bool
		str  string
	}{
		{"flags first", []string{"--json", "--s", "v", "a", "b"}, []string{"a", "b"}, true, "v"},
		{"positional first", []string{"a", "--json", "b"}, []string{"a", "b"}, true, ""},
		{"equals form", []string{"--s=x=y", "a"}, []string{"a"}, false, "x=y"},
		{"terminator", []string{"--json", "--", "--s", "v"}, []string{"--s", "v"}, true, ""},
		{"nothing", nil, nil, false, ""},
		{"single dash", []string{"-json", "a"}, []string{"a"}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFlagSet("t")
			var j bool
			var s string
			fs.BoolVar(&j, "json", false, "")
			fs.StringVar(&s, "s", "", "")
			pos, err := parseArgs(fs, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pos, tc.pos) || j != tc.json || s != tc.str {
				t.Errorf("pos=%v json=%v s=%q", pos, j, s)
			}
		})
	}
	fs := newFlagSet("t")
	if _, err := parseArgs(fs, []string{"--nope"}); err == nil {
		t.Error("unknown flag must be an error")
	}
	var m multiFlag
	if err := m.Set("a, b,,c"); err != nil || !reflect.DeepEqual([]string(m), []string{"a", "b", "c"}) {
		t.Errorf("multiFlag = %v, %v", m, err)
	}
}

func TestWantsHelp(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"-h"}, true}, {[]string{"x", "--help"}, true}, {[]string{"-help"}, true},
		{[]string{"--", "-h"}, false}, {[]string{"--json"}, false}, {nil, false},
	} {
		if got := wantsHelp(c.args); got != c.want {
			t.Errorf("wantsHelp(%v) = %v", c.args, got)
		}
	}
}

func TestIsJobID(t *testing.T) {
	for id, want := range map[string]bool{
		"20261016T093000-a1b2c3": true, "20261016T093000-A1B2C3": false, "test": false,
		"": false, "20261016T093000": false, "go-test": false,
	} {
		if got := isJobID(id); got != want {
			t.Errorf("isJobID(%q) = %v", id, got)
		}
	}
}

func TestCheckExitCode(t *testing.T) {
	for _, c := range []struct {
		r    model.FreshResult
		want int
	}{
		{model.FreshResult{Value: model.TriTrue, Cause: "fresh"}, ExitCheckFresh},
		{model.FreshResult{Value: model.TriFalse, Cause: "stale"}, ExitCheckStale},
		{model.FreshResult{Value: model.TriFalse, Cause: "not_valid_pass"}, ExitCheckStale},
		{model.FreshResult{Value: model.TriFalse, Cause: "no_record"}, ExitCheckNoEvidence},
		{model.FreshResult{Value: model.TriUnknown, Cause: "job_running"}, ExitCheckRunning},
		{model.FreshResult{Value: model.TriUnknown, Cause: "not_a_repo"}, ExitCheckUndecidable},
		{model.FreshResult{Value: model.TriUnknown, Cause: "no_project"}, ExitCheckUndecidable},
		{model.FreshResult{Value: model.TriUnknown, Cause: "project_invalid"}, ExitCheckUndecidable},
		{model.FreshResult{Value: model.TriUnknown, Cause: "unparseable_commit"}, ExitCheckUndecidable},
	} {
		if got := checkExitCode(c.r); got != c.want {
			t.Errorf("checkExitCode(%+v) = %d, want %d", c.r, got, c.want)
		}
		if msg := checkMessage(c.r); !strings.HasPrefix(msg, "ballast verify --check: ") || !strings.Contains(msg, c.r.Cause) {
			t.Errorf("checkMessage = %q", msg)
		}
	}
}

func TestWorstStatus(t *testing.T) {
	for _, c := range []struct {
		codes []int
		want  int
	}{
		{nil, 0}, {[]int{0, 0}, 0}, {[]int{0, 1}, 1}, {[]int{1, 6}, 6}, {[]int{11, 0}, 11},
		{[]int{3, 1}, 3}, {[]int{7, 11}, 7}, {[]int{5, 6}, 6}, {[]int{4, 3}, 4},
	} {
		if got := worstStatus(c.codes); got != c.want {
			t.Errorf("worstStatus(%v) = %d, want %d", c.codes, got, c.want)
		}
	}
}

// run executes Main in-process under a sandboxed process environment.
func run(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestMainInProcess(t *testing.T) {
	sb := testenv.Isolate(t)
	work := filepath.Join(sb.Root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	if code, out, _ := run(t, "", "version"); code != 0 || !strings.HasPrefix(out, "ballast ") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, out, _ := run(t, "", "--version"); code != 0 || !strings.HasPrefix(out, "ballast ") {
		t.Errorf("--version: %d %q", code, out)
	}
	if code, out, _ := run(t, "", "help"); code != 0 || !strings.Contains(out, "用法") {
		t.Errorf("help: %d %q", code, out)
	}
	if code, out, errb := run(t, ""); code != ExitUsage || out != "" || !strings.Contains(errb, "用法") {
		t.Errorf("no args: %d %q %q", code, out, errb)
	}
	if code, _, errb := run(t, "", "bogus"); code != ExitUsage || !strings.HasPrefix(errb, "ballast: unknown command") {
		t.Errorf("unknown: %d %q", code, errb)
	}
	if code, _, errb := run(t, "", "version", "extra"); code != ExitUsage || !strings.Contains(errb, "unexpected argument") {
		t.Errorf("version extra: %d %q", code, errb)
	}

	// Hook protocol errors stay on the exception path (exit 1, stdout empty).
	for _, args := range [][]string{
		{"hook"}, {"hook", "--client", "claude-code"}, {"hook", "--client", "nobody", "--event", "x"},
	} {
		code, out, errb := run(t, "{}", args...)
		if code != ExitHookError || out != "" || !strings.HasPrefix(errb, "ballast:") {
			t.Errorf("%v: %d %q %q", args, code, out, errb)
		}
	}
	code, out, errb := run(t, "{bad", "hook", "--client", "claude-code", "--event", "PreToolUse")
	if code != ExitHookError || out != "" || !strings.HasPrefix(errb, "ballast:") {
		t.Errorf("malformed stdin: %d %q %q", code, out, errb)
	}

	// Commands that read only sandboxed state.
	if code, out, _ := run(t, "", "doctor"); code != 0 || !strings.Contains(out, "CI 是最终裁判") {
		t.Errorf("doctor: %d %q", code, out)
	}
	if code, out, _ := run(t, "", "config", "show", "--json"); code != 0 || !strings.Contains(out, `"generation"`) {
		t.Errorf("config show: %d %q", code, out)
	}
	if code, out, _ := run(t, "", "stats"); code != 0 || !strings.Contains(out, "rule") {
		t.Errorf("stats: %d %q", code, out)
	}
	if code, _, _ := run(t, "", "audit", "lint"); code != 0 {
		t.Errorf("audit lint: %d", code)
	}
	if code, out, _ := run(t, "", "org", "render"); code != 0 || !strings.Contains(out, "allowManagedHooksOnly") {
		t.Errorf("org render: %d %q", code, out)
	}
	if code, _, _ := run(t, "", "lint"); code != 0 { // no layer files at all
		t.Errorf("lint without files: %d", code)
	}
	if code, out, _ := run(t, "", "init", "--dry-run"); code != ExitFindings || out != "" {
		t.Errorf("init in an empty directory (no stack): %d %q", code, out)
	}
	// Confirmation prompt: a "y" on stdin writes, anything else declines.
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.invalid/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := run(t, "n\n", "init"); code != ExitFindings || !strings.Contains(errb, "cancelled") {
		t.Errorf("init declined: %d %q", code, errb)
	}
	if _, err := os.Stat(filepath.Join(work, ".ballast", "project.json")); err == nil {
		t.Error("declined init wrote project.json")
	}
	if code, _, _ := run(t, "y\n", "init"); code != 0 {
		t.Errorf("init confirmed: %d", code)
	}
	if _, err := os.Stat(filepath.Join(work, ".ballast", "project.json")); err != nil {
		t.Errorf("confirmed init did not write project.json: %v", err)
	}
	if code, out, _ := run(t, "", "init", "--force", "--yes"); code != 0 || !strings.Contains(out, "无需写入") {
		t.Errorf("init --force with identical content: %d %q", code, out)
	}
}

func TestWireHookIsCheap(t *testing.T) {
	sb := testenv.Isolate(t)
	// WireHook must not touch the file system: with a BALLAST_HOME that does
	// not exist, nothing may be created.
	deps, err := WireHook(sb.Env())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.BallastHome); err == nil {
		t.Error("WireHook created BALLAST_HOME")
	}
	if deps.Clock == nil || deps.Rules == nil || deps.Engine == nil || deps.State == nil || deps.Audit == nil ||
		deps.Repo == nil || deps.Evidence == nil || deps.Injector == nil || deps.Spawner == nil || deps.Adapters == nil || deps.Paths == nil ||
		deps.Bash == nil || deps.PS == nil || deps.Exit == nil || deps.Self == "" {
		t.Errorf("incomplete deps: %+v", deps)
	}
	if deps.Faults != nil {
		t.Error("production builds must not wire a fault injector")
	}
	if _, err := WireHook(emptyEnv{}); err == nil {
		t.Error("an environment without a home must be an error")
	}
}

type emptyEnv struct{}

func (emptyEnv) Getenv(string) (string, bool) { return "", false }
func (emptyEnv) Environ() []string            { return nil }
func (emptyEnv) HomeDir() (string, error)     { return "", os.ErrNotExist }
func (emptyEnv) GOOS() string                 { return "linux" }
