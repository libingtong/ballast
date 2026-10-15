package install

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/plan")

const (
	gBinary = "/opt/ballast/bin/ballast"
	gClaude = "/home/u/.claude/settings.json"
	gCodexH = "/home/u/.codex/hooks.json"
	gCodexC = "/home/u/.codex/config.toml"
	gCursor = "/home/u/.cursor/hooks.json"
	gMDC    = "/home/u/.cursor/rules/ballast.mdc"
	gHermes = "/home/u/.hermes/config.yaml"
)

func gspec(c model.ClientID) EntrySpec {
	return EntrySpec{Client: c, Binary: gBinary, Events: defaultEvents(c), GOOS: "linux", MDC: []byte(testMDC)}
}

func gClaudeT() *jsonHooksTarget {
	return &jsonHooksTarget{client: model.ClientClaude, path: gClaude, goos: "linux"}
}
func gCodexHT() *jsonHooksTarget {
	return &jsonHooksTarget{client: model.ClientCodex, path: gCodexH, goos: "linux"}
}
func gCodexCT() *codexConfigTarget {
	return &codexConfigTarget{path: gCodexC, hooks: gCodexHT(), goos: "linux"}
}
func gCursorT() *jsonHooksTarget {
	return &jsonHooksTarget{client: model.ClientCursor, path: gCursor, goos: "linux"}
}
func gHermesT() *hermesTarget { return &hermesTarget{path: gHermes, goos: "linux"} }

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "plan", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func planAfter(t *testing.T, tg Target, cur string, spec EntrySpec, m *model.InstallManifest) *PlanResult {
	t.Helper()
	var b []byte
	if cur != "\x00absent" {
		b = []byte(cur)
	}
	p, err := tg.Plan(b, spec, m)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

const absent = "\x00absent"

// manifestFrom turns plan results into the manifest an install would store.
func manifestFrom(binary string, plans ...*PlanResult) *model.InstallManifest {
	m := &model.InstallManifest{V: model.ManifestVersion, Binary: binary}
	for _, p := range plans {
		m.Entries = append(m.Entries, p.Entries...)
		m.Files = append(m.Files, p.Files...)
		m.Flags = append(m.Flags, p.Flags...)
	}
	return m
}

func TestPlanGolden(t *testing.T) {
	t.Run("claude absent", func(t *testing.T) {
		p := planAfter(t, gClaudeT(), absent, gspec(model.ClientClaude), nil)
		checkGolden(t, "claude_absent", string(p.After))
		if !p.Created || p.Before != nil || !p.Changed() {
			t.Errorf("created=%v before=%v", p.Created, p.Before)
		}
	})
	t.Run("claude existing", func(t *testing.T) {
		cur := `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/usr/local/bin/audit"
          }
        ]
      }
    ],
    "Notification": []
  },
  "env": {
    "A": "1"
  }
}
`
		p := planAfter(t, gClaudeT(), cur, gspec(model.ClientClaude), nil)
		checkGolden(t, "claude_existing", string(p.After))
		if p.Created {
			t.Error("existing file must not be Created")
		}
	})
	t.Run("codex hooks absent", func(t *testing.T) {
		p := planAfter(t, gCodexHT(), absent, gspec(model.ClientCodex), nil)
		checkGolden(t, "codex_hooks_absent", string(p.After))
	})
	t.Run("codex config absent", func(t *testing.T) {
		p := planAfter(t, gCodexCT(), absent, gspec(model.ClientCodex), nil)
		checkGolden(t, "codex_config_absent", string(p.After))
	})
	t.Run("codex config trust", func(t *testing.T) {
		spec := gspec(model.ClientCodex)
		spec.CodexTrust = true
		hp := planAfter(t, gCodexHT(), absent, spec, nil)
		cp, err := gCodexCT().planWith([]byte("model = \"gpt\"\n\n[features]\nother = 1\n"), spec, nil, []*PlanResult{hp})
		if err != nil {
			t.Fatal(err)
		}
		checkGolden(t, "codex_config_trust", string(cp.After))
		var keys []string
		for _, f := range cp.Flags {
			keys = append(keys, f.Key)
		}
		checkGolden(t, "codex_config_trust_flags", strings.Join(keys, "\n")+"\n")
	})
	t.Run("cursor absent", func(t *testing.T) {
		p := planAfter(t, gCursorT(), absent, gspec(model.ClientCursor), nil)
		checkGolden(t, "cursor_hooks_absent", string(p.After))
	})
	t.Run("cursor existing without version", func(t *testing.T) {
		p := planAfter(t, gCursorT(), "{\n  \"hooks\": {\n    \"stop\": [\n      {\"command\": \"/bin/user-stop\"}\n    ]\n  }\n}\n", gspec(model.ClientCursor), nil)
		checkGolden(t, "cursor_hooks_existing", string(p.After))
	})
	t.Run("hermes absent", func(t *testing.T) {
		p := planAfter(t, gHermesT(), absent, gspec(model.ClientHermes), nil)
		checkGolden(t, "hermes_absent", string(p.After))
	})
	t.Run("hermes existing", func(t *testing.T) {
		cur := `# hermes config
model:
  name: nous   # keep me
terminal:
  shell: |
    hooks:
      not a real section
hooks:
  # user hooks
  pre_tool_call:
    - command: "/usr/local/bin/guard"
      timeout: 5
  on_session_end:
  - command: /usr/local/bin/bye

logging:
  level: info
`
		p := planAfter(t, gHermesT(), cur, gspec(model.ClientHermes), nil)
		checkGolden(t, "hermes_existing", string(p.After))
	})
	t.Run("cursor mdc", func(t *testing.T) {
		p := planAfter(t, gMDCT(), absent, gspec(model.ClientCursor), nil)
		checkGolden(t, "cursor_mdc", string(p.After))
	})
	t.Run("dry-run diff", func(t *testing.T) {
		p := planAfter(t, gClaudeT(), "{\n  \"model\": \"opus\"\n}\n", gspec(model.ClientClaude), nil)
		checkGolden(t, "claude_diff", UnifiedDiff(p.Path, p.Before, p.After))
	})
}

// TestGoldenContract checks the normative shapes in testdata/golden.json.
func TestGoldenContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	g := map[string]map[string]any{}
	for k, v := range top {
		if k == "_comment" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatal(err)
		}
		g[k] = m
	}
	decode := func(b []byte) map[string]any {
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, b)
		}
		return v
	}
	// every event of the golden document must be present with exactly the golden entries
	subset := func(t *testing.T, got, want map[string]any, exact bool) {
		t.Helper()
		gh, _ := got["hooks"].(map[string]any)
		wh, _ := want["hooks"].(map[string]any)
		for ev, wv := range wh {
			if !reflect.DeepEqual(gh[ev], wv) {
				t.Errorf("event %s:\n got %v\nwant %v", ev, gh[ev], wv)
			}
		}
		if exact && len(gh) != len(wh) {
			t.Errorf("got %d events, golden has %d", len(gh), len(wh))
		}
		if v, ok := want["version"]; ok && !reflect.DeepEqual(got["version"], v) {
			t.Errorf("version = %v, want %v", got["version"], v)
		}
	}

	t.Run("claude-code", func(t *testing.T) {
		p := planAfter(t, gClaudeT(), absent, gspec(model.ClientClaude), nil)
		subset(t, decode(p.After), g["claude-code"]["after"].(map[string]any), true)
		wantKey := g["claude-code"]["manifest_key_example"].(string)
		found := false
		for _, e := range p.Entries {
			found = found || e.Key == wantKey
		}
		if !found {
			t.Errorf("no manifest entry with key %s", wantKey)
		}
		if s := string(p.After); strings.Contains(s, `"async"`) || strings.Contains(s, `"onFailure"`) || strings.Contains(s, `"timeout"`) {
			t.Error("claude entries must not carry async/onFailure/timeout")
		}
	})
	t.Run("codex", func(t *testing.T) {
		p := planAfter(t, gCodexHT(), absent, gspec(model.ClientCodex), nil)
		subset(t, decode(p.After), g["codex"]["after"].(map[string]any), false)
		if n := len(decode(p.After)["hooks"].(map[string]any)); n != 6 {
			t.Errorf("codex writes %d natives, want 6", n)
		}
		c := planAfter(t, gCodexCT(), absent, gspec(model.ClientCodex), nil)
		if string(c.After) != g["codex"]["config_toml_append"].(string) {
			t.Errorf("config.toml = %q, want %q", c.After, g["codex"]["config_toml_append"])
		}
	})
	t.Run("cursor", func(t *testing.T) {
		p := planAfter(t, gCursorT(), absent, gspec(model.ClientCursor), nil)
		subset(t, decode(p.After), g["cursor"]["after"].(map[string]any), false)
		if n := len(decode(p.After)["hooks"].(map[string]any)); n != 10 {
			t.Errorf("cursor writes %d natives, want 10", n)
		}
	})
	t.Run("hermes", func(t *testing.T) {
		p := planAfter(t, gHermesT(), absent, gspec(model.ClientHermes), nil)
		want := g["hermes"]["after_text"].(string)
		if !strings.HasPrefix(string(p.After), "hooks:\n") || !strings.Contains(string(p.After), strings.TrimPrefix(want, "hooks:\n")) {
			t.Errorf("hermes output does not contain the golden block:\n%s", p.After)
		}
	})
}
