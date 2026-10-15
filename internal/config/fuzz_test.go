package config_test

import (
	"testing"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/model"
)

// Whatever bytes a rule file holds, Load must succeed (bad files degrade to
// LKG/empty with a notice) and the result must satisfy the merge invariants.
func FuzzLoadLayerBytes(f *testing.F) {
	for _, seed := range []string{
		"", "{}", "[]", "null", "\x00\xff\xfe", "{\"version\":1}",
		rulesJSON("", on("aaa-rule")),
		rulesJSON(`{"inject":{"anchor_every":-3}}`, on("aaa-rule"), off("bbb-rule")),
		`{"version":1,"rules":[{"id":"aaa-rule","event":"PreToolUse","inject":"text"}]}`,
		`{"version":1,"rules":[{"id":"aaa-rule","event":"PreToolUse"},{"id":"aaa-rule","event":"Stop"}]}`,
		`{"version":1,"unknown":true,"rules":[]}`,
		"{\"version\":1,\"rules\":[{\"id\":\"aaa-rule\",\"event\":\"PreToolUse\"",
	} {
		f.Add(seed, seed)
	}
	f.Fuzz(func(t *testing.T, repoBytes, userBytes string) {
		e := newEnv(t)
		e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
		e.write(model.LayerRepo, repoBytes)
		e.write(model.LayerUser, userBytes)
		for i := 0; i < 2; i++ { // second pass exercises the cache
			rs, err := e.loader().Load(e.req())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(rs.Rules) > model.MaxEnabledRules || len(rs.Sources) != 3 {
				t.Fatalf("rules=%d sources=%d", len(rs.Rules), len(rs.Sources))
			}
			seen := map[string]bool{}
			for _, r := range rs.Rules {
				if seen[r.Rule.ID] {
					t.Fatalf("duplicate active id %s", r.Rule.ID)
				}
				seen[r.Rule.ID] = true
			}
			if !seen["org-rule"] {
				t.Fatal("org rule lost")
			}
			var merged []model.Rule
			for _, r := range rs.Rules {
				merged = append(merged, r.Rule)
			}
			if rs.Generation != config.Generation(merged, rs.Settings) {
				t.Fatal("generation does not match merged rules")
			}
		}
		if _, err := e.loader().Explain(e.req()); err != nil {
			t.Fatalf("Explain: %v", err)
		}
	})
}
