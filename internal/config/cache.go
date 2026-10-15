package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

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

// cachedRules is cache/rules-<generation>.json.
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

func (l *Loader) rulesCachePath(gen string) string {
	return filepath.Join(l.opt.Layout.CacheDir, "rules-"+gen+".json")
}

func (l *Loader) readStatIndex() statIndex {
	var idx statIndex
	b, err := l.fs().ReadFile(filepath.Join(l.opt.Layout.CacheDir, statIndexName))
	if err != nil || json.Unmarshal(b, &idx) != nil || idx.V != cacheFormat {
		return statIndex{V: cacheFormat}
	}
	return idx
}

func sameSources(a []model.LayerSource, stats []model.StatKey) bool {
	if len(a) != len(stats) {
		return false
	}
	for i := range a {
		if a[i].Stat != stats[i] {
			return false
		}
	}
	return true
}

// cacheLookup serves a hit: stat index → rules file → Compile. Anything
// unexpected (missing, corrupt, written for other files) is a plain miss.
func (l *Loader) cacheLookup(idx statIndex, key string, stats []model.StatKey) (*model.CompiledRuleSet, bool) {
	gen, ok := idx.Keys[key]
	if !ok || !validGeneration(gen) {
		return nil, false
	}
	b, err := l.fs().ReadFile(l.rulesCachePath(gen))
	if err != nil {
		return nil, false
	}
	var c cachedRules
	if json.Unmarshal(b, &c) != nil || c.V != cacheFormat || c.Generation != gen || !sameSources(c.Sources, stats) {
		return nil, false
	}
	rs, err := l.compile(c.Rules, c.Settings, gen, c.Sources, c.Notices)
	if err != nil {
		return nil, false
	}
	return rs, true
}

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
	if err := sys.WriteFileAtomic(l.fs(), l.ids(), l.rulesCachePath(gen), body, 0o600, false); err != nil {
		l.diag("cannot write rules cache: %v", err)
		return
	}
	if idx.Keys == nil || len(idx.Keys) >= maxStatKeys {
		idx.Keys = make(map[string]string)
	}
	idx.V = cacheFormat
	idx.Keys[key] = gen
	ib, err := json.Marshal(idx)
	if err != nil {
		l.diag("cannot encode stat index: %v", err)
		return
	}
	if err := sys.WriteFileAtomic(l.fs(), l.ids(), filepath.Join(l.opt.Layout.CacheDir, statIndexName), ib, 0o600, false); err != nil {
		l.diag("cannot write stat index: %v", err)
	}
}
