package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFacts writes a minimal valid facts tree under dir and returns the
// path of m0b-cases.json. mutate may edit the cases before writing.
func buildFacts(t *testing.T, mutate func(cases []map[string]any)) string {
	t.Helper()
	dir := t.TempDir()
	var cases []map[string]any
	for _, id := range append(append([]string{}, NoCredCases...), CredCases...) {
		c := map[string]any{
			"case_id": id, "events": []string{"SessionStart"}, "schema_keys": map[string]any{"SessionStart": []string{"source"}},
			"assertion": "a", "result": "pass", "mode": "none", "claude_version": "2.1.300", "observation": "o",
			"evidence": []string{"payloads/m0b/" + id + "/"},
		}
		for _, cred := range CredCases {
			if cred == id {
				c["result"] = "needs_credentials"
				c["evidence"] = []string{}
				c["recheck"] = "ANTHROPIC_API_KEY=<用户自己提供> scripts/probe-m0b/run.sh --with-credentials --cases " + id
			}
		}
		if noEventsAllowed[id] {
			c["events"] = []string{}
		}
		cases = append(cases, c)
		if c["result"] != "needs_credentials" {
			p := filepath.Join(dir, "payloads", "m0b", id)
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "01.json"), []byte(`{"hook_event_name":"SessionStart","cwd":"<repo>"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if mutate != nil {
		mutate(cases)
	}
	b, _ := json.MarshalIndent(cases, "", " ")
	path := filepath.Join(dir, "m0b-cases.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var concl strings.Builder
	concl.WriteString("A13 A14 A15 A16 A20\n")
	for _, c := range cases {
		concl.WriteString(c["case_id"].(string) + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "M0b-CONCLUSIONS.md"), []byte(concl.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func find(cases []map[string]any, id string) map[string]any {
	for _, c := range cases {
		if c["case_id"] == id {
			return c
		}
	}
	return nil
}

func wantProblem(t *testing.T, path, sub string) {
	t.Helper()
	problems, _ := Validate(path)
	for _, p := range problems {
		if strings.Contains(p, sub) {
			return
		}
	}
	t.Fatalf("want a problem containing %q, got %v", sub, problems)
}

func TestValidFactsPass(t *testing.T) {
	problems, summary := Validate(buildFacts(t, nil))
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if !strings.Contains(summary, "21 cases") {
		t.Fatalf("summary %q", summary)
	}
}

func TestCaseCounts(t *testing.T) {
	if len(NoCredCases) != 15 || len(CredCases) != 6 {
		t.Fatalf("§6.4 has 15 no-credential and 6 credential cases, got %d and %d", len(NoCredCases), len(CredCases))
	}
}

func TestMissingCase(t *testing.T) {
	p := buildFacts(t, func(cases []map[string]any) {})
	raw, _ := os.ReadFile(p)
	var cs []map[string]any
	_ = json.Unmarshal(raw, &cs)
	var out []map[string]any
	for _, c := range cs {
		if c["case_id"] != "configchange-payload" {
			out = append(out, c)
		}
	}
	b, _ := json.Marshal(out)
	_ = os.WriteFile(p, b, 0o644)
	wantProblem(t, p, `required case "configchange-payload" is missing`)
}

func TestNeedsCredentialsRequiresRecheck(t *testing.T) {
	p := buildFacts(t, func(cases []map[string]any) { delete(find(cases, "mcp-write"), "recheck") })
	wantProblem(t, p, "needs_credentials requires a recheck")
	p = buildFacts(t, func(cases []map[string]any) { find(cases, "mcp-write")["recheck"] = "echo hi" })
	wantProblem(t, p, "runnable scripts/probe-m0b/run.sh")
	p = buildFacts(t, func(cases []map[string]any) {
		find(cases, "mcp-write")["recheck"] = "ANTHROPIC_API_KEY=<x> scripts/probe-m0b/run.sh --with-credentials --cases other"
	})
	wantProblem(t, p, "does not name the case")
}

// A needs_credentials entry that lists mock evidence must still have it on
// disk (a deleted directory would otherwise go unnoticed).
func TestNeedsCredentialsEvidenceMustExist(t *testing.T) {
	p := buildFacts(t, func(cases []map[string]any) {
		find(cases, "mcp-write")["evidence"] = []string{"payloads/m0b/mcp-write/"}
	})
	wantProblem(t, p, `evidence "payloads/m0b/mcp-write/" does not exist`)
}

func TestFieldsRequired(t *testing.T) {
	cases := map[string]func(c map[string]any){
		"assertion missing":         func(c map[string]any) { c["assertion"] = "" },
		"observation missing":       func(c map[string]any) { c["observation"] = "" },
		"is not one of pass|fail":   func(c map[string]any) { c["result"] = "maybe" },
		"is not one of none|blackh": func(c map[string]any) { c["mode"] = "live" },
		"claude_version missing":    func(c map[string]any) { c["claude_version"] = "" },
		"schema_keys missing":       func(c map[string]any) { delete(c, "schema_keys") },
		"events is empty":           func(c map[string]any) { c["events"] = []string{} },
		"evidence missing":          func(c map[string]any) { c["evidence"] = []string{} },
		"does not exist":            func(c map[string]any) { c["evidence"] = []string{"payloads/m0b/nope/"} },
	}
	for want, mut := range cases {
		p := buildFacts(t, func(cs []map[string]any) { mut(find(cs, "managed-d-merge-hooks")) })
		wantProblem(t, p, want)
	}
}

func TestCredentialCaseCannotPassWithoutCredentials(t *testing.T) {
	p := buildFacts(t, func(cs []map[string]any) {
		c := find(cs, "sandbox-denywrite-bash")
		c["result"] = "pass"
		c["mode"] = "mock"
		c["evidence"] = []string{"payloads/m0b/managed-d-merge-hooks/"}
	})
	wantProblem(t, p, "only a credentials-mode run may record pass")
}

func TestNocredEventsFailureDegradesDependents(t *testing.T) {
	p := buildFacts(t, func(cs []map[string]any) { find(cs, "nocred-events")["result"] = "fail" })
	wantProblem(t, p, "must be needs_credentials")
}

func TestHygienePatterns(t *testing.T) {
	bad := map[string]string{
		"local absolute path": `{"cwd":"/Users/` + `someone/NewCode/ballast"}`,
		"uid fragment":        `{"p":"/tmp/claude-` + `501-x"}`,
		"container home path": `{"cwd":"/home/agent/proj"}`,
		"home at line end":    "cwd=/home/agent",
		"home before quote":   `x='/home/agent'`,
		"home before colon":   `PATH=/home/agent:/usr/bin`,
		"project key":         `{"dir":"-home-agent-par-child"}`,
		"looks like a secret": `{"k":"sk-ant-api03-abcdefgh"}`,
		"non-dummy":           `ANTHROPIC_API_KEY=realvalue`,
		"private key":         `-----BEGIN RSA PRIVATE KEY-----`,
		"bearer":              `Authorization: Bearer abcdefghijklmnopqrstuvwxyz`,
		"directory structure": `x [~]/NewCode`,
		"session id":          `{"session_id": "00000000-0000-4000-8000-0000000000a1"}`,
		"prompt id":           `{"prompt_id":"00000000-0000-4000-8000-0000000000a2"}`,
		"transcript name":     `{"transcript_path":"~/x/00000000-0000-4000-8000-0000000000a1.jsonl"}`,
	}
	for name, content := range bad {
		if name == "directory structure" {
			content = "x ~" + "/NewCode"
		}
		var got []string
		scanText("f", content, func(f string, a ...any) { got = append(got, f) })
		if len(got) == 0 {
			t.Errorf("%s: %q was not flagged", name, content)
		}
	}
	ok := []string{`{"cwd":"<repo>","transcript_path":"<home>/.claude/projects/x.jsonl"}`, `ANTHROPIC_API_KEY=dummy-not-a-credential`, `ANTHROPIC_API_KEY=<用户自己提供>`,
		`{"session_id": "<session-3>","prompt_id":"<prompt-3>","transcript_path":"~/x/<session-3>.jsonl"}`}
	for _, content := range ok {
		var got []string
		scanText("f", content, func(f string, a ...any) { got = append(got, f) })
		if len(got) != 0 {
			t.Errorf("%q wrongly flagged: %v", content, got)
		}
	}
}

func TestPayloadHygieneFailsValidation(t *testing.T) {
	p := buildFacts(t, nil)
	leak := filepath.Join(filepath.Dir(p), "payloads", "m0b", "nocred-events", "02.json")
	if err := os.WriteFile(leak, []byte(`{"hook_event_name":"SessionStart","cwd":"/Users/`+`someone/Documents/x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, p, "local absolute path")
}

// A non-hook JSON file under payloads/m0b would break the adapter golden test
// TestFactsPayloadsParse, so the checker must reject it.
func TestPayloadJSONMustBeHookPayload(t *testing.T) {
	p := buildFacts(t, nil)
	dir := filepath.Join(filepath.Dir(p), "payloads", "m0b", "runner-bwrap-in-docker")
	items := filepath.Join(dir, "items.json")
	if err := os.WriteFile(items, []byte(`{"builds":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, p, "no hook_event_name")
	if err := os.WriteFile(items, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, p, "not a JSON object")
	if err := os.Remove(items); err != nil {
		t.Fatal(err)
	}
	// the same content under another extension is accepted
	if err := os.WriteFile(filepath.Join(dir, "items.txt"), []byte(`{"builds":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if problems, _ := Validate(p); len(problems) != 0 {
		t.Fatalf("items.txt must be accepted: %v", problems)
	}
}

func TestHygieneOnlyMode(t *testing.T) {
	p := buildFacts(t, func(cs []map[string]any) { find(cs, "mcp-write")["result"] = "bogus" })
	var out, errb bytes.Buffer
	if rc := run([]string{"--hygiene-only", p}, &out, &errb); rc != 0 {
		t.Fatalf("hygiene-only must ignore case content, rc=%d %s", rc, errb.String())
	}
	leak := filepath.Join(filepath.Dir(p), "payloads", "m0b", "nocred-events", "03.json")
	if err := os.WriteFile(leak, []byte(`{"hook_event_name":"SessionStart","cwd":"/home/agent/proj"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if rc := run([]string{"--hygiene-only", p}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "container home path") {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
}

// hermes-cases.json belongs to payloads/hermes, not payloads/m0b: its evidence
// tree must get the same path/secret scan (it used to be skipped entirely).
func TestHygieneOnlyScansTheEvidenceTreeOfItsCasesFile(t *testing.T) {
	dir := t.TempDir()
	cases := filepath.Join(dir, "hermes-cases.json")
	if err := os.WriteFile(cases, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "payloads", "m0b"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	// A missing hermes tree is a problem, not a silent pass over payloads/m0b.
	if rc := run([]string{"--hygiene-only", cases}, &out, &errb); rc != 1 || !strings.Contains(errb.String(), filepath.Join("payloads", "hermes")) {
		t.Fatalf("missing tree: rc=%d stderr=%s", rc, errb.String())
	}
	hdir := filepath.Join(dir, "payloads", "hermes", "case")
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Hermes evidence is not required to be a hook payload (no hook_event_name).
	if err := os.WriteFile(filepath.Join(hdir, "ok.json"), []byte(`{"event":"pre_tool_call"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if rc := run([]string{"--hygiene-only", cases}, &out, &errb); rc != 0 {
		t.Fatalf("clean tree: rc=%d stderr=%s", rc, errb.String())
	}
	leak := `{"cwd":"/Users/` + `jdoe/NewCode/repo","token":"gh` + `p_` + strings.Repeat("A1b2C3d4E5", 3) + `"}`
	if err := os.WriteFile(filepath.Join(hdir, "leak.json"), []byte(leak), 0o644); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	rc := run([]string{"--hygiene-only", cases}, &out, &errb)
	if rc != 1 || !strings.Contains(errb.String(), "local absolute path") || !strings.Contains(errb.String(), "looks like a secret") {
		t.Fatalf("leak in the hermes tree must fail: rc=%d stderr=%s", rc, errb.String())
	}
}

// sandbox-failifunavailable is judged by managed hook events, so it must
// degrade with nocred-events (§6.4 last paragraph).
func TestFailIfUnavailableDependsOnEvents(t *testing.T) {
	p := buildFacts(t, func(cs []map[string]any) { find(cs, "nocred-events")["result"] = "fail" })
	wantProblem(t, p, "so sandbox-failifunavailable must be needs_credentials")
}

func TestConclusionsMustCoverAllCases(t *testing.T) {
	p := buildFacts(t, nil)
	cp := filepath.Join(filepath.Dir(p), "M0b-CONCLUSIONS.md")
	b, _ := os.ReadFile(cp)
	_ = os.WriteFile(cp, []byte(strings.ReplaceAll(string(b), "mcp-write", "")), 0o644)
	wantProblem(t, p, "does not mention case mcp-write")
	_ = os.WriteFile(cp, []byte(strings.ReplaceAll(string(b), "A20", "")), 0o644)
	wantProblem(t, p, "no conclusion row for A20")
	_ = os.Remove(cp)
	wantProblem(t, p, "M0b-CONCLUSIONS.md")
}

func TestRunUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run(nil, &out, &errb); rc != 2 {
		t.Fatalf("rc=%d", rc)
	}
	p := buildFacts(t, nil)
	if rc := run([]string{p}, &out, &errb); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	if rc := run([]string{filepath.Join(t.TempDir(), "none.json")}, &out, &errb); rc != 1 {
		t.Fatalf("rc=%d", rc)
	}
}

// TestManagedT1FixtureMatchesFrozenForms keeps the T1 managed-settings fixture
// used by the container cases in step with the shared golden of
// tier.ManagedForms (internal/tier/testdata/managed_forms.json, linux-default).
func TestManagedT1FixtureMatchesFrozenForms(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "tier", "testdata", "managed_forms.json"))
	if err != nil {
		t.Skipf("golden not available: %v", err)
	}
	var g struct {
		Cases []struct {
			Name      string   `json:"name"`
			DenyWrite []string `json:"deny_write"`
			PermDeny  []string `json:"perm_deny"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(golden, &g); err != nil {
		t.Fatal(err)
	}
	var want *struct {
		Name      string   `json:"name"`
		DenyWrite []string `json:"deny_write"`
		PermDeny  []string `json:"perm_deny"`
	}
	for i := range g.Cases {
		if g.Cases[i].Name == "linux-default" {
			want = &g.Cases[i]
		}
	}
	if want == nil {
		t.Fatal("linux-default case missing from managed_forms.json")
	}
	raw, err := os.ReadFile(filepath.Join("..", "fixtures", "managed-t1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Sandbox struct {
			Enabled                  bool     `json:"enabled"`
			AllowUnsandboxedCommands bool     `json:"allowUnsandboxedCommands"`
			FailIfUnavailable        bool     `json:"failIfUnavailable"`
			ExcludedCommands         []string `json:"excludedCommands"`
			Filesystem               struct {
				DenyWrite []string `json:"denyWrite"`
			} `json:"filesystem"`
		} `json:"sandbox"`
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		AllowManagedPermissionRulesOnly bool `json:"allowManagedPermissionRulesOnly"`
		AllowManagedHooksOnly           bool `json:"allowManagedHooksOnly"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Sandbox.Filesystem.DenyWrite, "\n") != strings.Join(want.DenyWrite, "\n") {
		t.Errorf("denyWrite differs from managed_forms.json:\n got %v\nwant %v", f.Sandbox.Filesystem.DenyWrite, want.DenyWrite)
	}
	if strings.Join(f.Permissions.Deny, "\n") != strings.Join(want.PermDeny, "\n") {
		t.Errorf("permissions.deny differs from managed_forms.json")
	}
	if !f.Sandbox.Enabled || f.Sandbox.AllowUnsandboxedCommands || !f.Sandbox.FailIfUnavailable || len(f.Sandbox.ExcludedCommands) != 0 ||
		!f.AllowManagedPermissionRulesOnly || !f.AllowManagedHooksOnly {
		t.Errorf("fixture is not the T1 template: %+v", f)
	}
}
