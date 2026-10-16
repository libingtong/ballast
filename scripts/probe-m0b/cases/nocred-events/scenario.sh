#!/usr/bin/env bash
# nocred-events:§6.3。三种模式各跑一次,记录触发了哪些事件、客户端在哪一步退出。
source /m0b/lib.sh

summary=""
all_ok=1
declare -A TO=([none]=20 [blackhole]=12 [hang]=12)
for mode in none blackhole hang; do
  reset_world
  install_fixtures "$CASE_DIR"
  run_claude "$mode" "${TO[$mode]}"
  evs="$(probe_events | tr '\n' ' ' | sed 's/ $//')"
  res="$(jq -r '.result // empty' /tmp/run-last.out 2>/dev/null | head -c 120)"
  term="$(jq -r '.terminal_reason // empty' /tmp/run-last.out 2>/dev/null)"
  save_payloads "$mode"
  ss=0; ups=0
  probe_events | grep -q '^managed:SessionStart$' && ss=1
  probe_events | grep -q '^managed:UserPromptSubmit$' && ups=1
  summary="$summary[$mode] 退出码 $RC;触发事件:${evs:-无};SessionStart=$ss UserPromptSubmit=$ups;客户端输出 result=\"$res\" terminal_reason=\"$term\"。"
  if [ "$mode" = none ] || [ "$mode" = blackhole ] || [ "$mode" = hang ]; then
    [ "$ss" = 1 ] && [ "$ups" = 1 ] || all_ok=0
  fi
done
if [ "$all_ok" = 1 ]; then
  finish pass none "$summary"
else
  finish fail none "$summary"
fi
