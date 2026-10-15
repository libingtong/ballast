package stats

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func TestRewriteRuleActions(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		id     string
		to     model.Action
		want   string
		errSub string
	}{
		{
			name: "replace action, target equal to old action follows",
			in:   `{"version":1,"rules":[{"id":"aaa","action":"warn","target":"warn","event":"PostToolUse","examples":[]}]}`,
			id:   "aaa", to: model.ActionDeny,
			want: `{
  "version": 1,
  "rules": [
    {
      "id": "aaa",
      "action": "deny",
      "target": "deny",
      "event": "PostToolUse",
      "examples": []
    }
  ]
}
`,
		},
		{
			name: "target different from old action is kept",
			in:   `{"version":1,"rules":[{"id":"aaa","action":"log","target":"deny","event":"PreToolUse","examples":[]}]}`,
			id:   "aaa", to: model.ActionWarn,
			want: `{
  "version": 1,
  "rules": [
    {
      "id": "aaa",
      "action": "warn",
      "target": "deny",
      "event": "PreToolUse",
      "examples": []
    }
  ]
}
`,
		},
		{
			name: "missing action is inserted before target",
			in:   `{"version":1,"rules":[{"id":"aaa","target":"deny","event":"PreToolUse","examples":[]}]}`,
			id:   "aaa", to: model.ActionAsk,
			want: `{
  "version": 1,
  "rules": [
    {
      "id": "aaa",
      "action": "ask",
      "target": "deny",
      "event": "PreToolUse",
      "examples": []
    }
  ]
}
`,
		},
		{
			name: "missing action and target: inserted before event; default action log equals explicit target log",
			in:   `{"version":1,"rules":[{"id":"aaa","event":"PreToolUse","examples":[]}]}`,
			id:   "aaa", to: model.ActionDeny,
			want: `{
  "version": 1,
  "rules": [
    {
      "id": "aaa",
      "action": "deny",
      "event": "PreToolUse",
      "examples": []
    }
  ]
}
`,
		},
		{
			name: "implicit log action and target log: target follows",
			in:   `{"version":1,"rules":[{"id":"aaa","target":"log","event":"PreToolUse","examples":[]}]}`,
			id:   "aaa", to: model.ActionWarn,
			want: `{
  "version": 1,
  "rules": [
    {
      "id": "aaa",
      "action": "warn",
      "target": "warn",
      "event": "PreToolUse",
      "examples": []
    }
  ]
}
`,
		},
		{
			name: "no event key: appended at the end",
			in:   `{"version":1,"rules":[{"id":"aaa"}]}`,
			id:   "aaa", to: model.ActionDeny,
			want: `{
  "version": 1,
  "rules": [
    {
      "id": "aaa",
      "action": "deny"
    }
  ]
}
`,
		},
		{
			name: "only the named rule changes; key order and foreign values are kept",
			in: `{ "$schema" : "../schema/rules.v1.json", "settings":{"inject":{"anchor_every":2}},
  "version":1, "rules":[
  {"id":"first","action":"log","event":"Stop","message":"Zażółć <&> é \"q\"","examples":[{"expect":"hit","input":{"a":[1,2,{"action":"log"}]}}]},
  {"id":"second","match":{"action":"x"},"action":"log","event":"Stop","examples":[]}
]}`,
			id: "second", to: model.ActionDeny,
			want: `{
  "$schema": "../schema/rules.v1.json",
  "settings": {
    "inject": {
      "anchor_every": 2
    }
  },
  "version": 1,
  "rules": [
    {
      "id": "first",
      "action": "log",
      "event": "Stop",
      "message": "Zażółć <&> é \"q\"",
      "examples": [
        {
          "expect": "hit",
          "input": {
            "a": [
              1,
              2,
              {
                "action": "log"
              }
            ]
          }
        }
      ]
    },
    {
      "id": "second",
      "match": {
        "action": "x"
      },
      "action": "deny",
      "event": "Stop",
      "examples": []
    }
  ]
}
`,
		},
		{name: "unknown rule", in: `{"version":1,"rules":[{"id":"aaa"}]}`, id: "bbb", to: model.ActionDeny, errSub: "not found"},
		{name: "non-string action", in: `{"version":1,"rules":[{"id":"aaa","action":3}]}`, id: "aaa", to: model.ActionDeny, errSub: "non-string"},
		{name: "not an object", in: `[1]`, id: "aaa", to: model.ActionDeny, errSub: "not a JSON object"},
		{name: "rules not array", in: `{"rules":{}}`, id: "aaa", to: model.ActionDeny, errSub: "not an array"},
		{name: "truncated", in: `{"version":1,"rules":[{"id":"aaa"`, id: "aaa", to: model.ActionDeny, errSub: "parsing"},
		{name: "garbage", in: `nope`, id: "aaa", to: model.ActionDeny, errSub: "parsing"},
		{name: "rule not an object", in: `{"rules":[3]}`, id: "aaa", to: model.ActionDeny, errSub: "not an object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RewriteRuleActions([]byte(tc.in), tc.id, tc.to)
			if tc.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("err = %v, want containing %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			if !json.Valid(got) {
				t.Error("result is not valid JSON")
			}
		})
	}
}

func TestRewriteRoundTripsThroughStrictDecode(t *testing.T) {
	in := `{"version":1,"rules":[
	 {"id":"read-before-edit","action":"log","target":"deny","event":"PreToolUse","match":{"tool":["Edit","Write"]},"when":[{"not":{"read_this_session":{"path":"$target"}}}],"inject":"x","examples":[{"expect":"hit","input":{}}]},
	 {"id":"other-rule","event":"Stop","examples":[{"expect":"hit","input":{}}]}]}`
	out, err := RewriteRuleActions([]byte(in), "read-before-edit", model.ActionDeny)
	if err != nil {
		t.Fatal(err)
	}
	var c strictCompiler
	rf, diags, err := c.ParseFile(model.LayerUser, "x", out)
	if err != nil || len(diags) != 0 {
		t.Fatalf("decode: %v %v", err, diags)
	}
	if rf.Rules[0].Action != model.ActionDeny || rf.Rules[1].Action != "" {
		t.Errorf("actions: %q %q", rf.Rules[0].Action, rf.Rules[1].Action)
	}
	// Idempotent formatting: rewriting again to the same action is stable only
	// when it is stricter; rewriting a re-indented file keeps the bytes.
	again, err := RewriteRuleActions(out, "read-before-edit", model.ActionDeny)
	if err != nil || !bytes.Equal(again, out) {
		t.Errorf("not stable:\n%s\nvs\n%s", again, out)
	}
}

func FuzzRewriteRuleActions(f *testing.F) {
	seeds := []string{
		`{"version":1,"rules":[{"id":"aaa","action":"log","target":"log","event":"Stop","examples":[]}]}`,
		`{"rules":[{"id":"aaa"}]}`,
		`{"rules":[{"id":"aaa","target":"log"},{"id":"aaa"}]}`,
		`{"rules":[{"id":"a\"a","action":"log"}]}`,
		`{"x":{"rules":[]},"rules":[{"id":"aaa","nested":{"id":"aaa"}}]}`,
		`[`, ``, `{"rules":[{`, "\xef\xbb\xbf{}",
	}
	for _, s := range seeds {
		f.Add([]byte(s), "aaa")
	}
	f.Fuzz(func(t *testing.T, data []byte, id string) {
		out, err := RewriteRuleActions(data, id, model.ActionDeny)
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("invalid JSON produced from %q: %q", data, out)
		}
		var rf struct {
			Rules []struct {
				ID     string `json:"id"`
				Action string `json:"action"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(out, &rf); err != nil {
			t.Fatalf("result does not decode: %v", err)
		}
		found := false
		for _, r := range rf.Rules {
			if r.ID == id && r.Action == "deny" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("promoted rule has no deny action in %q", out)
		}
	})
}
