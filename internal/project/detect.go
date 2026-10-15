package project

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Stack identifies a detected toolchain.
type Stack string

// The 9 stacks.
const (
	StackGo     Stack = "go"
	StackNode   Stack = "node"
	StackPython Stack = "python"
	StackRust   Stack = "rust"
	StackMaven  Stack = "maven"
	StackGradle Stack = "gradle"
	StackDotnet Stack = "dotnet"
	StackSwift  Stack = "swift"
	StackRuby   Stack = "ruby"
)

// AllStacks in detection order.
var AllStacks = []Stack{StackGo, StackNode, StackPython, StackRust, StackMaven, StackGradle, StackDotnet, StackSwift, StackRuby}

// DefaultExempt is the suggested exempt list.
var DefaultExempt = []string{"**/*.md", "docs/**"}

// Suggestion is one detected stack with a proposed verify entry.
type Suggestion struct {
	Stack   Stack            `json:"stack"`
	Markers []string         `json:"markers"`
	Verify  model.VerifySpec `json:"verify"`
}

// dotnetSkipDirs are not searched for solution/project files.
var dotnetSkipDirs = map[string]bool{"node_modules": true, "vendor": true, "bin": true, "obj": true, "target": true}

// Detect inspects root (non-recursive except *.sln/*.csproj/*.fsproj one
// level down) and returns one Suggestion per stack found, in AllStacks
// order. Every suggestion is named "test", required, with the default
// timeout; Propose renames them when several stacks are present.
func Detect(fsys sys.FS, root string) ([]Suggestion, error) {
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	ents, err := fsys.ReadDir(root)
	if err != nil {
		return nil, err
	}
	files := map[string]bool{}
	dirs := map[string]bool{}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
		if e.IsDir() {
			dirs[e.Name()] = true
		} else {
			files[e.Name()] = true
		}
	}
	sort.Strings(names)
	has := func(n string) bool { return files[n] }

	var out []Suggestion
	add := func(st Stack, markers []string, cwd string, argv ...string) {
		out = append(out, Suggestion{Stack: st, Markers: markers, Verify: model.VerifySpec{
			Name: "test", Argv: argv, TimeoutS: DefaultTimeoutS, Required: true, Cwd: cwd,
		}})
	}
	first := func(cands ...string) []string {
		var m []string
		for _, c := range cands {
			if has(c) {
				m = append(m, c)
			}
		}
		return m
	}

	if has("go.mod") {
		add(StackGo, []string{"go.mod"}, "", "go", "test", "./...")
	}
	if has("package.json") {
		markers := []string{"package.json"}
		argv := []string{"npm", "test"}
		switch {
		case has("pnpm-lock.yaml"):
			markers, argv = append(markers, "pnpm-lock.yaml"), []string{"pnpm", "test"}
		case has("yarn.lock"):
			markers, argv = append(markers, "yarn.lock"), []string{"yarn", "test"}
		case has("package-lock.json"):
			markers = append(markers, "package-lock.json")
		}
		add(StackNode, markers, "", argv...)
	}
	if m := first("pyproject.toml", "setup.cfg", "setup.py", "pytest.ini"); len(m) > 0 {
		add(StackPython, m, "", "python", "-m", "pytest")
	}
	if has("Cargo.toml") {
		add(StackRust, []string{"Cargo.toml"}, "", "cargo", "test")
	}
	if has("pom.xml") {
		if has("mvnw") {
			add(StackMaven, []string{"pom.xml", "mvnw"}, "", "./mvnw", "-q", "test")
		} else {
			add(StackMaven, []string{"pom.xml"}, "", "mvn", "-q", "test")
		}
	}
	if m := first("build.gradle", "build.gradle.kts"); len(m) > 0 {
		if has("gradlew") {
			add(StackGradle, append(m, "gradlew"), "", "./gradlew", "test")
		} else {
			add(StackGradle, m, "", "gradle", "test")
		}
	}
	if markers, cwd := detectDotnet(fsys, root, names, files, dirs); len(markers) > 0 {
		add(StackDotnet, markers, cwd, "dotnet", "test")
	}
	if has("Package.swift") {
		add(StackSwift, []string{"Package.swift"}, "", "swift", "test")
	}
	if has("Gemfile") {
		if dirs["spec"] {
			add(StackRuby, []string{"Gemfile", "spec"}, "", "bundle", "exec", "rspec")
		} else {
			add(StackRuby, []string{"Gemfile"}, "", "bundle", "exec", "rake", "test")
		}
	}
	return out, nil
}

func isDotnetFile(n string) bool {
	l := strings.ToLower(n)
	return strings.HasSuffix(l, ".sln") || strings.HasSuffix(l, ".csproj") || strings.HasSuffix(l, ".fsproj")
}

// detectDotnet looks for *.sln/*.csproj/*.fsproj at the root and, when the
// root has none, one directory level down (the first such directory in name
// order becomes the entry's cwd).
func detectDotnet(fsys sys.FS, root string, names []string, files, dirs map[string]bool) (markers []string, cwd string) {
	for _, n := range names {
		if files[n] && isDotnetFile(n) {
			markers = append(markers, n)
		}
	}
	if len(markers) > 0 {
		return markers, ""
	}
	for _, d := range names {
		if !dirs[d] || strings.HasPrefix(d, ".") || dotnetSkipDirs[d] {
			continue
		}
		sub, err := fsys.ReadDir(filepath.Join(root, d))
		if err != nil {
			continue
		}
		var found []string
		for _, e := range sub {
			if !e.IsDir() && isDotnetFile(e.Name()) {
				found = append(found, d+"/"+e.Name())
			}
		}
		sort.Strings(found)
		if len(found) > 0 {
			return found, d
		}
	}
	return nil, ""
}

// Propose builds a ProjectConfig from suggestions (names "test",
// "test-<stack>" when several; all required) with the default exempt list.
func Propose(s []Suggestion) *model.ProjectConfig {
	cfg := &model.ProjectConfig{
		Version: FileVersion,
		Verify:  make([]model.VerifySpec, 0, len(s)),
		Exempt:  append([]string(nil), DefaultExempt...),
	}
	for _, sg := range s {
		v := sg.Verify
		v.Argv = append([]string(nil), v.Argv...)
		v.Required = true
		v.Name = "test"
		if len(s) > 1 {
			v.Name = "test-" + string(sg.Stack)
		}
		cfg.Verify = append(cfg.Verify, v)
	}
	return cfg
}
