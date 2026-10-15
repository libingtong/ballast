package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

var _ ports.RulesLoader = (*config.Loader)(nil)

func TestLayerPaths(t *testing.T) {
	e := newEnv(t)
	got := config.LayerPaths(e.lay, model.LoadRequest{Cwd: "/x/sub", Repo: &model.RepoInfo{Toplevel: "/x"}})
	want := []config.LayerPath{
		{Layer: model.LayerOrg, Path: filepath.Join(e.sb.ManagedDir, "ballast", "rules.json")},
		{Layer: model.LayerRepo, Path: filepath.Join("/x", ".ballast", "rules.json")},
		{Layer: model.LayerUser, Path: filepath.Join(e.sb.BallastHome, "rules.json")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LayerPaths = %+v, want %+v", got, want)
	}
	// Outside git the repo root is the cwd.
	got = config.LayerPaths(e.lay, model.LoadRequest{Cwd: "/y"})
	if got[1].Path != filepath.Join("/y", ".ballast", "rules.json") {
		t.Errorf("no-repo path = %s", got[1].Path)
	}
	if got = config.LayerPaths(e.lay, model.LoadRequest{}); got[1].Path != "" {
		t.Errorf("empty cwd must give no repo path, got %s", got[1].Path)
	}
}

func TestStatFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	k, err := config.StatFile(sys.OSFS{}, p)
	if err != nil || k.Exists || k.Path != p {
		t.Fatalf("missing: %+v %v", k, err)
	}
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1_700_000_000, 123)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	k, err = config.StatFile(sys.OSFS{}, p)
	if err != nil || !k.Exists || k.Size != 3 || k.MtimeNS != mt.UnixNano() {
		t.Fatalf("existing: %+v %v", k, err)
	}
	// A parent that is a file means "missing", not an I/O error.
	if k, err = config.StatFile(sys.OSFS{}, filepath.Join(p, "child")); err != nil || k.Exists {
		t.Errorf("ENOTDIR: %+v %v", k, err)
	}
	if k, err = config.StatFile(sys.OSFS{}, dir); err != nil || !k.Exists {
		t.Errorf("directory: %+v %v", k, err)
	}
	if k, err = config.StatFile(sys.OSFS{}, ""); err != nil || k.Exists {
		t.Errorf("empty path: %+v %v", k, err)
	}
}

func TestLoadNoFiles(t *testing.T) {
	e := newEnv(t)
	rs := e.load()
	if len(rs.Rules) != 0 || len(rs.Notices) != 0 {
		t.Errorf("rules=%v notices=%v", ids(rs), rs.Notices)
	}
	if len(rs.Sources) != 3 {
		t.Fatalf("sources = %d", len(rs.Sources))
	}
	for i, l := range []model.Layer{model.LayerOrg, model.LayerRepo, model.LayerUser} {
		if rs.Sources[i].Layer != l || rs.Sources[i].Status != model.LayerMissing || rs.Sources[i].Stat.Exists {
			t.Errorf("source %d = %+v", i, rs.Sources[i])
		}
	}
	if rs.Generation != config.Generation(nil, model.Settings{}) {
		t.Errorf("generation = %s", rs.Generation)
	}
}

func TestLoadMergesThreeLayers(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON(`{"inject":{"anchor_every":3}}`, on("org-rule"), on("shared-rule")))
	e.write(model.LayerRepo, rulesJSON(`{"inject":{"anchor_every":1,"batch_after_tools":10}}`, on("shared-rule"), on("repo-rule")))
	e.write(model.LayerUser, rulesJSON(`{"inject":{"batch_after_tools":50}}`, on("repo-rule"), on("user-rule")))
	rs := e.load()
	if want := []string{"org-rule", "shared-rule", "repo-rule", "user-rule"}; !reflect.DeepEqual(ids(rs), want) {
		t.Errorf("rules = %v, want %v", ids(rs), want)
	}
	if want := []string{"rule_shadowed/repo/shared-rule", "rule_shadowed/user/repo-rule"}; !reflect.DeepEqual(noticeKinds(rs), want) {
		t.Errorf("notices = %v, want %v", noticeKinds(rs), want)
	}
	in := rs.Settings.Inject
	if in == nil || *in.AnchorEvery != 3 || *in.BatchAfterTools != 10 {
		t.Errorf("settings = %+v", in)
	}
	for _, r := range rs.Rules {
		if r.Layer == "" || r.Source != e.path(r.Layer) || r.Action != model.ActionWarn || r.Target != model.ActionDeny {
			t.Errorf("rule %s = %+v", r.Rule.ID, r.LoadedRule)
		}
	}
	for _, s := range rs.Sources {
		if s.Status != model.LayerOK || s.Hash == "" || !s.Stat.Exists || s.Path != e.path(s.Layer) {
			t.Errorf("source %+v", s)
		}
	}
	// The generation matches the exported function over the merged rules.
	var merged []model.Rule
	for _, r := range rs.Rules {
		merged = append(merged, r.Rule)
	}
	if rs.Generation != config.Generation(merged, rs.Settings) {
		t.Errorf("generation %s != Generation(merged)", rs.Generation)
	}
}

func TestLoadCapsAtTwelveAndNeverDropsOrg(t *testing.T) {
	e := newEnv(t)
	var org, repo, user []spec
	for i := 0; i < 6; i++ {
		org = append(org, on("org-"+string(rune('a'+i))+"-rule"))
		repo = append(repo, on("repo-"+string(rune('a'+i))+"-rule"))
		user = append(user, on("user-"+string(rune('a'+i))+"-rule"))
	}
	e.write(model.LayerOrg, rulesJSON("", org...))
	e.write(model.LayerRepo, rulesJSON("", repo...))
	e.write(model.LayerUser, rulesJSON("", user...))
	rs := e.load()
	if len(rs.Rules) != 12 {
		t.Fatalf("rules = %d", len(rs.Rules))
	}
	for i := 0; i < 6; i++ {
		if rs.Rules[i].Layer != model.LayerOrg {
			t.Errorf("rule %d is %s, org must come first", i, rs.Rules[i].Layer)
		}
	}
	if len(rs.Notices) != 6 {
		t.Fatalf("notices = %v", noticeKinds(rs))
	}
	for _, n := range rs.Notices {
		if n.Kind != model.NoticeRuleDropped || n.Layer != model.LayerUser {
			t.Errorf("notice = %+v", n)
		}
	}
}

func TestLoadHotReloadEveryLayer(t *testing.T) {
	for _, layer := range []model.Layer{model.LayerOrg, model.LayerRepo, model.LayerUser} {
		t.Run(string(layer), func(t *testing.T) {
			e := newEnv(t)
			l := e.loader()
			load := func() *model.CompiledRuleSet {
				rs, err := l.Load(e.req())
				if err != nil {
					t.Fatal(err)
				}
				return rs
			}
			e.write(layer, rulesJSON("", on("first-rule")))
			g1 := load().Generation
			if g := load().Generation; g != g1 {
				t.Fatalf("unchanged file changed generation: %s vs %s", g, g1)
			}
			e.write(layer, rulesJSON("", on("first-rule"), on("second-rule")))
			e.bump(layer, time.Second)
			rs := load()
			if rs.Generation == g1 || len(rs.Rules) != 2 {
				t.Errorf("rewrite not picked up: gen %s (was %s), rules %v", rs.Generation, g1, ids(rs))
			}
			// Same size, new mtime still reloads.
			e.write(layer, rulesJSON("", on("first-rule"), on("secnd-rule")))
			e.bump(layer, 2*time.Second)
			if g := load().Generation; g == rs.Generation {
				t.Error("same-size rewrite with new mtime not picked up")
			}
			// Removal drops the layer.
			if err := os.Remove(e.path(layer)); err != nil {
				t.Fatal(err)
			}
			if rs = load(); len(rs.Rules) != 0 || source(rs, layer).Status != model.LayerMissing {
				t.Errorf("removed file: rules %v status %s", ids(rs), source(rs, layer).Status)
			}
		})
	}
}

func TestLoadReplacedFileDifferentInode(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("aaa-rule")))
	g1 := e.load().Generation
	fi, _ := os.Stat(e.path(model.LayerUser))
	// Atomic replace: same size and mtime, different file.
	tmp := e.path(model.LayerUser) + ".new"
	if err := os.WriteFile(tmp, []byte(rulesJSON("", on("bbb-rule"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, e.path(model.LayerUser)); err != nil {
		t.Fatal(err)
	}
	if g2 := e.load().Generation; g2 == g1 && st(e, model.LayerUser).Inode != 0 {
		t.Error("replaced file with identical size/mtime was not detected (inode)")
	}
}

func st(e *env, l model.Layer) model.StatKey {
	k, _ := config.StatFile(sys.OSFS{}, e.path(l))
	return k
}

func TestLoadCacheHitSkipsParsing(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON(`{"inject":{"anchor_every":2}}`, on("org-rule")))
	e.write(model.LayerRepo, rulesJSON("", on("repo-rule"), on("org-rule")))
	first := e.load()
	if e.comp.parses.Load() != 2 {
		t.Fatalf("parses = %d", e.comp.parses.Load())
	}
	for _, f := range []string{"stat.json", "rules-" + first.Generation + ".json"} {
		if _, err := os.Stat(filepath.Join(e.lay.CacheDir, f)); err != nil {
			t.Fatalf("cache file %s: %v", f, err)
		}
	}
	second := e.load()
	if e.comp.parses.Load() != 2 {
		t.Errorf("cache hit re-parsed the rule files (%d parses)", e.comp.parses.Load())
	}
	if e.comp.compiles.Load() != 2 {
		t.Errorf("cache hit must still Compile (compiles=%d)", e.comp.compiles.Load())
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("cached result differs:\n%+v\n%+v", first, second)
	}
	// A new Loader (new process) hits the same cache.
	third, err := e.loader().Load(e.req())
	if err != nil || !reflect.DeepEqual(first, third) || e.comp.parses.Load() != 2 {
		t.Errorf("fresh loader: err=%v parses=%d", err, e.comp.parses.Load())
	}
}

func TestLoadCacheInvalidatedByVersion(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("aaa-rule")))
	e.load()
	e.opt.Version = "test-2"
	e.load()
	if e.comp.parses.Load() != 2 {
		t.Errorf("a new version must not reuse the cache (parses=%d)", e.comp.parses.Load())
	}
}

func TestLoadCacheNotSharedAcrossRepos(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	// Two repos with identical rule content share a generation, not sources.
	other := filepath.Join(e.sb.Root, "work", "other")
	for _, root := range []string{e.repo, other} {
		p := layout.RepoRules(root)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rulesJSON("", on("repo-rule"))), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l := e.loader()
	a, _ := l.Load(e.req())
	b, err := l.Load(model.LoadRequest{Cwd: other, Repo: &model.RepoInfo{Toplevel: other}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Generation != b.Generation {
		t.Fatalf("expected same generation for same content")
	}
	if source(a, model.LayerRepo).Path == source(b, model.LayerRepo).Path {
		t.Fatal("repo sources must differ")
	}
	a2, _ := l.Load(e.req())
	if source(a2, model.LayerRepo).Path != source(a, model.LayerRepo).Path {
		t.Errorf("repo A got repo B's cached sources: %s", source(a2, model.LayerRepo).Path)
	}
}

func TestLoadNoCacheWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.opt.NoCache = true
	e.write(model.LayerUser, rulesJSON("", on("aaa-rule")))
	e.load()
	e.load()
	if e.comp.parses.Load() != 2 {
		t.Errorf("NoCache must parse every time (parses=%d)", e.comp.parses.Load())
	}
	if _, err := os.Stat(e.lay.CacheDir); !os.IsNotExist(err) {
		t.Errorf("NoCache created %s (err=%v)", e.lay.CacheDir, err)
	}
}

func TestLoadCorruptCacheIsAMiss(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("aaa-rule")))
	first := e.load()
	for _, name := range []string{"rules-" + first.Generation + ".json", "stat.json"} {
		p := filepath.Join(e.lay.CacheDir, name)
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		again, err := e.loader().Load(e.req())
		if err != nil || again.Generation != first.Generation {
			t.Fatalf("corrupt %s: err=%v", name, err)
		}
	}
	// A crafted stat index must not make the loader read outside cache/.
	idx := `{"v":1,"keys":{"k":"../../../../etc/passwd"}}`
	if err := os.WriteFile(filepath.Join(e.lay.CacheDir, "stat.json"), []byte(idx), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.loader().Load(e.req()); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCacheWriteFailureIsNotAnError(t *testing.T) {
	e := newEnv(t)
	e.opt.FS = sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"Rename": "cache"}, Err: errors.New("disk full")}
	e.write(model.LayerUser, rulesJSON("", on("aaa-rule")))
	rs := e.load()
	if len(rs.Rules) != 1 {
		t.Fatalf("rules = %v", ids(rs))
	}
	if !strings.Contains(e.stderr.String(), "disk full") {
		t.Errorf("stderr = %q, want a diagnostic", e.stderr.String())
	}
	// And no temp files are left behind.
	filepath.WalkDir(e.lay.CacheDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("leftover cache file %s", p)
		}
		return nil
	})
}

func TestLoadInvalidLayerWithLKG(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.write(model.LayerRepo, rulesJSON("", on("good-rule")))
	good := e.load()
	if got := ids(good); !reflect.DeepEqual(got, []string{"good-rule", "user-rule"}) {
		t.Fatalf("rules = %v", got)
	}
	lkgs, _ := filepath.Glob(filepath.Join(e.lay.LKGDir, "*.json"))
	if len(lkgs) != 2 {
		t.Fatalf("LKG files = %v", lkgs)
	}

	e.write(model.LayerRepo, `{"version":1,"rules":[{"id":"BAD ID","event":"PreToolUse"}]}`)
	e.bump(model.LayerRepo, time.Second)
	e.stderr.Reset()
	rs := e.load()
	if got := ids(rs); !reflect.DeepEqual(got, []string{"good-rule", "user-rule"}) {
		t.Errorf("LKG not used: %v", got)
	}
	src := source(rs, model.LayerRepo)
	if src.Status != model.LayerLKG || src.Error == "" || src.Hash != source(good, model.LayerRepo).Hash {
		t.Errorf("source = %+v", src)
	}
	if want := []string{"config_invalid/repo/"}; !reflect.DeepEqual(noticeKinds(rs), want) {
		t.Errorf("notices = %v", noticeKinds(rs))
	}
	if rs.Generation != good.Generation {
		t.Error("LKG must reproduce the previous generation")
	}
	if !strings.Contains(e.stderr.String(), "last-known-good") {
		t.Errorf("stderr = %q", e.stderr.String())
	}
	// The notice survives a cache hit.
	if again := e.load(); !reflect.DeepEqual(noticeKinds(again), noticeKinds(rs)) || source(again, model.LayerRepo).Status != model.LayerLKG {
		t.Errorf("cached notices = %v", noticeKinds(again))
	}
	// The invalid file must not have overwritten the LKG.
	e.write(model.LayerRepo, `not json at all`)
	e.bump(model.LayerRepo, 2*time.Second)
	if rs = e.load(); !reflect.DeepEqual(ids(rs), []string{"good-rule", "user-rule"}) {
		t.Errorf("after undecodable file: %v", ids(rs))
	}
	// Fixing the file recovers and refreshes the LKG.
	e.write(model.LayerRepo, rulesJSON("", on("fixed-rule")))
	e.bump(model.LayerRepo, 3*time.Second)
	rs = e.load()
	if got := ids(rs); !reflect.DeepEqual(got, []string{"fixed-rule", "user-rule"}) || len(rs.Notices) != 0 {
		t.Errorf("after fix: %v %v", got, noticeKinds(rs))
	}
	e.write(model.LayerRepo, rulesJSON("", on("fixed-rule"), on("bad id")))
	e.bump(model.LayerRepo, 4*time.Second)
	if got := ids(e.load()); !reflect.DeepEqual(got, []string{"fixed-rule", "user-rule"}) {
		t.Errorf("LKG not refreshed to the fixed file: %v", got)
	}
}

func TestLoadInvalidLayerWithoutLKG(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
	e.write(model.LayerRepo, `{"version":2,"rules":[]}`)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	rs := e.load()
	if got := ids(rs); !reflect.DeepEqual(got, []string{"org-rule", "user-rule"}) {
		t.Errorf("other layers must be unaffected: %v", got)
	}
	src := source(rs, model.LayerRepo)
	if src.Status != model.LayerInvalid || src.Error == "" || src.Hash != "" {
		t.Errorf("source = %+v", src)
	}
	if want := []string{"config_invalid/repo/"}; !reflect.DeepEqual(noticeKinds(rs), want) {
		t.Errorf("notices = %v", noticeKinds(rs))
	}
	if !strings.Contains(e.stderr.String(), "no last-known-good") {
		t.Errorf("stderr = %q", e.stderr.String())
	}
}

func TestLoadCorruptLKGIsIgnored(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.load()
	lkgs, _ := filepath.Glob(filepath.Join(e.lay.LKGDir, "user-*.json"))
	if len(lkgs) != 1 {
		t.Fatalf("LKG files = %v", lkgs)
	}
	if err := os.WriteFile(lkgs[0], []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.write(model.LayerUser, `{"version":9}`)
	e.bump(model.LayerUser, time.Second)
	rs := e.load()
	if len(rs.Rules) != 0 || source(rs, model.LayerUser).Status != model.LayerInvalid {
		t.Errorf("rules=%v status=%s", ids(rs), source(rs, model.LayerUser).Status)
	}
}

func TestLoadNoCacheStillReadsLKG(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.load() // writes the LKG
	e.opt.NoCache = true
	e.write(model.LayerUser, `{"version":9}`)
	e.bump(model.LayerUser, time.Second)
	rs := e.load()
	if !reflect.DeepEqual(ids(rs), []string{"user-rule"}) || source(rs, model.LayerUser).Status != model.LayerLKG {
		t.Errorf("rules=%v status=%s", ids(rs), source(rs, model.LayerUser).Status)
	}
}

func TestLoadRepoSameAsUserIsLoadedOnce(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	// cwd == $HOME outside git: <cwd>/.ballast/rules.json IS the user file.
	rs, err := e.loader().Load(model.LoadRequest{Cwd: e.sb.Home})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 1 || rs.Rules[0].Layer != model.LayerUser || len(rs.Notices) != 0 {
		t.Errorf("rules=%v layer=%s notices=%v", ids(rs), rs.Rules[0].Layer, noticeKinds(rs))
	}
	if s := source(rs, model.LayerRepo); s.Status != model.LayerMissing || s.Stat.Exists {
		t.Errorf("repo source = %+v", s)
	}
}

func TestLoadWithoutRepoUsesCwd(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerRepo, rulesJSON("", on("cwd-rule")))
	rs, err := e.loader().Load(model.LoadRequest{Cwd: e.repo})
	if err != nil || !reflect.DeepEqual(ids(rs), []string{"cwd-rule"}) {
		t.Errorf("rules=%v err=%v", ids(rs), err)
	}
}

func TestLoadUnexpectedIOErrors(t *testing.T) {
	t.Run("read failure", func(t *testing.T) {
		e := newEnv(t)
		e.write(model.LayerUser, rulesJSON("", on("aaa-rule")))
		e.opt.FS = sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"ReadFile": "rules.json"}, Err: errors.New("EIO")}
		if _, err := e.loader().Load(e.req()); err == nil || !strings.Contains(err.Error(), "EIO") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no compiler", func(t *testing.T) {
		e := newEnv(t)
		e.opt.Compiler = nil
		if _, err := e.loader().Load(e.req()); err == nil {
			t.Error("expected an error")
		}
	})
}

type failingCompiler struct{ fakeCompiler }

func (*failingCompiler) Compile([]model.LoadedRule, model.Settings, string) (*model.CompiledRuleSet, error) {
	return nil, errors.New("compile exploded")
}

func TestLoadCompileErrorIsReturned(t *testing.T) {
	e := newEnv(t)
	e.opt.Compiler = &failingCompiler{}
	if _, err := e.loader().Load(e.req()); err == nil || !strings.Contains(err.Error(), "compile exploded") {
		t.Errorf("err = %v", err)
	}
}

func TestExplain(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON("", on("org-rule"), off("org-off-rule")))
	var repo []spec
	repo = append(repo, on("org-rule"))
	for i := 0; i < 12; i++ {
		repo = append(repo, on("repo-"+string(rune('a'+i))+"-rule"))
	}
	e.write(model.LayerRepo, rulesJSON("", repo...))
	e.write(model.LayerUser, `{"version":1,"rules":[{"id":"BAD ID","event":"PreToolUse"}]}`)

	rep, err := e.loader().Explain(e.req())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.lay.CacheDir); !os.IsNotExist(err) {
		t.Errorf("Explain must not touch the cache dir (err=%v)", err)
	}
	if e.stderr.Len() != 0 {
		t.Errorf("Explain wrote to stderr: %q", e.stderr.String())
	}
	count := map[config.Fate]int{}
	for _, r := range rep.Rules {
		count[r.Fate]++
		if r.Source == "" || r.Event != model.EventPreToolUse || r.Action != model.ActionWarn || r.Target != model.ActionDeny {
			t.Errorf("row %+v", r)
		}
	}
	// org: 1 active + 1 disabled; repo: org-rule shadowed, 11 active, 1 dropped (12 cap).
	if count[config.FateActive] != 12 || count[config.FateDisabled] != 1 || count[config.FateShadowed] != 1 || count[config.FateDropped] != 1 {
		t.Errorf("fates = %v", count)
	}
	if last := rep.Rules[len(rep.Rules)-1]; last.Fate != config.FateDropped || last.Note == "" || last.Layer != model.LayerRepo {
		t.Errorf("last row = %+v", last)
	}
	if rep.Rules[1].Note != "enabled=false" {
		t.Errorf("disabled note = %q", rep.Rules[1].Note)
	}
	kinds := noticeKinds(&model.CompiledRuleSet{Notices: rep.Notices})
	want := []string{"config_invalid/user/", "rule_shadowed/repo/org-rule", "rule_dropped/repo/repo-l-rule"}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("notices = %v, want %v", kinds, want)
	}
	if len(rep.Sources) != 3 || rep.Sources[2].Status != model.LayerInvalid {
		t.Errorf("sources = %+v", rep.Sources)
	}
	// The explain generation is the loader's generation.
	if rs := e.load(); rs.Generation != rep.Generation {
		t.Errorf("explain generation %s != load generation %s", rep.Generation, rs.Generation)
	}
}

func TestExplainShowsLKGWithoutWritingIt(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.load()
	e.write(model.LayerUser, `{"version":3}`)
	e.bump(model.LayerUser, time.Second)
	rep, err := e.loader().Explain(e.req())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rules) != 1 || rep.Sources[2].Status != model.LayerLKG {
		t.Errorf("rules=%+v sources=%+v", rep.Rules, rep.Sources)
	}
}
