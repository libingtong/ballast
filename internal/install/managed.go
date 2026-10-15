package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/sys"
)

type managedFile struct {
	path string
	doc  map[string]any
}

// readManagedSettings parses <ManagedDir>/managed-settings.json and
// managed-settings.d/*.json (lexical order). Missing files are skipped;
// unreadable or invalid ones are reported through the returned error while
// the parsable ones are still returned.
func readManagedSettings(lay layout.Layout, fsys sys.FS) ([]managedFile, error) {
	paths := []string{lay.ClaudeManagedSettings()}
	if ents, err := fsys.ReadDir(lay.ClaudeManagedSettingsD()); err == nil {
		var names []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			paths = append(paths, lay.ClaudeManagedSettingsD()+string(layoutSep(lay))+n)
		}
	}
	var out []managedFile
	var errs []error
	for _, p := range paths {
		b, err := fsys.ReadFile(p)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
			}
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		out = append(out, managedFile{path: p, doc: doc})
	}
	return out, errors.Join(errs...)
}

func layoutSep(lay layout.Layout) byte {
	if lay.GOOS == "windows" {
		return '\\'
	}
	return '/'
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// managedHasBallast reports whether the hooks object contains a Ballast hook:
// command basename "ballast"/"ballast.exe" whose first argument is "hook".
func managedHasBallast(doc map[string]any, goos string) bool {
	hooks, _ := doc["hooks"].(map[string]any)
	for _, groups := range hooks {
		list, _ := groups.([]any)
		for _, g := range list {
			gm, _ := g.(map[string]any)
			hs, _ := gm["hooks"].([]any)
			for _, h := range hs {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if cmd == "" {
					continue
				}
				if args, ok := hm["args"].([]any); ok {
					if a0, _ := firstArg(args).(string); isBallastName(baseName(cmd)) && a0 == "hook" {
						return true
					}
					continue
				}
				if parts, err := SplitCommand(cmd, goos); err == nil && len(parts) >= 2 &&
					isBallastName(baseName(parts[0])) && parts[1] == "hook" {
					return true
				}
			}
		}
	}
	return false
}

func firstArg(a []any) any {
	if len(a) == 0 {
		return nil
	}
	return a[0]
}

func isBallastName(n string) bool {
	n = strings.ToLower(n)
	return n == "ballast" || n == "ballast.exe"
}

// DetectManaged reports whether Ballast hooks are present in Claude managed settings.
func DetectManaged(lay layout.Layout, fsys sys.FS) (bool, string, error) {
	files, err := readManagedSettings(lay, fsys)
	goos := lay.GOOS
	for _, f := range files {
		if managedHasBallast(f.doc, goos) {
			return true, f.path, nil
		}
	}
	return false, "", err
}
