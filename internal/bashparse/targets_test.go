package bashparse

import (
	"math"
	"testing"
)

func TestWriteTargets(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{"tee a b", []string{"tee:a", "tee:b"}},
		{"tee -a - out.log", []string{"tee:out.log"}},
		{"tee /dev/null", []string{}},
		{"tee $F", []string{"tee:*"}},
		{"cp a b c dest/", []string{"cp:dest/"}},
		{"cp -r -- a b", []string{"cp:b"}},
		{"cp -t dir a b", []string{"cp:dir"}},
		{"cp --target-directory=dir a b", []string{"cp:dir"}},
		{"cp -tdir a b", []string{"cp:dir"}},
		{"cp -S .bak a b", []string{"cp:b"}},
		{"cp a", []string{}},
		{"cp a $D", []string{"cp:*"}},
		{"mv a b", []string{"mv:b"}},
		{"mv -T a b", []string{"mv:b"}},
		{"install -m 644 -o root a b", []string{"install:b"}},
		{"install -d d1 d2", []string{"install:d1", "install:d2"}},
		{"install -t /usr/bin a b", []string{"install:/usr/bin"}},
		{"rsync -a src/ dest/", []string{"rsync:dest/"}},
		{"rsync -a -e 'ssh -p 2' --exclude .git src/ host:dst", []string{"rsync:host:dst"}},
		{"rsync src/", []string{}},
		{"sed -i 's/a/b/' f", []string{"sed -i:f"}},
		{"sed -i.bak 's/a/b/' f g", []string{"sed -i:f", "sed -i:g"}},
		{"sed --in-place=.bak -e s/a/b/ f", []string{"sed -i:f"}},
		{"sed --in-place s/a/b/ f", []string{"sed -i:f"}},
		{"sed -ni p f", []string{"sed -i:f"}},
		{"sed -i '' s/a/b/ f", []string{"sed -i:f"}},
		{"sed -i -e s/a/b/ -e s/c/d/ f", []string{"sed -i:f"}},
		{"sed -n p f", []string{}},
		{"sed -i s/a/b/ $F", []string{"sed -i:*"}},
		{"rm -rf a b", []string{"rm:a", "rm:b"}},
		{"rm -f -- -x", []string{"rm:-x"}},
		{"rm", []string{}},
		{"rm $X", []string{"rm:*"}},
		{"truncate -s 0 a b", []string{"truncate:a", "truncate:b"}},
		{"truncate --size=0 a", []string{"truncate:a"}},
		{"truncate -r ref a", []string{"truncate:a"}},
		{"dd if=a of=b bs=1M", []string{"dd:b"}},
		{"dd of=$OUT", []string{"dd:*"}},
		{"dd of=/tmp/$X", []string{"dd:*"}},
		{"dd if=a", []string{}},
		{"touch a b", []string{"touch:a", "touch:b"}},
		{"touch -d 'yesterday' -r ref a", []string{"touch:a"}},
		{"touch -t 202001010000 a", []string{"touch:a"}},
		{"ln -s target link", []string{"ln:link"}},
		{"ln -sf a b", []string{"ln:b"}},
		{"ln target", []string{"ln:target"}},
		{"ln /abs/dir/target", []string{"ln:target"}},
		{"ln -t dir a b", []string{"ln:dir"}},
		// perl / ruby in-place edits
		{"perl -pi -e 's/a/b/' f", []string{"perl -i:f"}},
		{"perl -pi.bak -e s/a/b/ f g", []string{"perl -i:f", "perl -i:g"}},
		{"perl -i -pe s/a/b/ f", []string{"perl -i:f"}},
		{"perl -i.orig -ne print f", []string{"perl -i:f"}},
		{"perl -lpi -e s/a/b/ f", []string{"perl -i:f"}},
		{"perl -p -i -e s/a/b/ -- f", []string{"perl -i:f"}},
		{"perl -pi -e s/a/b/ $F", []string{"perl -i:*"}},
		{"perl -pi -es/a/b/ f", []string{"perl -i:f"}},
		{"perl -pi script.pl f", []string{"perl -i:f"}},
		{"perl -I lib -Mstrict -pi -e s/a/b/ f", []string{"perl -i:f"}},
		{"perl5.38 -pi -e s/a/b/ f", []string{"perl -i:f"}},
		{"perl -pe s/a/b/ f", []string{}},
		{"perl -e 'print 1' f", []string{}},
		{"perl script.pl -i f", []string{}},
		{"perl -pi", []string{}},
		{"ruby -i -pe 'gsub(/a/,%q(b))' f", []string{"ruby -i:f"}},
		{"ruby -pi.bak -e gsub f g", []string{"ruby -i:f", "ruby -i:g"}},
		{"ruby -rjson -i -pe x f", []string{"ruby -i:f"}},
		{"ruby -r json -i -pe x f", []string{"ruby -i:f"}},
		{"ruby -pe x f", []string{}},
		// git: paths overwritten from a tree-ish
		{"git checkout -- f", []string{"git checkout:f"}},
		{"git checkout -- a b", []string{"git checkout:a", "git checkout:b"}},
		{"git checkout HEAD -- f", []string{"git checkout:f"}},
		{"git checkout main~2 -- sub/f", []string{"git checkout:sub/f"}},
		{"git checkout -f HEAD~1 -- f", []string{"git checkout:f"}},
		{"git checkout -- $F", []string{"git checkout:*"}},
		{"git checkout main", []string{}},
		{"git checkout -b feature", []string{}},
		{"git checkout f", []string{}},
		{"git restore f", []string{"git restore:f"}},
		{"git restore a b", []string{"git restore:a", "git restore:b"}},
		{"git restore --staged --worktree f", []string{"git restore:f"}},
		{"git restore -s HEAD~1 f", []string{"git restore:f"}},
		{"git restore --source=HEAD~1 -- f", []string{"git restore:f"}},
		{"git restore --source HEAD f", []string{"git restore:f"}},
		{"git restore -p f", []string{"git restore:f"}},
		{"git restore $F", []string{"git restore:*"}},
		{"git -C /r restore f", []string{"git restore:/r/f"}},
		{"git -C /r checkout -- f", []string{"git checkout:/r/f"}},
		{"git -C /r -C sub restore f", []string{"git restore:/r/sub/f"}},
		{"git -C /r restore /abs/f", []string{"git restore:/abs/f"}},
		{"git -C $D restore f", []string{"git restore:*"}},
		{"git status", []string{}},
		{"git commit -m x", []string{}},
		{"git restore", []string{}},
		{"ls a", nil},
		{"cat a", nil},
	}
	for _, tc := range tests {
		argv := ws(shellFields(tc.src)...)
		got := tgt(WriteTargets(baseName(argv[0].Lit), argv))
		if tc.want == nil {
			if len(got) != 0 {
				t.Errorf("%q: %v, want none", tc.src, got)
			}
			continue
		}
		if !eqStrings(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestReadTargets(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		// cat family
		{"cat a b", []string{"cat:a", "cat:b"}},
		{"cat -n - b", []string{"cat:b"}},
		{"cat -- -x", []string{"cat:-x"}},
		{"cat $F", []string{"cat:*"}},
		{"cat", []string{}},
		{"cat /dev/null", []string{}},
		{"tac -s , a", []string{"tac:a"}},
		{"nl -ba -w 3 -s : a", []string{"nl:a"}},
		{"nl --body-numbering a a.go", []string{"nl:a.go"}},
		{"wc -l a b", []string{"wc:a", "wc:b"}},
		{"wc --files0-from=list", []string{}},
		{"file -b -f list a", []string{"file:a"}},
		{"less +G a", []string{"less:a"}},
		{"less -p pattern a", []string{"less:a"}},
		{"more -n 5 a", []string{"more:a"}},
		// head/tail
		{"head -n 20 a", []string{"head:a"}},
		{"head -n20 a", []string{"head:a"}},
		{"head --lines=20 a", []string{"head:a"}},
		{"head --lines 20 a", []string{"head:a"}},
		{"head -c 10 a b", []string{"head:a", "head:b"}},
		{"head -50 a", []string{"head:a"}},
		{"head -qv a", []string{"head:a"}},
		{"tail -n +100 a", []string{"tail:a"}},
		{"tail +100 a", []string{"tail:a"}},
		{"tail -f a", []string{"tail:a"}},
		{"tail -s 2 -f a", []string{"tail:a"}},
		{"tail --pid 5 a", []string{"tail:a"}},
		{"tail -n 5 -- -weird", []string{"tail:-weird"}},
		{"tail", []string{}},
		// sed
		{"sed -n 1,5p a", []string{"sed:a"}},
		{"sed -n -e 1p -e 3p a b", []string{"sed:a", "sed:b"}},
		{"sed --expression=1p a", []string{"sed:a"}},
		{"sed -f script.sed a", []string{"sed:a"}},
		{"sed --file script.sed a", []string{"sed:a"}},
		{"sed -E s/a/b/ a", []string{"sed:a"}},
		{"sed -l 5 s/a/b/ a", []string{"sed:a"}},
		{"sed p", []string{}},
		{"sed -i s/a/b/ a", nil},
		{"sed -i.bak s/a/b/ a", nil},
		{"sed --in-place s/a/b/ a", nil},
		// grep family
		{"grep foo a", []string{"grep:a"}},
		{"grep -rn foo a dir/", []string{"grep:a", "grep:dir/"}},
		{"grep -e foo -e bar a", []string{"grep:a"}},
		{"grep -efoo a", []string{"grep:a"}},
		{"grep --regexp=foo a", []string{"grep:a"}},
		{"grep --regexp foo a", []string{"grep:a"}},
		{"grep -f pats a b", []string{"grep:a", "grep:b"}},
		{"grep -A 3 -B2 -C 1 -m 5 foo a", []string{"grep:a"}},
		{"grep --max-count=5 --include '*.go' --exclude-dir .git foo a", []string{"grep:a"}},
		{"grep --include '*.go' foo a", []string{"grep:a"}},
		{"grep --binary-files text foo a", []string{"grep:a"}},
		{"grep --color foo a b", []string{"grep:b"}},
		{"grep --color=always foo a", []string{"grep:a"}},
		{"grep foo", []string{}},
		{"grep foo -", []string{}},
		{"grep -- -v a", []string{"grep:a"}},
		{"egrep -i 'a|b' a", []string{"egrep:a"}},
		{"fgrep -x a b", []string{"fgrep:b"}},
		{"grep foo $F", []string{"grep:*"}},
		{"grep -ABC foo a", []string{"grep:a"}},
		{"rg TODO", []string{}},
		{"rg TODO src", []string{"rg:src"}},
		{"rg -n -g '*.go' -t go TODO a b", []string{"rg:a", "rg:b"}},
		{"rg --type-add 'x:*.x' --type x foo a", []string{"rg:a"}},
		{"rg --max-depth 2 --sort path -r X foo a", []string{"rg:a"}},
		{"rg -e foo a", []string{"rg:a"}},
		{"rg --files src", []string{"rg:src"}},
		{"rg -j4 -M 200 foo a", []string{"rg:a"}},
		{"rg --pre cat foo a", []string{"rg:a"}},
		{"rg -E utf-8 foo a", []string{"rg:a"}},
		{"rg --context-separator -- foo a", []string{"rg:a"}},
		// awk family
		{"awk '{print $1}' a b", []string{"awk:a", "awk:b"}},
		{"awk -F: -v n=1 prog x=1 a", []string{"awk:a"}},
		{"awk -F : -f prog.awk a", []string{"awk:a"}},
		{"awk -F, '{print}' a", []string{"awk:a"}},
		{"awk -vn=1 prog a", []string{"awk:a"}},
		{"awk prog $F", []string{"awk:*"}},
		{"awk prog x=$V a", []string{"awk:a"}},
		{"gawk -e 'BEGIN{}' a", []string{"gawk:a"}},
		{"gawk --source 'BEGIN{}' a", []string{"gawk:a"}},
		{"gawk -i inc prog a", []string{"gawk:a"}},
		{"mawk -W version prog a", []string{"mawk:a"}},
		{"awk", []string{}},
		// not in the table
		{"python3 -c 'print(1)'", nil},
		{"find . -name x", nil},
		{"xargs cat", nil},
		{"git show HEAD:a.go", nil},
	}
	for _, tc := range tests {
		argv := ws(shellFields(tc.src)...)
		got := tgt(ReadTargets(baseName(argv[0].Lit), argv))
		if tc.want == nil {
			if len(got) != 0 {
				t.Errorf("%q: %v, want none", tc.src, got)
			}
			continue
		}
		if !eqStrings(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestStripWrappers(t *testing.T) {
	tests := []struct {
		src      string
		rest     string
		wrappers string
		sudo     bool
	}{
		{"env X=1 Y=2 sleep 300", "sleep 300", "env", false},
		{"env -i -u HOME FOO=1 cmd a", "cmd a", "env", false},
		{"env -u HOME cmd", "cmd", "env", false},
		{"env --unset=HOME --ignore-environment cmd", "cmd", "env", false},
		{"env -C /tmp cmd", "cmd", "env", false},
		{"env -- cmd", "cmd", "env", false},
		{"env", "env", "", false},
		{"env X=1", "env X=1", "", false},
		{"env -S 'a b'", "env -S a b", "", false},
		{"/usr/bin/env X=1 cmd", "cmd", "env", false},
		{"nice cmd", "cmd", "nice", false},
		{"nice -n 5 cmd", "cmd", "nice", false},
		{"nice -n5 cmd", "cmd", "nice", false},
		{"nice -5 cmd", "cmd", "nice", false},
		{"nice --adjustment=5 cmd", "cmd", "nice", false},
		{"nice --adjustment 5 cmd", "cmd", "nice", false},
		{"nohup cmd a", "cmd a", "nohup", false},
		{"nohup", "nohup", "", false},
		{"timeout 5 cmd", "cmd", "timeout", false},
		{"timeout -s KILL 5 cmd", "cmd", "timeout", false},
		{"timeout -k 2 --preserve-status 5 cmd", "cmd", "timeout", false},
		{"timeout --signal=KILL --foreground 5m cmd", "cmd", "timeout", false},
		{"timeout $T cmd", "cmd", "timeout", false},
		{"timeout 5", "timeout 5", "", false},
		{"time cmd", "cmd", "time", false},
		{"time -p cmd", "cmd", "time", false},
		{"time -f %e -o out cmd", "cmd", "time", false},
		{"command cmd", "cmd", "command", false},
		{"command -p cmd", "cmd", "command", false},
		{"command -v cmd", "command -v cmd", "", false},
		{"command -V cmd", "command -V cmd", "", false},
		{"exec cmd", "cmd", "exec", false},
		{"exec -a name cmd", "cmd", "exec", false},
		{"exec -c cmd", "cmd", "exec", false},
		{"exec", "exec", "", false},
		{"sudo cmd", "cmd", "sudo", true},
		{"sudo -u root cmd", "cmd", "sudo", true},
		{"sudo -g wheel -n cmd", "cmd", "sudo", true},
		{"sudo -nu root cmd", "cmd", "sudo", true},
		{"sudo -uroot cmd", "cmd", "sudo", true},
		{"sudo --user=root cmd", "cmd", "sudo", true},
		{"sudo --user root cmd", "cmd", "sudo", true},
		{"sudo -E FOO=1 cmd", "cmd", "sudo", true},
		{"sudo -- cmd", "cmd", "sudo", true},
		{"sudo -i", "sudo -i", "", false},
		{"sudo", "sudo", "", false},
		{"sudo -u root nice -n 5 nohup make", "make", "sudo nice nohup", true},
		{"nohup sudo env A=1 timeout 3 time cmd", "cmd", "nohup sudo env timeout time", true},
		{"env env env cmd", "cmd", "env env env", false},
		{"$X cmd", "$X cmd", "", false},
		{"env $X cmd", "$X cmd", "env", false},
		{"sleep 5", "sleep 5", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		argv := ws(shellFields(tc.src)...)
		rest, wr, sudo := StripWrappers(argv)
		if got := joinLits(rest); got != tc.rest {
			t.Errorf("%q: rest = %q, want %q", tc.src, got, tc.rest)
		}
		if got := joinStr(wr); got != tc.wrappers {
			t.Errorf("%q: wrappers = %q, want %q", tc.src, got, tc.wrappers)
		}
		if sudo != tc.sudo {
			t.Errorf("%q: sudo = %v", tc.src, sudo)
		}
	}
}

func TestParseGit(t *testing.T) {
	type want struct {
		sub      string
		subDyn   bool
		cwd      string
		cwdDyn   bool
		gitDir   string
		workTree string
		config   string
		noPager  bool
		args     string
	}
	tests := []struct {
		src  string
		want want
	}{
		{"git status", want{sub: "status"}},
		{"git", want{}},
		{"git -C ../x commit -m m", want{sub: "commit", cwd: "../x", args: "-m m"}},
		{"git -C a -C b log", want{sub: "log", cwd: "a/b"}},
		{"git -C a -C /abs log", want{sub: "log", cwd: "/abs"}},
		{"git -C a/ -C b log", want{sub: "log", cwd: "a/b"}},
		{`git -C a\ -C b log`, want{sub: "log", cwd: `a\b`}},
		{"git -C '' log", want{sub: "log"}},
		{"git -C $D log", want{sub: "log", cwdDyn: true}},
		{"git -C a -C $D log", want{sub: "log", cwd: "a", cwdDyn: true}},
		{"git -c a=b -c core.x=y status", want{sub: "status", config: "a=b core.x=y"}},
		{"git --git-dir=.git --work-tree=. status", want{sub: "status", gitDir: ".git", workTree: "."}},
		{"git --git-dir .git --work-tree wt status", want{sub: "status", gitDir: ".git", workTree: "wt"}},
		{"git --no-pager log", want{sub: "log", noPager: true}},
		{"git -P log", want{sub: "log", noPager: true}},
		{"git --no-pager -p log", want{sub: "log"}},
		{"git --no-optional-locks --bare --literal-pathspecs -p status", want{sub: "status"}},
		{"git --namespace ns --exec-path=/x --config-env=a=B status", want{sub: "status"}},
		{"git $SUB x", want{subDyn: true, args: "x"}},
		{"git log --oneline -n 5 -- a b", want{sub: "log", args: "--oneline -n 5 -- a b"}},
		{"git -c", want{}},
		{"git -C", want{}},
		{"git --version", want{}},
		{"/usr/bin/git status", want{sub: "status"}},
		{"git.exe -C d status", want{sub: "status", cwd: "d"}},
		{`C:\Git\bin\git.exe status`, want{sub: "status"}},
		{"GIT status", want{sub: "status"}},
	}
	for _, tc := range tests {
		g := ParseGit(ws(shellFields(tc.src)...))
		if g == nil {
			t.Errorf("%q: nil", tc.src)
			continue
		}
		got := want{g.Sub, g.SubDynamic, g.Cwd, g.CwdDynamic, g.GitDir, g.WorkTree, joinStr(g.Config), g.NoPager, joinLits(g.Args)}
		if got != tc.want {
			t.Errorf("%q:\n got  %+v\n want %+v", tc.src, got, tc.want)
		}
	}
	for _, src := range []string{"gitk status", "", "$G status", "echo git", "gi"} {
		if g := ParseGit(ws(shellFields(src)...)); g != nil {
			t.Errorf("ParseGit(%q) = %+v, want nil", src, g)
		}
	}
}

func TestParseSleep(t *testing.T) {
	tests := []struct {
		src     string
		seconds float64
		known   bool
	}{
		{"300", 300, true},
		{"5m", 300, true},
		{"1h", 3600, true},
		{"1d", 86400, true},
		{"30s", 30, true},
		{"1.5", 1.5, true},
		{"1.5h", 5400, true},
		{".5", 0.5, true},
		{"0", 0, true},
		{"1e2", 100, true},
		{"1e2m", 6000, true},
		{"1h 30", 3630, true},
		{"1m 1m 1m", 180, true},
		{"2 3 4", 9, true},
		{"-- 5", 5, true},
		{"infinity", math.Inf(1), true},
		{"INF", math.Inf(1), true},
		{"5 infinity", math.Inf(1), true},
		{"", 0, false},
		{"$N", 0, false},
		{"5 $N", 0, false},
		{"abc", 0, false},
		{"5x", 0, false},
		{"-1", 0, false},
		{"--help", 0, false},
		{"1.2.3", 0, false},
		{"1e", 0, false},
		{"0x10", 0, false},
		{"m", 0, false},
		{".", 0, false},
		{"1_0", 0, false},
		{"+5", 0, false},
		{"NaN", 0, false},
	}
	for _, tc := range tests {
		got := ParseSleep(ws(shellFields(tc.src)...))
		if got.Known != tc.known || got.Seconds != tc.seconds {
			t.Errorf("ParseSleep(%q) = %+v, want {%v %v}", tc.src, got, tc.seconds, tc.known)
		}
	}
}
