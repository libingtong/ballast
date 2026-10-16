#!/usr/bin/env bash
source /m0b/lib.sh
SB_ON='{"sandbox":{"enabled":true,"failIfUnavailable":true}}'
SB_SOFT='{"sandbox":{"enabled":true}}'
obs=""
variant() { # variant <name> <bwrap:yes|no> <settings-json>
  reset_world
  if [ "$2" = no ]; then
    for b in bwrap socat; do p="$(command -v $b)"; [ -n "$p" ] && mv "$p" "$p.off"; done
  fi
  put_managed "$(merge_json "$(hooks_json managed)" "$3")"
  run_claude none 25
  save_payloads "$1"
  if [ "$2" = no ]; then for b in bwrap socat; do [ -e "/usr/bin/$b.off" ] && mv "/usr/bin/$b.off" "/usr/bin/$b"; done; fi
  local n; n="$(probe_events | tr '\n' ' ')"
  local res; res="$(jq -r '.result // empty' /tmp/run-last.out 2>/dev/null | head -c 160 | tr '\n' ' ')"
  local err; err="$(head -c 300 /tmp/run-last.err | tr '\n' ' ')"
  V_RC=$RC; V_EV="$n"
  obs="$obs[$1] bwrap=$2 退出码 $RC;触发事件:${n:-无};result=\"$res\";stderr=\"$err\"。"
  cp /tmp/run-last.out "/tmp/$1.out"; cp /tmp/run-last.err "/tmp/$1.err"
}
variant bwrap-ok-failifunavailable yes "$SB_ON"; ok_ev="$V_EV"; ok_rc="$V_RC"
variant no-bwrap-failifunavailable  no  "$SB_ON"; no_ev="$V_EV"; no_rc="$V_RC"
variant no-bwrap-soft               no  "$SB_SOFT"; soft_ev="$V_EV"; soft_rc="$V_RC"
obs="托管 sandbox.enabled:true;去掉 bwrap 与 socat 用 mv 改名(同一镜像,运行期)。$obs"
if [ -z "$ok_ev" ]; then
  finish unverifiable none "对照组(bwrap 可用 + failIfUnavailable:true)就没有触发任何事件,无法判断拒绝启动是否由 bwrap 缺失造成。$obs"
elif [ -z "$no_ev" ]; then
  finish pass none "$obs"
else
  finish fail none "$obs"
fi
