package psparse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// checkInvariants verifies structural properties of every successful parse.
func checkInvariants(t testing.TB, src string, cl *model.CommandList) {
	t.Helper()
	if cl == nil || cl.Nodes == nil {
		t.Fatalf("%q: nil list or nodes", src)
	}
	if cl.Shell != model.ShellPowerShell {
		t.Fatalf("%q: shell %q", src, cl.Shell)
	}
	if cl.Unknown != (cl.UnknownReason != "") {
		t.Fatalf("%q: unknown=%v reason=%q", src, cl.Unknown, cl.UnknownReason)
	}
	for i, n := range cl.Nodes {
		if len(n.Argv) == 0 {
			t.Fatalf("%q: node %d has empty argv (placeholder left behind?)", src, i)
		}
		if n.Name != "" && n.Name != CanonicalName(n.Argv[0].Lit) && n.Name != "write-output" {
			t.Fatalf("%q: node %d name %q vs argv0 %q", src, i, n.Name, n.Argv[0].Lit)
		}
		if n.Depth != 0 {
			t.Fatalf("%q: node %d depth %d", src, i, n.Depth)
		}
		if l := n.Loop; l != nil && (l.Depth < 1 || l.N < -1) {
			t.Fatalf("%q: node %d loop %+v", src, i, l)
		}
		for _, tg := range append(append([]model.WriteTarget{}, n.Writes...), n.Reads...) {
			if tg.Via == "" || tg.Dynamic == (tg.Path != "") {
				t.Fatalf("%q: node %d bad target %+v", src, i, tg)
			}
		}
		if n.Sleep != nil && !n.Sleep.Known && n.Sleep.Seconds != 0 {
			t.Fatalf("%q: node %d sleep %+v", src, i, n.Sleep)
		}
		for k := range n.Params {
			if len(k) < 2 || k[0] != '-' || k != lower(k) {
				t.Fatalf("%q: node %d param key %q", src, i, k)
			}
		}
	}
	for _, e := range cl.Pipes {
		if e.From < 0 || e.To >= len(cl.Nodes) || e.From >= e.To {
			t.Fatalf("%q: bad pipe edge %+v over %d nodes", src, e, len(cl.Nodes))
		}
	}
	if _, err := json.Marshal(cl); err != nil {
		inf := false
		for _, n := range cl.Nodes {
			if n.Sleep != nil && math.IsInf(n.Sleep.Seconds, 0) {
				inf = true
			}
		}
		if !inf {
			t.Fatalf("%q: json: %v", src, err)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

var fuzzSeeds = []string{
	"funCtion#", "function #c", "function\t#", "function ",
	"", " ", "\n", ";", "|", "&", "&&", "||", ">", ">>", "2>&1", "*>", "(", ")", "{", "}", "[", "]", "$", "$(", "${", "@", "@(", "@{", "@'", "@\"",
	"'", "\"", "`", "<#", "#>", "#", "-", "--", "-x", "-x:", "$x = ", "$x = 1", "& ", ". ", "& $x", "%", "?", "1..", "..",
	"foreach", "foreach (", "foreach ($", "foreach ($i in", "foreach ($i in 1,2) {", "if", "if (", "if ($a) {", "else", "do", "do {}", "do {} while", "do {} while (",
	"while", "while (1)", "for (;;) {}", "try {} catch", "try {} catch [", "switch (", "switch -", "function", "function f", "function f {", "param(", "return", "throw", "trap",
	"Get-A `", "Get-A `\n", "Get-A |\n", "a,", ",a", "a,,b", "'a'b'c'", "\"a\"\"b\"", "@'\n'@", "@'\n\n'@ |", "@\"\n$(\n)\n\"@",
	"echo \"$(\"", "echo \"$(echo \"$(echo \"$(echo x)\")\")\"", "[scriptblock]::Create(", "Start-Sleep -", "Start-Sleep -:", "Start-Sleep -Seconds:",
	"Set-Content -", "Remove-Item -Path:", "New-Item -Path a -Name", "Select-String -", "git -C", "\x00", "\xff\xfe", "‘", "’x‘", "“x”", "echo ‘a’’b’",
	"Start-Sleep 1e999999", "Start-Sleep -Duration ::", "Start-Sleep -Duration 1:2:3:4",
	"& { & { & { & { x } } } }", "((((((((x))))))))", "$($($($($(x)))))", "{{{{{{{{x}}}}}}}}", "[[[[[[[[x]]]]]]]]",
}

func seedAll(f interface{ Add(...any) }) {
	data, err := os.ReadFile(filepath.Join("testdata", "expect.json"))
	if err == nil {
		var ef expectFile
		if json.Unmarshal(data, &ef) == nil {
			for _, c := range ef.Cases {
				f.Add(c.Src)
			}
		}
	}
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
}

func FuzzParse(f *testing.F) {
	seedAll(f)
	f.Fuzz(func(t *testing.T, src string) {
		cl, err := Parse(src)
		if err != nil {
			if errors.Is(err, errInternal) {
				t.Fatalf("%q: %v", src, err)
			}
			if cl != nil {
				t.Fatalf("%q: list with error", src)
			}
			return
		}
		checkInvariants(t, src, cl)
		cl2, err2 := Parse(src)
		if err2 != nil || !reflect.DeepEqual(cl, cl2) {
			t.Fatalf("%q: not deterministic", src)
		}
	})
}

func FuzzCanonicalName(f *testing.F) {
	for _, s := range []string{"", "rm", "Git.EXE", `C:\x\y.exe`, ".exe", "a/b", "%", "?"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := CanonicalName(s)
		if c != lower(c) && c != CanonicalName(c) {
			t.Fatalf("%q -> %q", s, c)
		}
	})
}

func TestParseConcurrent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ef expectFile
	if err := json.Unmarshal(data, &ef); err != nil {
		t.Fatal(err)
	}
	var srcs []string
	for _, c := range ef.Cases {
		srcs = append(srcs, c.Src)
	}
	want := make([]*model.CommandList, len(srcs))
	for i, s := range srcs {
		cl, err := Parse(s)
		if err != nil {
			cl = nil
		}
		want[i] = cl
	}
	const workers, rounds = 16, 60
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				i := (w*11 + r*5) % len(srcs)
				cl, err := Parser{}.ParsePowerShell(srcs[i])
				if err != nil {
					cl = nil
				}
				if !reflect.DeepEqual(cl, want[i]) {
					errs <- fmt.Sprintf("worker %d: %q differs", w, srcs[i])
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

const childEnv = "BALLAST_PSPARSE_CHILD"

// TestMain lets the test binary act as a child process that parses the
// expectation corpus and prints the result (TestParseAcrossProcesses).
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		fmt.Print(string(corpusJSON()))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func corpusJSON() []byte {
	data, err := os.ReadFile(filepath.Join("testdata", "expect.json"))
	if err != nil {
		return []byte(err.Error())
	}
	var ef expectFile
	if err := json.Unmarshal(data, &ef); err != nil {
		return []byte(err.Error())
	}
	var out []*model.CommandList
	for _, c := range ef.Cases {
		cl, err := Parse(c.Src)
		if err != nil {
			cl = nil
		}
		out = append(out, cl)
	}
	b, _ := json.Marshal(out)
	return b
}

func TestParseAcrossProcesses(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	want := corpusJSON()
	for i := 0; i < 2; i++ {
		cmd := exec.Command(exe, "-test.run=NONE")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("child: %v: %s", err, stderr.String())
		}
		if !bytes.Equal(out, want) {
			t.Fatal("child process output differs from in-process parse")
		}
	}
}
