package bashparse

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/golden.json from the current parser output")

// goldenSources is the corpus whose full CommandList output is pinned in
// testdata/golden.json. Review the diff by hand when it changes.
var goldenSources = []string{
	// plan B1 regression set
	"for i in 1 2 3; do curl -f localhost:8080 && break; sleep 1; done",
	"sleep 300",
	"while true; do sleep 300; done",
	"sleep 300 &",
	"env X=1 sleep 300",
	"timeout 400 sleep 300",
	"bash -c 'sleep 300'",
	"echo \"sleep 300\"",
	"# sleep 300",
	"cat <<EOF\nsleep 300\nEOF",
	"for i in 1 2 3; do sleep 30; done",
	"git -C ../x commit -m m",
	// wrappers
	"sudo -u root nice -n 5 nohup make",
	"time -p sleep 3",
	"command -v git",
	"exec >log.txt",
	// structure
	"a | b |& c",
	"{ echo a; echo b; } > out.txt 2>/dev/null",
	"(sleep 1; sleep 2) &",
	"echo $(date) `uname` <(ls) | tee out",
	"x=$(sleep 3) y=1",
	"if grep -q x f; then rm -f y; else touch z; fi",
	"case $x in a) sleep 70;; esac",
	"until false; do sleep 61; done",
	"for ((i=0;i<3;i++)); do sleep 61; done",
	"for f in *.go; do wc -l \"$f\"; done",
	"for i in {1..5} {a,b}x; do sleep 1; done",
	"while read l; do echo \"$l\"; done < in.txt",
	"f() { sleep 90; }; f",
	// sh -c
	"sh -c \"bash -c 'zsh -c \\\"sleep 9\\\"'\"",
	"sudo bash -lc 'cd /tmp && cat a.go' &",
	"bash -c \"$CMD\"",
	"bash -c 'echo \"unterminated'",
	// unknown
	"eval \"$CMD\"",
	"source \"$F\"",
	". ./env.sh",
	// words
	"echo $'a\\tb' \"x\\$y\" a\\ b *.go ~/x ${HOME}/y",
	// writes
	"echo hi > ~/.ballast/state/x.json",
	"cp -t dest a b",
	"mv a b",
	"install -m 644 a b",
	"install -d d1 d2",
	"rsync -a -e ssh src/ host:dst/",
	"sed -i.bak 's/a/b/' f.txt",
	"sed -i '' -e s/a/b/ f.txt",
	"rm -rf -- build tmp",
	"truncate -s 0 a.log",
	"dd if=/dev/zero of=$OUT",
	"touch -d yesterday a b",
	"ln -s target link",
	"cat a >> b 2>&1",
	"cat > /dev/null",
	// reads
	"cat -n a.go b.go | head -50",
	"head -n 20 src/a.go",
	"tail -n +100 -- log.txt",
	"nl -ba main.go | sed -n '1,40p'",
	"grep -rn -A 3 foo src/a.go dir/",
	"rg -n --glob '*.go' Parse internal/x.go",
	"awk -F: -v n=1 '{print $1}' x=1 /etc/passwd",
	"wc -l < src/a.go",
	"less +G a.log",
	// git
	"git --no-pager -c a=b --git-dir=.git -C a -C b log -p",
	"git $SUB status",
	"git",
	// sleep forms
	"sleep 1h 30",
	"sleep 1.5d",
	"sleep $N",
}

func TestGolden(t *testing.T) {
	path := filepath.Join("testdata", "golden.json")
	type entry struct {
		Src  string          `json:"src"`
		Want json.RawMessage `json:"want,omitempty"`
		Err  bool            `json:"error,omitempty"`
	}
	var got []entry
	for _, src := range goldenSources {
		cl, err := Parse(src, Options{})
		if err != nil {
			got = append(got, entry{Src: src, Err: true})
			continue
		}
		var mb bytes.Buffer
		menc := json.NewEncoder(&mb)
		menc.SetEscapeHTML(false)
		if mErr := menc.Encode(cl); mErr != nil {
			t.Fatalf("%q: %v", src, mErr)
		}
		got = append(got, entry{Src: src, Want: bytes.TrimSpace(mb.Bytes())})
	}
	if *update {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(got); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test -run TestGolden -update`)", err)
	}
	var want []entry
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d entries, corpus has %d (run -update)", len(want), len(got))
	}
	for i := range got {
		if want[i].Src != got[i].Src || want[i].Err != got[i].Err {
			t.Errorf("entry %d: src/error mismatch: %q", i, got[i].Src)
			continue
		}
		var a, b bytes.Buffer
		if got[i].Want != nil {
			_ = json.Compact(&a, got[i].Want)
			_ = json.Compact(&b, want[i].Want)
		}
		if a.String() != b.String() {
			t.Errorf("%q:\n got  %s\n want %s", got[i].Src, a.String(), b.String())
		}
	}
}
