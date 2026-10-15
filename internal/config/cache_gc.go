package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/libingtong/ballast/internal/sys"
)

// CacheTTL is how long an unreferenced cache file (rules-*.json, stray
// atomic-write temp files) survives `ballast gc`.
const CacheTTL = 7 * 24 * time.Hour

const (
	rulesCachePrefix = "rules-"
	cacheJSONSuffix  = ".json"
)

// referencedRulesFiles lists the rules-<gen>-<key8>.json names the stat index
// points at.
func referencedRulesFiles(idx statIndex) map[string]bool {
	ref := make(map[string]bool, len(idx.Keys))
	for key, gen := range idx.Keys {
		if validGeneration(gen) && len(key) >= 8 {
			ref[rulesCachePrefix+gen+"-"+key[:8]+cacheJSONSuffix] = true
		}
	}
	return ref
}

// isRulesCacheFile reports whether name is a rules cache file. Atomic-write
// temp files ("<file>.json.tmp.<pid>.<rand>") belong to a writer that may be
// running right now, so they only count when withTemp is set (age-guarded GC).
func isRulesCacheFile(name string, withTemp bool) bool {
	if !strings.HasPrefix(name, rulesCachePrefix) {
		return false
	}
	return strings.HasSuffix(name, cacheJSONSuffix) || withTemp && strings.Contains(name, cacheJSONSuffix+".tmp.")
}

// pruneRulesCache removes every rules cache file that keep does not list and
// that is older than minAge relative to now. minAge 0 removes unreferenced
// files regardless of age but never touches temp files (a concurrent writer
// may be about to rename one); minAge > 0 also removes stale temp files. Failures are
// ignored; the files are only a cache. It returns the names removed.
func pruneRulesCache(fsys sys.FS, dir string, keep map[string]bool, now time.Time, minAge time.Duration) []string {
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !isRulesCacheFile(name, minAge > 0) || keep[name] {
			continue
		}
		if minAge > 0 {
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) < minAge {
				continue
			}
		}
		if fsys.Remove(filepath.Join(dir, name)) == nil {
			removed = append(removed, name)
		}
	}
	return removed
}

// GCCache is the cache part of `ballast gc`: it removes rules cache files
// that the stat index no longer references and that are older than ttl
// (<= 0 means CacheTTL). A missing directory is not an error. It returns the
// file names removed.
func GCCache(fsys sys.FS, cacheDir string, now time.Time, ttl time.Duration) ([]string, error) {
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	if ttl <= 0 {
		ttl = CacheTTL
	}
	var idx statIndex
	b, err := fsys.ReadFile(filepath.Join(cacheDir, statIndexName))
	switch {
	case err == nil:
		if json.Unmarshal(b, &idx) != nil || idx.V != cacheFormat {
			idx = statIndex{} // unreadable index references nothing
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return nil, err
	}
	return pruneRulesCache(fsys, cacheDir, referencedRulesFiles(idx), now, ttl), nil
}
