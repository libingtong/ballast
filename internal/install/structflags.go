package install

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Structural flags (contract v1.3). Uninstall must leave a file the way the
// user had it, but the manifest only knew the entries Ballast inserted, not
// the containers around them. For every container an install fills, Plan
// records one ManagedFlag for the edited file:
//
//	SetByBallast=true   the container did not exist; Ballast created it
//	SetByBallast=false  it already existed; Value = its original text where
//	                    the format needs it (YAML key line, e.g. "hooks: {}",
//	                    "hooks: # note", "  pre_tool_call: []")
//
// Remove deletes an emptied container only when it was created by Ballast
// (or when no flag is recorded, the pre-v1.3 behaviour) and otherwise
// restores the original text. Keys:
const (
	flagJSONHooks       = "json:hooks"   // top-level "hooks" object
	flagJSONVersion     = "json:version" // Cursor "version": 1 (SetByBallast only)
	flagYAMLHooks       = "yaml:hooks"   // top-level hooks: header
	flagYAMLEventPrefix = "yaml:hooks."  // + native event key
	// flagDirKept: Value = the nearest directory above a file that already
	// existed when install created the file; uninstall never prunes it or
	// anything above it (e.g. a user's empty ~/.cursor/rules).
	flagDirKept = "dir:kept"
)

func isStructFlag(key string) bool {
	return strings.HasPrefix(key, "json:") || strings.HasPrefix(key, "yaml:") || strings.HasPrefix(key, "dir:")
}

// hasFlagKey reports whether flags already holds a flag with the given key.
func hasFlagKey(flags []model.ManagedFlag, key string) bool {
	for _, f := range flags {
		if f.Key == key {
			return true
		}
	}
	return false
}

// keptDir returns the flagDirKept value recorded for file, if any.
func keptDir(m *model.InstallManifest, file string) string {
	if m == nil {
		return ""
	}
	for _, f := range m.Flags {
		if f.File == file && f.Key == flagDirKept {
			return f.Value
		}
	}
	return ""
}

// noteKeptDir adds the flagDirKept flag to a plan that creates its file:
// carried over from the manifest, or computed as the nearest existing
// ancestor directory on the first install.
func noteKeptDir(fsys sys.FS, m *model.InstallManifest, c model.ClientID, p *PlanResult, cur []byte) {
	if p == nil || p.After == nil {
		return
	}
	if hasFlagKey(p.Flags, flagDirKept) {
		return
	}
	kept := keptDir(m, p.Path)
	if kept == "" {
		if cur != nil || fileRec(m, p.Path) != nil {
			return // the file existed (or a pre-v1.3 install created it)
		}
		dir := filepath.Dir(p.Path)
		for {
			if fi, err := fsys.Stat(dir); err == nil && fi.IsDir() {
				kept = dir
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	}
	p.Flags = append(p.Flags, model.ManagedFlag{Client: c, File: p.Path, Key: flagDirKept, Value: kept})
	sort.Slice(p.Flags, func(i, j int) bool { return p.Flags[i].Key < p.Flags[j].Key })
}

// structFlags returns the structural flags already recorded for (c, file).
func structFlags(m *model.InstallManifest, c model.ClientID, file string) map[string]model.ManagedFlag {
	out := map[string]model.ManagedFlag{}
	for _, f := range flagsFor(m, c, file) {
		if isStructFlag(f.Key) {
			out[f.Key] = f
		}
	}
	return out
}

// noteStruct records key unless a flag for it exists already (the first
// install knows the original state; re-installs carry it over). existed
// reports whether the container exists in the file being planned; when it
// does but an earlier install already owns entries of this file without a
// flag for it, the earlier install created it.
func noteStruct(sf map[string]model.ManagedFlag, c model.ClientID, file, key string, existed, priorInstall bool, original string) {
	if _, ok := sf[key]; ok {
		return
	}
	f := model.ManagedFlag{Client: c, File: file, Key: key}
	switch {
	case !existed, priorInstall:
		f.SetByBallast = true
	default:
		f.Value = original
	}
	sf[key] = f
}

// flagList returns the flags sorted by key.
func flagList(sf map[string]model.ManagedFlag) []model.ManagedFlag {
	if len(sf) == 0 {
		return nil
	}
	out := make([]model.ManagedFlag, 0, len(sf))
	for _, f := range sf {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// createdByBallast reports whether key's container may be deleted when it
// becomes empty: created by Ballast, or no flag at all (pre-v1.3 manifest).
func createdByBallast(sf map[string]model.ManagedFlag, key string) bool {
	f, ok := sf[key]
	return !ok || f.SetByBallast
}
