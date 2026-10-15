#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# regress.sh - plan B1 regression set (14 cases) plus the PowerShell regression.
#
# 1. `ballast test-rules` over testdata/bash-regress.json and
#    scripts/fixtures/powershell-regress.json (engine level).
# 2. The 14 plan cases through a REAL hook process: the explicit-deny rules of
#    testdata/bash-regress.json sit in the user layer, so a hit is a deny
#    decision on stdout and a miss is "exit 0, empty stdout".
# 3. The PowerShell rules (repo layer) through the hook, plus the Go parser
#    regression tables of bashparse and psparse.
#
# No arguments. Runs on macOS, Linux and Windows Git Bash (the Windows CI job
# runs it for the PowerShell regression).
# shellcheck disable=SC2016  # PowerShell snippets contain literal $ in single quotes
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init regress
build_ballast
sandbox_new regress

ROOT_N="$(native "$REPO_ROOT")"

section "engine level: test-rules and lint over the regression rule files"
bl test-rules "$ROOT_N/testdata/bash-regress.json"
expect_rc "test-rules testdata/bash-regress.json" 0
bl test-rules "$ROOT_N/scripts/fixtures/powershell-regress.json"
expect_rc "test-rules scripts/fixtures/powershell-regress.json" 0
bl lint --client all --examples "$ROOT_N/testdata/bash-regress.json" "$ROOT_N/scripts/fixtures/powershell-regress.json"
expect_rc "lint --client all --examples" 0

section "hook level: user layer = bash-regress.json, repo layer = powershell-regress.json"
cp "$REPO_ROOT/testdata/bash-regress.json" "$SB_HOME/.ballast/rules.json"
PROJ="$SB_WORK/proj"
git_init "$PROJ"
mkdir -p "$PROJ/.ballast"
cp "$REPO_ROOT/scripts/fixtures/powershell-regress.json" "$PROJ/.ballast/rules.json"
write_file "$PROJ/a.txt" "x
"
gitc "$PROJ" add -A
gitc "$PROJ" commit -q -m init
CWD="$(native "$PROJ")"

N=0
# case_hook NAME WANT(deny|allow) TOOL COMMAND_JSON [EXTRA_INPUT_JSON_FRAGMENT]
case_hook() {
  local name=$1 want=$2 tool=$3 cmd=$4 extra=${5:-}
  N=$((N + 1))
  bl_in "{\"session_id\":\"regress\",\"cwd\":\"$CWD\",\"tool_name\":\"$tool\",\"tool_use_id\":\"rg$N\",\"tool_input\":{\"command\":\"$cmd\"$extra}}" \
    hook --client claude-code --event PreToolUse
  if [ "$want" = deny ]; then expect_deny "$name"; else expect_allow "$name"; fi
}

# The 14 cases of plan B1 (the command strings are JSON-escaped on purpose).
case_hook "B1-01 bounded retry loop with curl and a 1s sleep" allow Bash 'for i in 1 2 3; do curl -f localhost:8080 && break; sleep 1; done'
case_hook "B1-02 sleep 300"                                   deny  Bash 'sleep 300'
case_hook "B1-03 while true; do sleep 300; done"              deny  Bash 'while true; do sleep 300; done'
case_hook "B1-04 sleep 300 &"                                 allow Bash 'sleep 300 &'
case_hook "B1-05 run_in_background:true + sleep 300"          allow Bash 'sleep 300' ',"run_in_background":true'
case_hook "B1-06 env X=1 sleep 300"                           deny  Bash 'env X=1 sleep 300'
case_hook "B1-07 timeout 400 sleep 300"                       deny  Bash 'timeout 400 sleep 300'
case_hook "B1-08 bash -c 'sleep 300'"                         deny  Bash "bash -c 'sleep 300'"
case_hook "B1-09 echo \"sleep 300\""                          allow Bash 'echo \"sleep 300\"'
case_hook "B1-10 # sleep 300"                                 allow Bash '# sleep 300'
case_hook "B1-11 heredoc body containing sleep 300"           allow Bash 'cat <<EOF\nsleep 300\nEOF'
case_hook "B1-12 for i in 1 2 3; do sleep 30; done"           allow Bash 'for i in 1 2 3; do sleep 30; done'
case_hook "B1-13 git -C ../x commit -m m"                     deny  Bash 'git -C ../x commit -m m'
case_hook "B1-14 PowerShell Start-Sleep -Seconds 300"         deny  PowerShell 'Start-Sleep -Seconds 300'
check_eq "all 14 plan cases ran" "$N" "14"

section "fail-open on commands the parser cannot judge"
case_hook "eval makes the command list unknown (no decision)"  allow Bash 'eval \"$CMD\"'
case_hook "unterminated quote is a parse failure (no decision)" allow Bash 'echo \"unterminated'
case_hook "sleep inside a command substitution"                deny  Bash 'echo $(sleep 120)'
case_hook "sudo wrapper"                                        deny  Bash 'sudo sleep 90'
case_hook "hours syntax"                                        deny  Bash 'sleep 1h'

section "PowerShell regression through the hook"
case_hook "PS Start-Sleep -Seconds 300"                  deny  PowerShell 'Start-Sleep -Seconds 300'
case_hook "PS alias sleep 300"                           deny  PowerShell 'sleep 300'
case_hook "PS -Milliseconds 90000"                       deny  PowerShell 'Start-Sleep -Milliseconds 90000'
case_hook "PS call operator with a script block"        deny  PowerShell '& { Start-Sleep 300 }'
case_hook "PS Start-Sleep -Seconds 59"                   allow PowerShell 'Start-Sleep -Seconds 59'
case_hook "PS short sleeps in a bounded loop"            allow PowerShell 'foreach ($i in 1,2,3) { Start-Sleep 30 }'
case_hook "PS quoted text"                               allow PowerShell 'echo \"Start-Sleep 300\"'
case_hook "PS Start-Job runs it in the background"       allow PowerShell 'Start-Job { Start-Sleep 300 }'
case_hook "PS tool-level run_in_background"              allow PowerShell 'Start-Sleep 300' ',"run_in_background":true'
case_hook "PS git -C with a backslash path"              deny  PowerShell 'git -C ..\\x commit -m m'
case_hook "PS git.exe commit"                            deny  PowerShell 'git.exe commit -m m'
case_hook "PS git status is not commit"                  allow PowerShell 'git status'
case_hook "PS Set-Content into protected/"               deny  PowerShell 'Set-Content -Path protected/a.txt -Value x'
case_hook "PS Out-File into protected/"                  deny  PowerShell 'Get-Date | Out-File protected/d.txt'
case_hook "PS redirect into protected/"                  deny  PowerShell 'echo x > protected/a.txt'
case_hook "PS Get-Content only reads"                    allow PowerShell 'Get-Content protected/a.txt'
case_hook "PS write outside protected/"                  allow PowerShell 'Set-Content -Path src/a.txt -Value x'

section "the hooks above left a well-formed audit trail"
bl audit lint
expect_rc "audit lint after the regression run" 0
hits="$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=hit decision=deny)"
check_int_ge "audit holds a deny hit for every deny case" "$hits" 23

section "parser regression tables (Go)"
run_go "bashparse regress/expect/golden tables" test -count=1 -run 'TestRegressSet|TestExpectJSON|TestGolden' ./internal/bashparse
run_go "psparse regress/expect tables" test -count=1 -run 'TestRegressSet|TestExpectJSON' ./internal/psparse

finish
