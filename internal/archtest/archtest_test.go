package archtest

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const mod = "github.com/libingtong/ballast/"

// allowed lists, per package (relative to the module), the internal packages
// it may import directly. "*" means unrestricted. Subpackages of adapter are
// listed explicitly.
var allowed = map[string][]string{
	"internal/model":   {},
	"internal/ports":   {"internal/model"},
	"internal/sys":     {},
	"internal/version": {},
	"internal/layout":  {"internal/model", "internal/sys"},
	"internal/testenv": {"internal/layout", "internal/sys"},

	"internal/paths":     {"internal/model", "internal/ports", "internal/sys"},
	"internal/bashparse": {"internal/model", "internal/ports"},
	"internal/psparse":   {"internal/model", "internal/ports", "internal/bashparse"},
	"internal/dsl":       {"internal/model", "internal/ports", "internal/sys"},
	"internal/config":    {"internal/model", "internal/ports", "internal/sys", "internal/layout"},
	"internal/inject":    {"internal/model", "internal/ports"},
	"internal/state":     {"internal/model", "internal/ports", "internal/sys", "internal/layout"},
	"internal/audit":     {"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/redact"},

	// P2 (architecture v2.0, docs/p2-design.md §2).
	"internal/redact":  {},
	"internal/tier":    {"internal/model", "internal/sys", "internal/layout"},
	"internal/sandbox": {"internal/model", "internal/sys"},

	"internal/adapter":            {"internal/model", "internal/ports", "internal/adapter/claudecode", "internal/adapter/codex", "internal/adapter/cursor", "internal/adapter/hermes", "internal/adapter/common"},
	"internal/adapter/common":     {"internal/model"},
	"internal/adapter/claudecode": {"internal/model", "internal/ports", "internal/adapter/common"},
	"internal/adapter/codex":      {"internal/model", "internal/ports", "internal/adapter/common"},
	"internal/adapter/cursor":     {"internal/model", "internal/ports", "internal/adapter/common"},
	"internal/adapter/hermes":     {"internal/model", "internal/ports", "internal/adapter/common"},

	"internal/hook": {"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/version", "internal/redact"},

	"internal/snapshot": {"internal/model", "internal/ports", "internal/sys"},
	"internal/evidence": {"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/snapshot"},
	"internal/project":  {"internal/model", "internal/ports", "internal/sys"},
	"internal/runner":   {"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/evidence", "internal/snapshot", "internal/project", "internal/version", "internal/tier", "internal/sandbox"},

	"internal/stats":   {"internal/model", "internal/ports", "internal/sys", "internal/layout"},
	"internal/replay":  {"internal/model", "internal/ports", "internal/sys", "internal/layout"},
	"internal/install": {"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/tier"},
	"internal/report":  {"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/redact", "internal/stats"},

	"internal/archtest": {},
	"internal/cli":      {"*"},
	"cmd/ballast":       {"internal/cli"},
	"internal/tools":    {"*"},
}

// hotPath packages must not depend (transitively) on net/*, and may import
// os/exec directly only in the files listed in execAllowed.
var hotPath = []string{
	"internal/model", "internal/ports", "internal/sys", "internal/layout", "internal/version",
	"internal/paths", "internal/bashparse", "internal/psparse", "internal/dsl", "internal/config",
	"internal/inject", "internal/state", "internal/audit", "internal/adapter",
	"internal/adapter/common", "internal/adapter/claudecode", "internal/adapter/codex",
	"internal/adapter/cursor", "internal/adapter/hermes", "internal/hook",
	"internal/snapshot", "internal/evidence", "internal/project",
	"internal/redact", "internal/tier", // P2
}

var execAllowed = map[string]bool{"internal/snapshot/gitfallback.go": true}

// noExecTransitive packages must not reach os/exec even transitively.
var noExecTransitive = []string{"internal/hook", "internal/dsl", "internal/adapter", "internal/state", "internal/audit", "internal/config", "internal/inject", "internal/paths", "internal/bashparse", "internal/psparse", "internal/redact", "internal/tier"}

// noNetBinaries must not depend (transitively) on net/* at all: the whole
// ballast binary stays offline (P2 report is a local file; any future
// network upload lives in a separate binary, docs/p2-design.md §4.5). This
// is stricter than TestHotPathNoNet and also keeps net/http's package init
// out of every hook invocation.
var noNetBinaries = []string{"cmd/ballast"}

func repoRoot(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func goList(t *testing.T, root string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return string(out)
}

func TestInternalImportAllowlist(t *testing.T) {
	root := repoRoot(t)
	out := goList(t, root, "-f", `{{.ImportPath}}{{range .Imports}} {{.}}{{end}}`, "./cmd/...", "./internal/...")
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		pkg := strings.TrimPrefix(fields[0], mod)
		allow, ok := allowed[pkg]
		if !ok {
			t.Errorf("package %s missing from archtest allowlist (add it to docs/architecture.md §8 first)", pkg)
			continue
		}
		if len(allow) == 1 && allow[0] == "*" {
			continue
		}
		set := map[string]bool{}
		for _, a := range allow {
			set[a] = true
		}
		for _, imp := range fields[1:] {
			if !strings.HasPrefix(imp, mod) {
				continue
			}
			rel := strings.TrimPrefix(imp, mod)
			if !set[rel] {
				t.Errorf("%s imports %s, which is not allowed by the contract", pkg, rel)
			}
		}
	}
}

func TestHotPathNoNet(t *testing.T) {
	root := repoRoot(t)
	for _, p := range hotPath {
		deps := goList(t, root, "-deps", "./"+p)
		for _, d := range strings.Fields(deps) {
			if d == "net" || strings.HasPrefix(d, "net/") {
				t.Errorf("%s depends on %s (hot path must not import net/*)", p, d)
			}
		}
	}
	for _, p := range noExecTransitive {
		deps := goList(t, root, "-deps", "./"+p)
		for _, d := range strings.Fields(deps) {
			if d == "os/exec" {
				t.Errorf("%s depends on os/exec transitively", p)
			}
		}
	}
}

func TestBinaryNoNet(t *testing.T) {
	root := repoRoot(t)
	for _, p := range noNetBinaries {
		deps := goList(t, root, "-deps", "./"+p)
		for _, d := range strings.Fields(deps) {
			if d == "net" || strings.HasPrefix(d, "net/") {
				t.Errorf("%s depends on %s (the ballast binary must not link net/*)", p, d)
			}
		}
	}
}

func TestHotPathExecOnlyInGitFallback(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, p := range hotPath {
		dir := filepath.Join(root, filepath.FromSlash(p))
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if path == "os/exec" && !execAllowed[p+"/"+name] {
					t.Errorf("%s/%s imports os/exec (only internal/snapshot/gitfallback.go may)", p, name)
				}
			}
		}
	}
}

// initAllowed lists non-test files (module-relative) allowed to declare
// func init(). Every `ballast hook` invocation executes the init of EVERY
// package linked into cmd/ballast (install, stats, replay, runner, ...),
// so the whole binary is covered, not just the hot-path packages.
var initAllowed = map[string]bool{}

// regexpCompileFuncs are the regexp constructors forbidden in package-level
// variable initializers (compile lazily, e.g. sync.Once or on first use).
var regexpCompileFuncs = map[string]bool{"MustCompile": true, "Compile": true, "MustCompilePOSIX": true, "CompilePOSIX": true}

// binaryGoFiles returns every non-test .go file under internal/ and cmd/
// (testdata directories excluded), module-relative with '/' separators.
func binaryGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				rel, _ := filepath.Rel(root, path)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// TestNoHeavyPackageInit forbids, in every package linked into the binary
// (./internal/... and ./cmd/...), package-level regexp compilation and
// func init() (architecture §9: the hook pays for every package's init).
func TestNoHeavyPackageInit(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, rel := range binaryGoFiles(t, root) {
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		regexpName := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "regexp" {
				regexpName = "regexp"
				if imp.Name != nil {
					regexpName = imp.Name.Name
				}
			}
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.Name == "init" && !initAllowed[rel] {
					t.Errorf("%s:%d func init() is not allowed (every hook invocation runs it; initialize lazily)", rel, fset.Position(d.Pos()).Line)
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR || regexpName == "" {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for _, v := range vs.Values {
						ast.Inspect(v, func(n ast.Node) bool {
							switch x := n.(type) {
							case *ast.FuncLit:
								return false // bodies run lazily
							case *ast.CallExpr:
								if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
									if id, ok := sel.X.(*ast.Ident); ok && id.Name == regexpName && regexpCompileFuncs[sel.Sel.Name] {
										t.Errorf("%s:%d package-level regexp.%s (compile lazily)", rel, fset.Position(x.Pos()).Line, sel.Sel.Name)
									}
								}
							}
							return true
						})
					}
				}
			}
		}
	}
}
