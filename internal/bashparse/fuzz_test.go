package bashparse

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// checkInvariants verifies structural properties every successful parse must
// satisfy, whatever the input.
func checkInvariants(t testing.TB, src string, cl *model.CommandList, maxDepth int) {
	t.Helper()
	if cl == nil {
		t.Fatalf("%q: nil list without error", src)
	}
	if cl.Shell != model.ShellBash {
		t.Fatalf("%q: shell = %q", src, cl.Shell)
	}
	if cl.Nodes == nil {
		t.Fatalf("%q: nil Nodes", src)
	}
	if cl.Unknown != (cl.UnknownReason != "") {
		t.Fatalf("%q: unknown=%v reason=%q", src, cl.Unknown, cl.UnknownReason)
	}
	for i, n := range cl.Nodes {
		if len(n.Argv) == 0 {
			t.Fatalf("%q: node %d has empty argv", src, i)
		}
		wantName := ""
		if !n.Argv[0].Dynamic {
			wantName = baseName(n.Argv[0].Lit)
		}
		if n.Name != wantName {
			t.Fatalf("%q: node %d name %q, argv0 %+v", src, i, n.Name, n.Argv[0])
		}
		if n.Depth < 0 || n.Depth > maxDepth {
			t.Fatalf("%q: node %d depth %d", src, i, n.Depth)
		}
		if n.Sudo && !contains(n.Wrappers, "sudo") {
			t.Fatalf("%q: node %d sudo without wrapper", src, i)
		}
		if l := n.Loop; l != nil {
			if l.Depth < 1 || l.N < -1 {
				t.Fatalf("%q: node %d loop %+v", src, i, l)
			}
			if l.Bounded == model.TriTrue && l.Kind != model.LoopForList {
				t.Fatalf("%q: node %d loop %+v bounded", src, i, l)
			}
		}
		for _, tg := range append(append([]model.WriteTarget{}, n.Writes...), n.Reads...) {
			if tg.Via == "" || tg.Dynamic == (tg.Path != "") {
				t.Fatalf("%q: node %d bad target %+v", src, i, tg)
			}
		}
		if n.Sleep != nil && (n.Name != "sleep" || (!n.Sleep.Known && n.Sleep.Seconds != 0)) {
			t.Fatalf("%q: node %d sleep %+v", src, i, n.Sleep)
		}
		if n.Name == "git" && n.Git == nil {
			t.Fatalf("%q: git node %d without Git", src, i)
		}
	}
	for _, e := range cl.Pipes {
		if e.From < 0 || e.To >= len(cl.Nodes) || e.From >= e.To {
			t.Fatalf("%q: bad pipe edge %+v over %d nodes", src, e, len(cl.Nodes))
		}
	}
	if b, err := json.Marshal(cl); err != nil {
		inf := false
		for _, n := range cl.Nodes {
			if n.Sleep != nil && math.IsInf(n.Sleep.Seconds, 0) {
				inf = true
			}
		}
		if !inf {
			t.Fatalf("%q: json: %v", src, err)
		}
	} else {
		var back model.CommandList
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("%q: json round trip: %v", src, err)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func seedSources(f interface{ Add(...any) }) {
	data, err := os.ReadFile(filepath.Join("testdata", "expect.json"))
	if err == nil {
		var ef expectFile
		if json.Unmarshal(data, &ef) == nil {
			for _, c := range ef.Cases {
				f.Add(c.Src)
			}
		}
	}
	for _, s := range goldenSources {
		f.Add(s)
	}
	for _, s := range []string{
		"", " ", "\n", "\\", "'", "\"", "`", "$(", "$((", "${", "<<", "<<EOF", "<(", ">(", "((", "))", "{", "}",
		"a | ", "| a", "a &&", "a ;; b", "case", "esac", "for", "done", "if", "fi", "function", "()",
		"echo $'", "echo $\"", "echo ${a:-${b:-${c}}}", "echo {a,b}{c,d}{1..3}", "a=b c=d", "!", "! !",
		"time", "time -p", "coproc", "coproc a", "[[ a ]]", "[[", "(( 1 ))", "let", "declare -a x=(1 2)",
		"x=(a b); echo ${x[@]}", "echo \x00", "echo \xff\xfe", "sleep 1e999999", "sleep 99999999999999999999d",
		"env -S", "sudo -u", "timeout", "nice -n", "git -C", "git -c", "sed -i", "dd of=", "ln",
		"bash -c 'bash -c \"bash -c \\\"bash -c a\\\"\"'", "cat <<EOF\n$(cat <<IN\nx\nIN\n)\nEOF",
		"0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0", "git add a b c d e f g h i j k l m n o p", strings.Repeat("a ", 40) + "; " + strings.Repeat("b ", 40),
		strings.Repeat("(", 200) + strings.Repeat(")", 200), strings.Repeat("$(", 100) + "a" + strings.Repeat(")", 100),
		strings.Repeat("{ ", 100) + "a;" + strings.Repeat("} ", 100), strings.Repeat("a | ", 500) + "b",
	} {
		f.Add(s)
	}
}

func FuzzParse(f *testing.F) {
	seedSources(f)
	f.Fuzz(func(t *testing.T, src string) {
		cl, err := Parse(src, Options{})
		if err != nil {
			if errors.Is(err, errInternal) {
				t.Fatalf("%q: %v", src, err)
			}
			if cl != nil {
				t.Fatalf("%q: list with error", src)
			}
			return
		}
		checkInvariants(t, src, cl, MaxShellDepth)
		cl2, err2 := Parse(src, Options{})
		if err2 != nil || !reflect.DeepEqual(cl, cl2) {
			t.Fatalf("%q: not deterministic", src)
		}
		if utf8.ValidString(src) {
			cl3, err3 := Parse(src, Options{MaxDepth: 1})
			if err3 != nil {
				t.Fatalf("%q: MaxDepth=1 failed: %v", src, err3)
			}
			checkInvariants(t, src, cl3, 1)
			if len(cl3.Nodes) > len(cl.Nodes) {
				t.Fatalf("%q: shallower parse has more nodes", src)
			}
		}
	})
}

func FuzzParseSleep(f *testing.F) {
	for _, s := range []string{"300", "5m", "1h", "1.5d", "infinity", "1e3", "", "-1", "x", "1e999", ".", "0x1", "5 5"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var words []model.Word
		for _, w := range strings.Fields(s) {
			words = append(words, model.Word{Lit: w})
		}
		si := ParseSleep(words)
		if !si.Known && si.Seconds != 0 {
			t.Fatalf("%q: %+v", s, si)
		}
		if si.Known && (math.IsNaN(si.Seconds) || si.Seconds < 0) {
			t.Fatalf("%q: %+v", s, si)
		}
	})
}

func FuzzTargets(f *testing.F) {
	for _, s := range []string{"cp a b", "sed -i x f", "grep -e a b", "awk -f p a", "tail -n 3 f", "rsync a b", "git -C a b", "dd of=x", "env -i a", "sudo -u x a"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var argv []model.Word
		for _, w := range strings.Fields(s) {
			argv = append(argv, model.Word{Lit: w, Dynamic: strings.Contains(w, "$")})
		}
		rest, wr, sudo := StripWrappers(argv)
		if len(rest)+len(wr) > len(argv)+0 && len(argv) > 0 {
			t.Fatalf("%q: StripWrappers grew the argv", s)
		}
		if sudo && !contains(wr, "sudo") {
			t.Fatalf("%q: sudo flag without wrapper", s)
		}
		if len(rest) > 0 {
			name := ""
			if !rest[0].Dynamic {
				name = baseName(rest[0].Lit)
			}
			_ = WriteTargets(name, rest)
			_ = ReadTargets(name, rest)
			_ = ParseGit(rest)
		}
	})
}
