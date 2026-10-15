package dsl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

type lintCase struct {
	Name  string            `json:"name"`
	Count int               `json:"count"` // replicate the rule (ids lint-base-NN)
	Patch json.RawMessage   `json:"patch"` // RFC 7386 merge patch applied to the base rule
	Rules []json.RawMessage `json:"rules"` // replaces the generated rules when present
	File  json.RawMessage   `json:"file"`  // merge patch for the file object
	Raw   string            `json:"raw"`   // replaces the whole file text when present
	Opts  struct {
		Clients     []model.ClientID `json:"clients"`
		Layer       model.Layer      `json:"layer"`
		RunExamples bool             `json:"run_examples"`
	} `json:"options"`
	Want []string `json:"want"` // "<severity>:<code>@<pointer>"
}

type lintCaseFile struct {
	Base  json.RawMessage `json:"base"`
	Cases []lintCase      `json:"cases"`
}

func mergePatch(dst any, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	dm, ok := dst.(map[string]any)
	if !ok {
		dm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(dm, k)
			continue
		}
		dm[k] = mergePatch(dm[k], v)
	}
	return dm
}

func applyPatch(t *testing.T, base json.RawMessage, patch json.RawMessage) json.RawMessage {
	t.Helper()
	var b any
	if err := json.Unmarshal(base, &b); err != nil {
		t.Fatal(err)
	}
	if len(patch) > 0 {
		var p any
		if err := json.Unmarshal(patch, &p); err != nil {
			t.Fatal(err)
		}
		b = mergePatch(b, p)
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diagKeys(ds []model.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		k := string(d.Severity) + ":" + d.Code
		if d.Pointer != "" {
			k += "@" + d.Pointer
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestLintCases(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "lint_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f lintCaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	e := newEngine(t)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var text string
			if c.Raw != "" {
				text = c.Raw
			} else {
				var rules []json.RawMessage
				switch {
				case len(c.Rules) > 0:
					for _, r := range c.Rules {
						rules = append(rules, applyPatch(t, f.Base, r))
					}
				case c.Count > 0:
					for i := 0; i < c.Count; i++ {
						p := map[string]any{"id": fmt.Sprintf("lint-base-%02d", i)}
						var user map[string]any
						if len(c.Patch) > 0 {
							_ = json.Unmarshal(c.Patch, &user)
						}
						pb, _ := json.Marshal(mergePatch(p, user))
						rules = append(rules, applyPatch(t, f.Base, pb))
					}
				default:
					rules = append(rules, applyPatch(t, f.Base, c.Patch))
				}
				var rs []string
				for _, r := range rules {
					rs = append(rs, string(r))
				}
				file := applyPatch(t, json.RawMessage(`{"version":1,"rules":[`+strings.Join(rs, ",")+`]}`), c.File)
				text = string(file)
			}
			ds := e.Lint([]LintInput{{Path: "case.json", Layer: model.LayerFile, Data: []byte(text)}},
				LintOptions{Clients: c.Opts.Clients, Layer: c.Opts.Layer, RunExamples: c.Opts.RunExamples})
			got := diagKeys(ds)
			want := append([]string(nil), c.Want...)
			sort.Strings(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("diagnostics mismatch\n got: %q\nwant: %q\nfull: %+v", got, want, ds)
			}
			for _, d := range ds {
				if d.File != "case.json" || d.Message == "" {
					t.Errorf("incomplete diagnostic: %+v", d)
				}
			}
		})
	}
}
