package tier

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/sys"
)

// harmlessTopBoth: top-level keys whose value never matters, in both layers
// (functions rather than package-level maps: every ballast process pays for
// package initialization).
func harmlessTopBoth(k string) bool {
	switch k {
	case "$schema", "model", "includeCoAuthoredBy", "cleanupPeriodDays",
		"outputStyle", "spinnerTipsEnabled", "alwaysThinkingEnabled",
		"hooks", "disableAllHooks":
		return true
	}
	return false
}

// harmlessTopUser: top-level keys that are harmless only in the
// (human-authored, agent-unwritable) user layer.
func harmlessTopUser(k string) bool {
	switch k {
	case "env", "statusLine", "apiKeyHelper", "awsAuthRefresh",
		"awsCredentialExport", "otelHeadersHelper", "enabledPlugins",
		"extraKnownMarketplaces":
		return true
	}
	return false
}

// isNoop reports the values that add nothing for any key: null, [], {}, false.
func isNoop(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	switch string(s) {
	case "null", "false", "[]", "{}":
		return true
	}
	// Tolerate whitespace inside the empty containers.
	if len(s) >= 2 && (s[0] == '[' || s[0] == '{') {
		inner := bytes.TrimSpace(s[1 : len(s)-1])
		return len(inner) == 0
	}
	return false
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

func isString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func isBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func isStringArray(raw json.RawMessage) bool {
	var a []string
	return json.Unmarshal(raw, &a) == nil
}

// weakenedSandboxKeys implements WeakenedSandboxKeys.
func weakenedSandboxKeys(doc []byte, layer SettingsLayer) ([]string, error) {
	top, ok := decodeObject(bytes.TrimSpace(doc))
	if !ok {
		return nil, errors.New("tier: settings document is not a JSON object")
	}
	set := map[string]bool{}
	for k, v := range top {
		switch {
		case harmlessTopBoth(k):
		case layer == LayerUser && harmlessTopUser(k):
		case k == "permissions":
			weakenedPermissions(v, set)
		case k == "sandbox":
			weakenedSandbox(v, set)
		default:
			if !isNoop(v) {
				set[k] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func weakenedPermissions(raw json.RawMessage, set map[string]bool) {
	if string(bytes.TrimSpace(raw)) == "null" {
		return
	}
	obj, ok := decodeObject(raw)
	if !ok {
		set["permissions"] = true
		return
	}
	for k, v := range obj {
		switch k {
		case "allow", "deny", "ask", "disableBypassPermissionsMode":
		case "defaultMode":
			if isNoop(v) {
				continue
			}
			if s, ok := isString(v); ok && (s == "default" || s == "acceptEdits" || s == "plan") {
				continue
			}
			set["permissions.defaultMode"] = true
		default: // includes additionalDirectories (non-empty is reported)
			if !isNoop(v) {
				set["permissions."+k] = true
			}
		}
	}
}

func weakenedSandbox(raw json.RawMessage, set map[string]bool) {
	if string(bytes.TrimSpace(raw)) == "null" {
		return
	}
	obj, ok := decodeObject(raw)
	if !ok {
		set["sandbox"] = true
		return
	}
	for k, v := range obj {
		p := "sandbox." + k
		switch k {
		case "enabled", "failIfUnavailable":
			// Only the strict value (or null) is harmless; false weakens.
			if b, isb := isBool(v); (isb && b) || string(bytes.TrimSpace(v)) == "null" {
				continue
			}
			set[p] = true
		case "allowUnsandboxedCommands":
			if b, isb := isBool(v); (isb && !b) || string(bytes.TrimSpace(v)) == "null" {
				continue
			}
			set[p] = true
		case "autoAllowBashIfSandboxed":
			if _, isb := isBool(v); isb || isNoop(v) {
				continue
			}
			set[p] = true
		case "filesystem":
			weakenedFilesystem(v, p, set)
		default:
			reportDescend(p, v, set)
		}
	}
}

func weakenedFilesystem(raw json.RawMessage, p string, set map[string]bool) {
	if isNoop(raw) {
		return
	}
	obj, ok := decodeObject(raw)
	if !ok {
		set[p] = true
		return
	}
	for k, v := range obj {
		switch k {
		case "denyWrite", "denyRead":
			if isStringArray(v) || isNoop(v) {
				continue
			}
			set[p+"."+k] = true
		default:
			reportDescend(p+"."+k, v, set)
		}
	}
}

// reportDescend reports path p unless v is a no-op value; objects are
// descended (the reported name is that of the first non-harmless leaf
// key), arrays and scalars are reported at p.
func reportDescend(p string, v json.RawMessage, set map[string]bool) {
	if isNoop(v) {
		return
	}
	if obj, ok := decodeObject(v); ok {
		for k, c := range obj {
			reportDescend(p+"."+k, c, set)
		}
		return
	}
	set[p] = true
}

// repoSandboxWeakened implements RepoSandboxWeakened.
func repoSandboxWeakened(fsys sys.FS, repo string) []string {
	dir := filepath.Join(repo, ".claude")
	fi, err := fsys.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return []string{".claude:unreadable"}
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return []string{".claude:symlink"}
	}
	if !fi.IsDir() {
		return []string{".claude:unreadable"}
	}
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return []string{".claude:unreadable"}
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if !isSettingsName(name) {
			continue
		}
		t := e.Type()
		switch {
		case t&fs.ModeSymlink != 0:
			out = append(out, name+":symlink")
			continue
		case t.IsDir():
			// Claude does not read a directory as settings.
			continue
		case !t.IsRegular():
			out = append(out, name+":unreadable")
			continue
		}
		b, err := fsys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			out = append(out, name+":unreadable")
			continue
		}
		keys, err := weakenedSandboxKeys(b, LayerRepo)
		if err != nil {
			out = append(out, name+":unreadable")
			continue
		}
		for _, k := range keys {
			out = append(out, name+":"+k)
		}
	}
	sort.Strings(out)
	return out
}

// isSettingsName reports whether a directory entry is, or on a
// case-insensitive file system (macOS APFS default) could be opened as, a
// Claude settings file: settings*.json compared with Unicode simple case
// folding. Matching is deliberately wider than what a case-sensitive
// file system would serve (Settings.json is only a candidate on Linux), so
// the failure direction is the conservative one; the original spelling is
// kept in reports.
func isSettingsName(name string) bool {
	r := []rune(name)
	if len(r) < len("settings")+len(".json") {
		return false
	}
	return strings.EqualFold(string(r[:len("settings")]), "settings") &&
		strings.EqualFold(string(r[len(r)-len(".json"):]), ".json")
}
