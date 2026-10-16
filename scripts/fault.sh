#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# fault.sh - plan B7 fault injection: every FaultPoint x {error, panic, hang}.
#
# The binary is built with `-tags ballast_fault`. For each point and mode a
# real hook process handles a PreToolUse event whose decision would be "deny"
# (so the "Decision formed, stdout not committed" stage is meaningful) and must
# end with: exit 1, EMPTY stdout, stderr starting with "ballast:". Exit 2 or a
# half-written decision would block or alter the tool call. The fault points
# are read from internal/hook/faults.go so a new point is covered automatically
# and a removed one fails the script.
#
# No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init fault
build_ballast
build_ballast_fault
sandbox_new fault
BL_ALLOW_FAULT=1
export BL_ALLOW_FAULT

PROD_BIN="$BALLAST_BIN"
FAULT_BIN="$BALLAST_FAULT_BIN"

section "fault points"
POINTS="$(sed -n 's/^[[:space:]]*Fault[A-Za-z]*[[:space:]]*FaultPoint = "\([a-z_]*\)".*/\1/p' "$REPO_ROOT/internal/hook/faults.go" | tr '\n' ' ')"
# shellcheck disable=SC2086  # word splitting of the point list is intended
set -- $POINTS
check_eq "faults.go declares 8 fault points" "$#" "8"
for want in parse load state_read evaluate decided state_commit audit_write render; do
  case " $POINTS " in
    *" $want "*) ok "fault point $want is declared" ;;
    *) bad "fault point $want is declared" "found: $POINTS" ;;
  esac
done

cp "$REPO_ROOT/testdata/bash-regress.json" "$SB_HOME/.ballast/rules.json"
CWD="$(native "$SB_WORK")"

# payload CLIENT -> (event, JSON) for a command that the rules deny.
client_event() {
  case $1 in
    claude-code) echo PreToolUse ;;
    codex) echo PreToolUse ;;
    cursor) echo beforeShellExecution ;;
    hermes) echo pre_tool_call ;;
  esac
}
client_payload() { # CLIENT ID
  case $1 in
    claude-code) printf '{"session_id":"f-%s","cwd":"%s","tool_name":"Bash","tool_use_id":"%s","tool_input":{"command":"sleep 300"}}' "$1" "$CWD" "$2" ;;
    codex) printf '{"session_id":"f-%s","cwd":"%s","tool_name":"exec_command","tool_use_id":"%s","tool_input":{"cmd":"sleep 300"}}' "$1" "$CWD" "$2" ;;
    cursor) printf '{"conversation_id":"f-%s","cwd":"%s","command":"sleep 300"}' "$1" "$CWD" ;;
    hermes) printf '{"session_id":"f-%s","cwd":"%s","tool_name":"terminal","tool_input":{"command":"sleep 300"}}' "$1" "$CWD" ;;
  esac
}

N=0
run_hook() { # CLIENT
  N=$((N + 1))
  bl_in "$(client_payload "$1" "fault$N")" hook --client "$1" --event "$(client_event "$1")"
}

# expect_aborted NAME MODE POINT: exit 1, empty stdout, "ballast:" on stderr.
expect_aborted() {
  local name=$1 mode=$2 point=$3 want
  if [ "$BL_RC" != 1 ]; then
    bad "$name" "exit $BL_RC, want 1" "stdout: $(head -c 300 "$BL_OUT_FILE")" "stderr: $BL_ERR"
    return
  fi
  if [ -s "$BL_OUT_FILE" ]; then
    bad "$name" "stdout must stay empty, got: $(head -c 300 "$BL_OUT_FILE")"
    return
  fi
  case "$BL_ERR" in
    ballast:*) ;;
    *) bad "$name" "stderr must start with \"ballast:\", got: $BL_ERR"; return ;;
  esac
  case $mode in
    error) want="fault injected at $point" ;;
    panic) want="panic" ;;
    hang) want="watchdog" ;;
  esac
  case "$BL_ERR" in
    *"$want"*) ok "$name" ;;
    *) bad "$name" "stderr should mention \"$want\", got: $BL_ERR" ;;
  esac
}

for client in claude-code codex cursor hermes; do
  section "client $client: control run without a fault is a deny"
  BL_BIN="$FAULT_BIN"
  run_hook "$client"
  if [ "$BL_RC" = 0 ] && [ -s "$BL_OUT_FILE" ]; then
    ok "$client: the fault-enabled build still denies when BALLAST_FAULT is unset"
  else
    bad "$client: the fault-enabled build still denies when BALLAST_FAULT is unset" "exit $BL_RC stdout: $BL_OUT stderr: $BL_ERR"
  fi

  section "client $client: every point x {error, panic, hang}"
  for point in $POINTS; do
    for mode in error panic hang; do
      if [ "$mode" = hang ]; then
        # The hang cases assert the product's own 150 ms watchdog, so a CI
        # relaxation (BALLAST_TEST_WATCHDOG_MS) is blanked out for them; the
        # error and panic cases keep it so a stalled runner cannot let the
        # watchdog pre-empt the injected failure.
        BALLAST_TEST_WATCHDOG_MS="" BALLAST_FAULT="$point:$mode" run_hook "$client"
      else
        BALLAST_FAULT="$point:$mode" run_hook "$client"
      fi
      expect_aborted "$client $point:$mode" "$mode" "$point"
    done
  done
done

section "BALLAST_FAULT is ignored by the production build"
BL_BIN="$PROD_BIN"
for spec in decided:error decided:panic decided:hang render:error parse:error; do
  BALLAST_FAULT="$spec" run_hook claude-code
  if [ "$BL_RC" = 0 ] && printf '%s' "$BL_OUT" | grep -q '"permissionDecision":"deny"'; then
    ok "production build ignores BALLAST_FAULT=$spec and still denies"
  else
    bad "production build ignores BALLAST_FAULT=$spec" "exit $BL_RC stdout: $BL_OUT stderr: $BL_ERR"
  fi
done

section "malformed fault specs are ignored (no accidental fail-closed)"
BL_BIN="$FAULT_BIN"
for spec in "nosuchpoint:error" "decided:explode" "decided" ":" "decided:error:extra"; do
  BALLAST_FAULT="$spec" run_hook claude-code
  if [ "$BL_RC" = 0 ] && printf '%s' "$BL_OUT" | grep -q '"permissionDecision":"deny"'; then
    ok "fault build ignores the malformed spec \"$spec\""
  else
    bad "fault build ignores the malformed spec \"$spec\"" "exit $BL_RC stdout: $BL_OUT stderr: $BL_ERR"
  fi
done

section "a fault never corrupts what the later healthy runs see"
BL_BIN="$PROD_BIN"
bl audit lint
expect_rc "audit lint after the fault runs" 0
run_hook claude-code
if [ "$BL_RC" = 0 ] && [ -s "$BL_OUT_FILE" ]; then ok "a healthy run after all faults denies as before"; else bad "a healthy run after all faults denies as before" "exit $BL_RC stdout: $BL_OUT"; fi

finish
