package tier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/layout"
)

type formsCase struct {
	Name       string   `json:"name"`
	Goos       string   `json:"goos"`
	ManagedDir string   `json:"managed_dir"`
	Self       string   `json:"self"`
	Extra      []string `json:"extra"`
	DenyWrite  []string `json:"deny_write"`
	PermDeny   []string `json:"perm_deny"`
}

func TestManagedFormsGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/managed_forms.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []formsCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 4 {
		t.Fatalf("golden has %d cases", len(f.Cases))
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			sep := "/"
			if c.Goos == "windows" {
				sep = `\`
			}
			lay := layout.Layout{GOOS: c.Goos, OrgRules: c.ManagedDir + sep + "ballast" + sep + "rules.json"}
			dw, pd := ManagedForms(lay, c.Self, c.Extra)
			if !reflect.DeepEqual(dw, c.DenyWrite) {
				t.Errorf("denyWrite:\n got %q\nwant %q", dw, c.DenyWrite)
			}
			if !reflect.DeepEqual(pd, c.PermDeny) {
				t.Errorf("permissions.deny:\n got %q\nwant %q", pd, c.PermDeny)
			}
		})
	}
}

// The golden pins the v1.5 any-depth forms right after the "./" form of each
// group.
func TestManagedFormsAnyDepthForms(t *testing.T) {
	lay := layout.Layout{GOOS: "linux", OrgRules: "/etc/claude-code/ballast/rules.json"}
	dw, pd := ManagedForms(lay, "/usr/local/libexec/ballast/ballast", nil)
	idx := func(ss []string, s string) int {
		for i, x := range ss {
			if x == s {
				return i
			}
		}
		return -1
	}
	if idx(dw, "**/.ballast") != idx(dw, "./.ballast")+1 {
		t.Errorf("**/.ballast does not follow ./.ballast: %q", dw)
	}
	if idx(dw, "**/.claude/settings*.json") != idx(dw, "./.claude/settings*.json")+1 {
		t.Errorf("**/.claude/settings*.json does not follow ./.claude/settings*.json: %q", dw)
	}
	if idx(pd, "Edit(**/.ballast/**)") < 0 || idx(pd, "Edit(**/.claude/settings*.json)") < 0 {
		t.Errorf("Edit lacks the any-depth forms: %q", pd)
	}
}

func TestManagedFormsPureAndDeduplicated(t *testing.T) {
	lay := layout.Layout{GOOS: "linux", OrgRules: "/etc/claude-code/ballast/rules.json"}
	a1, b1 := ManagedForms(lay, "/opt/b/ballast", []string{"/x", "/x", "~/.ballast"})
	a2, b2 := ManagedForms(lay, "/opt/b/ballast", []string{"/x", "/x", "~/.ballast"})
	if !reflect.DeepEqual(a1, a2) || !reflect.DeepEqual(b1, b2) {
		t.Fatal("not deterministic")
	}
	seen := map[string]bool{}
	for _, s := range a1 {
		if seen[s] {
			t.Errorf("duplicate denyWrite entry %q", s)
		}
		seen[s] = true
	}
	// extra equal to a built-in keeps its first position.
	if a1[1] != "~/.ballast" {
		t.Errorf("a1 = %q", a1)
	}
	// Only lay.GOOS and lay.OrgRules matter.
	lay2 := lay
	lay2.Home, lay2.InstallRoot, lay2.ClaudeDir = "/h/.ballast", "/h", "/h/.claude"
	a3, b3 := ManagedForms(lay2, "/opt/b/ballast", []string{"/x", "/x", "~/.ballast"})
	if !reflect.DeepEqual(a1, a3) || !reflect.DeepEqual(b1, b3) {
		t.Fatal("output depends on user or repo")
	}
	// Literal self: a relative or symlink-ish path is used as written.
	a4, _ := ManagedForms(lay, "ballast", nil)
	if a4[4] != "." {
		t.Errorf("dir of a bare name = %q, want \".\"", a4[4])
	}
	a5, _ := ManagedForms(lay, "/ballast", nil)
	if a5[4] != "/" {
		t.Errorf("dir of /ballast = %q, want \"/\"", a5[4])
	}
}

// --- ProtectedPaths ------------------------------------------------------

func TestProtectedPaths(t *testing.T) {
	m := newMemFS()
	// macOS style: /var -> /private/var, repo below it.
	m.mkdirAll("/private/var/repo", tUser, 0o755)
	m.symlink("/var", "/private/var", 0)
	m.mkdirAll("/private/var/repo/.ballast", tUser, 0o755)
	m.mkdirAll("/home/u/.ballast", tUser, 0o755)
	m.mkdirAll("/home/u/.claude", tUser, 0o755)
	lay := layout.Layout{GOOS: "linux", Home: "/home/u/.ballast", InstallRoot: "/home/u", ClaudeDir: "/home/u/.claude", OrgRules: "/etc/claude-code/ballast/rules.json"}
	got := ProtectedPaths(m, lay, "/usr/local/libexec/ballast/ballast", "/var/repo", []string{"/srv/policy", "~/.config/acme/ci-token"})
	type pp = ProtectedPath
	want := []pp{
		{Kind: ProtectedOrgDir, Path: "/etc/claude-code/ballast", Dir: true},
		{Kind: ProtectedBallastHome, Path: "/home/u/.ballast", Dir: true},
		{Kind: ProtectedRepoBallast, Path: "/var/repo/.ballast", Resolved: "/private/var/repo/.ballast", Dir: true},
		{Kind: ProtectedBinaryDir, Path: "/usr/local/libexec/ballast", Dir: true},
		{Kind: ProtectedClaudeUser, Path: "/home/u/.claude/settings*.json", Glob: true},
		{Kind: ProtectedClaudeRepo, Path: "/var/repo/.claude/settings*.json", Resolved: "/private/var/repo/.claude/settings*.json", Glob: true},
		{Kind: ProtectedExtraPath, Path: "/srv/policy", Dir: true},
		{Kind: ProtectedExtraPath, Path: "/home/u/.config/acme/ci-token", Dir: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got:\n%+v\nwant:\n%+v", got, want)
	}
}

func TestProtectedPathsWithoutRepoAndClaudeConfigDir(t *testing.T) {
	m := newMemFS()
	m.mkdirAll("/home/u/.ballast", tUser, 0o755)
	lay := layout.Layout{GOOS: "linux", Home: "/home/u/.ballast", InstallRoot: "/home/u", ClaudeDir: "/cfg/claude", OrgRules: "/etc/claude-code/ballast/rules.json"}
	got := ProtectedPaths(m, lay, "/opt/ballast/ballast", "", nil)
	var kinds []ProtectedKind
	for _, p := range got {
		kinds = append(kinds, p.Kind)
		if strings.Contains(p.Path, "/.claude/") && p.Kind != ProtectedClaudeUser {
			t.Errorf("repo entry without a repo: %+v", p)
		}
	}
	wantKinds := []ProtectedKind{ProtectedOrgDir, ProtectedBallastHome, ProtectedBinaryDir, ProtectedClaudeUser}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("kinds = %v, want %v", kinds, wantKinds)
	}
	if got[3].Path != filepath.Join("/cfg/claude", "settings*.json") {
		t.Errorf("CLAUDE_CONFIG_DIR not honoured: %q", got[3].Path)
	}
}

func TestProtectedPathsResolvesExistingSymlinkedEntry(t *testing.T) {
	m := newMemFS()
	m.mkdirAll("/real/ballast", tUser, 0o755)
	m.symlink("/home/u/.ballast", "/real/ballast", tUser)
	lay := layout.Layout{GOOS: "linux", Home: "/home/u/.ballast", InstallRoot: "/home/u", ClaudeDir: "/home/u/.claude", OrgRules: "/etc/claude-code/ballast/rules.json"}
	got := ProtectedPaths(m, lay, "/opt/b/ballast", "", nil)
	if got[1].Kind != ProtectedBallastHome || got[1].Resolved != "/real/ballast" {
		t.Errorf("home entry = %+v", got[1])
	}
	// Nothing exists below "/": no panic, Resolved stays empty.
	if got[0].Resolved != "" {
		t.Errorf("org dir Resolved = %q", got[0].Resolved)
	}
}

// A symlinked binary adds the directory of its real file as a second
// binary_dir entry.
func TestProtectedPathsSymlinkedBinaryAddsRealDir(t *testing.T) {
	m := newMemFS()
	m.writeFile("/usr/local/libexec/ballast/ballast", "bin", 0, 0o755)
	m.symlink("/usr/local/bin/ballast", "/usr/local/libexec/ballast/ballast", 0)
	lay := layout.Layout{GOOS: "linux", Home: "/home/u/.ballast", InstallRoot: "/home/u", ClaudeDir: "/home/u/.claude", OrgRules: "/etc/claude-code/ballast/rules.json"}
	var dirs []string
	for _, p := range ProtectedPaths(m, lay, "/usr/local/bin/ballast", "", nil) {
		if p.Kind == ProtectedBinaryDir {
			dirs = append(dirs, p.Path)
		}
	}
	if want := []string{"/usr/local/bin", "/usr/local/libexec/ballast"}; !reflect.DeepEqual(dirs, want) {
		t.Errorf("binary dirs = %v, want %v", dirs, want)
	}
}
