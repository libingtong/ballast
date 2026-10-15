package model_test

import (
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/psparse"
)

func TestIsAbsShellPath(t *testing.T) {
	tests := []struct {
		p    string
		want bool
	}{
		// POSIX and Git Bash
		{"/", true},
		{"/a", true},
		{"/a/b.go", true},
		{"/c/Users/x/a.go", true},
		{"//server/share/a", true},
		// home
		{"~", true},
		{"~/a", true},
		{`~\a`, true},
		{"~user/a", false},
		{"~a", false},
		{"~~", false},
		// drives
		{`C:\a`, true},
		{"C:/a", true},
		{`c:\`, true},
		{"z:/", true},
		{"Z:\\x\\y", true},
		{"C:foo", false},
		{`C:foo\bar`, false},
		{"C:", false},
		{"C:.", false},
		{"1:/a", false},
		{"é:/a", false},
		{"::/a", false},
		{"CC:/a", false},
		// UNC
		{`\\server\share`, true},
		{`\\?\C:\a`, true},
		{`\\`, true},
		// relative
		{`\a`, false},
		{"a", false},
		{"a/b", false},
		{"./a", false},
		{"../a", false},
		{`.\a`, false},
		{`..\a`, false},
		{"-", false},
		{"", false},
		{" /a", false},
	}
	for _, tc := range tests {
		if got := model.IsAbsShellPath(tc.p); got != tc.want {
			t.Errorf("IsAbsShellPath(%q) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func node(argv0 string, dyn bool, reads ...string) model.CommandNode {
	n := model.CommandNode{Argv: []model.Word{{Lit: argv0, Dynamic: dyn}}}
	for _, r := range reads {
		if r == "*" {
			n.Reads = append(n.Reads, model.WriteTarget{Dynamic: true, Via: "cat"})
			continue
		}
		n.Reads = append(n.Reads, model.WriteTarget{Path: r, Via: "cat"})
	}
	return n
}

func list(shell model.ShellKind, nodes ...model.CommandNode) *model.CommandList {
	return &model.CommandList{Shell: shell, Nodes: nodes}
}

func TestShellReadPaths(t *testing.T) {
	bash, ps := model.ShellBash, model.ShellPowerShell
	tests := []struct {
		name string
		cl   *model.CommandList
		want []string
	}{
		{"nil", nil, nil},
		{"empty", list(bash), nil},
		{"no reads", list(bash, node("ls", false)), nil},
		{"one", list(bash, node("cat", false, "a.go")), []string{"a.go"}},
		{"order and dedupe", list(bash,
			node("cat", false, "b", "a", "b"), node("head", false, "c", "a")), []string{"b", "a", "c"}},
		{"dynamic and empty skipped", list(bash, node("cat", false, "*", "", "a")), []string{"a"}},
		{"dedupe is on raw text", list(bash, node("cat", false, "a", "./a", "a/")), []string{"a", "./a", "a/"}},
		{"cd keeps absolute only", list(bash,
			node("cd", false), node("cat", false, "rel.go", "/abs/a.go", "~/h", "~", `C:\w`, "C:rel", `\\srv\x`, "../up")),
			[]string{"/abs/a.go", "~/h", "~", `C:\w`, `\\srv\x`}},
		{"cd later in the list still applies", list(bash,
			node("cat", false, "rel.go", "/abs"), node("cd", false, "/x")), []string{"/abs", "/x"}},
		{"cd with only relative reads", list(bash, node("cat", false, "rel"), node("cd", false)), nil},
		{"pushd", list(bash, node("pushd", false), node("cat", false, "rel", "/a")), []string{"/a"}},
		{"popd", list(bash, node("popd", false), node("cat", false, "rel", "/a")), []string{"/a"}},
		{"bash is case-sensitive", list(bash, node("CD", false), node("cat", false, "rel")), []string{"rel"}},
		{"bash ignores PowerShell names", list(bash, node("Set-Location", false), node("cat", false, "rel")), []string{"rel"}},
		{"dynamic argv0 is not cd", list(bash, node("cd", true), node("cat", false, "rel")), []string{"rel"}},
		{"cd with a read of its own", list(bash, node("cd", false, "rel", "/a")), []string{"/a"}},
		{"shell none behaves like bash", list(model.ShellNone, node("cd", false), node("cat", false, "rel", "/a")), []string{"/a"}},
		{"ps Set-Location", list(ps, node("Set-Location", false), node("get-content", false, "rel", `C:\a`)), []string{`C:\a`}},
		{"ps case-insensitive", list(ps, node("SET-LOCATION", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps push-location", list(ps, node("Push-Location", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps pop-location", list(ps, node("pop-location", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps cd", list(ps, node("CD", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps sl", list(ps, node("sl", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps chdir", list(ps, node("ChDir", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps pushd", list(ps, node("pushd", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps popd", list(ps, node("POPD", false), node("x", false, "rel", "/a")), []string{"/a"}},
		{"ps without cd keeps relative", list(ps, node("x", false, "rel", "/a")), []string{"rel", "/a"}},
		{"ps Set-Location dynamic", list(ps, node("Set-Location", true), node("x", false, "rel")), []string{"rel"}},
		{"node without argv", list(bash, model.CommandNode{Reads: []model.WriteTarget{{Path: "a", Via: "cat"}}}), []string{"a"}},
	}
	for _, tc := range tests {
		got := model.ShellReadPaths(tc.cl)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The end-to-end rows below run the real parsers: what a command reads is
// exactly what ShellReadPaths registers.
func TestShellReadPathsParsed(t *testing.T) {
	bashTests := []struct {
		src  string
		want []string
	}{
		{"sed -n '1,80p' src/a.go", []string{"src/a.go"}},
		{"cat a.go | head -5 && grep -n x b.go", []string{"a.go", "b.go"}},
		{"cd /repo && cat a.go /etc/hosts", []string{"/etc/hosts"}},
		{"(cd sub; cat a.go) && cat ~/b.go", []string{"~/b.go"}},
		{"cat \"$F\" a.go", []string{"a.go"}},
		{"wc -l < in.txt", []string{"in.txt"}},
		{"echo $(cat VERSION)", []string{"VERSION"}},
		{"bash -c 'cd /x; cat rel /abs'", []string{"/abs"}},
		{"sed -i s/a/b/ f.txt", nil},
		{"python3 -c 'open(\"a\")'", nil},
		{"pushd /x >/dev/null && head -n 5 y /z", []string{"/z"}},
	}
	for _, tc := range bashTests {
		cl, err := bashparse.Parse(tc.src, bashparse.Options{})
		if err != nil {
			t.Fatalf("%q: %v", tc.src, err)
		}
		if got := model.ShellReadPaths(cl); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("bash %q: got %q, want %q", tc.src, got, tc.want)
		}
	}
	psTests := []struct {
		src  string
		want []string
	}{
		{`Get-Content src\a.go -TotalCount 50`, []string{`src\a.go`}},
		{`Select-String foo a.go; cat b.txt`, []string{"a.go", "b.txt"}},
		{`Set-Location C:\repo; Get-Content a.go; Get-Content D:\x.txt`, []string{`D:\x.txt`}},
		{`cd ..; gc rel.txt; gc \\srv\share\f`, []string{`\\srv\share\f`}},
		{`Get-Content $f`, nil},
		{`rg.exe -n Parse src\x.go`, []string{`src\x.go`}},
	}
	for _, tc := range psTests {
		cl, err := psparse.Parse(tc.src)
		if err != nil {
			t.Fatalf("%q: %v", tc.src, err)
		}
		if got := model.ShellReadPaths(cl); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ps %q: got %q, want %q", tc.src, got, tc.want)
		}
	}
}
