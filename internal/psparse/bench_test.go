package psparse

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// bigScript builds a realistic ~size-byte PowerShell script mixing the
// constructs agents emit: pipelines, here-strings, redirects, aliases,
// sub-expressions, loops and the cmdlets whose targets are extracted.
func bigScript(size int) string {
	lines := []string{
		`Get-ChildItem -Path .\src -Recurse -Filter *.go | Select-String -Pattern "TODO" | Out-File -FilePath todo.txt -Append`,
		`foreach ($f in Get-ChildItem *.md) { Write-Host "checking $($f.Name)"; Add-Content -Path counts.txt -Value $f.Length }`,
		`$items = @(1,2,3) | ForEach-Object { $_ * 2 }; $h = @{ a = 1; b = 'two' }; $x = $items[0] + $h['a']`,
		`Set-Content -Path notes.md -Value @'
# notes
text with $(not a command) and "quotes"
'@`,
		`if (Test-Path $env:CONFIG) { Get-Content -Raw $env:CONFIG | Tee-Object -FilePath out.log } else { Write-Output 'missing' 2>&1 }`,
		`Copy-Item -Path build\output -Destination dist\ -Recurse -Force; Remove-Item -Recurse -Force build\tmp`,
		`[System.IO.File]::WriteAllText("a.txt", "b"); Start-Sleep -Seconds 2; Move-Item a.txt b.txt`,
		`$r = Invoke-RestMethod -Uri "https://example.invalid/api?q=$q" | ConvertTo-Json # trailing comment`,
		`while ($true) { Start-Sleep -Milliseconds 500; if ($done) { break } }`,
		`ls | where { $_.Length -gt 10 } | sort Name | select -First 5 > top.txt`,
	}
	var sb strings.Builder
	for i := 0; sb.Len() < size; i++ {
		fmt.Fprintf(&sb, "%s\n", lines[i%len(lines)])
	}
	return sb.String()
}

func BenchmarkParse100KB(b *testing.B) {
	src := bigScript(100 * 1024)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseSmall(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(`Remove-Item -Recurse -Force .\build; Start-Sleep -Seconds 3`); err != nil {
			b.Fatal(err)
		}
	}
}

// budgetLimit is the per-parse bound for the budget tests: the acceptance
// target of 5ms, relaxed under the race detector and on shared CI runners.
func budgetLimit() time.Duration {
	limit := 5 * time.Millisecond
	switch {
	case raceEnabled:
		limit = 60 * time.Millisecond
	case os.Getenv("CI") != "":
		limit *= 3
	}
	return limit
}

func bestOf(n int, src string) time.Duration {
	best := time.Hour
	for i := 0; i < n; i++ {
		start := time.Now()
		// A parse error is an acceptable outcome; the time is what matters.
		_, _ = Parse(src)
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// TestParse100KBBudget enforces the acceptance budget on a realistic script.
func TestParse100KBBudget(t *testing.T) {
	src := bigScript(100 * 1024)
	if len(src) < 100*1024 {
		t.Fatalf("generator produced %d bytes", len(src))
	}
	if _, err := Parse(src); err != nil {
		t.Fatal(err)
	}
	best := bestOf(30, src)
	runtime.GC()
	t.Logf("100KB parse best of 30: %v (limit %v)", best, budgetLimit())
	if best > budgetLimit() {
		t.Errorf("100KB parse took %v, budget %v", best, budgetLimit())
	}
}

// TestAdversarialBudget checks that pathological inputs stay linear: a
// quadratic scan would blow the 150ms watchdog and make the matcher fail-open.
func TestAdversarialBudget(t *testing.T) {
	const n = 100 * 1024
	cases := map[string]string{
		"comma list":        "Remove-Item " + strings.Repeat("a,", n/2) + "a",
		"comma list quoted": "Remove-Item " + strings.Repeat("'a',", n/4) + "'a'",
		"comma list calls":  "Remove-Item -Path " + strings.Repeat("$x,", n/3) + "$x",
		"open brackets":     "Get-A " + strings.Repeat("[", n),
		"bracket pairs":     "Get-A " + strings.Repeat("[a", n/2),
		"brackets quotes":   "Get-A " + strings.Repeat("['a'", n/4),
		"brackets long sq":  "Get-A [" + "'" + strings.Repeat("a", n) + "'",
		"index chain":       "Get-A $a" + strings.Repeat("[0]", n/3),
		"bracket newlines":  "Get-A " + strings.Repeat("[\n", n/2),
		"many params":       "Get-A " + strings.Repeat("-p a ", n/5),
		"many redirects":    "Get-A " + strings.Repeat("a > b ", n/6),
		"many statements":   strings.Repeat("Get-A x; ", n/9),
		"nested groups":     "Get-A " + strings.Repeat("(", n/2),
		"nested subexpr":    "Get-A " + strings.Repeat("$(", n/2),
		"open strings":      "Get-A " + strings.Repeat("\"$(", n/3),
	}
	// A quadratic scan of 100KB takes seconds, so a generous factor still
	// separates linear from quadratic; a tight one flakes on a loaded
	// machine (best of 5 reached ~400ms under -race next to 20 packages).
	limit := 10 * budgetLimit()
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if best := bestOf(5, src); best > limit {
				t.Errorf("%d-byte input took %v, budget %v", len(src), best, limit)
			}
		})
	}
}
