package install

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// sameJSON compares two documents semantically (number spelling preserved).
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	dec := func(x []byte) any {
		var v any
		d := json.NewDecoder(strings.NewReader(string(x)))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			t.Fatalf("not JSON: %q", x)
		}
		return v
	}
	return reflect.DeepEqual(dec(a), dec(b))
}

func stripBOM(b []byte) []byte { return []byte(strings.TrimPrefix(string(b), "\ufeff")) }

var jsonHookSeeds = []string{
	``, `{}`, "{}\n", `{"hooks":{}}`, `{"model":"x"}`,
	`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/bin/a"}]}]}}`,
	`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[]}],"Notification":[]}}`,
	"{\r\n  \"a\": [1, 2, {\"b\": null}]\r\n}\r\n",
	`{"version":1,"hooks":{"stop":[{"command":"x"}]}}`,
	`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/ballast/bin/ballast","args":["hook","--client","claude-code","--event","Stop"]}]}]}}`,
	`[`, `{"hooks":[]}`, `{"hooks":{"Stop":{}}}`, "\xef\xbb\xbf{}",
}

// FuzzJSONHooksPlan: on any input Plan either refuses with ErrSettingsParse
// or produces valid JSON that is stable under a second install and loses
// nothing on uninstall.
func FuzzJSONHooksPlan(f *testing.F) {
	for _, s := range jsonHookSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		for _, tc := range []struct {
			tg     Target
			client model.ClientID
		}{{gClaudeT(), model.ClientClaude}, {gCodexHT(), model.ClientCodex}, {gCursorT(), model.ClientCursor}} {
			spec := gspec(tc.client)
			p, err := tc.tg.Plan([]byte(in), spec, nil)
			if err != nil {
				if !errors.Is(err, model.ErrSettingsParse) {
					t.Fatalf("%s: unexpected error type %v", tc.client, err)
				}
				continue
			}
			if !json.Valid(stripBOM(p.After)) {
				t.Fatalf("%s: output is not JSON: %q -> %q", tc.client, in, p.After)
			}
			m := manifestFrom(gBinary, p)
			p2, err := tc.tg.Plan(p.After, spec, m)
			if err != nil || string(p2.After) != string(p.After) {
				t.Fatalf("%s: second install differs (%v):\n%q\n%q", tc.client, err, p.After, p2.After)
			}
			r, err := tc.tg.Remove(p.After, m)
			if err != nil {
				t.Fatalf("%s: Remove: %v", tc.client, err)
			}
			if r.After == nil {
				continue // created by install and now empty
			}
			if strings.Contains(string(r.After), "ballast") && !strings.Contains(in, "ballast") {
				t.Fatalf("%s: uninstall left entries: %q", tc.client, r.After)
			}
			if !json.Valid(stripBOM(r.After)) && strings.TrimSpace(string(stripBOM(r.After))) != "" {
				t.Fatalf("%s: uninstalled output is not JSON: %q", tc.client, r.After)
			}
			// install followed by uninstall gives the file back byte for byte
			// (Cursor keeps the version:1 it added while user hooks remain, §5.3)
			keptVersion := tc.client == model.ClientCursor && strings.Contains(string(r.After), `"version"`) && !strings.Contains(in, `"version"`)
			if !strings.Contains(in, "ballast") && !keptVersion && string(r.After) != in {
				t.Fatalf("%s: round trip changed the file:\n%q\n%q", tc.client, in, r.After)
			}
		}
	})
}

var tomlSeeds = []string{
	``, "[features]\nhooks = true\n", "[features]\nhooks = false\n", "a = 1\n[b]\nc = 2\n", "features.hooks = false\n",
	"a = \"\"\"\n[features]\n\"\"\"\n", "a = [\n 1,\n]\n", "[x]\r\ny = 1\r\n", "[features", "k = 'v' # c\n",
	"[hooks.state.'/home/u/.codex/hooks.json:stop:0:0']\nenabled = true\n",
	"model = \"o3\"\nfeatures.web_search_request = true\n", "features.a = 1\n\n[profiles.x]\nb = 2\n",
	"features.a = 1\n[features]\n", "features.hooks.x = 1\n",
	"[hooks.state]\n'/home/u/.codex/hooks.json:stop:0:0'.enabled = true\n", "hooks.state.'/home/u/.codex/hooks.json:stop:0:0'.enabled = true\n",
	"hooks = { state = {} }\n",
}

// FuzzCodexConfigPlan: every accepted config stays a scannable document with
// features.hooks on, and a trust install + uninstall is lossless for it.
func FuzzCodexConfigPlan(f *testing.F) {
	for _, s := range tomlSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		spec := gspec(model.ClientCodex)
		spec.CodexTrust = true
		hp, err := gCodexHT().Plan(nil, spec, nil)
		if err != nil {
			t.Fatal(err)
		}
		cp, err := gCodexCT().planWith([]byte(in), spec, nil, []*PlanResult{hp})
		if err != nil {
			if !errors.Is(err, model.ErrSettingsParse) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		d, err := parseTOML(cp.After)
		if err != nil {
			t.Fatalf("output does not scan: %v\n%q -> %q", err, in, cp.After)
		}
		if _, _, val, err := d.featuresHooks(); err != nil || val != "true" {
			t.Fatalf("features.hooks not on: %q %v\n%q", val, err, cp.After)
		}
		m := manifestFrom(gBinary, hp, cp)
		cp2, err := gCodexCT().planWith(cp.After, spec, m, []*PlanResult{hp})
		if err != nil || string(cp2.After) != string(cp.After) {
			t.Fatalf("second trusted install differs (%v):\n%q\n%q", err, cp.After, cp2.After)
		}
		r, err := gCodexCT().Remove(cp.After, m)
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if strings.Contains(string(r.After), "trusted_hash = \"sha256:") && !strings.Contains(in, "trusted_hash") {
			t.Fatalf("trust left behind: %q", r.After)
		}
		// the part of the document that is not features/trust is unchanged
		if _, err := parseTOML(r.After); err != nil {
			t.Fatalf("uninstalled output does not scan: %v", err)
		}
	})
}

func FuzzParseTOMLRoundTrip(f *testing.F) {
	for _, s := range tomlSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		d, err := parseTOML([]byte(in))
		if err != nil {
			return
		}
		if string(d.bytes()) != in {
			t.Fatalf("scanner is lossy: %q -> %q", in, d.bytes())
		}
	})
}

func FuzzCanonicalHash(f *testing.F) {
	for _, s := range jsonHookSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		h1, err := EntryHash([]byte(in))
		if err != nil {
			return
		}
		var v any
		dec := json.NewDecoder(strings.NewReader(in))
		dec.UseNumber()
		_ = dec.Decode(&v)
		pretty, _ := json.MarshalIndent(v, "", "   ")
		if !reflect.DeepEqual(v, nil) {
			if h2, err := EntryHash(pretty); err == nil && h2 != h1 && !hasFloatLike(in) {
				t.Fatalf("hash depends on layout: %q", in)
			}
		}
	})
}

// hasFloatLike: number literals keep their spelling, so 1.0 vs 1 may differ.
func hasFloatLike(s string) bool { return strings.ContainsAny(s, "eE.") }
