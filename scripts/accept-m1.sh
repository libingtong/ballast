#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# accept-m1.sh - milestone M1 (kernel), assembled from docs/architecture.md
# section 8.3 and the plan's milestone table:
#
#   static checks   gofmt, build, vet (host, windows, linux, darwin)
#   go test -race ./...   (includes internal/archtest)
#   lint / test-rules over rules/, rules/optional/, examples/ and the regress rules
#   the default pack injects at most 8000 characters
#   Codex: a shell read (`sed -n 1,80p a.go`) lets a later apply_patch of a.go
#          pass read-before-edit (and the control without the read does not)
#   regress.sh, fault.sh, concurrency.sh, hotreload.sh
#   bench           (scripts/bench, when present)
#
# No arguments. Everything runs in temporary directories.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init accept-m1
shared_binaries
ROOT_N="$(native "$REPO_ROOT")"

static_checks

sandbox_new gotest
section "go test -race ./..."
rc=0
(cd "$REPO_ROOT" && gocmd test -race -count=1 ./...) || rc=$?
if [ "$rc" = 0 ]; then ok "go test -race ./... (GOPROXY=off)"; else bad "go test -race ./..." "exit $rc"; fi

sandbox_new content
section "lint and test-rules over every content file"
CONTENT=("$ROOT_N/rules" "$ROOT_N/rules/optional" "$ROOT_N/examples" "$ROOT_N/testdata/bash-regress.json")
bl lint --client all --examples "${CONTENT[@]}"
expect_rc "ballast lint --client all --examples rules rules/optional examples testdata/bash-regress.json" 0
note "$(printf '%s' "$BL_OUT" | tail -n 1)"
bl test-rules "${CONTENT[@]}"
expect_rc "ballast test-rules over the same files" 0
note "$(printf '%s' "$BL_OUT" | tail -n 1)"
bl lint --client all --examples "$ROOT_N/schema/examples"
expect_rc "ballast lint over schema/examples" 0

section "the default pack is at most 8000 characters when injected"
cp "$REPO_ROOT/rules/default.json" "$BALLAST_HOME/rules.json"
CWD="$(native "$SB_WORK")"
hook claude-code SessionStart "{\"session_id\":\"inj\",\"cwd\":\"$CWD\",\"source\":\"startup\"}"
expect_rc "SessionStart with the default pack" 0
len="$("$ACCUTIL" json len "$BL_OUT_FILE" hookSpecificOutput.additionalContext)"
note "injected text: $len characters"
check_int_le "full injection <= 8000 characters" "$len" 8000
check_int_ge "and it is not empty" "$len" 100

section "Codex: a shell read satisfies read-before-edit for apply_patch"
write_file "$SB_WORK/a.go" "package a
"
PATCH='*** Begin Patch\n*** Update File: a.go\n@@\n-package a\n+package b\n*** End Patch\n'
cx() { # SESSION EVENT TOOL INPUT_JSON ID [EXTRA]
  hook codex "$2" "{\"session_id\":\"$1\",\"cwd\":\"$CWD\",\"tool_name\":\"$3\",\"tool_use_id\":\"$5\",\"tool_input\":$4${6:-}}"
}
cx cx-control PreToolUse apply_patch "{\"input\":\"$PATCH\"}" c0
expect_rc "control: apply_patch of an unread file" 0
n="$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" session_id=cx-control rule_id=read-before-edit type=hit)"
check_eq "control: read-before-edit fires (shadow decision recorded)" "$n" "1"
cx cx-read PreToolUse exec_command '{"cmd":"sed -n 1,80p a.go"}' c1
cx cx-read PostToolUse exec_command '{"cmd":"sed -n 1,80p a.go"}' c1 ',"tool_response":{"output":"package a"}'
cx cx-read PreToolUse apply_patch "{\"input\":\"$PATCH\"}" c2
expect_rc "apply_patch after a shell read" 0
n="$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" session_id=cx-read rule_id=read-before-edit type=hit)"
check_eq "apply_patch after a shell read (sed -n 1,80p a.go) does not hit read-before-edit" "$n" "0"

run_child "regress.sh" regress.sh
run_child "fault.sh" fault.sh
run_child "concurrency.sh" concurrency.sh
run_child "hotreload.sh" hotreload.sh
run_bench "bench (p95 <= 30 ms, every scenario)"

finish
