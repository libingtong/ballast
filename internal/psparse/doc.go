// Package psparse (unit U2) is a CONSERVATIVE PowerShell tokenizer (plan
// B1 "PowerShell(ps)"; docs/dsl-semantics.md §6.3). Go has no mature
// PowerShell parser, so coverage is intentionally narrower than bash and
// this is documented for users.
//
// Hot path: yes. Imports: model, ports, stdlib only.
//
// Contract:
//   - Statements split on ';' and newlines; pipelines on '|' (outside quotes,
//     braces, parens). Single quotes are literal (a doubled single quote escapes one), double
//     quotes are literal unless they contain '$' or '`(' ⇒ Dynamic.
//   - Node.Name: lower-cased canonical cmdlet after alias resolution
//     (Aliases table below: rm/del/erase/ri/rd/rmdir → remove-item,
//     sleep → start-sleep, sc → set-content, ac → add-content, cp/copy/cpi →
//     copy-item, mv/move/mi → move-item, ni → new-item, cat/gc/type →
//     get-content, echo/write → write-output, iex → invoke-expression,
//     saps/start → start-process, ...). External executables keep their
//     basename lower-cased without ".exe" ("git.exe" → "git").
//   - Params: "-name" (lower-cased) → following non-parameter words until the
//     next parameter; positional words stay in Argv.
//   - Start-Sleep: -Seconds/-s N, -Milliseconds/-m N, or first positional
//     (seconds) ⇒ Sleep{Seconds, Known}.
//   - Writes: `>`/`>>`/`2>`/`*>` redirects; Out-File [-FilePath] X;
//     Set-Content/Add-Content [-Path] X; Remove-Item/Copy-Item -Destination/
//     Move-Item -Destination/New-Item [-Path] X; git and other externals as
//     in bashparse (cp/mv are aliases here, not externals).
//     P2 E1 additions: -LiteralPath (-PSPath) on Out-File/Set-Content/
//     Add-Content/Clear-Content/Remove-Item/Tee-Object, Tee-Object
//     [-FilePath] X, Clear-Content, Rename-Item (aliases ren/rni: both
//     -Path/-LiteralPath and -NewName are targets; a leaf -NewName is
//     resolved against the directory of a literal source path), the second
//     positional word of Copy-Item/Move-Item as the destination (a bound
//     -LiteralPath leaves no positional slot for -Path), and the static
//     calls [IO.File]::WriteAllText/AppendAllText/WriteAllBytes/
//     WriteAllLines (also System.IO.File, AppendAllLines and the *Async
//     forms) whose FIRST argument is the target: a plain quoted literal
//     gives Path, anything else Dynamic. Such a call yields a node named
//     like the member ("[io.file]::writealltext", Via the same) with
//     Argv = member word + the call arguments; a call that is only an
//     argument of a command word is a string and yields no node.
//     A first argument containing any typographic quote (U+2018..U+201E,
//     which PowerShell treats as quotes) is never a literal: Dynamic.
//     Known limits (not recognized, no node): `.Invoke(...)` forms such as
//     [IO.File]::WriteAllText.Invoke('a','x'); the bare [File]:: after
//     `using namespace System.IO`; other file-writing members (OpenWrite,
//     Create, CreateText, AppendText, Copy, Move, Replace) and
//     [IO.StreamWriter]::new; the type held in a variable (`$f=[IO.File]; $f::WriteAllText(...)`);
//     the `[global::System.IO.File]::` spelling; calls inside a hashtable
//     literal value (`@{a=[IO.File]::WriteAllText('a','x')}`, the baseline
//     does not parse `@{a=$(Remove-Item x)}` either). None of these
//     produce a node or set Unknown (docs/trust-model.md §2.1 class 1).
//     Rename-Item with a source ending in a separator resolves -NewName against the parent of that directory.
//   - Reads (same purpose and Word rules as bashparse): get-content
//     (aliases cat/gc/type) -Path/-LiteralPath/-PSPath values or the first
//     positional word ⇒ Reads{Via:"get-content"}; select-string
//     -Path/-LiteralPath values, or without them the second positional
//     word (the first is -Pattern) ⇒ Reads{Via:"select-string"}; external
//     executables use the bashparse read table (rg, grep, sed, awk, head,
//     tail, wc, ...; note `cat` is an alias here, not external). Other
//     parameters of these cmdlets (-TotalCount/-Head/-Tail/-Encoding/
//     -Pattern/...) consume their value as usual. Dynamic values ⇒
//     Reads{Dynamic:true}.
//   - git: same GitInvocation parsing as bashparse (shared helper allowed via
//     bashparse.ParseGit, the only permitted cross-package call).
//   - Async: Start-Job / `&` background operator at statement end ⇒ Async.
//   - Loops: foreach(...) {..}, ForEach-Object {..}, while(...) {..},
//     do {..} while/until ⇒ Loop (Bounded unknown except foreach over a
//     literal array ⇒ true, while($true) ⇒ false).
//   - Script blocks { ... } are tokenized recursively (depth ≤ 2).
//   - Invoke-Expression/iex, & $var, [scriptblock]::Create ⇒ Unknown=true.
//   - Unbalanced quotes/braces or here-strings that cannot be delimited ⇒ error.
//
// Acceptance (U2): testdata/expect.json; `Start-Sleep -Seconds 300` hits
// the explicit-deny no-long-sleep test rule; `Start-Sleep 30` inside a
// foreach over 3 literals does not; fuzz never panics.
package psparse
