package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func rulesCacheFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "rules-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The stat index holds at most 64 keys and is rebuilt when full; the rules
// files of the discarded keys must go with it instead of piling up forever.
func TestCacheDirStaysBoundedWhenTheStatIndexResets(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 150; i++ {
		e.write(model.LayerUser, rulesJSON("", on(fmt.Sprintf("rule-%d", i))))
		e.bump(model.LayerUser, time.Duration(i+1)*time.Second)
		e.load()
	}
	if n := len(rulesCacheFiles(t, e.lay.CacheDir)); n == 0 || n > 64 {
		t.Fatalf("%d rules cache files after 150 distinct loads, want 1..64", n)
	}
	// a concurrent writer's temp file must survive a reset of the index
	tmp := filepath.Join(e.lay.CacheDir, "rules-0123456789abcdef-dddddddd.json.tmp.1.abc")
	if err := os.WriteFile(tmp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 150; i < 220; i++ {
		e.write(model.LayerUser, rulesJSON("", on(fmt.Sprintf("rule-%d", i))))
		e.bump(model.LayerUser, time.Duration(i+1)*time.Second)
		e.load()
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("an index reset deleted a temp file that may belong to a running writer: %v", err)
	}
	// the newest state is still a cache hit
	before := e.comp.parses.Load()
	e.load()
	if e.comp.parses.Load() != before {
		t.Error("the latest load should be served from the cache")
	}
}

func TestGCCacheRemovesOnlyStaleUnreferencedFiles(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("keep-me")))
	rs := e.load()
	live := rulesCacheFiles(t, e.lay.CacheDir)
	if len(live) != 1 {
		t.Fatalf("live cache files = %v", live)
	}
	now := time.Now()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(e.lay.CacheDir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := mk("rules-0123456789abcdef-aaaaaaaa.json", 8*24*time.Hour)
	oldTmp := mk("rules-0123456789abcdef-bbbbbbbb.json.tmp.1.abc", 8*24*time.Hour)
	fresh := mk("rules-0123456789abcdef-cccccccc.json", time.Hour)
	foreign := mk("unrelated.json", 30*24*time.Hour)
	// the live file is old too, but the stat index references it
	if err := os.Chtimes(live[0], now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, err := config.GCCache(sys.OSFS{}, e.lay.CacheDir, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v, want the two stale files", removed)
	}
	for p, want := range map[string]bool{old: false, oldTmp: false, fresh: true, foreign: true, live[0]: true, filepath.Join(e.lay.CacheDir, "stat.json"): true} {
		_, err := os.Stat(p)
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
	// still a hit after the GC
	before := e.comp.parses.Load()
	if got := e.load(); got.Generation != rs.Generation || e.comp.parses.Load() != before {
		t.Error("GC must not disturb live cache entries")
	}
	// a missing directory is not an error
	if _, err := config.GCCache(sys.OSFS{}, filepath.Join(e.sb.Root, "nope"), now, 0); err != nil {
		t.Errorf("missing cache dir: %v", err)
	}
}
