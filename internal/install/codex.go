package install

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// features.hooks flag (contract v1.8): Value = the switch as install found
// it, so uninstall can tell "never written" from "written false".
//
//	"absent"  no features.hooks key        SetByBallast=true, stays on (documented exception);
//	                                       becomes "false" if the user later sets false and reinstalls
//	"false"   an explicit false            SetByBallast=true, restored to false on uninstall
//	"true"    already on                   SetByBallast=false, untouched
//
// A manifest from before v1.8 holds Value "true" with SetByBallast=true; it
// reads as "absent" (the old behaviour: left enabled).
const (
	flagFeaturesHooks = "features.hooks"
	featuresAbsent    = "absent"
	featuresFalse     = "false"
	featuresTrue      = "true"
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

func hasPathPrefix(p, prefix []string) bool {
	return len(p) >= len(prefix) && pathEq(p[:len(prefix)], prefix)
}

func joinPath(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}

// featuresLoc locates features.hooks. idx is the line holding the value; hdr
// the single [features] header; dotEnd the line after the last root-table
// dotted "features.*" key (where "features.hooks" can be added without a
// header); featuresScan rejects layouts that cannot be edited without producing
// invalid TOML.
type featuresLoc struct {
	idx, hdr, dotEnd int
}

func (d *tomlDoc) featuresScan() (featuresLoc, error) {
	loc := featuresLoc{idx: -1, hdr: -1, dotEnd: -1}
	feat := []string{"features"}
	fh := []string{"features", "hooks"}
	var cur []string
	nHdr, lastDot := 0, -1
	for i, l := range d.lines {
		switch l.kind {
		case tlHeader:
			cur = l.path
			switch {
			case l.arr && (hasPathPrefix(fh, l.path) || hasPathPrefix(l.path, fh)):
				return loc, fmt.Errorf("%w: features is an array of tables", errTOMLSyntax)
			case l.arr:
			case hasPathPrefix(l.path, fh):
				return loc, fmt.Errorf("%w: features.hooks is a table", errTOMLSyntax)
			case pathEq(l.path, feat):
				nHdr++
				loc.hdr = i
			}
		case tlKV:
			abs := joinPath(cur, l.path)
			switch {
			case pathEq(abs, fh):
				if loc.idx >= 0 {
					return loc, fmt.Errorf("%w: features.hooks is defined twice", errTOMLSyntax)
				}
				loc.idx = i
			case hasPathPrefix(abs, fh):
				return loc, fmt.Errorf("%w: features.hooks is a table", errTOMLSyntax)
			case pathEq(abs, feat):
				return loc, fmt.Errorf("%w: features is an inline table, which the installer does not edit", errTOMLSyntax)
			}
			if len(cur) == 0 && len(abs) >= 2 && abs[0] == "features" {
				lastDot = i
			}
		}
	}
	if nHdr > 1 {
		return loc, fmt.Errorf("%w: duplicate [features] table", errTOMLSyntax)
	}
	if nHdr == 1 && lastDot >= 0 {
		return loc, fmt.Errorf("%w: features is defined both by a [features] table and by dotted keys", errTOMLSyntax)
	}
	if lastDot >= 0 {
		end := lastDot + 1
		for end < len(d.lines) && d.lines[end].kind == tlCont {
			end++
		}
		loc.dotEnd = end
	}
	return loc, nil
}

func (d *tomlDoc) featuresHooks() (idx, hdr int, val string, err error) {
	loc, err := d.featuresScan()
	if err != nil {
		return 0, 0, "", err
	}
	if loc.idx >= 0 {
		val = d.value(loc.idx)
	}
	return loc.idx, loc.hdr, val, nil
}

// ensureFeaturesHooks turns features.hooks on and reports whether it had to
// edit the document.
func (d *tomlDoc) ensureFeaturesHooks() (changed bool, err error) {
	loc, err := d.featuresScan()
	if err != nil {
		return false, err
	}
	var val string
	if loc.idx >= 0 {
		val = d.value(loc.idx)
	}
	switch {
	case loc.idx >= 0 && val == "true":
		return false, nil
	case loc.idx >= 0 && val == "false":
		d.setValue(loc.idx, "true")
	case loc.idx >= 0:
		return false, fmt.Errorf("%w: features.hooks has an unexpected value %s", errTOMLSyntax, val)
	case loc.hdr >= 0:
		d.insertLines(loc.hdr+1, "hooks = true")
	case loc.dotEnd >= 0:
		d.insertLines(loc.dotEnd, "features.hooks = true")
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

// trustConflict reports layouts in which appending a [hooks.state.'k'] header
// would redefine a table (TOML forbids re-declaring a table that dotted keys,
// inline tables or values already define), so the file is refused instead.
func (d *tomlDoc) trustConflict(k string) error {
	tp := trustTablePath(k)
	var cur []string
	for _, l := range d.lines {
		switch l.kind {
		case tlHeader:
			cur = l.path
			if l.arr && (hasPathPrefix(tp, l.path) || hasPathPrefix(l.path, tp)) {
				return fmt.Errorf("%w: hooks.state is an array of tables", errTOMLSyntax)
			}
		case tlKV:
			if len(cur) >= len(tp) {
				continue
			}
			abs := joinPath(cur, l.path)
			if hasPathPrefix(abs, tp) || hasPathPrefix(tp, abs) {
				return fmt.Errorf("%w: hooks.state.%s is defined by dotted or inline keys, which the installer does not edit", errTOMLSyntax, tomlString(k))
			}
		}
	}
	return nil
}

// setTrust writes (or updates in place) one trust table.
func (d *tomlDoc) setTrust(k, hash string) error {
	if err := d.trustConflict(k); err != nil {
		return err
	}
	tbls := d.tables(trustTablePath(k))
	if len(tbls) == 0 {
		d.appendBlock("[hooks.state."+tomlString(k)+"]", "enabled = true", `trusted_hash = "`+hash+`"`)
		return nil
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
	return nil
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
	want := map[string]string{}
	for _, ne := range events {
		h, _, _ := t.hooks.handler(spec.Binary, ne, goos)
		want[ne.Native] = h.hash()
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
			if l.groupIdx == g && l.handleIdx == h && l.node.hash() == e.Hash {
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

	var prevFeature *model.ManagedFlag
	var prevTrust []model.ManagedFlag
	for _, fl := range flagsFor(m, model.ClientCodex, t.path) {
		switch {
		case fl.Key == flagFeaturesHooks:
			fl := fl
			prevFeature = &fl
		case strings.HasPrefix(fl.Key, trustKeyPrefix):
			prevTrust = append(prevTrust, fl)
		}
	}

	_, _, curVal, err := doc.featuresHooks()
	if err != nil {
		return nil, t.parseErr(err)
	}
	if _, err := doc.ensureFeaturesHooks(); err != nil {
		return nil, t.parseErr(err)
	}
	if curVal == "false" {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s: features.hooks was explicitly false; switching it to true so the Ballast hooks run. Other hooks in hooks.json now run too. Uninstall restores false",
			t.path))
	}
	orig := featuresTrue
	switch {
	case prevFeature != nil && prevFeature.SetByBallast:
		// An earlier install changed the switch: its record is the original,
		// unless the user has since turned it off again by hand. That latest
		// explicit false wins, so the warning's "Uninstall restores false"
		// stays true even when the first install found the key absent.
		orig = featuresAbsent
		if prevFeature.Value == featuresFalse || curVal == "false" {
			orig = featuresFalse
		}
	case curVal == "false":
		orig = featuresFalse
	case curVal == "":
		orig = featuresAbsent
	}
	res.Flags = append(res.Flags, model.ManagedFlag{
		Client: model.ClientCodex, File: t.path, Key: flagFeaturesHooks, Value: orig,
		SetByBallast: orig != featuresTrue,
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
				if err := doc.setTrust(k, want[k]); err != nil {
					return nil, t.parseErr(err)
				}
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
// removed. features.hooks goes back to an explicit false when that is what
// install found; when the key was absent (or already true) it is left on.
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
		if fl.Key == flagFeaturesHooks && fl.SetByBallast && fl.Value == featuresFalse {
			// Only while the switch is still the "true" install wrote.
			if idx, _, val, err := doc.featuresHooks(); err == nil && idx >= 0 && val == "true" {
				doc.setValue(idx, "false")
			}
			continue
		}
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
