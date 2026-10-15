#!/usr/bin/env bash
# M0a 探针:把 hook stdin 原样落盘,并按 BALLAST_PROBE_CASE 返回指定输出。
# 仅用于取证,不属于 Ballast 产品代码。
set -u
out="${BALLAST_PROBE_OUT:?}/${BALLAST_PROBE_CASE:-none}"
mkdir -p "$out"
input="$(cat)"
ev="$(printf '%s' "$input" | jq -r '.hook_event_name // "unknown"')"
ts="$(python3 -I -c 'import time;print(time.time_ns())')"
printf '%s' "$input" > "$out/${ts}-${ev}.json"
case "${BALLAST_PROBE_CASE:-}:$ev" in
  exit1-json-deny:PreToolUse)
    printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"probe deny via exit1"}}'
    exit 1 ;;
  updatedinput-no-allow:PreToolUse)
    printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"touch probe_b.txt"}}}' ;;
  updatedinput-allow:PreToolUse)
    printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"command":"touch probe_b.txt"}}}' ;;
  warn-additionalcontext:PreToolUse)
    printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"probe warn context"}}' ;;
  stop-systemmessage:Stop)
    printf '%s' '{"systemMessage":"probe system message"}' ;;
  stop-cap:Stop)
    printf '%s' '{"decision":"block","reason":"Reply with exactly the word: again"}' ;;
  parallel-posttooluse:PostToolUse)
    sleep 0.3
    python3 -I -c 'import time;print(time.time_ns())' > "$out/${ts}-${ev}.end" ;;
esac
exit 0
