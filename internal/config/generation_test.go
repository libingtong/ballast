package config_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/model"
)

func rule(id string) model.Rule {
	return model.Rule{ID: id, Event: model.EventPreToolUse, Action: model.ActionDeny, Message: "m " + id}
}

func TestGenerationFormat(t *testing.T) {
	g := config.Generation([]model.Rule{rule("aaa-rule")}, model.Settings{})
	if len(g) != 16 {
		t.Fatalf("generation %q is not 16 chars", g)
	}
	if _, err := hex.DecodeString(g); err != nil {
		t.Errorf("generation %q is not hex", g)
	}
	// The canonical encoding is part of the contract: recompute it by hand.
	canon := `{"rules":[{"id":"aaa-rule","enabled":true,"action":"deny","target":"deny","event":"PreToolUse","message":"m aaa-rule","examples":null}],"settings":{}}`
	sum := sha256.Sum256([]byte(canon))
	if want := hex.EncodeToString(sum[:])[:16]; g != want {
		t.Errorf("generation = %s, want %s (canonical JSON %s)", g, want, canon)
	}
	empty := config.Generation(nil, model.Settings{})
	sum = sha256.Sum256([]byte(`{"rules":[],"settings":{}}`))
	if want := hex.EncodeToString(sum[:])[:16]; empty != want {
		t.Errorf("empty generation = %s, want %s", empty, want)
	}
}

func TestGenerationIgnoresNonBehaviour(t *testing.T) {
	tr := true
	base := rule("aaa-rule")
	base.Action = "" // default log
	base.Target = "" // default per event
	explicit := base
	explicit.Enabled = &tr
	explicit.Action = model.ActionLog
	explicit.Target = model.DefaultTarget(model.EventPreToolUse)
	explicit.Description = "described"
	explicit.Examples = []model.Example{{Name: "x", Expect: model.ExpectHit}}

	a := config.Generation([]model.Rule{base}, model.Settings{})
	b := config.Generation([]model.Rule{explicit}, model.Settings{})
	if a != b {
		t.Errorf("defaults/examples/description changed generation: %s vs %s", a, b)
	}
}

func TestGenerationSensitivity(t *testing.T) {
	one := 1
	base := []model.Rule{rule("aaa-rule"), rule("bbb-rule")}
	g0 := config.Generation(base, model.Settings{})
	fl := false
	variants := map[string]func() string{
		"message": func() string {
			r := append([]model.Rule(nil), base...)
			r[0].Message = "other"
			return config.Generation(r, model.Settings{})
		},
		"action": func() string {
			r := append([]model.Rule(nil), base...)
			r[0].Action = model.ActionWarn
			return config.Generation(r, model.Settings{})
		},
		"target": func() string {
			r := append([]model.Rule(nil), base...)
			r[0].Target = model.ActionWarn
			return config.Generation(r, model.Settings{})
		},
		"enabled": func() string {
			r := append([]model.Rule(nil), base...)
			r[0].Enabled = &fl
			return config.Generation(r, model.Settings{})
		},
		"order":   func() string { return config.Generation([]model.Rule{base[1], base[0]}, model.Settings{}) },
		"dropped": func() string { return config.Generation(base[:1], model.Settings{}) },
		"inject": func() string {
			r := append([]model.Rule(nil), base...)
			r[0].Inject = &model.Inject{Text: "t"}
			return config.Generation(r, model.Settings{})
		},
		"settings": func() string {
			return config.Generation(base, model.Settings{Inject: &model.InjectSettings{AnchorEvery: &one}})
		},
	}
	seen := map[string]string{g0: "base"}
	for name, f := range variants {
		g := f()
		if prev, dup := seen[g]; dup {
			t.Errorf("%s collides with %s (%s)", name, prev, g)
		}
		seen[g] = name
	}
	if again := config.Generation(base, model.Settings{}); again != g0 {
		t.Errorf("not deterministic: %s vs %s", again, g0)
	}
}

func TestGenerationDoesNotMutateInput(t *testing.T) {
	r := []model.Rule{rule("aaa-rule")}
	r[0].Action = ""
	config.Generation(r, model.Settings{})
	if r[0].Action != "" || r[0].Enabled != nil || r[0].Target != "" {
		t.Errorf("input mutated: %+v", r[0])
	}
}
