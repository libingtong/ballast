package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// hermesTarget edits the top-level hooks: block of <HERMES_HOME>/config.yaml.
type hermesTarget struct {
	path string
	goos string
}

func (t *hermesTarget) Path() string   { return t.path }
func (t *hermesTarget) Format() Format { return FormatYAML }

func (t *hermesTarget) parseErr(err error) error {
	return fmt.Errorf("%w: %s: %v", model.ErrSettingsParse, t.path, err)
}

func hermesItemHash(cmd string) string {
	raw, _ := json.Marshal(map[string]any{"command": cmd, "timeout": json.Number(fmt.Sprint(hermesHookTimeout))})
	h, _ := EntryHash(raw)
	return h
}

func hermesItemLines(indent int, cmd string) []string {
	pad := strings.Repeat(" ", indent)
	return []string{
		pad + "- command: " + string(marshalNoEscape(cmd)),
		pad + "  timeout: " + fmt.Sprint(hermesHookTimeout),
	}
}

func (d *yamlDoc) ensureEOL(i int) {
	if i >= 0 && i < len(d.lines) && d.lines[i].eol == "" {
		d.lines[i].eol = d.eol
	}
}

func (t *hermesTarget) isBallast(it yItem, binaries map[string]bool, goos string) bool {
	return !it.opaque && it.command != "" && commandHasPrefix(it.command, binaries, model.ClientHermes, goos)
}

// Plan implements Target.
func (t *hermesTarget) Plan(cur []byte, spec EntrySpec, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	goos := firstNonEmpty(spec.GOOS, t.goos, runtime.GOOS)
	doc := parseYAMLDoc(cur)
	orig, err := doc.section()
	if err != nil {
		return nil, t.parseErr(err)
	}
	res := &PlanResult{Path: t.path, Format: FormatYAML, Before: cur, Created: cur == nil}
	if prior := fileRec(m, t.path); prior != nil && prior.Created {
		res.Created = true
	}
	events := spec.Events
	if len(events) == 0 {
		events = defaultEvents(model.ClientHermes)
	}
	recs := entriesFor(m, model.ClientHermes, t.path)
	byHash := map[string]model.ManagedEntry{}
	for _, r := range recs {
		byHash[r.Hash] = r
	}
	binaries := ownerBinaries(model.ClientHermes, goos, spec.Binary, m, recs)

	// Structural flags (v1.3): remember whether the hooks: header and each
	// event key existed, and their original text, so uninstall restores it.
	sf := structFlags(m, model.ClientHermes, t.path)
	hdrText := ""
	if orig.hdr >= 0 {
		hdrText = doc.lines[orig.hdr].text
	}
	noteStruct(sf, model.ClientHermes, t.path, flagYAMLHooks, orig.hdr >= 0, len(recs) > 0, hdrText)
	t.noteFileShape(sf, cur, doc)
	for _, ne := range events {
		e := orig.event(ne.Native)
		text := ""
		if e != nil {
			text = doc.lines[e.keyLine].text
		}
		owned := false
		for _, r := range recs {
			owned = owned || r.Native == ne.Native
		}
		noteStruct(sf, model.ClientHermes, t.path, flagYAMLEventPrefix+ne.Native, e != nil, owned, text)
	}

	processed := map[string]bool{}
	for _, ne := range events {
		processed[ne.Native] = true
		sec, err := doc.section()
		if err != nil {
			return nil, t.parseErr(err)
		}
		cmd := QuoteCommand(HookArgv(spec.Binary, model.ClientHermes, ne.Native), goos)
		wantHash := hermesItemHash(cmd)
		entry := model.ManagedEntry{Client: model.ClientHermes, File: t.path, Native: ne.Native, Hash: wantHash, Command: cmd}

		ev := sec.event(ne.Native)
		var owned *yItem
		foreign := 0
		if ev != nil {
			for i := range ev.items {
				it := ev.items[i]
				if !t.isBallast(it, binaries, goos) {
					continue
				}
				if r, ok := byHash[it.hash()]; ok && r.Native == ne.Native && owned == nil {
					owned = &ev.items[i]
				} else {
					foreign++
				}
			}
		}
		switch {
		case owned != nil:
			if owned.hash() != wantHash {
				doc.remove(owned.start, owned.end+1)
				doc.insert(owned.start, hermesItemLines(ev.itemInd, cmd)...)
			}
			res.Entries = append(res.Entries, entry)
		case foreign > 0:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: an existing hermes entry for %q was modified by the user; left in place and no duplicate added",
				t.path, ne.Native))
			for _, r := range recs {
				if r.Native == ne.Native {
					res.Entries = append(res.Entries, r)
				}
			}
		default:
			t.addItem(doc, sec, ev, ne.Native, cmd)
			res.Entries = append(res.Entries, entry)
		}
	}
	for _, r := range recs {
		if !processed[r.Native] {
			res.Entries = append(res.Entries, r)
		}
	}

	// Final pass: record the positions.
	sec, err := doc.section()
	if err != nil {
		return nil, t.parseErr(err)
	}
	for i := range res.Entries {
		e := &res.Entries[i]
		if e.Key != "" {
			continue
		}
		if ev := sec.event(e.Native); ev != nil {
			for k := range ev.items {
				if ev.items[k].hash() == e.Hash {
					e.Key = fmt.Sprintf("hooks.%s[%d]", e.Native, k)
					break
				}
			}
		}
	}
	res.After = doc.bytes()
	res.Files = []model.ManagedFile{{Client: model.ClientHermes, Path: t.path, Hash: sha256Hex(res.After), Created: res.Created}}
	res.Flags = flagList(sf)
	return res, nil
}

// noteFileShape records what the file looked like when install first
// touched it: only whitespace (flagYAMLBlank, usually 0 bytes), or a last
// line without a line terminator (flagYAMLNoEOL). It reads doc before any
// edit; a flag recorded by an earlier install is kept.
func (t *hermesTarget) noteFileShape(sf map[string]model.ManagedFlag, cur []byte, doc *yamlDoc) {
	if cur == nil {
		return // install creates the file; nothing to give back
	}
	if len(bytes.TrimSpace(bytes.TrimPrefix(cur, []byte("\xef\xbb\xbf")))) == 0 {
		if _, ok := sf[flagYAMLBlank]; !ok {
			sf[flagYAMLBlank] = model.ManagedFlag{Client: model.ClientHermes, File: t.path, Key: flagYAMLBlank, Value: string(cur)}
		}
		return
	}
	if n := len(doc.lines); n > 0 && doc.lines[n-1].eol == "" {
		if _, ok := sf[flagYAMLNoEOL]; !ok {
			sf[flagYAMLNoEOL] = model.ManagedFlag{Client: model.ClientHermes, File: t.path, Key: flagYAMLNoEOL, Value: doc.lines[n-1].text}
		}
	}
}

func (t *hermesTarget) addItem(doc *yamlDoc, sec *ySection, ev *yEvent, native, cmd string) {
	if sec.hdr < 0 {
		block := []string{"hooks:", "  " + native + ":"}
		block = append(block, hermesItemLines(4, cmd)...)
		doc.appendBlock(block...)
		return
	}
	if sec.inlineEmpty {
		doc.lines[sec.hdr].text = "hooks:"
	}
	ki := sec.keyIndent
	if ki < 0 {
		ki = 2
	}
	if ev == nil {
		at := sec.last + 1
		doc.ensureEOL(sec.last)
		lines := []string{strings.Repeat(" ", ki) + native + ":"}
		lines = append(lines, hermesItemLines(ki+2, cmd)...)
		doc.insert(at, lines...)
		return
	}
	if ev.emptyFlow {
		doc.lines[ev.keyLine].text = strings.Repeat(" ", ev.indent) + ev.name + ":"
	}
	ii := ev.itemInd
	if ii < 0 {
		ii = ev.indent + 2
	}
	after := ev.insertAfter()
	doc.ensureEOL(after)
	doc.insert(after+1, hermesItemLines(ii, cmd)...)
}

// Remove implements Target.
func (t *hermesTarget) Remove(cur []byte, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	res := &PlanResult{Path: t.path, Format: FormatYAML, Before: cur}
	if cur == nil {
		return res, nil
	}
	goos := firstNonEmpty(t.goos, runtime.GOOS)
	doc := parseYAMLDoc(cur)
	if _, err := doc.section(); err != nil {
		return nil, t.parseErr(err)
	}
	recs := entriesFor(m, model.ClientHermes, t.path)
	prior := fileRec(m, t.path)
	binaries := ownerBinaries(model.ClientHermes, goos, "", m, recs)
	sf := structFlags(m, model.ClientHermes, t.path)
	removed := false
	for _, r := range recs {
		sec, err := doc.section()
		if err != nil {
			return nil, t.parseErr(err)
		}
		ev := sec.event(r.Native)
		hit, foreign := -1, false
		if ev != nil {
			for i, it := range ev.items {
				if hit < 0 && it.hash() == r.Hash && !it.opaque {
					hit = i
				} else if t.isBallast(it, binaries, goos) {
					foreign = true
				}
			}
		}
		switch {
		case hit >= 0:
			it := ev.items[hit]
			doc.remove(it.start, it.end+1)
			removed = true
			if sec2, err := doc.section(); err == nil {
				if e2 := sec2.event(r.Native); e2 != nil && len(e2.items) == 0 && !e2.emptyFlow {
					key := flagYAMLEventPrefix + r.Native
					if createdByBallast(sf, key) {
						doc.remove(e2.keyLine, e2.keyLine+1)
					} else {
						doc.lines[e2.keyLine].text = sf[key].Value // e.g. "  pre_tool_call: []"
					}
				}
			}
		case foreign:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: hermes entry for %q was modified by the user; kept", t.path, r.Native))
			res.Entries = append(res.Entries, r)
		}
	}
	if removed {
		if sec, err := doc.section(); err == nil && sec.hdr >= 0 && !sec.hasContent && !sec.hasComment {
			if createdByBallast(sf, flagYAMLHooks) {
				from := sec.hdr
				if from > 0 && doc.blank(from-1) {
					from--
				}
				doc.remove(from, sec.last+1)
			} else {
				// The user's header (with its comment or flow value) comes back.
				doc.lines[sec.hdr].text = sf[flagYAMLHooks].Value
			}
		}
	}
	if len(res.Entries) > 0 && prior != nil {
		res.Files = []model.ManagedFile{*prior}
		res.Flags = flagList(sf) // still needed by a later uninstall
	}
	if prior != nil && prior.Created && doc.isBlankDoc() {
		return res, nil // After == nil: delete
	}
	if f, ok := sf[flagYAMLBlank]; ok && len(res.Entries) == 0 && doc.isBlankDoc() {
		res.After = []byte(f.Value) // the file held nothing but whitespace (also 0 bytes)
		return res, nil
	}
	if f, ok := sf[flagYAMLNoEOL]; ok {
		doc.dropFinalEOL(f.Value)
	}
	res.After = doc.bytes()
	return res, nil
}
