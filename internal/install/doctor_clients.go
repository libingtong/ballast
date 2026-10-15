package install

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

type entryState int

const (
	entryMissing entryState = iota
	entryModified
	entryOK
)

// locateEntry looks for a recorded entry in the current file content.
func locateEntry(c model.ClientID, data []byte, path, goos string, rec model.ManagedEntry, m *model.InstallManifest) (entryState, error) {
	binaries := ownerBinaries(c, goos, "", m, []model.ManagedEntry{rec})
	state := entryMissing
	if c == model.ClientHermes {
		t := &hermesTarget{path: path, goos: goos}
		sec, err := parseYAMLDoc(data).section()
		if err != nil {
			return state, t.parseErr(err)
		}
		if ev := sec.event(rec.Native); ev != nil {
			for _, it := range ev.items {
				if !it.opaque && it.hash() == rec.Hash {
					return entryOK, nil
				}
				if t.isBallast(it, binaries, goos) {
					state = entryModified
				}
			}
		}
		return state, nil
	}
	t := &jsonHooksTarget{client: c, path: path, goos: goos}
	doc, err := t.parse(data)
	if err != nil {
		return state, err
	}
	hooks, err := t.hooksObject(doc.root)
	if err != nil {
		return state, err
	}
	list, err := t.eventList(hooks, rec.Native)
	if err != nil {
		return state, err
	}
	f := jfmt{unit: doc.unit, eol: doc.eol}
	t.eachHandler(list, func(l located) bool {
		if l.node.hash(f) == rec.Hash {
			state = entryOK
			return false
		}
		if t.isBallast(l.node, binaries, goos) {
			state = entryModified
		}
		return true
	})
	return state, nil
}

func hasRecords(m *model.InstallManifest, c model.ClientID) bool {
	for _, e := range m.Entries {
		if e.Client == c {
			return true
		}
	}
	for _, f := range m.Files {
		if f.Client == c {
			return true
		}
	}
	return false
}

// entryChecks verifies every recorded entry of client c against its file.
func entryChecks(c model.ClientID, lay layout.Layout, fsys sys.FS, m *model.InstallManifest) []Check {
	var out []Check
	cache := map[string][]byte{}
	cacheErr := map[string]error{}
	goos := envGOOS(lay, nil)
	for _, rec := range m.Entries {
		if rec.Client != c {
			continue
		}
		id := fmt.Sprintf("%s.entry.%s", c, rec.Native)
		fix := fmt.Sprintf("run: ballast install --client %s", c)
		data, ok := cache[rec.File]
		err := cacheErr[rec.File]
		if !ok && err == nil {
			data, err = fsys.ReadFile(rec.File)
			cache[rec.File], cacheErr[rec.File] = data, err
		}
		if err != nil {
			msg := fmt.Sprintf("%s cannot be read: %v", rec.File, err)
			if errors.Is(err, fs.ErrNotExist) {
				msg = fmt.Sprintf("%s is missing", rec.File)
			}
			out = append(out, Check{ID: id, Client: c, Status: CheckFail, Message: msg, Fix: fix})
			continue
		}
		st, err := locateEntry(c, data, rec.File, goos, rec, m)
		switch {
		case err != nil:
			out = append(out, Check{ID: id, Client: c, Status: CheckFail, Message: err.Error(),
				Fix: "repair the file by hand; Ballast will not edit a file it cannot parse"})
		case st == entryOK:
			out = append(out, Check{ID: id, Client: c, Status: CheckOK, Message: fmt.Sprintf("hook entry for %s is present in %s", rec.Native, rec.File)})
		case st == entryModified:
			out = append(out, Check{ID: id, Client: c, Status: CheckWarn,
				Message: fmt.Sprintf("the %s entry in %s was modified after install; Ballast leaves it alone", rec.Native, rec.File),
				Fix:     "restore the original entry or remove it, then run: ballast install --client " + string(c)})
		default:
			out = append(out, Check{ID: id, Client: c, Status: CheckFail,
				Message: fmt.Sprintf("hook entry for %s is missing from %s", rec.Native, rec.File), Fix: fix})
		}
	}
	return out
}

// ---- Claude Code ---------------------------------------------------------

func (claudeClient) Doctor(lay layout.Layout, fsys sys.FS, m *model.InstallManifest) []Check {
	return entryChecks(model.ClientClaude, lay, fsys, nonNilManifest(m))
}

// ---- Codex ---------------------------------------------------------------

func (codexClient) Doctor(lay layout.Layout, fsys sys.FS, m *model.InstallManifest) []Check {
	m = nonNilManifest(m)
	out := entryChecks(model.ClientCodex, lay, fsys, m)

	cfg := filepath.Join(lay.CodexDir, "config.toml")
	var trust []model.ManagedFlag
	for _, fl := range m.Flags {
		if fl.Client != model.ClientCodex {
			continue
		}
		if fl.File != "" {
			cfg = fl.File
		}
		if strings.HasPrefix(fl.Key, trustKeyPrefix) {
			trust = append(trust, fl)
		}
	}
	var doc *tomlDoc
	data, err := fsys.ReadFile(cfg)
	if err == nil {
		doc, err = parseTOML(data)
	}
	switch {
	case err != nil && errors.Is(err, fs.ErrNotExist):
		out = append(out, Check{ID: "codex.features_hooks", Client: model.ClientCodex, Status: CheckFail,
			Message: cfg + " is missing, so Codex hooks are disabled", Fix: "run: ballast install --client codex"})
	case err != nil:
		out = append(out, Check{ID: "codex.features_hooks", Client: model.ClientCodex, Status: CheckFail,
			Message: fmt.Sprintf("%s cannot be read: %v", cfg, err)})
	default:
		_, _, val, ferr := doc.featuresHooks()
		switch {
		case ferr != nil:
			out = append(out, Check{ID: "codex.features_hooks", Client: model.ClientCodex, Status: CheckFail, Message: ferr.Error()})
		case val == "true":
			out = append(out, Check{ID: "codex.features_hooks", Client: model.ClientCodex, Status: CheckOK, Message: "[features] hooks = true"})
		default:
			out = append(out, Check{ID: "codex.features_hooks", Client: model.ClientCodex, Status: CheckFail,
				Message: "[features] hooks is not enabled in " + cfg + "; Codex ignores hooks.json", Fix: "run: ballast install --client codex"})
		}
	}

	if len(trust) == 0 {
		out = append(out, Check{ID: "codex.trust", Client: model.ClientCodex, Status: CheckWarn,
			Message: "hooks are not pre-trusted; Codex asks for approval before running them (unverified)",
			Fix:     "approve them with /hooks in the Codex TUI, or reinstall with: ballast install --client codex --codex-trust"})
	} else {
		stale := 0
		for _, fl := range trust {
			k, ok := parseTrustFlagKey(fl.Key)
			if !ok || doc == nil {
				stale++
				continue
			}
			if _, h, found := doc.trustHashOf(k); !found || h != fl.Value {
				stale++
			}
		}
		if stale == 0 {
			out = append(out, Check{ID: "codex.trust", Client: model.ClientCodex, Status: CheckOK,
				Message: fmt.Sprintf("%d trust entries match (hash algorithm unverified for Codex versions other than 0.130)", len(trust))})
		} else {
			out = append(out, Check{ID: "codex.trust", Client: model.ClientCodex, Status: CheckWarn,
				Message: fmt.Sprintf("%d of %d trust entries are missing or changed", stale, len(trust)),
				Fix:     "reinstall with: ballast install --client codex --codex-trust"})
		}
	}
	out = append(out, Check{ID: "codex.tui_only", Client: model.ClientCodex, Status: CheckWarn,
		Message: "Codex hooks fire only in interactive TUI sessions, not in `codex exec` (observation by the pinrule project, unverified)"})
	return out
}

// ---- Cursor --------------------------------------------------------------

func (cursorClient) Doctor(lay layout.Layout, fsys sys.FS, m *model.InstallManifest) []Check {
	out := entryChecks(model.ClientCursor, lay, fsys, nonNilManifest(m))
	return append(out, Check{ID: "cursor.reload", Client: model.ClientCursor, Status: CheckOK,
		Message: "after install or a rule change run \"Developer: Reload Window\" in Cursor"})
}

// mdcCheck implements cursor.mdc_current.
func mdcCheck(lay layout.Layout, fsys sys.FS, m *model.InstallManifest, want []byte) Check {
	path := filepath.Join(lay.CursorDir, "rules", "ballast.mdc")
	for _, f := range m.Files {
		if f.Client == model.ClientCursor && f.Whole {
			path = f.Path
		}
	}
	c := Check{ID: "cursor.mdc_current", Client: model.ClientCursor}
	got, err := fsys.ReadFile(path)
	switch {
	case err != nil:
		c.Status = CheckFail
		c.Message = path + " is missing or unreadable"
		c.Fix = "run: ballast install --client cursor"
	case want == nil:
		c.Status = CheckWarn
		c.Message = "not evaluated: the expected ballast.mdc content was not supplied"
	case string(got) == string(want):
		c.Status = CheckOK
		c.Message = path + " matches the current rules"
	default:
		c.Status = CheckWarn
		c.Message = path + " is out of date with the current rules"
		c.Fix = "run: ballast install --client cursor"
	}
	return c
}

// ---- Hermes --------------------------------------------------------------

func (hermesClient) Doctor(lay layout.Layout, fsys sys.FS, m *model.InstallManifest) []Check {
	out := entryChecks(model.ClientHermes, lay, fsys, nonNilManifest(m))
	return append(out, Check{ID: "hermes.accept_hooks", Client: model.ClientHermes, Status: CheckWarn,
		Message: "Hermes asks for consent to run hooks; start it with --accept-hooks (unverified)"})
}
