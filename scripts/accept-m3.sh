#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# accept-m3.sh - milestone M3 (statistics and replay), from the plan's table:
#
#   * `ballast audit lint` on real hook output; a corrupt line is found
#   * audit rotation / retention (unit tests) and `ballast gc` (retention)
#   * stats: the golden tests, then shadow-mode counting through real hooks,
#     --sample review credentials, promote and its audit record
#   * replay: replay.sh over a SYNTHETIC replay set (3 generated transcripts,
#     one 40 MB, 24 generated labels) in a temporary BALLAST_HOME. Your real
#     set ($BALLAST_HOME/replay-set.txt) is only used when you run
#     scripts/replay.sh yourself or set BALLAST_ACCEPT_REAL_REPLAY=1 here.
#
# No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init accept-m3
shared_binaries
ROOT_N="$(native "$REPO_ROOT")"

sandbox_new gotest
section "unit tests: audit, stats, replay"
rc=0
(cd "$REPO_ROOT" && gocmd test -count=1 ./internal/audit ./internal/stats ./internal/replay) || rc=$?
if [ "$rc" = 0 ]; then ok "go test audit stats replay (stats golden included)"; else bad "go test audit stats replay" "exit $rc"; fi
rc=0
(cd "$REPO_ROOT" && gocmd test -count=1 -run 'TestComputeGolden|TestRenderTable' -v ./internal/stats | grep -c '^--- PASS' >"$TMP_ROOT/golden.n") || rc=$?
check_int_ge "stats golden tests ran and passed" "$(<"$TMP_ROOT/golden.n")" 2

sandbox_new m3
CWD="$(native "$SB_WORK")"
cp "$REPO_ROOT/rules/default.json" "$BALLAST_HOME/rules.json"

section "audit lint on real hook output"
# A rule that only logs (shadow mode) with a deny target gives stats something to count.
EXTRA='{"id":"shadow-frob","action":"log","target":"deny","event":"PreToolUse","match":{"bash":{"cmd":"frobnicate"}},"message":"shadow","inject":"Do not frobnicate.","examples":[{"name":"hit","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"frobnicate --all"}}},{"name":"miss","expect":"miss","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}}]}'
"$ACCUTIL" json append "$BALLAST_HOME/rules.json" rules "$EXTRA"
bl install --client claude-code --force --binary "$(native "$BALLAST_BIN")"
expect_rc "install claude-code (the manifest drives the capability check of promote)" 0
i=0
while [ "$i" -lt 6 ]; do
  i=$((i + 1))
  hook claude-code PreToolUse "$(payload_bash "m3-$((i % 2))" "sh$i" 'frobnicate --all' "$CWD")"
done
expect_allow "shadow hits are silent (action log)"
hook claude-code PreToolUse "$(payload_bash m3-0 sl 'sleep 300' "$CWD")"
expect_allow "default pack rules start in log mode (silent)"
hook claude-code Stop "{\"session_id\":\"m3-0\",\"cwd\":\"$CWD\",\"last_assistant_message\":\"done\"}"
bl audit lint
expect_rc "audit lint is clean on what the hooks wrote" 0
expect_out_has "audit lint says so" "0 个问题"
check_true "every audit line is a JSON object" "$ACCUTIL" jsonl valid "$BALLAST_HOME/audit"
check_int_ge "eval_summary lines are flushed at Stop" "$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=eval_summary)" 1
audit_file=""
for audit_file in "$BALLAST_HOME"/audit/*.jsonl; do break; done
printf 'this line is not an audit record\n' >>"$audit_file"
bl audit lint
expect_rc "audit lint finds the corrupt line (1)" 1
expect_out_has "and names the file" "$(basename "$audit_file")"
bl stats
expect_rc "stats tolerates the corrupt line" 0
expect_err_has "and mentions it on stderr" "1 条损坏"
# repair: drop the bad line again
grep -v '^this line is not an audit record$' "$audit_file" >"$audit_file.fixed"
mv "$audit_file.fixed" "$audit_file"
bl audit lint
expect_rc "audit lint is clean after the repair" 0

section "stats, sample, promote"
bl stats --json --since 1d
expect_rc "stats --json" 0
would=0
n="$("$ACCUTIL" json len "$BL_OUT_FILE" "")" 2>/dev/null || n=0
idx=0
while [ "$idx" -lt "${n:-0}" ]; do
  if [ "$("$ACCUTIL" json get "$BL_OUT_FILE" "$idx.rule")" = shadow-frob ]; then
    would="$("$ACCUTIL" json get "$BL_OUT_FILE" "$idx.would")"
  fi
  idx=$((idx + 1))
done
check_eq "stats counts the six shadow hits as would-deny" "$would" "6"
bl stats --by session
expect_rc "stats --by session" 0
bl rules promote shadow-frob --to deny
expect_rc "promote without a review credential is refused (1)" 1
bl stats --rule shadow-frob --sample 3
expect_rc "stats --rule --sample writes the review credential" 0
check_file "reviews/shadow-frob.json exists" "$BALLAST_HOME/reviews/shadow-frob.json"
bl rules promote shadow-frob --to deny
expect_rc "promote after the review" 0
check_eq "promote is audited" "$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=promote)" "1"
hook claude-code PreToolUse "$(payload_bash m3-9 sh9 'frobnicate' "$CWD")"
expect_deny "the promoted rule now denies"
bl rules promote no-such-rule --to deny
expect_rc "promote of an unknown rule (4)" 4

section "audit retention"
write_file "$BALLAST_HOME/audit/2000-01.jsonl" ""
bl gc
expect_rc "ballast gc" 0
check_no_file "an audit file past retention is removed" "$BALLAST_HOME/audit/2000-01.jsonl"
check_true "the current audit file is kept" test -f "$audit_file"

section "replay.sh over a synthetic replay set"
sandbox_new replayset
RP="$SB_HOME/synthetic"
mkdir -p "$RP"
"$ACCUTIL" gen-transcript "$RP/t1.jsonl" "$(native "$SB_WORK")" synth1 40 >/dev/null
"$ACCUTIL" gen-transcript "$RP/t2.jsonl" "$(native "$SB_WORK")" synth2 2 >/dev/null
"$ACCUTIL" gen-transcript "$RP/t3.jsonl" "$(native "$SB_WORK")" synth3 1 >/dev/null
printf '# synthetic replay set\n%s\n%s\n%s\n' "$(native "$RP/t1.jsonl")" "$(native "$RP/t2.jsonl")" "$(native "$RP/t3.jsonl")" >"$BALLAST_HOME/replay-set.txt"

# Without labels the script must report the missing ones.
before_tree="$(mktmp rs-before)"
{ "$ACCUTIL" tree "$BALLAST_HOME" labels; "$ACCUTIL" tree "$RP"; } >"$before_tree"
rc=0
out="$(BALLAST_REPLAY_OUT="$TMP_ROOT/rs-reports" bash "$LIB_DIR/replay.sh" 2>&1)" || rc=$?
check_eq "replay.sh fails while there are no labels" "$rc" "1"
check_contains "and says what is missing" "$out" "labels"

# Produce the labels from the reports of that very run, then run again.
sample_ids=0
for r in "$TMP_ROOT"/rs-reports/*.json; do
  [ -f "$r" ] || continue
  "$ACCUTIL" replay-label "$r" "$BALLAST_HOME/labels" 8 >/dev/null
  sample_ids=$((sample_ids + 1))
done
check_eq "three reports were produced" "$sample_ids" "3"
rc=0
out="$(BALLAST_REPLAY_OUT="$TMP_ROOT/rs-reports2" bash "$LIB_DIR/replay.sh" 2>&1)" || rc=$?
if [ "$rc" = 0 ]; then
  ok "replay.sh passes: 3 transcripts (one 40 MB), schema, memory, >= 20 labels"
  printf '%s\n' "$out" | grep -E '^(ok|SKIP|FAIL)|labels=' | sed 's/^/     /' | head -20
else
  bad "replay.sh passes on the synthetic set" "exit $rc" "$out"
fi
after_tree="$(mktmp rs-after)"
{ "$ACCUTIL" tree "$BALLAST_HOME" labels; "$ACCUTIL" tree "$RP"; } >"$after_tree"
# Only labels/ (created by this script, not by replay) is left out.
if cmp -s "$before_tree" "$after_tree"; then
  ok "ballast replay only reads: BALLAST_HOME (labels aside) and the transcripts are byte-identical"
else
  bad "ballast replay only reads" "$(diff "$before_tree" "$after_tree" | head -10)"
fi

rc=0
BALLAST_REQUIRE_REPLAY=1 BALLAST_HOME="$(native "$SB_ROOT/nowhere")" bash "$LIB_DIR/replay.sh" >/dev/null 2>&1 || rc=$?
check_eq "BALLAST_REQUIRE_REPLAY=1 turns a missing set into a failure" "$rc" "1"
rc=0
out="$(BALLAST_HOME="$(native "$SB_ROOT/nowhere")" bash "$LIB_DIR/replay.sh" 2>&1)" || rc=$?
check_eq "a missing set is an explicit SKIP with exit 0" "$rc" "0"
check_contains "and it says SKIP" "$out" "SKIP replay"

if [ "${BALLAST_ACCEPT_REAL_REPLAY:-0}" = 1 ]; then
  run_child "replay.sh over your real replay-set.txt" replay.sh
else
  skip "replay.sh over your real replay-set.txt" "run scripts/replay.sh yourself, or BALLAST_ACCEPT_REAL_REPLAY=1"
fi

finish
