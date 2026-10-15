package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// statFailFS makes Stat and ReadFile of paths containing sub fail with err.
type statFailFS struct {
	sys.FS
	sub string
	err error
}

func (f statFailFS) ReadFile(n string) ([]byte, error) {
	if strings.Contains(n, f.sub) {
		return nil, &fs.PathError{Op: "open", Path: n, Err: f.err}
	}
	return f.FS.ReadFile(n)
}

func (f statFailFS) Stat(n string) (fs.FileInfo, error) {
	if strings.Contains(n, f.sub) {
		return nil, &fs.PathError{Op: "stat", Path: n, Err: f.err}
	}
	return f.FS.Stat(n)
}

// A bad layer location (directory, no permission) must degrade that layer
// only: org rules stay enforced and Load does not fail.
func TestLoadUnusableLayerKeepsOrgRules(t *testing.T) {
	asDir := func(e *env, l model.Layer) {
		if err := os.MkdirAll(e.path(l), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	noPerm := func(e *env, l model.Layer) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("file permissions are not enforced here")
		}
		e.write(l, rulesJSON("", on("hidden-rule")))
		if err := os.Chmod(e.path(l), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(e.path(l), 0o600) })
	}
	statDenied := func(e *env, l model.Layer) {
		e.write(l, rulesJSON("", on("hidden-rule")))
		e.opt.FS = statFailFS{FS: sys.OSFS{}, sub: e.path(l), err: syscall.EACCES}
	}
	for _, tc := range []struct {
		name   string
		layer  model.Layer
		break_ func(*env, model.Layer)
	}{
		{"user dir", model.LayerUser, asDir},
		{"repo dir", model.LayerRepo, asDir},
		{"user no permission", model.LayerUser, noPerm},
		{"repo no permission", model.LayerRepo, noPerm},
		{"repo stat denied", model.LayerRepo, statDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
			tc.break_(e, tc.layer)
			// Unusable layers are never cached (a fixed permission changes no
			// stat key), except that a directory is still a stable state.
			for i := 0; i < 2; i++ {
				rs := e.load()
				if !reflect.DeepEqual(ids(rs), []string{"org-rule"}) {
					t.Fatalf("pass %d: rules = %v", i, ids(rs))
				}
				if s := source(rs, tc.layer); s.Status != model.LayerInvalid || s.Error == "" {
					t.Errorf("pass %d: source = %+v", i, s)
				}
				if got := noticeKinds(rs); len(got) != 1 || !strings.HasPrefix(got[0], "config_invalid/"+string(tc.layer)) {
					t.Errorf("pass %d: notices = %v", i, got)
				}
			}
			if !strings.Contains(e.stderr.String(), "ignored") {
				t.Errorf("stderr = %q", e.stderr.String())
			}
		})
	}
}

// winDirFS reports reading a directory the way Windows does: neither EISDIR
// nor a permission error.
type winDirFS struct{ sys.FS }

func (f winDirFS) ReadFile(n string) ([]byte, error) {
	if fi, err := f.FS.Stat(n); err == nil && fi.IsDir() {
		return nil, &fs.PathError{Op: "read", Path: n, Err: errors.New("Incorrect function.")}
	}
	return f.FS.ReadFile(n)
}

func TestLoadDirectoryWithForeignReadError(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
	if err := os.MkdirAll(e.path(model.LayerUser), 0o700); err != nil {
		t.Fatal(err)
	}
	e.opt.FS = winDirFS{FS: sys.OSFS{}}
	rs := e.load()
	if !reflect.DeepEqual(ids(rs), []string{"org-rule"}) {
		t.Fatalf("rules = %v", ids(rs))
	}
	if s := source(rs, model.LayerUser); s.Status != model.LayerInvalid || s.Error == "" {
		t.Errorf("source = %+v", s)
	}
}

// Fixing a permission changes no stat key, so a layer that was unreadable
// must be read again on the next load.
func TestLoadRecoversAfterPermissionFixed(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file permissions are not enforced here")
	}
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
	p := e.path(model.LayerOrg)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	rs := e.load()
	if len(rs.Rules) != 0 || source(rs, model.LayerOrg).Status != model.LayerInvalid {
		t.Fatalf("unreadable: rules = %v, source = %+v", ids(rs), source(rs, model.LayerOrg))
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	e.stderr.Reset()
	rs = e.load()
	if !reflect.DeepEqual(ids(rs), []string{"org-rule"}) || source(rs, model.LayerOrg).Status != model.LayerOK {
		t.Fatalf("fixed: rules = %v, source = %+v", ids(rs), source(rs, model.LayerOrg))
	}
	if e.stderr.Len() != 0 || len(rs.Notices) != 0 {
		t.Errorf("stale diagnostics: stderr = %q, notices = %v", e.stderr.String(), rs.Notices)
	}
	before := e.comp.parses.Load()
	e.load()
	if e.comp.parses.Load() != before {
		t.Error("a healthy load must be cached again")
	}
}

// A refused stat yields a synthetic stat key; the result must not be cached
// under it, or the recovered layer would stay hidden.
func TestLoadRecoversAfterStatDenied(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
	good := e.opt.FS
	e.opt.FS = statFailFS{FS: sys.OSFS{}, sub: e.path(model.LayerOrg), err: syscall.EACCES}
	if rs := e.load(); len(rs.Rules) != 0 {
		t.Fatalf("denied: rules = %v", ids(rs))
	}
	e.opt.FS = good
	if rs := e.load(); !reflect.DeepEqual(ids(rs), []string{"org-rule"}) {
		t.Fatalf("recovered: rules = %v", ids(rs))
	}
}

func TestLoadDirectoryFallsBackToLKG(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerOrg, rulesJSON("", on("org-rule")))
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.load() // writes the LKG
	if err := os.Remove(e.path(model.LayerUser)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.path(model.LayerUser), 0o700); err != nil {
		t.Fatal(err)
	}
	rs := e.load()
	if got := ids(rs); !reflect.DeepEqual(got, []string{"org-rule", "user-rule"}) {
		t.Errorf("rules = %v", got)
	}
	if s := source(rs, model.LayerUser); s.Status != model.LayerLKG {
		t.Errorf("status = %s", s.Status)
	}
}

func TestLoadStatIOErrorIsStillAnError(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.opt.FS = statFailFS{FS: sys.OSFS{}, sub: e.path(model.LayerUser), err: syscall.EIO}
	if _, err := e.loader().Load(e.req()); err == nil || !errors.Is(err, syscall.EIO) {
		t.Errorf("err = %v", err)
	}
}

// Two load contexts that differ only in layers that do not exist share one
// rules-<generation>.json; after warm-up neither may miss.
func TestLoadCacheHitsWhenOnlyMissingLayersDiffer(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	repoA, repoB := e.repo, filepath.Join(e.sb.Root, "work", "other")
	if err := os.MkdirAll(repoB, 0o700); err != nil {
		t.Fatal(err)
	}
	reqs := []model.LoadRequest{
		{Client: model.ClientClaude, Cwd: repoA, Repo: &model.RepoInfo{Toplevel: repoA}},
		{Client: model.ClientClaude, Cwd: repoB, Repo: &model.RepoInfo{Toplevel: repoB}},
	}
	for _, r := range reqs {
		if _, err := e.loader().Load(r); err != nil {
			t.Fatal(err)
		}
	}
	parses, compiles := e.comp.parses.Load(), e.comp.compiles.Load()
	e.stderr.Reset()
	for i := 0; i < 6; i++ {
		r := reqs[i%2]
		rs, err := e.loader().Load(r)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(rs); !reflect.DeepEqual(got, []string{"user-rule"}) {
			t.Fatalf("rules = %v", got)
		}
		wantRepo := filepath.Join(r.Repo.Toplevel, ".ballast", "rules.json")
		if s := source(rs, model.LayerRepo); s.Path != wantRepo || s.Stat.Path != wantRepo || s.Status != model.LayerMissing {
			t.Errorf("repo source = %+v, want path %s", s, wantRepo)
		}
	}
	if n := e.comp.parses.Load(); n != parses {
		t.Errorf("alternating loads parsed %d more times", n-parses)
	}
	if n := e.comp.compiles.Load() - compiles; n != 6 {
		t.Errorf("compiles = %d, want 6 (one per hit)", n)
	}
}

// Loads with an existing repo layer in one repo and none in the other must
// never serve each other's sources.
func TestLoadCacheDoesNotMixExistingLayers(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	repoB := filepath.Join(e.sb.Root, "work", "other")
	if err := os.MkdirAll(filepath.Join(repoB, ".ballast"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoB, ".ballast", "rules.json"), []byte(rulesJSON("", on("repo-b"))), 0o600); err != nil {
		t.Fatal(err)
	}
	reqB := model.LoadRequest{Client: model.ClientClaude, Cwd: repoB, Repo: &model.RepoInfo{Toplevel: repoB}}
	for i := 0; i < 3; i++ {
		if got := ids(e.load()); !reflect.DeepEqual(got, []string{"user-rule"}) {
			t.Fatalf("A rules = %v", got)
		}
		rs, err := e.loader().Load(reqB)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(rs); !reflect.DeepEqual(got, []string{"repo-b", "user-rule"}) {
			t.Fatalf("B rules = %v", got)
		}
	}
}

func TestLoadReportsDroppedRulesOnMissAndHit(t *testing.T) {
	e := newEnv(t)
	var specs []spec
	for i := 0; i < 14; i++ {
		specs = append(specs, on("org-rule-"+string(rune('a'+i))))
	}
	e.write(model.LayerOrg, rulesJSON("", specs...))
	for pass := 0; pass < 2; pass++ {
		e.stderr.Reset()
		rs := e.load()
		if len(rs.Rules) != model.MaxEnabledRules {
			t.Fatalf("rules = %d", len(rs.Rules))
		}
		out := e.stderr.String()
		if strings.Count(out, "dropped") != 2 {
			t.Errorf("pass %d: stderr = %q", pass, out)
		}
	}
}

func TestLoadReportsInvalidLayerOnCacheHit(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, `{"version":9}`)
	for pass := 0; pass < 2; pass++ {
		e.stderr.Reset()
		e.load()
		if out := e.stderr.String(); strings.Count(out, "no last-known-good") != 1 {
			t.Errorf("pass %d: stderr = %q", pass, out)
		}
	}
	if e.comp.parses.Load() != 1 {
		t.Errorf("parses = %d, the second load must be a cache hit", e.comp.parses.Load())
	}
	// With an LKG the layer is served, and a hit stays quiet.
	e2 := newEnv(t)
	e2.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e2.load()
	e2.write(model.LayerUser, `{"version":9}`)
	e2.bump(model.LayerUser, time.Second)
	e2.load()
	e2.stderr.Reset()
	e2.load()
	if e2.stderr.Len() != 0 {
		t.Errorf("stderr on LKG hit = %q", e2.stderr.String())
	}
}

// A failed LKG write must not be cached over, or it would never be retried.
func TestLoadLKGWriteFailureIsRetried(t *testing.T) {
	e := newEnv(t)
	e.write(model.LayerUser, rulesJSON("", on("user-rule")))
	e.opt.FS = sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"Rename": "lkg"}, Err: errors.New("disk full")}
	e.load()
	e.load()
	if n := e.comp.parses.Load(); n != 2 {
		t.Errorf("parses = %d, want 2 (no cache entry while the LKG is missing)", n)
	}
	if !strings.Contains(e.stderr.String(), "last-known-good") {
		t.Errorf("stderr = %q", e.stderr.String())
	}
	// Once the disk recovers, the next load writes the LKG and the cache.
	e.opt.FS = sys.OSFS{}
	e.load()
	if files, _ := filepath.Glob(filepath.Join(e.lay.LKGDir, "user-*.json")); len(files) != 1 {
		t.Fatalf("LKG files = %v", files)
	}
	before := e.comp.parses.Load()
	e.load()
	if e.comp.parses.Load() != before {
		t.Error("a successful load must be cached")
	}
	// The layer breaking now falls back to the LKG.
	e.write(model.LayerUser, `{"version":9}`)
	e.bump(model.LayerUser, time.Second)
	if rs := e.load(); !reflect.DeepEqual(ids(rs), []string{"user-rule"}) {
		t.Errorf("rules = %v", ids(rs))
	}
}
