package install

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

const (
	flagFeaturesHooks = "features.hooks"
	trustKeyPrefix    = "hooks.state."
)

// codexConfigTarget edits <CODEX_HOME>/config.toml: it makes sure
// [features] hooks = true and, only with --codex-trust, writes the trust
// state of the hooks that the sibling hooks.json target planned.
type codexConfigTarget struct {
	path  string
	hooks *jsonHooksTarget
	goos  string
}

func (t *codexConfigTarget) Path() string   { return t.path }
func (t *codexConfigTarget) Format() Format { return FormatTOML }

func (t *codexConfigTarget) parseErr(err error) error {
	return fmt.Errorf("%w: %s: %v", model.ErrSettingsParse, t.path, err)
}

// Plan implements Target. Trust entries need the final positions of the
// hooks.json entries, which only Install knows (it calls planWith with the
// sibling plan); a stand-alone Plan therefore never adds trust entries.
func (t *codexConfigTarget) Plan(cur []byte, spec EntrySpec, m *model.InstallManifest) (*PlanResult, error) {
	return t.planWith(cur, spec, m, nil)
}

// peerPlanner is implemented by targets whose plan depends on the plans of
// earlier targets of the same client.
type peerPlanner interface {
	planWith(cur []byte, spec EntrySpec, m *model.InstallManifest, done []*PlanResult) (*PlanResult, error)
}

func (d *tomlDoc) featuresHooks() (idx, hdr int, val string, err error) {
	idx, hdr = -1, -1
	re := d.rootEnd()
	if i, ok := d.kvIn(0, re, []string{"features", "hooks"}); ok {
		idx = i
	}
	if _, ok := d.kvIn(0, re, []string{"features"}); ok {
		return 0, 0, "", fmt.Errorf("%w: features is an inline table, which the installer does not edit", errTOMLSyntax)
	}
	tbls := d.tables([]string{"features"})
	if len(tbls) > 1 {
		return 0, 0, "", fmt.Errorf("%w: duplicate [features] table", errTOMLSyntax)
	}
	if len(tbls) == 1 {
		hdr = tbls[0]
		if idx < 0 {
			if i, ok := d.kvIn(hdr+1, d.tableEnd(hdr), []string{"hooks"}); ok {
				idx = i
			}
		}
	}
	if idx >= 0 {
		val = d.value(idx)
	}
	return idx, hdr, val, nil
}

// ensureFeaturesHooks turns features.hooks on and reports whether it had to
// edit the document.
func (d *tomlDoc) ensureFeaturesHooks() (changed bool, err error) {
	idx, hdr, val, err := d.featuresHooks()
	if err != nil {
		return false, err
	}
	switch {
	case idx >= 0 && val == "true":
		return false, nil
	case idx >= 0 && val == "false":
		d.setValue(idx, "true")
	case idx >= 0:
		return false, fmt.Errorf("%w: features.hooks has an unexpected value %s", errTOMLSyntax, val)
	case hdr >= 0:
		d.insertLines(hdr+1, "hooks = true")
	default:
		d.appendBlock("[features]", "hooks = true")
	}
	return true, nil
}

func trustFlagKey(k string) string { return trustKeyPrefix + "'" + k + "'" }

// parseTrustFlagKey extracts k from "hooks.state.'<k>'".
func parseTrustFlagKey(key string) (string, bool) {
	if !strings.HasPrefix(key, trustKeyPrefix+"'") || !strings.HasSuffix(key, "'") {
		return "", false
	}
	return key[len(trustKeyPrefix)+1 : len(key)-1], true
}

func trustTablePath(k string) []string { return []string{"hooks", "state", k} }

// setTrust writes (or updates in place) one trust table.
func (d *tomlDoc) setTrust(k, hash string) {
	tbls := d.tables(trustTablePath(k))
	if len(tbls) == 0 {
		d.appendBlock("[hooks.state."+tomlString(k)+"]", "enabled = true", `trusted_hash = "`+hash+`"`)
		return
	}
	hdr := tbls[0]
	want := map[string]string{"enabled": "true", "trusted_hash": `"` + hash + `"`}
	for _, key := range []string{"enabled", "trusted_hash"} {
		end := d.tableEnd(hdr)
		if i, ok := d.kvIn(hdr+1, end, []string{key}); ok {
			if d.value(i) != want[key] {
				d.setValue(i, want[key])
			}
			continue
		}
		d.insertLines(hdr+1, key+" = "+want[key])
	}
}

// trustHashOf reads trusted_hash of a trust table ("" when absent).
func (d *tomlDoc) trustHashOf(k string) (hdr int, hash string, found bool) {
	tbls := d.tables(trustTablePath(k))
	if len(tbls) == 0 {
		return -1, "", false
	}
	hdr = tbls[0]
	if i, ok := d.kvIn(hdr+1, d.tableEnd(hdr), []string{"trusted_hash"}); ok {
		hash, _ = unquoteTOMLValue(d.value(i))
	}
	return hdr, hash, true
}

// dropTrust removes our two keys from a trust table, and the whole table
// when nothing else is left in it.
func (d *tomlDoc) dropTrust(hdr int) {
	end := d.tableEnd(hdr)
	foreign := false
	for i := hdr + 1; i < end; i++ {
		l := d.lines[i]
		if l.kind == tlBlank && strings.TrimSpace(l.text) == "" {
			continue
		}
		if l.kind == tlKV && (pathEq(l.path, []string{"enabled"}) || pathEq(l.path, []string{"trusted_hash"})) {
			continue
		}
		foreign = true
	}
	if !foreign {
		d.removeTable(hdr)
		return
	}
	for i := end - 1; i > hdr; i-- {
		l := d.lines[i]
		if l.kind == tlKV && (pathEq(l.path, []string{"enabled"}) || pathEq(l.path, []string{"trusted_hash"})) {
			d.removeRange(i, i+1)
		}
	}
}

// desiredTrust computes key -> trusted hash for the entries that the sibling
// hooks.json plan owns after this install.
func (t *codexConfigTarget) desiredTrust(spec EntrySpec, goos string, peer *PlanResult) map[string]string {
	out := map[string]string{}
	if peer == nil {
		return out
	}
	events := spec.Events
	if len(events) == 0 {
		events = defaultEvents(model.ClientCodex)
	}
	f := jfmt{unit: "  ", eol: "\n"}
	want := map[string]string{}
	for _, ne := range events {
		h, _, _ := t.hooks.handler(spec.Binary, ne, goos)
		want[ne.Native] = h.hash(f)
	}
	doc, err := t.hooks.parse(peer.After)
	if err != nil {
		return out
	}
	hooks, err := t.hooks.hooksObject(doc.root)
	if err != nil || hooks == nil {
		return out
	}
	for _, e := range peer.Entries {
		if e.Client != model.ClientCodex || e.File != t.hooks.path || want[e.Native] != e.Hash {
			continue
		}
		i := strings.IndexByte(e.Key, '[')
		var g, h int
		if i < 0 {
			continue
		}
		if _, err := fmt.Sscanf(e.Key[i:], "[%d].hooks[%d]", &g, &h); err != nil {
			continue
		}
		// only a hook that is really at that position with that content is trusted
		list, err := t.hooks.eventList(hooks, e.Native)
		if err != nil {
			continue
		}
		present := false
		t.hooks.eachHandler(list, func(l located) bool {
			if l.groupIdx == g && l.handleIdx == h && l.node.hash(f) == e.Hash {
				present = true
				return false
			}
			return true
		})
		if !present {
			continue
		}
		k := fmt.Sprintf("%s:%s:%d:%d", t.hooks.path, snakeEvent(e.Native), g, h)
		out[k] = CodexTrustHash(snakeEvent(e.Native), e.Command, codexHookTimeout)
	}
	return out
}

func (t *codexConfigTarget) planWith(cur []byte, spec EntrySpec, m *model.InstallManifest, done []*PlanResult) (*PlanResult, error) {
	m = nonNilManifest(m)
	goos := firstNonEmpty(spec.GOOS, t.goos, runtime.GOOS)
	doc, err := parseTOML(cur)
	if err != nil {
		return nil, t.parseErr(err)
	}
	res := &PlanResult{Path: t.path, Format: FormatTOML, Before: cur, Created: cur == nil}
	if prior := fileRec(m, t.path); prior != nil && prior.Created {
		res.Created = true
	}

	prevFeature := false
	var prevTrust []model.ManagedFlag
	for _, fl := range flagsFor(m, model.ClientCodex, t.path) {
		switch {
		case fl.Key == flagFeaturesHooks:
			prevFeature = fl.SetByBallast
		case strings.HasPrefix(fl.Key, trustKeyPrefix):
			prevTrust = append(prevTrust, fl)
		}
	}

	changed, err := doc.ensureFeaturesHooks()
	if err != nil {
		return nil, t.parseErr(err)
	}
	res.Flags = append(res.Flags, model.ManagedFlag{
		Client: model.ClientCodex, File: t.path, Key: flagFeaturesHooks, Value: "true",
		SetByBallast: changed || prevFeature,
	})

	if !spec.CodexTrust {
		res.Flags = append(res.Flags, prevTrust...)
	} else {
		var peer *PlanResult
		for _, p := range done {
			if p != nil && p.Path == t.hooks.path {
				peer = p
			}
		}
		if peer == nil {
			res.Warnings = append(res.Warnings, "codex trust requested but the hooks.json plan is unavailable; no trust entry written")
			res.Flags = append(res.Flags, prevTrust...)
		} else {
			want := t.desiredTrust(spec, goos, peer)
			for _, fl := range prevTrust {
				k, ok := parseTrustFlagKey(fl.Key)
				if !ok {
					continue
				}
				if _, keep := want[k]; keep {
					continue
				}
				if hdr, h, found := doc.trustHashOf(k); found {
					if h == fl.Value {
						doc.dropTrust(hdr)
					} else {
						res.Warnings = append(res.Warnings, fmt.Sprintf("%s: trust entry %s was changed by the user; kept", t.path, k))
					}
				}
			}
			for _, k := range sortedKeys(want) {
				doc.setTrust(k, want[k])
				res.Flags = append(res.Flags, model.ManagedFlag{
					Client: model.ClientCodex, File: t.path, Key: trustFlagKey(k), Value: want[k], SetByBallast: true,
				})
			}
		}
	}

	res.After = doc.bytes()
	res.Files = []model.ManagedFile{{Client: model.ClientCodex, Path: t.path, Hash: sha256Hex(res.After), Created: res.Created}}
	return res, nil
}

// Remove implements Target: trust entries recorded in the manifest are
// removed, features.hooks is left on.
func (t *codexConfigTarget) Remove(cur []byte, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	res := &PlanResult{Path: t.path, Format: FormatTOML, Before: cur}
	if cur == nil {
		return res, nil
	}
	doc, err := parseTOML(cur)
	if err != nil {
		return nil, t.parseErr(err)
	}
	for _, fl := range flagsFor(m, model.ClientCodex, t.path) {
		k, ok := parseTrustFlagKey(fl.Key)
		if !ok {
			continue
		}
		hdr, h, found := doc.trustHashOf(k)
		switch {
		case !found:
		case h == fl.Value:
			doc.dropTrust(hdr)
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: trust entry %s was changed by the user; kept", t.path, k))
			res.Flags = append(res.Flags, fl)
		}
	}
	if len(res.Flags) > 0 {
		if prior := fileRec(m, t.path); prior != nil {
			res.Files = []model.ManagedFile{*prior}
		}
	}
	res.After = doc.bytes()
	return res, nil
}
