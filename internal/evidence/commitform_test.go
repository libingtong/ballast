package evidence

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func lits(ss ...string) []model.Word {
	var w []model.Word
	for _, s := range ss {
		w = append(w, model.Word{Lit: s})
	}
	return w
}

func dyn(s string) model.Word { return model.Word{Lit: s, Dynamic: true} }

func TestCommitFormTable(t *testing.T) {
	data, err := os.ReadFile("testdata/commitform.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Rows []struct {
			Cmd  string               `json:"cmd"`
			Git  *model.GitInvocation `json:"git"`
			Want model.CommitForm     `json:"want"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) < 14 {
		t.Fatalf("only %d rows", len(f.Rows))
	}
	for _, r := range f.Rows {
		t.Run(r.Cmd, func(t *testing.T) {
			got := ParseCommitForm(r.Git, "/repo")
			if !reflect.DeepEqual(got, r.Want) {
				t.Errorf("ParseCommitForm = %+v\nwant %+v", got, r.Want)
			}
		})
	}
}

func TestParseCommitFormExtra(t *testing.T) {
	g := func(args ...model.Word) *model.GitInvocation { return &model.GitInvocation{Sub: "commit", Args: args} }
	tests := []struct {
		name string
		git  *model.GitInvocation
		cwd  string
		want model.CommitForm
	}{
		{"nil git", nil, "/r", model.CommitForm{Kind: "unparseable"}},
		{"not commit", &model.GitInvocation{Sub: "status"}, "/r", model.CommitForm{Kind: "unparseable"}},
		{"dynamic sub", &model.GitInvocation{Sub: "commit", SubDynamic: true}, "/r", model.CommitForm{Kind: "unparseable"}},
		{"git-dir", &model.GitInvocation{Sub: "commit", GitDir: "/x/.git"}, "/r", model.CommitForm{Kind: "unparseable"}},
		{"work-tree", &model.GitInvocation{Sub: "commit", WorkTree: "/x"}, "/r", model.CommitForm{Kind: "unparseable"}},
		{"dynamic -C", &model.GitInvocation{Sub: "commit", Cwd: "$D", CwdDynamic: true}, "/r", model.CommitForm{Kind: "unparseable"}},
		{"bare", g(), "/r", model.CommitForm{Kind: "plain"}},
		{"-C absolute", &model.GitInvocation{Sub: "commit", Cwd: "/elsewhere/x"}, "/r", model.CommitForm{Kind: "plain", Dir: "/elsewhere/x"}},
		{"-C relative with cwd", &model.GitInvocation{Sub: "commit", Cwd: "sub/../other"}, "/r/s", model.CommitForm{Kind: "plain", Dir: "/r/s/other"}},
		{"-C relative without cwd", &model.GitInvocation{Sub: "commit", Cwd: "x"}, "", model.CommitForm{Kind: "unparseable"}},
		{"-C relative to relative cwd", &model.GitInvocation{Sub: "commit", Cwd: "x"}, "rel", model.CommitForm{Kind: "unparseable"}},
		{"-C windows drive", &model.GitInvocation{Sub: "commit", Cwd: "D:/w/repo"}, "/r", model.CommitForm{Kind: "plain", Dir: "D:/w/repo"}},
		{"-am cluster", g(lits("-am", "x")...), "/r", model.CommitForm{Kind: "all"}},
		{"-ma stays a message", g(lits("-ma", "src/a.go")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"src/a.go"}}},
		{"-mmsg attached", g(lits("-mmsg")...), "/r", model.CommitForm{Kind: "plain"}},
		{"-m without value", g(lits("-m")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"-m value looks like option", g(lits("-m", "-a")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--message=", g(lits("--message=hello world")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--message separate", g(lits("--message", "hello")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--author separate", g(lits("--author", "A <a@b>", "src/x")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"src/x"}}},
		{"--date and --cleanup", g(lits("--date=now", "--cleanup", "strip", "-m", "x")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--fixup separate", g(lits("--fixup", "abc", "a.go")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"a.go"}}},
		{"--squash=", g(lits("--squash=abc")...), "/r", model.CommitForm{Kind: "plain"}},
		{"-C commit", g(lits("-C", "HEAD", "--amend")...), "/r", model.CommitForm{Kind: "plain", Amend: true}},
		{"-c commit", g(lits("-c", "HEAD")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--reuse-message", g(lits("--reuse-message", "HEAD")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--no-verify -n -s -e -q -v", g(lits("--no-verify", "-n", "-s", "-e", "-q", "-v", "-m", "x")...), "/r", model.CommitForm{Kind: "plain"}},
		{"-nsm", g(lits("-nsm", "x")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--allow-empty", g(lits("--allow-empty", "-m", "x")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--dry-run", g(lits("--dry-run")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--porcelain", g(lits("--porcelain", "-a")...), "/r", model.CommitForm{Kind: "all"}},
		{"--gpg-sign optional", g(lits("--gpg-sign", "a.go")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"a.go"}}},
		{"-S optional", g(lits("-SKEYID", "-m", "x")...), "/r", model.CommitForm{Kind: "plain"}},
		{"-u optional", g(lits("-uno", "-m", "x")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--untracked-files=no", g(lits("--untracked-files=no")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--no-edit negation", g(lits("--no-edit", "--no-gpg-sign", "--no-status")...), "/r", model.CommitForm{Kind: "plain"}},
		{"--all with amend", g(lits("--all", "--amend", "--no-edit")...), "/r", model.CommitForm{Kind: "all", Amend: true}},
		{"-a with paths is refused by git", g(lits("-a", "x.go")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"-i without paths", g(lits("-i", "-m", "x")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"-o without paths", g(lits("--only")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"--include paths", g(lits("--include", "a", "b")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"a", "b"}, Include: true}},
		{"--only paths", g(lits("--only", "a")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"a"}}},
		{"-ap cluster", g(lits("-ap")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"--patch", g(lits("--patch")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"--pathspec-from-file", g(lits("--pathspec-from-file=list.txt")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"--pathspec-from-file separate", g(lits("--pathspec-from-file", "list.txt")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"unknown long option", g(lits("--frobnicate")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"unknown short option", g(lits("-X")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"flag with value", g(lits("--amend=1")...), "/r", model.CommitForm{Kind: "plain", Amend: true}},
		{"--all with value", g(lits("--all=1")...), "/r", model.CommitForm{Kind: "unparseable"}},
		{"dynamic message is fine", g(lits("-m")[0], dyn("$(cat msg)")), "/r", model.CommitForm{Kind: "plain"}},
		{"dynamic option", g(dyn("$FLAGS"), model.Word{Lit: "x"}), "/r", model.CommitForm{Kind: "unparseable"}},
		{"dynamic path", g(lits("-m", "x")[0], model.Word{Lit: "x"}, dyn("$F")), "/r", model.CommitForm{Kind: "unparseable"}},
		{"dynamic after --", g(model.Word{Lit: "--"}, dyn("$F")), "/r", model.CommitForm{Kind: "unparseable"}},
		{"-- then dash path", g(lits("--", "-weird")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"-weird"}}},
		{"lone dash is a path", g(lits("-")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"-"}}},
		{"-m after paths", g(lits("a", "b", "-m", "x", "c")...), "/r", model.CommitForm{Kind: "paths", Paths: []string{"a", "b", "c"}}},
		{"-C and paths", &model.GitInvocation{Sub: "commit", Cwd: "../o", Args: lits("-a", "-m", "x")}, "/w/r", model.CommitForm{Kind: "all", Dir: "/w/o"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCommitForm(tc.git, tc.cwd)
			if string(got.Kind) != string(tc.want.Kind) {
				t.Fatalf("kind = %q, want %q (%+v)", got.Kind, tc.want.Kind, got)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func FuzzParseCommitForm(f *testing.F) {
	f.Add("commit", "-m x -a", "/r", "")
	f.Add("commit", "-p", "/r", "")
	f.Add("commit", "-- a b", "/r", "../x")
	f.Add("commit", "-i a", "", "x")
	f.Add("status", "", "/r", "")
	f.Add("commit", "-am\x00 --fixup", "/r", "/")
	f.Fuzz(func(t *testing.T, sub, args, cwd, gitCwd string) {
		var words []model.Word
		for _, a := range strings.Fields(args) {
			words = append(words, model.Word{Lit: a, Dynamic: strings.HasPrefix(a, "$")})
		}
		got := ParseCommitForm(&model.GitInvocation{Sub: sub, Cwd: gitCwd, Args: words}, cwd)
		switch got.Kind {
		case model.CommitPlain:
			if len(got.Paths) != 0 || got.Include {
				t.Fatalf("plain with paths: %+v", got)
			}
		case model.CommitAll:
			if len(got.Paths) != 0 {
				t.Fatalf("all with paths: %+v", got)
			}
		case model.CommitPaths:
			if len(got.Paths) == 0 {
				t.Fatalf("paths without paths: %+v", got)
			}
		case model.CommitUnparseable:
			if !reflect.DeepEqual(got, model.CommitForm{Kind: model.CommitUnparseable}) {
				t.Fatalf("unparseable with extra fields: %+v", got)
			}
		default:
			t.Fatalf("bad kind %q", got.Kind)
		}
		if got.Dir != "" && gitCwd == "" {
			t.Fatalf("Dir without -C: %+v", got)
		}
	})
}
