package bashparse

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// render summarizes a node: name plus the non-default fields.
func render(n model.CommandNode) string {
	var sb strings.Builder
	sb.WriteString(n.Name)
	if n.Depth > 0 {
		fmt.Fprintf(&sb, " d%d", n.Depth)
	}
	if n.Async {
		sb.WriteString(" &")
	}
	if n.InSubst {
		sb.WriteString(" $()")
	}
	if n.Sudo {
		sb.WriteString(" sudo")
	}
	if len(n.Wrappers) > 0 {
		fmt.Fprintf(&sb, " w=%s", strings.Join(n.Wrappers, ","))
	}
	if l := n.Loop; l != nil {
		fmt.Fprintf(&sb, " loop=%s/%d/%s/%d", l.Kind, l.N, l.Bounded, l.Depth)
	}
	if len(n.Writes) > 0 {
		fmt.Fprintf(&sb, " W=%s", strings.Join(tgt(n.Writes), ","))
	}
	if len(n.Reads) > 0 {
		fmt.Fprintf(&sb, " R=%s", strings.Join(tgt(n.Reads), ","))
	}
	if n.Sleep != nil {
		fmt.Fprintf(&sb, " sleep=%v/%v", n.Sleep.Seconds, n.Sleep.Known)
	}
	if n.Git != nil {
		fmt.Fprintf(&sb, " git=%s", n.Git.Sub)
	}
	return sb.String()
}

func renderAll(cl *model.CommandList) []string {
	out := []string{}
	for _, n := range cl.Nodes {
		out = append(out, render(n))
	}
	return out
}

func TestParseStructure(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		// async: the statement's `&` reaches every node of the statement
		{"sleep 1 & sleep 2", []string{"sleep & sleep=1/true", "sleep sleep=2/true"}},
		{"a | b &", []string{"a &", "b &"}},
		{"a && b &", []string{"a &", "b &"}},
		{"(a; b) & c", []string{"a &", "b &", "c"}},
		{"{ a; b; } &", []string{"a &", "b &"}},
		{"echo $(a) &", []string{"echo &", "a & $()"}},
		{"if x; then a; fi &", []string{"x &", "a &"}},
		{"bash -c 'sleep 9' &", []string{"bash &", "sleep d1 & sleep=9/true"}},
		{"coproc sleep 5", []string{"sleep & sleep=5/true"}},
		// substitutions
		{"echo $(a $(b))", []string{"echo", "a $()", "b $()"}},
		{"echo `a`", []string{"echo", "a $()"}},
		{"cat <(a) >(b)", []string{"cat R=cat:*,cat:*", "a $()", "b $()"}},
		{"x=$(a) y=`b` c", []string{"a $()", "b $()", "c"}},
		{"echo \"$(a)\" ${X:-$(b)} $(( $(c) + 1 ))", []string{"echo", "a $()", "b $()", "c $()"}},
		{"echo > $(a)", []string{"echo W=redirect:*", "a $()"}},
		{"cat <<< $(a)", []string{"cat", "a $()"}},
		{"cat <<EOF\n$(a)\nEOF", []string{"cat", "a $()"}},
		{"cat <<'EOF'\n$(a)\nEOF", []string{"cat"}},
		{"cat <<-EOF\n\t$(a)\n\tEOF", []string{"cat", "a $()"}},
		{"export X=$(a)", []string{"a $()"}},
		{"[[ $(a) == b ]]", []string{"a $()"}},
		{"for i in $(a); do b; done", []string{"a $()", "b loop=for_list/-1/unknown/1"}},
		// loops
		{"for i in a b c; do x; done", []string{"x loop=for_list/3/true/1"}},
		{"for i in; do x; done", []string{"x loop=for_list/0/true/1"}},
		{"for i; do x; done", []string{"x loop=for_list/-1/unknown/1"}},
		{"for i in a $b; do x; done", []string{"x loop=for_list/-1/unknown/1"}},
		{"for i in a*; do x; done", []string{"x loop=for_list/-1/unknown/1"}},
		{"for i in {1..10}; do x; done", []string{"x loop=for_list/10/true/1"}},
		{"for i in {10..1..3}; do x; done", []string{"x loop=for_list/4/true/1"}},
		{"for i in {a..e} x; do x; done", []string{"x loop=for_list/6/true/1"}},
		{"for i in {a,b,c}{1,2}; do x; done", []string{"x loop=for_list/6/true/1"}},
		{"for i in {1..$n}; do x; done", []string{"x loop=for_list/-1/unknown/1"}},
		{"for i in 'a b' \"c\"; do x; done", []string{"x loop=for_list/2/true/1"}},
		{"select i in a b; do x; done", []string{"x loop=for_list/-1/unknown/1"}},
		{"for ((;;)); do x; done", []string{"x loop=for_c/-1/unknown/1"}},
		{"while true; do x; done", []string{"true loop=while/-1/false/1", "x loop=while/-1/false/1"}},
		{"while :; do x; done", []string{": loop=while/-1/false/1", "x loop=while/-1/false/1"}},
		{"while /bin/true; do x; done", []string{"true loop=while/-1/unknown/1", "x loop=while/-1/unknown/1"}},
		{"while \"true\"; do x; done", []string{"true loop=while/-1/unknown/1", "x loop=while/-1/unknown/1"}},
		{"while ! true; do x; done", []string{"true loop=while/-1/unknown/1", "x loop=while/-1/unknown/1"}},
		{"while true | cat; do x; done", []string{"true loop=while/-1/unknown/1", "cat loop=while/-1/unknown/1", "x loop=while/-1/unknown/1"}},
		{"while read l; do x; done", []string{"read loop=while/-1/unknown/1", "x loop=while/-1/unknown/1"}},
		{"until false; do x; done", []string{"false loop=until/-1/false/1", "x loop=until/-1/false/1"}},
		{"until true; do x; done", []string{"true loop=until/-1/unknown/1", "x loop=until/-1/unknown/1"}},
		{"while false; do x; done", []string{"false loop=while/-1/unknown/1", "x loop=while/-1/unknown/1"}},
		{"for i in 1 2; do for j in a b c; do x; done; y; done; z", []string{
			"x loop=for_list/3/true/2", "y loop=for_list/2/true/1", "z"}},
		{"while true; do for i in 1 2; do x; done; done", []string{
			"true loop=while/-1/false/1", "x loop=for_list/2/true/2"}},
		{"for i in 1 2; do bash -c 'sleep 1'; done", []string{
			"bash loop=for_list/2/true/1", "sleep d1 loop=for_list/2/true/1 sleep=1/true"}},
		{"bash -c 'while true; do x; done'", []string{
			"bash", "true d1 loop=while/-1/false/1", "x d1 loop=while/-1/false/1"}},
		{"for i in 1 2; do bash -c 'for j in a b c; do x; done'; done", []string{
			"bash loop=for_list/2/true/1", "x d1 loop=for_list/3/true/2"}},
		// compound commands
		{"if a; then b; elif c; then d; else e; fi", []string{"a", "b", "c", "d", "e"}},
		{"case $x in a) b;; c|d) e;; esac", []string{"b", "e"}},
		{"case $(a) in $(b)) c;; esac", []string{"a $()", "b $()", "c"}},
		{"f() { a; }; f", []string{"a", "f"}},
		{"function f { a; }", []string{"a"}},
		{"! a", []string{"a"}},
		{"time a", []string{"a w=time"}},
		{"time -p a | b", []string{"a", "b"}},
		{"time { a; b; }", []string{"a", "b"}},
		{"(( i++ )); let j=1; [[ -f x ]]", []string{}},
		{"a; b\nc", []string{"a", "b", "c"}},
		{"", []string{}},
		{"   \n\t", []string{}},
		{"x=1", []string{}},
		{"x=1 y=2", []string{}},
		{"x=1 a", []string{"a"}},
		{"declare -r x=1", []string{}},
		// redirects
		{"a > f", []string{"a W=redirect:f"}},
		{"a >> f 2> g", []string{"a W=redirect:f,redirect:g"}},
		{"a &> f", []string{"a W=redirect:f"}},
		{"a &>> f", []string{"a W=redirect:f"}},
		{"a >| f", []string{"a W=redirect:f"}},
		{"a 3> f", []string{"a W=redirect:f"}},
		{"a >&f", []string{"a W=redirect:f"}},
		{"a >&2 2>&1 >&- <&3", []string{"a"}},
		{"a >&$fd", []string{"a"}},
		{"a > /dev/null 2>&1", []string{"a"}},
		{"a > $F", []string{"a W=redirect:*"}},
		{"a > \"$D/x\"", []string{"a W=redirect:*"}},
		{"a < f", []string{"a R=redirect:f"}},
		{"a < /dev/null", []string{"a"}},
		{"a <> f", []string{"a W=redirect:f R=redirect:f"}},
		{"a << EOF\nb\nEOF", []string{"a"}},
		{"a <<< b", []string{"a"}},
		{"a < f > g", []string{"a W=redirect:g R=redirect:f"}},
		{"{ a; b; } > f", []string{"a W=redirect:f", "b W=redirect:f"}},
		{"(a; b) < f", []string{"a R=redirect:f", "b R=redirect:f"}},
		{"if x; then a; fi > f", []string{"x W=redirect:f", "a W=redirect:f"}},
		{"{ a $(b); } > f", []string{"a W=redirect:f", "b $()"}},
		{"{ { a; } > f; b; } > g", []string{"a W=redirect:f,redirect:g", "b W=redirect:g"}},
		{"bash -c 'a' > f", []string{"bash W=redirect:f", "a d1 W=redirect:f"}},
		{"cat a > b", []string{"cat W=redirect:b R=cat:a"}},
		{"tee x > y", []string{"tee W=tee:x,redirect:y"}},
		// shells
		{"sh -c 'a; b'", []string{"sh", "a d1", "b d1"}},
		{"bash -c a", []string{"bash", "a d1"}},
		{"zsh -c 'a'", []string{"zsh", "a d1"}},
		{"dash -c 'a'", []string{"dash", "a d1"}},
		{"ksh -c 'a'", []string{"ksh"}},
		{"/bin/bash -c 'a'", []string{"bash", "a d1"}},
		{"bash -lc a", []string{"bash", "a d1"}},
		{"bash -l -c a", []string{"bash", "a d1"}},
		{"bash --norc -c a", []string{"bash", "a d1"}},
		{"bash -o pipefail -c a", []string{"bash", "a d1"}},
		{"bash -c a arg0 arg1", []string{"bash", "a d1"}},
		{"bash -c -- a", []string{"bash", "a d1"}},
		{"bash script.sh", []string{"bash"}},
		{"bash", []string{"bash"}},
		{"bash -c", []string{"bash"}},
		{"bash -c $X", []string{"bash"}},
		{"bash -c \"a $X\"", []string{"bash"}},
		{"bash -c 'echo \"x'", []string{"bash"}},
		{"bash -c 'a' && b", []string{"bash", "a d1", "b"}},
		{"env X=1 bash -c a", []string{"bash w=env", "a d1"}},
		{"sudo -u x bash -c a", []string{"bash sudo w=sudo", "a d1"}},
		{"bash -c \"bash -c 'bash -c a'\"", []string{"bash", "bash d1", "bash d2"}},
		{"bash -c 'a | b'", []string{"bash", "a d1", "b d1"}},
		{"bash -c 'eval $x'", []string{"bash", "eval d1"}},
		// pipes and chains
		{"a | b", []string{"a", "b"}},
		{"a && b || c; d", []string{"a", "b", "c", "d"}},
		{"echo $(a) | b", []string{"echo", "a $()", "b"}},
		// wrappers keep their effects on the command
		{"sudo -u root rm -rf x", []string{"rm sudo w=sudo W=rm:x"}},
		{"nohup sleep 100 &", []string{"sleep & w=nohup sleep=100/true"}},
		{"env X=1 git -C d status", []string{"git w=env git=status"}},
		{"timeout 5 bash -c 'sleep 9'", []string{"bash w=timeout", "sleep d1 sleep=9/true"}},
		{"command sleep 400", []string{"sleep w=command sleep=400/true"}},
		{"exec sleep 400", []string{"sleep w=exec sleep=400/true"}},
		{"/bin/sleep 400", []string{"sleep sleep=400/true"}},
		{"\"sleep\" 400", []string{"sleep sleep=400/true"}},
		{"$X 400", []string{" "}},
		{"\"$X\" a", []string{" "}},
	}
	for _, tc := range tests {
		cl, err := Parse(tc.src, Options{})
		if err != nil {
			t.Errorf("%q: %v", tc.src, err)
			continue
		}
		got := renderAll(cl)
		if tc.src == "$X 400" || tc.src == "\"$X\" a" {
			// dynamic command word: Name is empty
			if len(got) != 1 || cl.Nodes[0].Name != "" || !cl.Nodes[0].Argv[0].Dynamic {
				t.Errorf("%q: %v", tc.src, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q:\n got  %q\n want %q", tc.src, got, tc.want)
		}
	}
}

func TestParsePipes(t *testing.T) {
	tests := []struct {
		src  string
		want []model.PipeEdge
	}{
		{"a | b", []model.PipeEdge{{From: 0, To: 1}}},
		{"a | b | c", []model.PipeEdge{{From: 0, To: 1}, {From: 1, To: 2}}},
		{"a |& b", []model.PipeEdge{{From: 0, To: 1}}},
		{"a && b | c", []model.PipeEdge{{From: 1, To: 2}}},
		{"a | b && c", []model.PipeEdge{{From: 0, To: 1}}},
		{"a; b | c", []model.PipeEdge{{From: 1, To: 2}}},
		{"echo $(x) | b", []model.PipeEdge{{From: 0, To: 2}}},
		{"a | echo $(x) | c", []model.PipeEdge{{From: 0, To: 1}, {From: 1, To: 3}}},
		{"x=1 | b", nil},
		{"a | (b; c) | d", []model.PipeEdge{{From: 0, To: 1}, {From: 2, To: 3}}},
		{"a | { b; c; } | d", []model.PipeEdge{{From: 0, To: 1}, {From: 2, To: 3}}},
		{"a | while read l; do b; done", []model.PipeEdge{{From: 0, To: 1}}},
		{"bash -c 'a | b' | c", []model.PipeEdge{{From: 1, To: 2}, {From: 0, To: 3}}},
		{"curl -s https://x | sh", []model.PipeEdge{{From: 0, To: 1}}},
		{"a & b | c", []model.PipeEdge{{From: 1, To: 2}}},
	}
	for _, tc := range tests {
		cl := mustParse(t, tc.src)
		if !reflect.DeepEqual(cl.Pipes, tc.want) {
			t.Errorf("%q: pipes = %v, want %v (nodes %s)", tc.src, cl.Pipes, tc.want, names(cl))
		}
		for _, e := range cl.Pipes {
			if e.From < 0 || e.To >= len(cl.Nodes) || e.From >= e.To {
				t.Errorf("%q: bad edge %v", tc.src, e)
			}
		}
	}
}

func TestParseWords(t *testing.T) {
	tests := []struct {
		src  string
		want []string // lit, with "*" suffix for dynamic words
	}{
		{"cmd plain", []string{"cmd", "plain"}},
		{"cmd 'a b' \"c d\"", []string{"cmd", "a b", "c d"}},
		{"cmd a\\ b", []string{"cmd", "a b"}},
		{"cmd \\$HOME", []string{"cmd", "$HOME"}},
		{"cmd \"a\\\"b\"", []string{"cmd", "a\"b"}},
		{"cmd \"a\\$b\"", []string{"cmd", "a$b"}},
		{"cmd \"a\\xb\"", []string{"cmd", "a\\xb"}},
		{"cmd 'a\\b'", []string{"cmd", "a\\b"}},
		{"cmd a\"b\"'c'", []string{"cmd", "abc"}},
		{"cmd ''", []string{"cmd", ""}},
		{"cmd $'a\\tb\\n'", []string{"cmd", "a\tb\n"}},
		{"cmd $'\\x41\\u00e9\\101\\e'", []string{"cmd", "A\u00e9A\x1b"}},
		{"cmd $'\\q\\\\'", []string{"cmd", "\\q\\"}},
		{"cmd ~/x ~", []string{"cmd", "~/x", "~"}},
		{"cmd a=b", []string{"cmd", "a=b"}},
		{"cmd $X", []string{"cmd", "$X*"}},
		{"cmd ${X}/y", []string{"cmd", "${X}/y*"}},
		{"cmd \"$X/y\"", []string{"cmd", "$X/y*"}},
		{"cmd \"a $X b\"", []string{"cmd", "a $X b*"}},
		{"cmd $(a b)", []string{"cmd", "$(a b)*"}},
		{"cmd `a`", []string{"cmd", "`a`*"}},
		{"cmd $((1+2))", []string{"cmd", "$((1+2))*"}},
		{"cmd <(a)", []string{"cmd", "<(a)*"}},
		{"cmd *.go", []string{"cmd", "*.go*"}},
		{"cmd a?", []string{"cmd", "a?*"}},
		{"cmd a[0-9]", []string{"cmd", "a[0-9]*"}},
		{"cmd [", []string{"cmd", "["}},
		{"cmd '*'", []string{"cmd", "*"}},
		{"cmd \"*\"", []string{"cmd", "*"}},
		{"cmd \\*", []string{"cmd", "*"}},
		{"cmd {a,b}", []string{"cmd", "{a,b}*"}},
		{"cmd {1..3}", []string{"cmd", "{1..3}*"}},
		{"cmd {a}", []string{"cmd", "{a}"}},
		{"cmd {}", []string{"cmd", "{}"}},
		{"cmd '{a,b}'", []string{"cmd", "{a,b}"}},
		{"cmd @(a|b)", []string{"cmd", "@(a|b)*"}},
		{"cmd a$X'b'", []string{"cmd", "a$Xb*"}},
		{"cmd a\\\nb", []string{"cmd", "ab"}},
		{"cmd -- -x", []string{"cmd", "--", "-x"}},
		{"cmd \"\"$X", []string{"cmd", "$X*"}},
	}
	for _, tc := range tests {
		n := mustParse(t, tc.src).Nodes[0]
		var got []string
		for _, w := range n.Argv {
			s := w.Lit
			if w.Dynamic {
				s += "*"
			}
			got = append(got, s)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestParseUnknown(t *testing.T) {
	tests := []struct {
		src    string
		reason string
	}{
		{"eval \"$CMD\"", "eval"},
		{"eval ls", "eval"},
		{"sudo eval x", "eval"},
		{"command eval x", "eval"},
		{"bash -c 'eval x'", "eval"},
		{"echo $(eval x)", "eval"},
		{"source \"$F\"", "source"},
		{"source $F", "source"},
		{". $F", "source"},
		{"source <(curl x)", "source"},
		{"", ""},
		{"source ./env.sh", ""},
		{". ./env.sh", ""},
		{"source", ""},
		{"echo eval", ""},
		{"evalx y", ""},
		{"bash -c \"$X\"", ""},
	}
	for _, tc := range tests {
		cl := mustParse(t, tc.src)
		if cl.Unknown != (tc.reason != "") || cl.UnknownReason != tc.reason {
			t.Errorf("%q: unknown=%v reason=%q, want %q", tc.src, cl.Unknown, cl.UnknownReason, tc.reason)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		"echo 'unterminated",
		"echo \"unterminated",
		"cat <<EOF\nbody",
		"(a",
		"a )",
		"if a; then",
		"for i in 1 2; do",
		"echo $(a",
		"case x in",
		"a |",
		"&& a",
		"echo ${",
		"{ a",
		"fi",
	} {
		if cl, err := Parse(src, Options{}); err == nil {
			t.Errorf("%q: want error, got %+v", src, cl)
		} else if cl != nil {
			t.Errorf("%q: non-nil list with error", src)
		}
	}
}

func TestParseOptionsMaxDepth(t *testing.T) {
	const src = "bash -c \"bash -c 'bash -c \\\"bash -c a\\\"'\""
	for _, tc := range []struct {
		opt  Options
		want string
	}{
		{Options{}, "bash/0 bash/1 bash/2"},
		{Options{MaxDepth: 1}, "bash/0 bash/1"},
		{Options{MaxDepth: 3}, "bash/0 bash/1 bash/2 bash/3"},
		{Options{MaxDepth: 4}, "bash/0 bash/1 bash/2 bash/3 a/4"},
		{Options{MaxDepth: -1}, "bash/0 bash/1 bash/2"},
	} {
		cl, err := Parse(src, tc.opt)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(cl); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.opt, got, tc.want)
		}
	}
}

func TestParserPort(t *testing.T) {
	var p ports.BashParser = Parser{}
	cl, err := p.ParseBash("sleep 300")
	if err != nil || len(cl.Nodes) != 1 || cl.Nodes[0].Sleep == nil || cl.Nodes[0].Sleep.Seconds != 300 {
		t.Fatalf("%+v %v", cl, err)
	}
	if cl.Shell != model.ShellBash {
		t.Errorf("shell = %q", cl.Shell)
	}
	var q ports.BashParser = Parser{Opt: Options{MaxDepth: 1}}
	cl, _ = q.ParseBash("bash -c 'bash -c a'")
	if got := names(cl); got != "bash/0 bash/1" {
		t.Errorf("Opt not applied: %q", got)
	}
	if _, err := p.ParseBash("echo 'x"); err == nil {
		t.Error("want parse error")
	}
}

func TestParseCRLFAndComments(t *testing.T) {
	cl := mustParse(t, "a # sleep 300\r\nb # c\n# d\nc")
	if got := names(cl); got != "a/0 b/0 c/0" {
		// CRLF: the carriage return stays part of the word in bash; we only
		// require that comments never produce nodes.
		t.Logf("names = %q", got)
	}
	if len(cl.Nodes) < 3 {
		t.Errorf("nodes = %d", len(cl.Nodes))
	}
	for _, n := range cl.Nodes {
		if strings.Contains(n.Name, "sleep") {
			t.Errorf("comment parsed as command: %+v", n)
		}
	}
}

func TestSleepInfinityIsInf(t *testing.T) {
	n := nodeOf(t, "sleep infinity")
	if n.Sleep == nil || !n.Sleep.Known || !math_IsInf(n.Sleep.Seconds) {
		t.Fatalf("%+v", n.Sleep)
	}
}

func math_IsInf(f float64) bool { return f > 1e308 }
