package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const (
	cacheFormat   = 1
	maxStatKeys   = 64
	statIndexName = "stat.json"
)

// statIndex is cache/stat.json: stat-fingerprint key → generation.
type statIndex struct {
	V    int               `json:"v"`
	Keys map[string]string `json:"keys"`
}

// cachedRules is cache/rules-<generation>-<key[:8]>.json (contract v1.3:
// the key suffix keeps two repos whose rules merge to the same generation
// from overwriting each other's sources on alternate loads).
type cachedRules struct {
	V          int                  `json:"v"`
	Generation string               `json:"generation"`
	Sources    []model.LayerSource  `json:"sources"`
	Rules      []model.LoadedRule   `json:"rules"`
	Settings   model.Settings       `json:"settings"`
	Notices    []model.ConfigNotice `json:"notices,omitempty"`
}

// cacheKey hashes the binary version with the layer stat fingerprints.
func cacheKey(version string, stats []model.StatKey) string {
	h := sha256.New()
	fmt.Fprintf(h, "ballast-config/%d\x00%s", cacheFormat, version)
	for _, s := range stats {
		fmt.Fprintf(h, "\x00%s|%t|%d|%d|%d", s.Path, s.Exists, s.Size, s.MtimeNS, s.Inode)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func validGeneration(g string) bool {
	if len(g) != 16 {
		return false
	}
	for i := 0; i < len(g); i++ {
		c := g[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (l *Loader) rulesCachePath(gen, key string) string {
	if len(key) > 8 {
		key = key[:8]
	}
	return filepath.Join(l.opt.Layout.CacheDir, "rules-"+gen+"-"+key+".json")
}

func (l *Loader) readStatIndex() statIndex {
	var idx statIndex
	b, err := l.fs().ReadFile(filepath.Join(l.opt.Layout.CacheDir, statIndexName))
	if err != nil || json.Unmarshal(b, &idx) != nil || idx.V != cacheFormat {
		return statIndex{V: cacheFormat}
	}
	return idx
}

// matchSources checks that the cached sources describe the current layer
// stats and returns the sources to report. A rules file may still be reached
// from another load context whose key prefix collides, so a layer that is
// missing both in the cache and now may differ only in its path (another
// repo without repo rules); it contributes no rules, and its source is
// re-stamped with the current stat.
func matchSources(a []model.LayerSource, stats []model.StatKey) ([]model.LayerSource, bool) {
	if len(a) != len(stats) {
		return nil, false
	}
	out, copied := a, false
	for i := range a {
		if a[i].Stat == stats[i] {
			continue
		}
		if a[i].Stat.Exists || stats[i].Exists {
			return nil, false
		}
		if !copied {
			out, copied = append([]model.LayerSource(nil), a...), true
		}
		out[i].Path = stats[i].Path
		out[i].Stat = stats[i]
	}
	return out, true
}

// cacheLookup serves a hit: stat index → rules file → Compile. Anything
// unexpected (missing, corrupt, written for other files) is a plain miss.
func (l *Loader) cacheLookup(idx statIndex, key string, stats []model.StatKey) (*model.CompiledRuleSet, bool) {
	gen, ok := idx.Keys[key]
	if !ok || !validGeneration(gen) {
		return nil, false
	}
	b, err := l.fs().ReadFile(l.rulesCachePath(gen, key))
	if err != nil {
		return nil, false
	}
	var c cachedRules
	if json.Unmarshal(b, &c) != nil || c.V != cacheFormat || c.Generation != gen {
		return nil, false
	}
	sources, ok := matchSources(c.Sources, stats)
	if !ok {
		return nil, false
	}
	rs, err := l.compile(c.Rules, c.Settings, gen, sources, c.Notices)
	if err != nil {
		return nil, false
	}
	return rs, true
}

// hotWriteRenameBudget bounds the rename retry of the writes the hook does on
// its hot path (rules cache, stat index). They are disposable (the
// last-known-good file is NOT: it keeps the default budget, it is the fallback
// that makes a bad rules edit harmless and a contended rewrite must not lose it): a failed write only costs one cache miss on the next call, so
// they must not spend the 100 ms default of sys.renameRetryBudget (which, on
// top of Windows' ~40 ms baseline, would collide with the 150 ms watchdog).
const hotWriteRenameBudget = 20 * time.Millisecond

// cacheStore writes the rules file, then points the stat index at it. Any
// failure is a stderr diagnostic only.
func (l *Loader) cacheStore(idx statIndex, key string, res MergeResult, gen string, sources []model.LayerSource) {
	body, err := json.Marshal(cachedRules{
		V: cacheFormat, Generation: gen, Sources: sources,
		Rules: res.Rules, Settings: res.Settings, Notices: res.Notices,
	})
	if err != nil {
		l.diag("cannot encode rules cache: %v", err)
		return
	}
	if err := sys.WriteFileAtomicWithin(l.fs(), l.ids(), l.rulesCachePath(gen, key), body, 0o600, false, hotWriteRenameBudget); err != nil {
		l.diag("cannot write rules cache: %v", err)
		return
	}
	reset := idx.Keys == nil || len(idx.Keys) >= maxStatKeys
	if reset {
		idx.Keys = make(map[string]string)
	}
	idx.V = cacheFormat
	idx.Keys[key] = gen
	ib, err := json.Marshal(idx)
	if err != nil {
		l.diag("cannot encode stat index: %v", err)
		return
	}
	if err := sys.WriteFileAtomicWithin(l.fs(), l.ids(), filepath.Join(l.opt.Layout.CacheDir, statIndexName), ib, 0o600, false, hotWriteRenameBudget); err != nil {
		l.diag("cannot write stat index: %v", err)
		return
	}
	if reset {
		// The old index is gone, so every rules file it referenced is
		// unreachable. Drop them; a racing writer whose file is removed here
		// only suffers one cache miss.
		pruneRulesCache(l.fs(), l.opt.Layout.CacheDir, referencedRulesFiles(idx), time.Time{}, 0)
	}
}
