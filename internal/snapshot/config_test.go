package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

type cfgKey struct {
	section, sub, key, raw string
	hasEq                  bool
}

func scanAll(src string) []cfgKey {
	var out []cfgKey
	scanConfig([]byte(src), func(section, sub, key, raw string, hasEq bool) {
		out = append(out, cfgKey{section, sub, key, raw, hasEq})
	})
	return out
}

func TestScanConfig(t *testing.T) {
	tests := []struct {
		name, src string
		want      []cfgKey
	}{
		{"basic", "[core]\n\tfilemode = false\n\tBare=true\n", []cfgKey{{"core", "", "filemode", "false", true}, {"core", "", "bare", "true", true}}},
		{"subsection", "[remote \"origin\"]\n\turl = u\n", []cfgKey{{"remote", "origin", "url", "u", true}}},
		{"subsection escapes", `[url "a\"b\\c"]` + "\n\tinsteadOf = x\n", []cfgKey{{"url", `a"b\c`, "insteadof", "x", true}}},
		{"legacy subsection is lower-cased", "[remote.Origin]\nurl=u\n", []cfgKey{{"remote", "origin", "url", "u", true}}},
		{"key on header line", "[core] bare = true\n", []cfgKey{{"core", "", "bare", "true", true}}},
		{"bare key", "[core]\n\tbare\n", []cfgKey{{"core", "", "bare", "", false}}},
		{"comments", "# c\n; c\n[core] # c\n\tbare = true # c\n", []cfgKey{{"core", "", "bare", "true # c", true}}},
		{"crlf", "[core]\r\n\tbare = true\r\n", []cfgKey{{"core", "", "bare", "true", true}}},
		{"continuation", "[core]\n\tx = a\\\nb\n\ty = z\n", []cfgKey{{"core", "", "x", "ab", true}, {"core", "", "y", "z", true}}},
		{"keys before any section are ignored", "x = 1\n[core]\n\ty = 2\n", []cfgKey{{"core", "", "y", "2", true}}},
		{"garbage line skipped", "[core]\n\t= nope\n\ty = 2\n", []cfgKey{{"core", "", "y", "2", true}}},
		{"unterminated header resets", "[core\n\ty = 2\n[ok]\n\tz = 3\n", []cfgKey{{"ok", "", "z", "3", true}}},
		{"empty value", "[core]\n\tx =\n", []cfgKey{{"core", "", "x", "", true}}},
		{"value with equals", "[core]\n\tx = a=b\n", []cfgKey{{"core", "", "x", "a=b", true}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanAll(tc.src); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestCfgStringAndBool(t *testing.T) {
	strs := map[string]string{
		`plain`:             `plain`,
		`  padded  `:        `padded`,
		`"quoted value"`:    `quoted value`,
		`a # comment`:       `a`,
		`a ; comment`:       `a`,
		`"a # not comment"`: `a # not comment`,
		`"a" b`:             `a b`,
		`a\tb`:              "a\tb",
		`a\\b`:              `a\b`,
		`"unterminated`:     `unterminated`,
		`x "  keep  " y`:    `x   keep   y`,
	}
	for in, want := range strs {
		if got := cfgString(in); got != want {
			t.Errorf("cfgString(%q) = %q, want %q", in, got, want)
		}
	}
	bools := []struct {
		raw   string
		hasEq bool
		val   bool
		ok    bool
	}{
		{"", false, true, true}, {"", true, false, true}, {"true", true, true, true}, {"Yes", true, true, true},
		{"on", true, true, true}, {"false", true, false, true}, {"NO", true, false, true}, {"off", true, false, true},
		{"1", true, true, true}, {"0", true, false, true}, {"2", true, true, true}, {"1k", true, true, true},
		{"input", true, false, false}, {"maybe", true, false, false}, {"true # c", true, true, true},
	}
	for _, tc := range bools {
		v, ok := cfgBool(tc.raw, tc.hasEq)
		if v != tc.val || ok != tc.ok {
			t.Errorf("cfgBool(%q, %v) = %v,%v want %v,%v", tc.raw, tc.hasEq, v, ok, tc.val, tc.ok)
		}
	}
}

func TestScanAttributes(t *testing.T) {
	tests := []struct {
		name, src string
		want      attrFlags
	}{
		{"empty", "", attrFlags{}},
		{"comments only", "# * text=auto\n\n", attrFlags{}},
		{"text=auto", "* text=auto\n", attrFlags{soft: true}},
		{"-text", "*.png -text\n", attrFlags{soft: true}},
		{"binary macro does not convert", "*.png binary\n", attrFlags{}},
		{"eol", "*.sh eol=lf\n", attrFlags{soft: true}},
		{"legacy crlf", "*.txt crlf\n", attrFlags{soft: true}},
		{"filter", "*.bin filter=lfs diff=lfs merge=lfs -text\n", attrFlags{hard: true, soft: true}},
		{"unset filter still counts", "*.bin -filter\n", attrFlags{hard: true}},
		{"ident", "*.c ident\n", attrFlags{hard: true}},
		{"encoding", "*.txt working-tree-encoding=UTF-16\n", attrFlags{hard: true}},
		{"quoted pattern", "\"a b/*.dat\" filter=x\n", attrFlags{hard: true}},
		{"macro definition", "[attr]lfs filter=lfs diff=lfs\n*.bin lfs\n", attrFlags{hard: true}},
		{"unrelated attributes", "*.go diff=golang export-ignore linguist-vendored\n", attrFlags{}},
		{"attribute named like a prefix", "*.go textual filtered\n", attrFlags{}},
		{"pattern only", "*.go\n", attrFlags{}},
		{"tabs", "*.go\ttext\teol=crlf\n", attrFlags{soft: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanAttributes([]byte(tc.src)); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// settingsEnv returns an Env whose global/system lookups stay in dir.
func settingsEnv(dir string, extra map[string]string) sys.MapEnv {
	vars := map[string]string{
		"HOME": dir, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(dir, ".gitconfig"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "xdg"),
	}
	for k, v := range extra {
		vars[k] = v
	}
	return sys.MapEnv{Vars: vars}
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRepo is a minimal on-disk git dir for settings tests.
func fakeRepo(t testing.TB, localConfig string) (*model.RepoInfo, string) {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(root, "repo", ".git")
	writeFile(t, filepath.Join(gitDir, "config"), localConfig)
	return &model.RepoInfo{Toplevel: filepath.Join(root, "repo"), GitDir: gitDir, CommonDir: gitDir, IndexPath: filepath.Join(gitDir, "index"), ObjectFormat: "sha1"}, root
}

func TestLoadSettings(t *testing.T) {
	type check func(t *testing.T, s *repoSettings)
	conv := func(want bool) check {
		return func(t *testing.T, s *repoSettings) {
			t.Helper()
			if s.convRisk != want {
				t.Errorf("convRisk = %v, want %v", s.convRisk, want)
			}
		}
	}
	tests := []struct {
		name   string
		local  string
		global string
		xdg    string
		extra  map[string]string
		env    func(dir string) sys.Env // nil ⇒ settingsEnv
		check  check
	}{
		{name: "defaults", check: func(t *testing.T, s *repoSettings) {
			if !s.fileMode || !s.symlinks || s.convRisk || s.safeCRLF || s.uncertain != "" || s.objectFormat != "sha1" {
				t.Errorf("%+v", s)
			}
		}},
		{name: "autocrlf true", local: "[core]\n\tautocrlf = true\n", check: conv(true)},
		{name: "autocrlf input", local: "[core]\n\tautocrlf = input\n", check: conv(true)},
		{name: "autocrlf false", local: "[core]\n\tautocrlf = false\n", check: conv(false)},
		{name: "autocrlf bare key means true", local: "[core]\n\tautocrlf\n", check: conv(true)},
		{name: "autocrlf global", global: "[core]\n\tautocrlf = true\n", check: conv(true)},
		{name: "autocrlf xdg", xdg: "[core]\n\tautocrlf = true\n", env: func(dir string) sys.Env {
			e := settingsEnv(dir, nil)
			delete(e.Vars, "GIT_CONFIG_GLOBAL") // otherwise it replaces the XDG file
			return e
		}, check: conv(true)},
		{name: "GIT_CONFIG_GLOBAL replaces the default global files", global: "", xdg: "[core]\n\tautocrlf = true\n", check: conv(false)},
		{name: "local overrides global", global: "[core]\n\tautocrlf = true\n", local: "[core]\n\tautocrlf = false\n", check: conv(false)},
		{name: "global overrides nothing when local is false then true", global: "[core]\n\tautocrlf = false\n", local: "[core]\n\tautocrlf = input\n", check: conv(true)},
		{name: "eol set", local: "[core]\n\teol = crlf\n", check: conv(true)},
		{name: "eol empty", local: "[core]\n\teol =\n", check: conv(false)},
		{name: "bad autocrlf is uncertain", local: "[core]\n\tautocrlf = sometimes\n", check: func(t *testing.T, s *repoSettings) {
			if s.uncertain == "" {
				t.Error("want uncertain")
			}
		}},
		{name: "safecrlf warn", local: "[core]\n\tsafecrlf = warn\n", check: func(t *testing.T, s *repoSettings) {
			if s.safeCRLF {
				t.Error("warn must not count as strict")
			}
		}},
		{name: "safecrlf true", local: "[core]\n\tsafecrlf = true\n", check: func(t *testing.T, s *repoSettings) {
			if !s.safeCRLF {
				t.Error("want strict")
			}
		}},
		{name: "filemode false", local: "[core]\n\tfilemode = false\n", check: func(t *testing.T, s *repoSettings) {
			if s.fileMode {
				t.Error("filemode")
			}
		}},
		{name: "symlinks false", global: "[core]\n\tsymlinks = false\n", check: func(t *testing.T, s *repoSettings) {
			if s.symlinks {
				t.Error("symlinks")
			}
		}},
		{name: "windows defaults filemode off", env: func(dir string) sys.Env {
			e := settingsEnv(dir, nil)
			e.OS = "windows"
			return e
		}, check: func(t *testing.T, s *repoSettings) {
			if s.fileMode {
				t.Error("core.filemode must default to false on windows")
			}
		}},
		{name: "windows config can turn filemode on", local: "[core]\n\tfilemode = true\n", env: func(dir string) sys.Env {
			e := settingsEnv(dir, nil)
			e.OS = "windows"
			return e
		}, check: func(t *testing.T, s *repoSettings) {
			if !s.fileMode {
				t.Error("filemode")
			}
		}},
		{name: "objectformat", local: "[extensions]\n\tobjectformat = sha256\n", check: func(t *testing.T, s *repoSettings) {
			if s.objectFormat != "sha256" {
				t.Error(s.objectFormat)
			}
		}},
		{name: "include relative", global: "[include]\n\tpath = extra.cfg\n", check: conv(true)},
		{name: "include home", global: "[include]\n\tpath = ~/home.cfg\n", check: conv(true)},
		{name: "includeIf with relevant key is uncertain", global: "[includeIf \"gitdir:~/work/\"]\n\tpath = extra.cfg\n", check: func(t *testing.T, s *repoSettings) {
			if !strings.Contains(s.uncertain, "core.autocrlf") {
				t.Errorf("uncertain = %q", s.uncertain)
			}
		}},
		{name: "includeIf with irrelevant keys is fine", global: "[includeIf \"gitdir:~/work/\"]\n\tpath = identity.cfg\n", check: func(t *testing.T, s *repoSettings) {
			if s.uncertain != "" {
				t.Errorf("uncertain = %q", s.uncertain)
			}
		}},
		{name: "local include", local: "[include]\n\tpath = ../local-extra\n", check: conv(true)},
		{name: "env override is uncertain", extra: map[string]string{"GIT_CONFIG_PARAMETERS": "'core.autocrlf=true'"}, check: func(t *testing.T, s *repoSettings) {
			if s.uncertain == "" {
				t.Error("want uncertain")
			}
		}},
		{name: "env override unrelated is fine", extra: map[string]string{"GIT_CONFIG_PARAMETERS": "'user.name=x'"}, check: func(t *testing.T, s *repoSettings) {
			if s.uncertain != "" {
				t.Error(s.uncertain)
			}
		}},
		{name: "env count", extra: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.eol", "GIT_CONFIG_VALUE_0": "crlf"}, check: func(t *testing.T, s *repoSettings) {
			if s.uncertain == "" {
				t.Error("want uncertain")
			}
		}},
		{name: "include cycle terminates", global: "[include]\n\tpath = .gitconfig\n", check: func(t *testing.T, s *repoSettings) {}},
		{name: "attributesfile", local: "[core]\n\tattributesfile = ~/.myattrs\n", check: func(t *testing.T, s *repoSettings) {
			if len(s.attrFiles) == 0 || !strings.HasSuffix(s.attrFiles[0], ".myattrs") || strings.HasPrefix(s.attrFiles[0], "~") {
				t.Errorf("attrFiles = %v", s.attrFiles)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, root := fakeRepo(t, tc.local)
			writeFile(t, filepath.Join(root, ".gitconfig"), tc.global)
			if tc.xdg != "" {
				writeFile(t, filepath.Join(root, "xdg", "git", "config"), tc.xdg)
			}
			writeFile(t, filepath.Join(root, "extra.cfg"), "[core]\n\tautocrlf = true\n")
			writeFile(t, filepath.Join(root, "home.cfg"), "[core]\n\tautocrlf = true\n")
			writeFile(t, filepath.Join(root, "repo", "local-extra"), "[core]\n\tautocrlf = true\n")
			writeFile(t, filepath.Join(root, "identity.cfg"), "[user]\n\tname = me\n")
			var env sys.Env = settingsEnv(root, tc.extra)
			if tc.env != nil {
				env = tc.env(root)
			}
			// ~/home.cfg and ~/.myattrs resolve against HOME = root.
			tc.check(t, loadSettings(sys.OSFS{}, env, repo))
		})
	}
	t.Run("nil env reads only the repository config", func(t *testing.T) {
		repo, root := fakeRepo(t, "")
		writeFile(t, filepath.Join(root, ".gitconfig"), "[core]\n\tautocrlf = true\n")
		t.Setenv("HOME", root)
		if s := loadSettings(sys.OSFS{}, nil, repo); s.convRisk {
			t.Error("global config must not be read without an Env")
		}
	})
	t.Run("config.worktree is read", func(t *testing.T) {
		repo, _ := fakeRepo(t, "")
		writeFile(t, filepath.Join(repo.GitDir, "config.worktree"), "[core]\n\tautocrlf = true\n")
		if s := loadSettings(sys.OSFS{}, nil, repo); !s.convRisk {
			t.Error("config.worktree ignored")
		}
	})
}

func TestSystemConfigPaths(t *testing.T) {
	if got := systemConfigPaths(sys.MapEnv{Vars: map[string]string{"GIT_CONFIG_NOSYSTEM": "1"}}); got != nil {
		t.Errorf("NOSYSTEM: %v", got)
	}
	if got := systemConfigPaths(sys.MapEnv{Vars: map[string]string{"GIT_CONFIG_SYSTEM": "/x/y"}}); len(got) != 1 || got[0] != "/x/y" {
		t.Errorf("SYSTEM: %v", got)
	}
	if got := systemConfigPaths(sys.MapEnv{Vars: map[string]string{"GIT_CONFIG_SYSTEM": ""}}); got != nil {
		t.Errorf("empty SYSTEM: %v", got)
	}
	if got := systemConfigPaths(sys.MapEnv{}); len(got) == 0 || got[0] != "/etc/gitconfig" {
		t.Errorf("default: %v", got)
	}
	win := sys.MapEnv{OS: "windows", Vars: map[string]string{"ProgramFiles": `C:\Program Files`, "ProgramData": `C:\ProgramData`}}
	if got := systemConfigPaths(win); len(got) != 2 {
		t.Errorf("windows: %v", got)
	}
	if got := globalConfigPaths(sys.MapEnv{Vars: map[string]string{"GIT_CONFIG_GLOBAL": ""}}); got != nil {
		t.Errorf("empty GLOBAL: %v", got)
	}
	got := globalConfigPaths(sys.MapEnv{Vars: map[string]string{"HOME": "/h"}})
	if len(got) != 2 || got[0] != filepath.Join("/h", ".config", "git", "config") || got[1] != filepath.Join("/h", ".gitconfig") {
		t.Errorf("default global: %v", got)
	}
}

func TestAttrResolver(t *testing.T) {
	repo, root := fakeRepo(t, "")
	top := repo.Toplevel
	writeFile(t, filepath.Join(top, ".gitattributes"), "*.go diff=golang\n")
	writeFile(t, filepath.Join(top, "a", ".gitattributes"), "*.txt text\n")
	writeFile(t, filepath.Join(top, "a", "b", "c", ".gitattributes"), "*.bin filter=x\n")
	writeFile(t, filepath.Join(repo.GitDir, "info", "attributes"), "")
	env := settingsEnv(root, nil)
	set := loadSettings(sys.OSFS{}, env, repo)
	r := newAttrResolver(sys.OSFS{}, repo, set, nil)
	for dir, want := range map[string]attrFlags{
		"":        {},
		"x":       {},
		"a":       {soft: true},
		"a/b":     {soft: true},
		"a/b/c":   {soft: true, hard: true},
		"a/b/c/d": {soft: true, hard: true},
		"a/z":     {soft: true},
	} {
		got, err := r.flagsFor(dir)
		if err != nil || got != want {
			t.Errorf("flagsFor(%q) = %+v, %v; want %+v", dir, got, err, want)
		}
	}
	// Sources outside the work tree.
	writeFile(t, filepath.Join(repo.GitDir, "info", "attributes"), "* ident\n")
	if got, _ := newAttrResolver(sys.OSFS{}, repo, set, nil).flagsFor("x"); !got.hard {
		t.Error("info/attributes ignored")
	}
	writeFile(t, filepath.Join(root, "xdg", "git", "attributes"), "* text=auto\n")
	set = loadSettings(sys.OSFS{}, env, repo)
	if got, _ := newAttrResolver(sys.OSFS{}, repo, set, nil).flagsFor("x"); !got.soft {
		t.Error("global attributes ignored")
	}
}

func TestNeedsFallback(t *testing.T) {
	tests := []struct {
		name     string
		local    string
		global   string
		files    map[string]string // relative to the work tree
		format   string
		wantNeed bool
		wantWhy  string
		envOnly  bool // only visible through NeedsFallbackEnv
	}{
		{name: "clean"},
		{name: "sha256 by RepoInfo", format: "sha256", wantNeed: true, wantWhy: "sha256"},
		{name: "sha256 by config", local: "[extensions]\n\tobjectformat = sha256\n", wantNeed: true, wantWhy: "sha256"},
		{name: "autocrlf true", local: "[core]\n\tautocrlf = true\n", wantNeed: true, wantWhy: "autocrlf"},
		{name: "autocrlf input", local: "[core]\n\tautocrlf = input\n", wantNeed: true, wantWhy: "autocrlf"},
		{name: "autocrlf false", local: "[core]\n\tautocrlf = false\n"},
		{name: "eol", local: "[core]\n\teol = lf\n", wantNeed: true, wantWhy: "eol"},
		{name: "safecrlf", local: "[core]\n\tsafecrlf = true\n", wantNeed: true, wantWhy: "safecrlf"},
		{name: "symlinks false", local: "[core]\n\tsymlinks = false\n", wantNeed: true, wantWhy: "symlinks"},
		{name: "filemode false is fine", local: "[core]\n\tfilemode = false\n"},
		{name: "root text attribute", files: map[string]string{".gitattributes": "* text=auto\n"}, wantNeed: true, wantWhy: "text"},
		{name: "root filter attribute", files: map[string]string{".gitattributes": "*.x filter=lfs\n"}, wantNeed: true, wantWhy: "filter"},
		{name: "plain attributes are fine", files: map[string]string{".gitattributes": "*.go diff=golang\n"}},
		{name: "info attributes", files: map[string]string{".git/info/attributes": "*.x ident\n"}, wantNeed: true, wantWhy: "filter"},
		{name: "global autocrlf only through Env", global: "[core]\n\tautocrlf = true\n", wantNeed: true, wantWhy: "autocrlf", envOnly: true},
		{name: "global attributes only through Env", files: map[string]string{"../xdg/git/attributes": "* eol=crlf\n"}, wantNeed: true, wantWhy: "text", envOnly: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, root := fakeRepo(t, tc.local)
			if tc.format != "" {
				repo.ObjectFormat = tc.format
			}
			writeFile(t, filepath.Join(root, ".gitconfig"), tc.global)
			for rel, content := range tc.files {
				writeFile(t, filepath.Join(repo.Toplevel, rel), content)
			}
			env := settingsEnv(root, nil)
			need, why := NeedsFallbackEnv(sys.OSFS{}, env, repo)
			if need != tc.wantNeed || (need && !strings.Contains(why, tc.wantWhy)) {
				t.Errorf("NeedsFallbackEnv = %v %q, want %v containing %q", need, why, tc.wantNeed, tc.wantWhy)
			}
			need, why = NeedsFallback(sys.OSFS{}, repo)
			wantNeed := tc.wantNeed && !tc.envOnly
			if need != wantNeed {
				t.Errorf("NeedsFallback = %v %q, want %v", need, why, wantNeed)
			}
		})
	}
}

// TestUserConfigDecidesFallback: a global core.autocrlf makes dirty CRLF
// files fall back, and the git fallback sees the same configuration.
func TestUserConfigDecidesFallback(t *testing.T) {
	f := newFixture(t)
	baseRepo(f)
	f.git("config", "--unset", "core.autocrlf")
	writeFile(t, filepath.Join(f.sb.Home, ".gitconfig"), "[core]\n\tautocrlf = true\n")
	f.write("src/a.go", "package x\r\ny\r\n")
	want := f.reference(true, nil, nil)
	if _, err := TreeS(f.opts(false), f.repo, nil); !errors.Is(err, model.ErrFallback) {
		t.Fatalf("native with global autocrlf and CRLF content: %v", err)
	}
	got, err := TreeS(f.opts(true), f.repo, nil)
	if err != nil || got != want {
		t.Errorf("fallback: %s err=%v", describe(got, want), err)
	}
	// LF-only edits stay native even with the global setting.
	f.write("src/a.go", "package lf\n")
	want = f.reference(true, nil, nil)
	if got, err := TreeS(f.opts(false), f.repo, nil); err != nil || got != want {
		t.Errorf("native LF: %s err=%v", describe(got, want), err)
	}
}

func FuzzScanConfig(f *testing.F) {
	for _, s := range []string{
		"[core]\n\tfilemode = false\n", "[remote \"origin\"]\n\turl = x\n", "[core]\n\tx = a\\\nb\n",
		"[a.b]\nc\n", "[\n", "[a \"b\n", "x = 1", "\xff\xfe[core]\n= \\", "[core] # c\n\tbare = \"unterminated\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		scanConfig([]byte(src), func(section, sub, key, raw string, hasEq bool) {
			_ = cfgString(raw)
			_, _ = cfgBool(raw, hasEq)
			if section == "" || key == "" {
				t.Fatalf("empty section or key for %q", src)
			}
		})
		_ = scanAttributes([]byte(src))
	})
}

func TestPathHelpers(t *testing.T) {
	for p, want := range map[string]bool{
		"a": true, "a/b.go": true, "a b/é.txt": true, ".gitattributes": true, "a/.hidden": true, "...": true,
		"": false, "/abs": false, "a/": false, "a//b": false, "./a": false, "a/./b": false, "../a": false, "a/..": false, "a/../b": false,
		"a\x00b": false,
	} {
		if got := safeWorktreePath(p); got != want {
			t.Errorf("safeWorktreePath(%q) = %v, want %v", p, got, want)
		}
	}
	for in, want := range map[string]string{
		"a/b": "a/b", "./a": "a", "a//b": "a/b", "a/./b": "a/b", "a/../b": "b", "a/": "a/", "a/.": "a/", "a/b/..": "a/", ".": "", "./": "", "": "", "a/**/b": "a/**/b",
	} {
		if got, err := normalizeSpec(in); err != nil || got != want {
			t.Errorf("normalizeSpec(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"..", "../a", "a/../../b"} {
		if _, err := normalizeSpec(bad); err == nil {
			t.Errorf("normalizeSpec(%q) should fail", bad)
		}
	}
	for in, want := range map[string]bool{strings.Repeat("a", 40): true, strings.Repeat("0f", 32): true, strings.Repeat("A", 40): false, "abc": false, "": false, strings.Repeat("g", 40): false} {
		if isHexID(in) != want {
			t.Errorf("isHexID(%q) = %v", in, !want)
		}
	}
	if rel, ok := relInside("/r", "/r/a/b"); !ok || rel != "a/b" {
		t.Errorf("relInside = %q %v", rel, ok)
	}
	if rel, ok := relInside("/r", "/r"); !ok || rel != "." {
		t.Errorf("relInside root = %q %v", rel, ok)
	}
	if _, ok := relInside("/r", "/rr/a"); ok {
		t.Error("sibling with common prefix is outside")
	}
	if _, ok := relInside("/r", "/x"); ok {
		t.Error("outside")
	}
}
