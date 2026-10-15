package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/libingtong/ballast/internal/model"
)

func merge(layers []LayerRules) MergeResult {
	var res MergeResult
	for _, lr := range layers {
		if n, ok := invalidNotice(lr); ok {
			res.Notices = append(res.Notices, n)
		}
	}

	owner := make(map[string]model.Layer)
	enabled := 0
	for _, lr := range layers {
		if lr.File == nil {
			continue
		}
		layer := lr.Source.Layer
		for i := range lr.File.Rules {
			r := lr.File.Rules[i]
			loaded := model.LoadedRule{Rule: r, Layer: layer, Source: lr.Source.Path, Index: i}
			if by, dup := owner[r.ID]; dup {
				res.All = append(res.All, RuleFate{Rule: loaded, Fate: FateShadowed, ShadowBy: by})
				res.Notices = append(res.Notices, model.ConfigNotice{
					Kind:    model.NoticeRuleShadowed,
					Layer:   layer,
					RuleID:  r.ID,
					Path:    lr.Source.Path,
					Message: fmt.Sprintf("rule %q in the %s layer is shadowed by the %s layer", r.ID, layer, by),
				})
				continue
			}
			owner[r.ID] = layer
			if !r.IsEnabled() {
				res.All = append(res.All, RuleFate{Rule: loaded, Fate: FateDisabled})
				continue
			}
			if enabled >= model.MaxEnabledRules {
				res.All = append(res.All, RuleFate{Rule: loaded, Fate: FateDropped})
				res.Notices = append(res.Notices, model.ConfigNotice{
					Kind:    model.NoticeRuleDropped,
					Layer:   layer,
					RuleID:  r.ID,
					Path:    lr.Source.Path,
					Message: fmt.Sprintf("%s rule %q dropped: more than %d enabled rules", layer, r.ID, model.MaxEnabledRules),
				})
				continue
			}
			enabled++
			res.All = append(res.All, RuleFate{Rule: loaded, Fate: FateActive})
			res.Rules = append(res.Rules, loaded)
		}
	}
	res.Settings = mergeSettings(layers)
	return res
}

func invalidNotice(lr LayerRules) (model.ConfigNotice, bool) {
	var tail string
	switch lr.Source.Status {
	case model.LayerLKG:
		tail = "using last-known-good rules"
	case model.LayerInvalid:
		tail = "layer ignored (no last-known-good)"
	default:
		return model.ConfigNotice{}, false
	}
	cause := lr.Source.Error
	if cause == "" {
		for _, d := range lr.Diags {
			if d.Severity == model.SevError {
				cause = d.Code + ": " + d.Message
				break
			}
		}
	}
	msg := fmt.Sprintf("%s rule file %s is invalid; %s", lr.Source.Layer, lr.Source.Path, tail)
	if cause != "" {
		msg = fmt.Sprintf("%s rule file %s is invalid (%s); %s", lr.Source.Layer, lr.Source.Path, cause, tail)
	}
	return model.ConfigNotice{Kind: model.NoticeConfigInvalid, Layer: lr.Source.Layer, Path: lr.Source.Path, Message: msg}, true
}

// mergeSettings applies "first layer that sets a key wins" per key.
func mergeSettings(layers []LayerRules) model.Settings {
	var out model.Settings
	ensure := func() *model.InjectSettings {
		if out.Inject == nil {
			out.Inject = &model.InjectSettings{}
		}
		return out.Inject
	}
	for _, lr := range layers {
		if lr.File == nil || lr.File.Settings == nil || lr.File.Settings.Inject == nil {
			continue
		}
		in := lr.File.Settings.Inject
		if in.AnchorEvery != nil && (out.Inject == nil || out.Inject.AnchorEvery == nil) {
			v := *in.AnchorEvery
			ensure().AnchorEvery = &v
		}
		if in.BatchAfterTools != nil && (out.Inject == nil || out.Inject.BatchAfterTools == nil) {
			v := *in.BatchAfterTools
			ensure().BatchAfterTools = &v
		}
	}
	return out
}

func fateNote(f RuleFate) string {
	switch f.Fate {
	case FateDisabled:
		return "enabled=false"
	case FateShadowed:
		return fmt.Sprintf("shadowed by the %s layer", f.ShadowBy)
	case FateDropped:
		return fmt.Sprintf("more than %d enabled rules", model.MaxEnabledRules)
	}
	return ""
}

func generation(rules []model.Rule, settings model.Settings) string {
	norm := make([]model.Rule, len(rules))
	for i, r := range rules {
		en := r.IsEnabled()
		r.Enabled = &en
		r.Action = r.EffectiveAction()
		if r.Target == model.ActionNone {
			r.Target = model.DefaultTarget(r.Event)
		}
		r.Examples = nil
		r.Description = ""
		norm[i] = r
	}
	b, err := json.Marshal(struct {
		Rules    []model.Rule   `json:"rules"`
		Settings model.Settings `json:"settings"`
	}{norm, settings})
	if err != nil {
		// Rule and Settings contain only encodable types; keep a stable value.
		return "0000000000000000"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
