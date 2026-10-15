package psparse

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

func tgt(ts []model.WriteTarget) string {
	var out []string
	for _, t := range ts {
		p := t.Path
		if t.Dynamic {
			p = "*"
		}
		out = append(out, t.Via+":"+p)
	}
	return strings.Join(out, ",")
}

// render summarizes a node: canonical name plus the non-default fields.
func render(n model.CommandNode) string {
	var sb strings.Builder
	sb.WriteString(n.Name)
	if n.Async {
		sb.WriteString(" &")
	}
	if n.InSubst {
		sb.WriteString(" $()")
	}
	if l := n.Loop; l != nil {
		fmt.Fprintf(&sb, " loop=%s/%d/%s/%d", l.Kind, l.N, l.Bounded, l.Depth)
	}
	if len(n.Writes) > 0 {
		fmt.Fprintf(&sb, " W=%s", tgt(n.Writes))
	}
	if len(n.Reads) > 0 {
		fmt.Fprintf(&sb, " R=%s", tgt(n.Reads))
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

func mustParse(t testing.TB, src string) *model.CommandList {
	t.Helper()
	cl, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return cl
}

func TestCanonicalName(t *testing.T) {
	tests := map[string]string{
		"Start-Sleep":        "start-sleep",
		"START-SLEEP":        "start-sleep",
		"sleep":              "start-sleep",
		"Sleep":              "start-sleep",
		"rm":                 "remove-item",
		"RM":                 "remove-item",
		"del":                "remove-item",
		"erase":              "remove-item",
		"ri":                 "remove-item",
		"rd":                 "remove-item",
		"rmdir":              "remove-item",
		"sc":                 "set-content",
		"ac":                 "add-content",
		"cp":                 "copy-item",
		"copy":               "copy-item",
		"cpi":                "copy-item",
		"mv":                 "move-item",
		"move":               "move-item",
		"mi":                 "move-item",
		"ni":                 "new-item",
		"md":                 "new-item",
		"mkdir":              "new-item",
		"cat":                "get-content",
		"gc":                 "get-content",
		"type":               "get-content",
		"echo":               "write-output",
		"write":              "write-output",
		"iex":                "invoke-expression",
		"saps":               "start-process",
		"start":              "start-process",
		"sajb":               "start-job",
		"foreach":            "foreach-object",
		"%":                  "foreach-object",
		"?":                  "where-object",
		"cd":                 "set-location",
		"sl":                 "set-location",
		"chdir":              "set-location",
		"tee":                "tee-object",
		"clc":                "clear-content",
		"sls":                "select-string",
		"git":                "git",
		"git.exe":            "git",
		"GIT.EXE":            "git",
		"rg.exe":             "rg",
		"cp.exe":             "cp", // an external executable, not the alias
		"rm.exe":             "rm",
		"sleep.exe":          "sleep",
		`C:\Git\bin\git.exe`: "git",
		"/usr/bin/git":       "git",
		`.\run.ps1`:          "run.ps1",
		`.\rm`:               "rm",
		`Microsoft.PowerShell.Management\Get-Content`: "get-content",
		"Out-File":      "out-file",
		"unknown-thing": "unknown-thing",
		".exe":          ".exe",
		"":              "",
		".":             ".",
	}
	for in, want := range tests {
		if got := CanonicalName(in); got != want {
			t.Errorf("CanonicalName(%q) = %q, want %q", in, got, want)
		}
	}
	for alias, canon := range Aliases {
		if alias != strings.ToLower(alias) || canon != strings.ToLower(canon) {
			t.Errorf("alias table entry %q -> %q is not lower-case", alias, canon)
		}
		if CanonicalName(alias) != canon {
			t.Errorf("alias %q does not resolve to %q", alias, canon)
		}
	}
}

// The seed alias list is frozen: U2 may add entries, never remove or change them.
func TestAliasSeedsKept(t *testing.T) {
	seeds := map[string]string{
		"rm": "remove-item", "del": "remove-item", "erase": "remove-item", "ri": "remove-item",
		"rd": "remove-item", "rmdir": "remove-item", "sleep": "start-sleep", "sc": "set-content",
		"ac": "add-content", "cp": "copy-item", "copy": "copy-item", "cpi": "copy-item",
		"mv": "move-item", "move": "move-item", "mi": "move-item", "ni": "new-item", "md": "new-item",
		"mkdir": "new-item", "cat": "get-content", "gc": "get-content", "type": "get-content",
		"echo": "write-output", "write": "write-output", "iex": "invoke-expression",
		"saps": "start-process", "start": "start-process", "sajb": "start-job",
		"foreach": "foreach-object", "%": "foreach-object", "cd": "set-location",
		"sl": "set-location", "chdir": "set-location",
	}
	for k, v := range seeds {
		if Aliases[k] != v {
			t.Errorf("Aliases[%q] = %q, want %q", k, Aliases[k], v)
		}
	}
}

func TestParseStructure(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		// aliases and external names
		{"sleep 300", []string{"start-sleep sleep=300/true"}},
		{"Start-Sleep 5; sleep.exe 400", []string{"start-sleep sleep=5/true", "sleep sleep=400/true"}},
		{"git.exe status", []string{"git git=status"}},
		{`C:\Git\bin\git.exe -C d log`, []string{"git git=log"}},
		{"echo hi", []string{"write-output"}},
		// async
		{"Start-Sleep 300 &", []string{"start-sleep & sleep=300/true"}},
		{"Get-A | Get-B &", []string{"get-a &", "get-b &"}},
		{"Start-Job -ScriptBlock { Start-Sleep 300 }", []string{"start-job &", "start-sleep & sleep=300/true"}},
		{"Start-ThreadJob { Get-X }", []string{"start-threadjob &", "get-x &"}},
		{"Start-Process notepad", []string{"start-process"}},
		{"Start-Sleep 1; Start-Sleep 2 & Start-Sleep 3", []string{
			"start-sleep sleep=1/true", "start-sleep & sleep=2/true", "start-sleep sleep=3/true"}},
		// loops
		{"foreach ($i in 1,2,3) { Get-X }", []string{"get-x loop=foreach/3/true/1"}},
		{"foreach ($i in @(1,2)) { Get-X }", []string{"get-x loop=foreach/2/true/1"}},
		{"foreach ($i in 'a','b') { Get-X }", []string{"get-x loop=foreach/2/true/1"}},
		{"foreach ($i in \"a\",\"b c\") { Get-X }", []string{"get-x loop=foreach/2/true/1"}},
		{"foreach ($i in \"a$x\",'b') { Get-X }", []string{"get-x loop=foreach/-1/unknown/1"}},
		{"foreach ($i in 1..10) { Get-X }", []string{"get-x loop=foreach/10/true/1"}},
		{"foreach ($i in 10..1) { Get-X }", []string{"get-x loop=foreach/10/true/1"}},
		{"foreach ($i in 1..$n) { Get-X }", []string{"get-x loop=foreach/-1/unknown/1"}},
		{"foreach ($i in $list) { Get-X }", []string{"get-x loop=foreach/-1/unknown/1"}},
		{"foreach ($i in Get-ChildItem) { Get-X }", []string{"get-childitem", "get-x loop=foreach/-1/unknown/1"}},
		{"foreach ($i in (Get-ChildItem)) { Get-X }", []string{"get-childitem $()", "get-x loop=foreach/-1/unknown/1"}},
		{"foreach ($i in 1,2) { foreach ($j in 'a','b','c') { Get-X } Get-Y }", []string{
			"get-x loop=foreach/3/true/2", "get-y loop=foreach/2/true/1"}},
		{"1,2,3 | ForEach-Object { Get-X }", []string{"write-output", "foreach-object", "get-x loop=foreach/-1/unknown/1"}},
		{"Get-A | % { Get-X }", []string{"get-a", "foreach-object", "get-x loop=foreach/-1/unknown/1"}},
		{"Get-A | foreach { Get-X }", []string{"get-a", "foreach-object", "get-x loop=foreach/-1/unknown/1"}},
		{"Get-A | Where-Object { Get-X }", []string{"get-a", "where-object", "get-x"}},
		{"Get-A | ? { Get-X }", []string{"get-a", "where-object", "get-x"}},
		{"ForEach-Object -Begin { Get-B } -Process { Get-P }", []string{
			"foreach-object", "get-b loop=foreach/-1/unknown/1", "get-p loop=foreach/-1/unknown/1"}},
		{"while ($true) { Get-X }", []string{"get-x loop=while/-1/false/1"}},
		{"while ($True) { Get-X }", []string{"get-x loop=while/-1/false/1"}},
		{"while (1) { Get-X }", []string{"get-x loop=while/-1/false/1"}},
		{"while ($x -lt 3) { Get-X }", []string{"get-x loop=while/-1/unknown/1"}},
		{"while (Test-Path x) { Get-X }", []string{"test-path loop=while/-1/unknown/1", "get-x loop=while/-1/unknown/1"}},
		{"do { Get-X } while ($true)", []string{"get-x loop=while/-1/false/1"}},
		{"do { Get-X } while ($x)", []string{"get-x loop=while/-1/unknown/1"}},
		{"do { Get-X } until ($false)", []string{"get-x loop=until/-1/false/1"}},
		{"do { Get-X } until ($done)", []string{"get-x loop=until/-1/unknown/1"}},
		{"do { Get-X }\nuntil ($false)", []string{"get-x loop=until/-1/false/1"}},
		{"for ($i = 0; $i -lt 3; $i++) { Get-X }", []string{"get-x loop=for_c/-1/unknown/1"}},
		{"for (;;) { Get-X }", []string{"get-x loop=for_c/-1/unknown/1"}},
		{"while ($true) { foreach ($i in 1,2) { Get-X } }", []string{"get-x loop=foreach/2/true/2"}},
		// statement bodies
		{"if ($a) { Get-A } elseif ($b) { Get-B } else { Get-C }", []string{"get-a", "get-b", "get-c"}},
		{"if ($a) { Get-A }\nelse { Get-C }", []string{"get-a", "get-c"}},
		{"if (Test-Path x) { Get-A }", []string{"test-path", "get-a"}},
		{"try { Get-A } catch { Get-B } finally { Get-C }", []string{"get-a", "get-b", "get-c"}},
		{"try { Get-A } catch [System.IO.IOException], [System.Exception] { Get-B }", []string{"get-a", "get-b"}},
		{"try { Get-A }\ncatch { Get-B }\nfinally { Get-C }", []string{"get-a", "get-b", "get-c"}},
		{"switch ($x) { 1 { Get-A } default { Get-B } }", []string{"get-a", "get-b"}},
		{"switch -Regex ($x) { 'a' { Get-A } }", []string{"get-a"}},
		{"switch -File f.txt { 'a' { Get-A } }", []string{"get-a"}},
		{"function F { Get-A }\nF", []string{"get-a", "f"}},
		{"function F($a, $b = 2) { Get-A }", []string{"get-a"}},
		{"filter F { Get-A }", []string{"get-a"}},
		{"function F { param($x) begin { Get-B } process { Get-P } end { Get-E } }", []string{"get-b", "get-p", "get-e"}},
		{"trap { Get-A }", []string{"get-a"}},
		{"return Get-A", []string{"get-a"}},
		{"throw \"x\"", []string{}},
		{"exit 1", []string{}},
		{"break; continue", []string{}},
		{"ifx", []string{"ifx"}},
		{"if-then", []string{"if-then"}},
		// expressions
		{"$x = 5", []string{}},
		{"$x = Get-A", []string{"get-a"}},
		{"$x += Get-A", []string{"get-a"}},
		{"[int]$x = Get-A", []string{"get-a"}},
		{"$a, $b = Get-A", []string{"get-a"}},
		{"$x = 5; Get-A", []string{"get-a"}},
		{"$x = Get-A | Get-B", []string{"get-a", "get-b"}},
		{"$x = (Get-A)", []string{"get-a $()"}},
		{"$x = $(Get-A)", []string{"get-a $()"}},
		{"$x = @(Get-A; Get-B)", []string{"get-a $()", "get-b $()"}},
		{"$x = @{ a = 1; b = { Get-A } }", []string{}},
		{"[Console]::WriteLine('x')", []string{}},
		{"[System.IO.File]::WriteAllText('a', 'b')", []string{}},
		{"$x.Method(Get-A)", []string{"get-a $()"}},
		{"1 + 2", []string{}},
		{"60 * 5", []string{}},
		{"'x'", []string{"write-output"}},
		{"\"a$b\"", []string{"write-output"}},
		{"5", []string{"write-output"}},
		{"$x", []string{"write-output"}},
		{"1,2,3", []string{"write-output"}},
		{"-not (Test-Path x)", []string{"test-path $()"}},
		{"(Get-A).Count", []string{"get-a $()"}},
		{"Write-Output (Get-A) $(Get-B)", []string{"write-output", "get-a $()", "get-b $()"}},
		{"Write-Output \"v=$(Get-A)\"", []string{"write-output", "get-a $()"}},
		{"Write-Output \"v=$(Get-A \"$(Get-B)\")\"", []string{"write-output", "get-a $()", "get-b $()"}},
		{"Write-Output \"`$(Get-A)\"", []string{"write-output"}},
		{"Write-Output '$(Get-A)'", []string{"write-output"}},
		{"@\"\nv=$(Get-A)\n\"@ | Out-Null", []string{"get-a $()", "write-output", "out-null"}},
		{"@'\nv=$(Get-A)\n'@ | Out-Null", []string{"write-output", "out-null"}},
		// call operators
		{"& Get-A", []string{"get-a"}},
		{"& 'Get-A' -x", []string{"get-a"}},
		{"& \"C:\\tools\\sleep.exe\" 400", []string{"sleep sleep=400/true"}},
		{"& { Get-A }", []string{"get-a"}},
		{"& $cmd", []string{}},
		{"& (Get-Command x)", []string{"get-command $()"}},
		{"& \"$dir\\x.exe\"", []string{}},
		{". .\\profile.ps1", []string{"."}},
		{". { Get-A }", []string{".", "get-a"}},
		// pipes and chains
		{"Get-A | Get-B | Get-C", []string{"get-a", "get-b", "get-c"}},
		{"Get-A &&  Get-B || Get-C", []string{"get-a", "get-b", "get-c"}},
		{"Get-A; Get-B\nGet-C\r\nGet-D", []string{"get-a", "get-b", "get-c", "get-d"}},
		{"Get-A `\n  -x 1", []string{"get-a"}},
		{"Get-A |\n  Get-B", []string{"get-a", "get-b"}},
		{"Get-A | # comment\n  Get-B", []string{"get-a", "get-b"}},
		{"", []string{}},
		{"   \n\t;;", []string{}},
		{"# c", []string{}},
		{"<# c #>", []string{}},
		{"<#\nmulti\nline\n#>\nGet-A", []string{"get-a"}},
		{"Get-A # Start-Sleep 300", []string{"get-a"}},
		{"Get-A#notacomment", []string{"get-a#notacomment"}},
		// paths and wrappers
		{`.\script.ps1 -x 1`, []string{"script.ps1"}},
		{`C:\x\tool.exe a b`, []string{"tool"}},
		// writes and reads
		{"Set-Content -Path a.txt -Value hi", []string{"set-content W=set-content:a.txt"}},
		{"Set-Content a.txt hi", []string{"set-content W=set-content:a.txt"}},
		{"Set-Content -Value hi a.txt", []string{"set-content W=set-content:a.txt"}},
		{"Set-Content -LiteralPath a.txt -Value hi", []string{"set-content W=set-content:a.txt"}},
		{"Set-Content -Pa a.txt -Va hi", []string{"set-content W=set-content:a.txt"}},
		{"Set-Content -V hi a.txt", []string{"set-content W=set-content:hi"}}, // ambiguous prefix: positional binding
		{"Set-Content a.txt,b.txt hi", []string{"set-content W=set-content:a.txt,set-content:b.txt"}},
		{"Set-Content $f hi", []string{"set-content W=set-content:*"}},
		{"Set-Content \"$d\\a.txt\" hi", []string{"set-content W=set-content:*"}},
		{"Set-Content a.txt", []string{"set-content W=set-content:a.txt"}},
		{"Set-Content", []string{"set-content"}},
		{"Add-Content -Path:a.txt hi", []string{"add-content W=add-content:a.txt"}},
		{"Out-File a.txt", []string{"out-file W=out-file:a.txt"}},
		{"Out-File -FilePath a.txt -Encoding utf8 -Append", []string{"out-file W=out-file:a.txt"}},
		{"Out-File -Encoding utf8 a.txt", []string{"out-file W=out-file:a.txt"}},
		{"Out-File -Path a.txt", []string{"out-file W=out-file:a.txt"}},
		{"Out-File -Width 200 a.txt", []string{"out-file W=out-file:a.txt"}},
		{"Out-File -InputObject x a.txt", []string{"out-file W=out-file:a.txt"}},
		{"Copy-Item a b", []string{"copy-item W=copy-item:b"}},
		{"Copy-Item -Path a -Destination b -Recurse", []string{"copy-item W=copy-item:b"}},
		{"Copy-Item -Recurse a b", []string{"copy-item W=copy-item:b"}},
		{"Copy-Item -Dest b a", []string{"copy-item W=copy-item:b"}},
		{"Copy-Item a", []string{"copy-item"}},
		{"Move-Item a b -Force", []string{"move-item W=move-item:b"}},
		{"New-Item a.txt", []string{"new-item W=new-item:a.txt"}},
		{"New-Item -ItemType Directory -Path d", []string{"new-item W=new-item:d"}},
		{"New-Item -Path d -Name f -ItemType File", []string{"new-item W=new-item:d/f"}},
		{`New-Item -Path d\ -Name f`, []string{`new-item W=new-item:d\f`}},
		{"New-Item -Name f", []string{"new-item W=new-item:f"}},
		{"New-Item -Path $d -Name f", []string{"new-item W=new-item:*"}},
		{"ni a -Type File", []string{"new-item W=new-item:a"}},
		{"mkdir a", []string{"new-item W=new-item:a"}},
		{"md a", []string{"new-item W=new-item:a"}},
		{"Remove-Item a", []string{"remove-item W=remove-item:a"}},
		{"Remove-Item -Recurse -Force a", []string{"remove-item W=remove-item:a"}},
		{"rm -Recurse -Force a", []string{"remove-item W=remove-item:a"}},
		{"rm -r -fo a", []string{"remove-item W=remove-item:a"}},
		{"rm a b", []string{"remove-item W=remove-item:a,remove-item:b"}},
		{"rm a -ErrorAction SilentlyContinue", []string{"remove-item W=remove-item:a"}},
		{"rm -ea 0 a", []string{"remove-item W=remove-item:a"}},
		{"rm -rf a", []string{"remove-item W=remove-item:a"}},
		{"rm -LiteralPath a,b", []string{"remove-item W=remove-item:a,remove-item:b"}},
		{"rm -Path:a", []string{"remove-item W=remove-item:a"}},
		{"rm -Confirm:$false a", []string{"remove-item W=remove-item:a"}},
		{"rm *.log", []string{"remove-item W=remove-item:*"}},
		{"rm .\\build\\*", []string{"remove-item W=remove-item:*"}},
		{"rm 'a b'", []string{"remove-item W=remove-item:a b"}},
		{"Clear-Content a.txt", []string{"clear-content W=clear-content:a.txt"}},
		{"clc -Path a.txt", []string{"clear-content W=clear-content:a.txt"}},
		{"Get-A | Tee-Object -FilePath a.txt", []string{"get-a", "tee-object W=tee-object:a.txt"}},
		{"Get-A | tee a.txt", []string{"get-a", "tee-object W=tee-object:a.txt"}},
		{"Get-Content a.txt", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content -Path a.txt -Raw", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content -Raw a.txt", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content a.txt -TotalCount 5", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content -TotalCount 5 a.txt", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content -Head 5 a.txt", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content a.txt -Tail 5 -Encoding utf8", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content a.txt,b.txt", []string{"get-content R=get-content:a.txt,get-content:b.txt"}},
		{"Get-Content -LiteralPath a.txt", []string{"get-content R=get-content:a.txt"}},
		{"Get-Content -PSPath a.txt", []string{"get-content R=get-content:a.txt"}},
		{"cat a.txt", []string{"get-content R=get-content:a.txt"}},
		{"type a.txt", []string{"get-content R=get-content:a.txt"}},
		{"gc $f", []string{"get-content R=get-content:*"}},
		{"gc", []string{"get-content"}},
		{"Get-Content a.txt | Select-String foo", []string{"get-content R=get-content:a.txt", "select-string"}},
		{"Select-String foo a.go", []string{"select-string R=select-string:a.go"}},
		{"Select-String -Pattern foo a.go", []string{"select-string R=select-string:a.go"}},
		{"Select-String -Pattern foo -Path a.go,b.go", []string{"select-string R=select-string:a.go,select-string:b.go"}},
		{"Select-String -Path a.go foo", []string{"select-string R=select-string:a.go"}},
		{"Select-String -SimpleMatch -Pattern foo -LiteralPath a.go", []string{"select-string R=select-string:a.go"}},
		{"Select-String -Pattern foo", []string{"select-string"}},
		{"sls foo a.go", []string{"select-string R=select-string:a.go"}},
		{"rg.exe -n Parse src\\x.go", []string{"rg R=rg:src\\x.go"}},
		{"rg -n Parse src\\x.go", []string{"rg R=rg:src\\x.go"}},
		{"grep.exe -rn foo a b", []string{"grep R=grep:a,grep:b"}},
		{"sed.exe -n 1,5p a.go", []string{"sed R=sed:a.go"}},
		{"sed.exe -i s/a/b/ f", []string{"sed W=sed -i:f"}},
		{"head.exe -n 5 a.go", []string{"head R=head:a.go"}},
		{"awk.exe '{print}' a", []string{"awk R=awk:a"}},
		{"cat.exe a.txt", []string{"cat R=cat:a.txt"}},
		{"cp.exe a b", []string{"cp W=cp:b"}},
		{"rm.exe -rf a", []string{"rm W=rm:a"}},
		{"tee.exe a.txt", []string{"tee W=tee:a.txt"}},
		{"python -c \"open('a').read()\"", []string{"python"}},
		// redirects
		{"echo hi > out.txt", []string{"write-output W=redirect:out.txt"}},
		{"echo hi >out.txt", []string{"write-output W=redirect:out.txt"}},
		{"echo hi >> out.txt", []string{"write-output W=redirect:out.txt"}},
		{"echo hi 2> err.txt", []string{"write-output W=redirect:err.txt"}},
		{"echo hi 2>> err.txt", []string{"write-output W=redirect:err.txt"}},
		{"echo hi *> all.txt", []string{"write-output W=redirect:all.txt"}},
		{"echo hi 3> a 4>> b 5> c 6> d", []string{"write-output W=redirect:a,redirect:b,redirect:c,redirect:d"}},
		{"echo hi > a 2> b", []string{"write-output W=redirect:a,redirect:b"}},
		{"echo hi 2>&1", []string{"write-output"}},
		{"echo hi *>&1", []string{"write-output"}},
		{"echo hi > $null", []string{"write-output"}},
		{"echo hi 2> $null", []string{"write-output"}},
		{"echo hi > NUL", []string{"write-output"}},
		{"echo hi > $f", []string{"write-output W=redirect:*"}},
		{"echo hi > \"$env:TEMP\\x\"", []string{"write-output W=redirect:*"}},
		{"echo hi > 'a b.txt'", []string{"write-output W=redirect:a b.txt"}},
		{"echo hi 2>&1 > out.txt", []string{"write-output W=redirect:out.txt"}},
		{"echo hi > out.txt | Get-A", []string{"write-output W=redirect:out.txt", "get-a"}},
		{"'x' > out.txt", []string{"write-output W=redirect:out.txt"}},
		{"Get-A > out.txt", []string{"get-a W=redirect:out.txt"}},
		{"echo 2", []string{"write-output"}},
		{"echo a2>f", []string{"write-output W=redirect:f"}},
		{"Set-Content a.txt hi > b.txt", []string{"set-content W=set-content:a.txt,redirect:b.txt"}},
		// unknown-ish
		{"Invoke-Expression $c", []string{"invoke-expression"}},
		{"iex 'Get-A'", []string{"invoke-expression"}},
	}
	for _, tc := range tests {
		cl, err := Parse(tc.src)
		if err != nil {
			t.Errorf("%q: %v", tc.src, err)
			continue
		}
		got := renderAll(cl)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q:\n got  %q\n want %q", tc.src, got, tc.want)
		}
	}
}

func TestRedirectRecords(t *testing.T) {
	type r struct{ op, fd, target string }
	tests := []struct {
		src  string
		want []r
	}{
		{"a > f", []r{{">", "", "f"}}},
		{"a >> f", []r{{">>", "", "f"}}},
		{"a 2> f", []r{{"2>", "2", "f"}}},
		{"a 2>> f", []r{{"2>>", "2", "f"}}},
		{"a *> f", []r{{"*>", "*", "f"}}},
		{"a 2>&1", []r{{"2>&", "2", "1"}}},
		{"a *>&1", []r{{"*>&", "*", "1"}}},
		{"a 4>f", []r{{"4>", "4", "f"}}},
		{"a > f 2>&1", []r{{">", "", "f"}, {"2>&", "2", "1"}}},
	}
	for _, tc := range tests {
		n := mustParse(t, tc.src).Nodes[0]
		var got []r
		for _, rd := range n.Redirects {
			got = append(got, r{rd.Op, rd.Fd, rd.Target.Lit})
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: %v, want %v", tc.src, got, tc.want)
		}
	}
}

func TestParsePipes(t *testing.T) {
	tests := []struct {
		src  string
		want []model.PipeEdge
	}{
		{"Get-A | Get-B", []model.PipeEdge{{From: 0, To: 1}}},
		{"Get-A | Get-B | Get-C", []model.PipeEdge{{From: 0, To: 1}, {From: 1, To: 2}}},
		{"Get-A; Get-B | Get-C", []model.PipeEdge{{From: 1, To: 2}}},
		{"Get-A && Get-B | Get-C", []model.PipeEdge{{From: 1, To: 2}}},
		{"'x' | Out-File f", []model.PipeEdge{{From: 0, To: 1}}},
		{"$x | Get-A", []model.PipeEdge{{From: 0, To: 1}}},
		{"Write-Output (Get-A) | Get-B", []model.PipeEdge{{From: 0, To: 2}}},
		{"(Get-A) | Get-B", []model.PipeEdge{{From: 0, To: 1}}}, // the group's node is 0, Get-B is 1
		{"Get-A | % { Get-B | Get-C } | Get-D", []model.PipeEdge{{From: 2, To: 3}, {From: 0, To: 1}, {From: 1, To: 4}}},
		{"$x = Get-A | Get-B", []model.PipeEdge{{From: 0, To: 1}}},
		{"Get-A\nGet-B", nil},
	}
	for _, tc := range tests {
		cl := mustParse(t, tc.src)
		if !reflect.DeepEqual(cl.Pipes, tc.want) {
			t.Errorf("%q: pipes = %v, want %v (%q)", tc.src, cl.Pipes, tc.want, renderAll(cl))
		}
		for _, e := range cl.Pipes {
			if e.From < 0 || e.To >= len(cl.Nodes) || e.From >= e.To {
				t.Errorf("%q: bad edge %v", tc.src, e)
			}
		}
	}
}

func TestParseWordsAndParams(t *testing.T) {
	type want struct {
		argv   []string // lit, "*" suffix when dynamic
		params string   // sorted "name=v1|v2" entries joined by ";"
	}
	tests := []struct {
		src string
		w   want
	}{
		{"Get-A plain", want{[]string{"Get-A", "plain"}, ""}},
		{"Get-A 'it''s' \"a`\"b\" ‘s’ “d”", want{[]string{"Get-A", "it's", "a\"b", "s", "d"}, ""}},
		{"Get-A \"a\"\"b\"", want{[]string{"Get-A", "a\"b"}, ""}},
		{"Get-A 'a b' \"c d\"", want{[]string{"Get-A", "a b", "c d"}, ""}},
		{"Get-A \"tab`there\" \"nl`n\"", want{[]string{"Get-A", "tab\there", "nl\n"}, ""}},
		{"Get-A a`\" b`'c", want{[]string{"Get-A", "a\"", "b'c"}, ""}},
		{"Get-A $x", want{[]string{"Get-A", "$x*"}, ""}},
		{"Get-A $x.Prop", want{[]string{"Get-A", "$x.Prop*"}, ""}},
		{"Get-A $env:HOME\\.ballast", want{[]string{"Get-A", "$env:HOME\\.ballast*"}, ""}},
		{"Get-A ${env:HOME}\\x", want{[]string{"Get-A", "${env:HOME}\\x*"}, ""}},
		{"Get-A $_ $? $$ $^", want{[]string{"Get-A", "$_*", "$?*", "$$*", "$^*"}, ""}},
		{"Get-A \"a $x b\"", want{[]string{"Get-A", "a $x b*"}, ""}},
		{"Get-A \"a ${x} b\"", want{[]string{"Get-A", "a ${x} b*"}, ""}},
		{"Get-A \"cost: $5\"", want{[]string{"Get-A", "cost: $5*"}, ""}},
		{"Get-A \"cost $ 5\" \"end$\"", want{[]string{"Get-A", "cost $ 5", "end$"}, ""}},
		{"Get-A \"`$x\"", want{[]string{"Get-A", "$x"}, ""}},
		{"Get-A '$x'", want{[]string{"Get-A", "$x"}, ""}},
		{"Get-A $(Get-B x)", want{[]string{"Get-A", "$(Get-B x)*"}, ""}},
		{"Get-A (1+2)", want{[]string{"Get-A", "(1+2)*"}, ""}},
		{"Get-A @(1,2)", want{[]string{"Get-A", "@(1,2)*"}, ""}},
		{"Get-A @{a=1}", want{[]string{"Get-A", "@{a=1}*"}, ""}},
		{"Get-A @args", want{[]string{"Get-A", "@args*"}, ""}},
		{"Get-A { x }", want{[]string{"Get-A", "{ x }*"}, ""}},
		{"Get-A *.go a?.txt", want{[]string{"Get-A", "*.go*", "a?.txt*"}, ""}},
		{"Get-A '*.go'", want{[]string{"Get-A", "*.go"}, ""}},
		{"Get-A ?", want{[]string{"Get-A", "?"}, ""}},
		{"Get-A [int]$x", want{[]string{"Get-A", "[int]$x*"}, ""}},
		{"Get-A $a[0]", want{[]string{"Get-A", "$a[0]*"}, ""}},
		{"Get-A $h['a b']", want{[]string{"Get-A", "$h['a b']*"}, ""}},
		{"Get-A a,b,c", want{[]string{"Get-A", "a,b,c"}, ""}},
		{"Get-A 'a', 'b' ,\"c\"", want{[]string{"Get-A", "a,b,c"}, ""}},
		{"Get-A 1,$x", want{[]string{"Get-A", "1,$x*"}, ""}},
		{"Get-A a,\n b", want{[]string{"Get-A", "a,b"}, ""}},
		{"Get-A a`\n b", want{[]string{"Get-A", "a", "b"}, ""}},
		{"Get-A -Name value", want{[]string{"Get-A", "-Name", "value"}, "-name=value"}},
		{"Get-A -Name:value", want{[]string{"Get-A", "-Name:value"}, "-name=value"}},
		{"Get-A -Name:$x", want{[]string{"Get-A", "-Name:$x*"}, "-name=$x"}},
		{"Get-A -Name 'a b' -Other", want{[]string{"Get-A", "-Name", "a b", "-Other"}, "-name=a b;-other="}},
		{"Get-A -Name a b -Other c", want{[]string{"Get-A", "-Name", "a", "b", "-Other", "c"}, "-name=a|b;-other=c"}},
		{"Get-A pos -Name a", want{[]string{"Get-A", "pos", "-Name", "a"}, "-name=a"}},
		{"Get-A -NAME a", want{[]string{"Get-A", "-NAME", "a"}, "-name=a"}},
		{"Get-A -Name a,b", want{[]string{"Get-A", "-Name", "a,b"}, "-name=a|b"}},
		{"Get-A -5 -x1", want{[]string{"Get-A", "-5", "-x1"}, "-x1="}},
		{"Get-A '-Name' value", want{[]string{"Get-A", "-Name", "value"}, ""}},
		{"Get-A -", want{[]string{"Get-A", "-"}, ""}},
		{"Get-A --", want{[]string{"Get-A", "--"}, ""}},
		{"Get-A --long", want{[]string{"Get-A", "--long"}, ""}},
		{"Get-A -Name -Other v", want{[]string{"Get-A", "-Name", "-Other", "v"}, "-name=;-other=v"}},
		{"Remove-Item -Force -Recurse a", want{[]string{"Remove-Item", "-Force", "-Recurse", "a"}, "-force=;-recurse="}},
		{"Remove-Item -Path a -Force", want{[]string{"Remove-Item", "-Path", "a", "-Force"}, "-force=;-path=a"}},
		{"Get-A a=b", want{[]string{"Get-A", "a=b"}, ""}},
		{"Get-A ~/x", want{[]string{"Get-A", "~/x"}, ""}},
		{"Get-A C:\\x\\y.txt", want{[]string{"Get-A", "C:\\x\\y.txt"}, ""}},
		{"Get-A a#b", want{[]string{"Get-A", "a#b"}, ""}},
		{"Get-A a`nb", want{[]string{"Get-A", "anb"}, ""}},
		{"Get-A @'\nline1\nline2\n'@ b", want{[]string{"Get-A", "line1\nline2", "b"}, ""}},
		{"Get-A @\"\nv=$x\n\"@", want{[]string{"Get-A", "v=$x*"}, ""}},
		{"Get-A @'\n'@", want{[]string{"Get-A", ""}, ""}},
		{"Get-A 'a'b\"c\"", want{[]string{"Get-A", "abc"}, ""}},
		{"Get-A $x'b'", want{[]string{"Get-A", "$xb*"}, ""}},
	}
	for _, tc := range tests {
		cl, err := Parse(tc.src)
		if err != nil {
			t.Errorf("%q: %v", tc.src, err)
			continue
		}
		if len(cl.Nodes) == 0 {
			t.Errorf("%q: no nodes", tc.src)
			continue
		}
		n := cl.Nodes[0]
		var got []string
		for _, w := range n.Argv {
			s := w.Lit
			if w.Dynamic {
				s += "*"
			}
			got = append(got, s)
		}
		if !reflect.DeepEqual(got, tc.w.argv) {
			t.Errorf("%q: argv %q, want %q", tc.src, got, tc.w.argv)
		}
		var ps []string
		for k, v := range n.Params {
			var vs []string
			for _, w := range v {
				vs = append(vs, w.Lit)
			}
			ps = append(ps, k+"="+strings.Join(vs, "|"))
		}
		sort.Strings(ps)
		if gp := strings.Join(ps, ";"); gp != tc.w.params {
			t.Errorf("%q: params %q, want %q", tc.src, gp, tc.w.params)
		}
	}
}

func TestStartSleepForms(t *testing.T) {
	tests := []struct {
		src     string
		seconds float64
		known   bool
	}{
		{"Start-Sleep 5", 5, true},
		{"Start-Sleep -Seconds 5", 5, true},
		{"Start-Sleep -seconds 5", 5, true},
		{"Start-Sleep -Se 5", 5, true},
		{"Start-Sleep -s 5", 5, true},
		{"Start-Sleep -Seconds:5", 5, true},
		{"Start-Sleep -Seconds '5'", 5, true},
		{"Start-Sleep -Seconds \"5\"", 5, true},
		{"Start-Sleep -Seconds 1.5", 1.5, true},
		{"Start-Sleep -Seconds 0x10", 16, true},
		{"Start-Sleep -Seconds 1e2", 100, true},
		{"Start-Sleep -Milliseconds 1500", 1.5, true},
		{"Start-Sleep -ms 250", 0.25, true},
		{"Start-Sleep -Mi 250", 0.25, true},
		{"Start-Sleep -Seconds 1 -Milliseconds 500", 1.5, true},
		{"Start-Sleep -Duration 00:01:30", 90, true},
		{"Start-Sleep -Duration 1.02:00:00", 93600, true},
		{"Start-Sleep -Duration 00:00:01.5", 1.5, true},
		{"Start-Sleep -Duration 00:10", 600, true},
		{"Start-Sleep -Duration 5", 0, false},
		{"Start-Sleep -d 00:00:05", 0, false}, // ambiguous with -Debug
		{"Start-Sleep -Seconds $n", 0, false},
		{"Start-Sleep -Seconds (1+1)", 0, false},
		{"Start-Sleep -Seconds -5", 0, false},
		{"Start-Sleep -Seconds abc", 0, false},
		{"Start-Sleep -Seconds 1,2", 0, false},
		{"Start-Sleep -Seconds", 0, false},
		{"Start-Sleep", 0, false},
		{"Start-Sleep -Verbose", 0, false},
		{"Start-Sleep -Verbose 5", 5, true},
		{"sleep 7", 7, true},
		{"sleep -Seconds 7", 7, true},
		{"Start-Sleep 5 6", 5, true},
		{"Start-Sleep 1e999", 0, false},
	}
	for _, tc := range tests {
		cl := mustParse(t, tc.src)
		if len(cl.Nodes) != 1 || cl.Nodes[0].Sleep == nil {
			t.Errorf("%q: %v", tc.src, renderAll(cl))
			continue
		}
		s := cl.Nodes[0].Sleep
		if s.Known != tc.known || s.Seconds != tc.seconds {
			t.Errorf("%q: %+v, want {%v %v}", tc.src, *s, tc.seconds, tc.known)
		}
	}
}

func TestParseUnknownAndNesting(t *testing.T) {
	tests := []struct {
		src     string
		reason  string
		unknown bool
	}{
		{"iex $c", "invoke-expression", true},
		{"Invoke-Expression 'x'", "invoke-expression", true},
		{"sudo; iex x", "invoke-expression", true},
		{"& $c", "dynamic invocation", true},
		{"& \"$c\" a", "dynamic invocation", true},
		{"& ($c) a", "dynamic invocation", true},
		{"&$c", "dynamic invocation", true},
		{". $c", "source", true},
		{". \"$p\\x.ps1\"", "source", true},
		{"[scriptblock]::Create('x')", "scriptblock create", true},
		{"[System.Management.Automation.ScriptBlock]::Create($x)", "scriptblock create", true},
		{"$sb = [ScriptBlock]::Create($x); & $sb", "scriptblock create", true},
		{"& { & { Get-A } }", "", false},
		{"& { & { & { Get-A } } }", "block nesting too deep", true},
		{"ForEach-Object { ForEach-Object { ForEach-Object { Get-A } } }", "block nesting too deep", true},
		{"ForEach-Object { ForEach-Object { Get-A } }", "", false},
		{"((((((Get-A))))))", "nesting too deep", true},
		{"(((Get-A)))", "", false},
		{"Get-A", "", false},
		{". .\\x.ps1", "", false},
		{"& 'Get-A'", "", false},
		{"& { Get-A }", "", false},
	}
	for _, tc := range tests {
		cl := mustParse(t, tc.src)
		if cl.Unknown != tc.unknown || cl.UnknownReason != tc.reason {
			t.Errorf("%q: unknown=%v reason=%q, want %v %q", tc.src, cl.Unknown, cl.UnknownReason, tc.unknown, tc.reason)
		}
	}
	// Statement bodies (if/foreach/try) are not script blocks: only a generous
	// depth cap applies.
	deep := "if ($a) { foreach ($i in 1,2) { try { while ($true) { Get-A } } catch { } } }"
	cl := mustParse(t, deep)
	if cl.Unknown || len(cl.Nodes) != 1 || cl.Nodes[0].Name != "get-a" {
		t.Errorf("%q: %+v", deep, cl)
	}
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("if ($a) { ")
	}
	sb.WriteString("Get-A")
	sb.WriteString(strings.Repeat(" }", 20))
	cl = mustParse(t, sb.String())
	if !cl.Unknown || cl.UnknownReason != "block nesting too deep" {
		t.Errorf("20 nested ifs: %+v", cl)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		"Write-Output 'unterminated",
		"Write-Output \"unterminated",
		"Write-Output ‘smart",
		"Write-Output \"a $(Get-A \"",
		"Write-Output $(Get-A",
		"Write-Output (Get-A",
		"Write-Output ${x",
		"Write-Output \"${x\"",
		"Get-A { Get-B",
		"Get-A }",
		"Get-A )",
		"Get-A | ",
		"Get-A |\n",
		"| Get-A",
		"Get-A | | Get-B",
		"Get-A | ; Get-B",
		"Get-A | }",
		"@'\nbody",
		"@\"\nbody",
		"@'\nbody\n\"@",
		"<# open",
		"Get-A > ",
		"Get-A >\n",
		"Get-A > | Get-B",
		"foreach ($i in 1,2) { Get-A",
		"foreach ($i 1,2) { Get-A }",
		"foreach (i in 1,2) { Get-A }",
		"foreach ($i in 1,2)",
		"foreach ($i in 1,2) Get-A",
		"if ($a)",
		"if ($a) Get-A",
		"if ($a { Get-A }",
		"while ($true",
		"do { Get-A }",
		"do { Get-A } foo ($x)",
		"for ($i=0; $i -lt 3; $i++",
		"try { Get-A",
		"function F",
		"function",
		"switch",
		"switch ($x) { 'a' ",
		"switch ($x) Get-A",
		"& { Get-A",
		"[Get-A",
		"Get-A && ",
		"Get-A (",
		"Get-A ,",
		"Get-A $( ",
	} {
		cl, err := Parse(src)
		if err == nil {
			// Some of these are lenient by design; make sure nothing panics
			// and the list is well-formed.
			t.Logf("accepted %q: %v", src, renderAll(cl))
			continue
		}
		if cl != nil {
			t.Errorf("%q: non-nil list with error", src)
		}
	}
	// The ones that must be errors (unbalanced quotes / brackets / here-strings).
	for _, src := range []string{
		"Write-Output 'unterminated",
		"Write-Output \"unterminated",
		"Write-Output ‘smart",
		"Write-Output $(Get-A",
		"Write-Output (Get-A",
		"Get-A { Get-B",
		"Get-A }",
		"Get-A )",
		"@'\nbody",
		"@\"\nbody",
		"<# open",
		"foreach ($i in 1,2) { Get-A",
		"try { Get-A",
		"| Get-A",
		"Get-A | ",
		"Get-A > ",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%q: want error", src)
		}
	}
}

func TestParserPort(t *testing.T) {
	var p ports.PowerShellParser = Parser{}
	cl, err := p.ParsePowerShell("Start-Sleep -Seconds 300")
	if err != nil || len(cl.Nodes) != 1 || cl.Nodes[0].Sleep == nil || cl.Nodes[0].Sleep.Seconds != 300 {
		t.Fatalf("%+v %v", cl, err)
	}
	if cl.Shell != model.ShellPowerShell {
		t.Errorf("shell = %q", cl.Shell)
	}
	if _, err := p.ParsePowerShell("Write-Output 'x"); err == nil {
		t.Error("want error")
	}
}

func TestHelpers(t *testing.T) {
	for _, s := range []string{"0", "5", "-5", "+5", "1.5", ".5", "1e3", "1E-3", "0x1F", "10kb", "2GB", "5d", "5l", "5u", "1.5mb", "0xFFul"} {
		if !isNumber(s) {
			t.Errorf("isNumber(%q) = false", s)
		}
	}
	for _, s := range []string{"", "-", "+", "a", "5a", "1e", "e5", "0x", "0xZZ", "kb", "1..3", "1.2.3", "$5", "5-"} {
		if isNumber(s) {
			t.Errorf("isNumber(%q) = true", s)
		}
	}
	counts := []struct {
		in string
		n  int
		ok bool
	}{
		{"1,2,3", 3, true}, {" 1 , 2 ", 2, true}, {"'a','b'", 2, true}, {"@(1,2,3,4)", 4, true},
		{"1..5", 5, true}, {"5..1", 5, true}, {"-1..1", 3, true}, {"1", 1, true}, {"'a b'", 1, true},
		{"", 0, false}, {"$x", 0, false}, {"1..$n", 0, false}, {"1,$x", 0, false}, {"\"a$b\"", 0, false},
		{"Get-A", 0, false}, {"(1,2)", 0, false}, {"1,", 0, false}, {"@()", 0, false}, {"a,b", 0, false},
		{"'a,b'", 1, true}, {"'a','b,c',3", 3, true},
	}
	for _, tc := range counts {
		n, ok := literalCount(tc.in)
		if n != tc.n || ok != tc.ok {
			t.Errorf("literalCount(%q) = %d,%v want %d,%v", tc.in, n, ok, tc.n, tc.ok)
		}
	}
	for _, tc := range []struct {
		in    string
		until bool
		want  bool
	}{
		{"$true", false, true}, {" $True ", false, true}, {"1", false, true}, {"$false", false, false}, {"$x", false, false},
		{"$false", true, true}, {"0", true, true}, {"$true", true, false}, {"", false, false},
	} {
		if got := isInfinite(tc.in, tc.until); got != tc.want {
			t.Errorf("isInfinite(%q,%v) = %v", tc.in, tc.until, got)
		}
	}
}

func TestSpecResolve(t *testing.T) {
	s := specFor("remove-item")
	for in, want := range map[string]string{
		"-path": "-path", "-pa": "-path", "-rec": "-recurse", "-recurse": "-recurse", "-fo": "-force",
		"-ea": "-erroraction", "-erroraction": "-erroraction", "-lp": "-literalpath", "-l": "-literalpath",
		"-wi": "-whatif", "-conf": "-confirm", "-v": "-verbose",
	} {
		d := s.resolve(in)
		if d == nil || d.name != want {
			t.Errorf("resolve(%q) = %v, want %q", in, d, want)
		}
	}
	for _, in := range []string{"-f", "-i", "-e", "-zzz", "-", "-rf", "-ex-"} {
		if d := s.resolve(in); d != nil {
			t.Errorf("resolve(%q) = %q, want nil (ambiguous or unknown)", in, d.name)
		}
	}
	if specFor("get-process") != nil || specFor("") != nil {
		t.Error("unexpected spec")
	}
}
