package project

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func TestDetectStackFixtures(t *testing.T) {
	want := map[Stack]struct {
		argv    []string
		markers []string
	}{
		StackGo:     {[]string{"go", "test", "./..."}, []string{"go.mod"}},
		StackNode:   {[]string{"npm", "test"}, []string{"package.json"}},
		StackPython: {[]string{"python", "-m", "pytest"}, []string{"pyproject.toml"}},
		StackRust:   {[]string{"cargo", "test"}, []string{"Cargo.toml"}},
		StackMaven:  {[]string{"./mvnw", "-q", "test"}, []string{"pom.xml", "mvnw"}},
		StackGradle: {[]string{"./gradlew", "test"}, []string{"build.gradle", "gradlew"}},
		StackDotnet: {[]string{"dotnet", "test"}, []string{"App.sln"}},
		StackSwift:  {[]string{"swift", "test"}, []string{"Package.swift"}},
		StackRuby:   {[]string{"bundle", "exec", "rspec"}, []string{"Gemfile", "spec"}},
	}
	if len(want) != len(AllStacks) {
		t.Fatalf("table covers %d stacks, AllStacks has %d", len(want), len(AllStacks))
	}
	for _, st := range AllStacks {
		t.Run(string(st), func(t *testing.T) {
			sug, err := Detect(sys.OSFS{}, filepath.Join("testdata", "stacks", string(st)))
			if err != nil {
				t.Fatal(err)
			}
			if len(sug) != 1 || sug[0].Stack != st {
				t.Fatalf("Detect = %+v; want exactly %s", sug, st)
			}
			w := want[st]
			if !reflect.DeepEqual(sug[0].Verify.Argv, w.argv) {
				t.Errorf("argv = %v; want %v", sug[0].Verify.Argv, w.argv)
			}
			if !reflect.DeepEqual(sug[0].Markers, w.markers) {
				t.Errorf("markers = %v; want %v", sug[0].Markers, w.markers)
			}
			v := sug[0].Verify
			if v.Name != "test" || !v.Required || v.TimeoutS != DefaultTimeoutS {
				t.Errorf("verify spec = %+v", v)
			}
			// The proposal for a single stack validates and round trips.
			cfg := Propose(sug)
			assertProposalValid(t, cfg)
		})
	}
}

func assertProposalValid(t *testing.T, cfg *model.ProjectConfig) {
	t.Helper()
	data := mustJSON(t, cfg)
	back, err := Decode(data)
	if err != nil {
		t.Fatalf("proposal does not validate: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(cfg, back) {
		t.Errorf("proposal does not round trip:\n%+v\n%+v", cfg, back)
	}
}

// mkRepo creates files (and directories for names ending in '/') under a
// temp dir.
func mkRepo(t *testing.T, entries ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, e := range entries {
		p := filepath.Join(root, filepath.FromSlash(e))
		if e[len(e)-1] == '/' {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDetectVariants(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		stack   Stack
		argv    []string
		cwd     string
	}{
		{"node default", []string{"package.json"}, StackNode, []string{"npm", "test"}, ""},
		{"node npm lock", []string{"package.json", "package-lock.json"}, StackNode, []string{"npm", "test"}, ""},
		{"node pnpm", []string{"package.json", "pnpm-lock.yaml"}, StackNode, []string{"pnpm", "test"}, ""},
		{"node yarn", []string{"package.json", "yarn.lock"}, StackNode, []string{"yarn", "test"}, ""},
		{"node pnpm beats yarn", []string{"package.json", "yarn.lock", "pnpm-lock.yaml"}, StackNode, []string{"pnpm", "test"}, ""},
		{"python setup.cfg", []string{"setup.cfg"}, StackPython, []string{"python", "-m", "pytest"}, ""},
		{"python setup.py", []string{"setup.py"}, StackPython, []string{"python", "-m", "pytest"}, ""},
		{"python pytest.ini", []string{"pytest.ini"}, StackPython, []string{"python", "-m", "pytest"}, ""},
		{"maven without wrapper", []string{"pom.xml"}, StackMaven, []string{"mvn", "-q", "test"}, ""},
		{"gradle kts", []string{"build.gradle.kts"}, StackGradle, []string{"gradle", "test"}, ""},
		{"gradle kts wrapper", []string{"build.gradle.kts", "gradlew"}, StackGradle, []string{"./gradlew", "test"}, ""},
		{"dotnet csproj", []string{"App.csproj"}, StackDotnet, []string{"dotnet", "test"}, ""},
		{"dotnet fsproj uppercase", []string{"APP.FSPROJ"}, StackDotnet, []string{"dotnet", "test"}, ""},
		{"dotnet one level down", []string{"src/App.sln", "other/readme.txt"}, StackDotnet, []string{"dotnet", "test"}, "src"},
		{"ruby rake", []string{"Gemfile"}, StackRuby, []string{"bundle", "exec", "rake", "test"}, ""},
		{"ruby spec file is not a dir", []string{"Gemfile", "spec"}, StackRuby, []string{"bundle", "exec", "rake", "test"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sug, err := Detect(sys.OSFS{}, mkRepo(t, tc.entries...))
			if err != nil {
				t.Fatal(err)
			}
			if len(sug) != 1 || sug[0].Stack != tc.stack {
				t.Fatalf("Detect = %+v; want only %s", sug, tc.stack)
			}
			if !reflect.DeepEqual(sug[0].Verify.Argv, tc.argv) || sug[0].Verify.Cwd != tc.cwd {
				t.Errorf("verify = %+v; want argv %v cwd %q", sug[0].Verify, tc.argv, tc.cwd)
			}
		})
	}
}

func TestDetectNoStackAndSkips(t *testing.T) {
	for name, entries := range map[string][]string{
		"empty":             nil,
		"docs only":         {"README.md", "docs/"},
		"nested go.mod":     {"sub/go.mod"},
		"dotnet in vendor":  {"vendor/App.sln"},
		"dotnet in hidden":  {".git/App.sln"},
		"dotnet two deep":   {"a/b/App.sln"},
		"dotnet dir named":  {"App.sln/"},
		"node_modules proj": {"node_modules/App.csproj"},
	} {
		t.Run(name, func(t *testing.T) {
			sug, err := Detect(sys.OSFS{}, mkRepo(t, entries...))
			if err != nil || len(sug) != 0 {
				t.Fatalf("Detect = %+v, %v; want nothing", sug, err)
			}
		})
	}
	if _, err := Detect(sys.OSFS{}, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing root: want error")
	}
	if _, err := Detect(nil, t.TempDir()); err != nil {
		t.Errorf("nil FS: %v", err)
	}
}

func TestDetectMultiStack(t *testing.T) {
	root := mkRepo(t, "go.mod", "package.json", "yarn.lock", "Cargo.toml", "pom.xml", "build.gradle", "Gemfile", "Package.swift", "pyproject.toml", "X.sln")
	sug, err := Detect(sys.OSFS{}, root)
	if err != nil {
		t.Fatal(err)
	}
	var got []Stack
	for _, s := range sug {
		got = append(got, s.Stack)
	}
	if !reflect.DeepEqual(got, AllStacks) {
		t.Fatalf("stacks = %v; want %v in detection order", got, AllStacks)
	}
	cfg := Propose(sug)
	var names []string
	for _, v := range cfg.Verify {
		names = append(names, v.Name)
		if !v.Required {
			t.Errorf("%s not required", v.Name)
		}
	}
	sort.Strings(names)
	want := []string{"test-dotnet", "test-go", "test-gradle", "test-maven", "test-node", "test-python", "test-ruby", "test-rust", "test-swift"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v", names)
	}
	assertProposalValid(t, cfg)
}

func TestProposeAliasing(t *testing.T) {
	sug := []Suggestion{{Stack: StackGo, Verify: model.VerifySpec{Name: "test", Argv: []string{"go", "test"}}}}
	cfg := Propose(sug)
	cfg.Verify[0].Argv[0] = "mutated"
	cfg.Exempt[0] = "mutated"
	if sug[0].Verify.Argv[0] != "go" || DefaultExempt[0] != "**/*.md" {
		t.Fatal("Propose shares memory with its input or the default list")
	}
	if cfg := Propose(nil); len(cfg.Verify) != 0 || cfg.Verify == nil || cfg.Version != 1 || len(cfg.Exempt) != 2 {
		t.Errorf("Propose(nil) = %+v", cfg)
	}
}
