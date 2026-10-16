#!/usr/bin/env bash
source /m0b/lib.sh
HOOKCMD='/opt/probe/hook-write.sh managed /home/agent/protected/from-hook'
mk_hooks() {
  jq -n --arg c "$HOOKCMD" '{hooks: ({SessionStart:[{hooks:[{type:"command",command:$c}]}], UserPromptSubmit:[{hooks:[{type:"command",command:$c}]}]})}'
}
SB='{"sandbox":{"enabled":true,"failIfUnavailable":true,"allowUnsandboxedCommands":false,"filesystem":{"denyWrite":["/home/agent/protected"]}}}'
one() { # one <name> <managed-extra-json>
  reset_world
  mkdir -p /home/agent/protected; chown agent:agent /home/agent/protected
  put_managed "$(merge_json "$(mk_hooks)" "$2")"
  run_claude none 25
  save_payloads "$1"
  cp /tmp/probe/write.log "/tmp/$1.write.log" 2>/dev/null || : > "/tmp/$1.write.log"
  [ -f /home/agent/protected/from-hook ] && F=yes || F=no
  W="$(cat /tmp/$1.write.log | tr '\n' ';')"
  EV="$(probe_events | tr '\n' ' ')"
  mkdir -p "$OUT/payloads/$1"; cp "/tmp/$1.write.log" "$OUT/payloads/$1/write.log"
  head -c 400 /tmp/run-last.err > "/tmp/$1.err"
}
one sandbox-on "$SB"; w_on="$W"; f_on="$F"; ev_on="$EV"; err_on="$(tr '\n' ' ' < /tmp/sandbox-on.err)"
one sandbox-off '{}'; w_off="$W"; f_off="$F"
# 佐证:同一配置下用模拟模型让客户端执行一次 Bash 写入同一目录。沙箱真在工作时,Bash 的写入失败而 hook 的写入成功。
mock_check() { # mock_check <name> <managed-extra-json>
  reset_world
  mkdir -p /home/agent/protected; chown agent:agent /home/agent/protected
  put_managed "$(merge_json "$(mk_hooks)" "$2")"
  start_mock "$(steps_script "$(bash_step 'echo bash > /home/agent/protected/from-bash; echo ok > /home/agent/proj/from-bash-ok.txt')")" || { M_STATE=mock_down; return; }
  RUN_PROMPT="run the command"
  run_claude mock 60 --permission-mode default --allowedTools Bash
  [ -f /home/agent/protected/from-bash ] && MB=written || MB=blocked
  [ -f /home/agent/proj/from-bash-ok.txt ] && MO=yes || MO=no
  [ -f /home/agent/protected/from-hook ] && MH=yes || MH=no
  M_STEPS="$(mock_steps_done)"
  mkdir -p "$OUT/payloads/$1"; cp /tmp/probe/write.log "$OUT/payloads/$1/write.log" 2>/dev/null
  printf 'bash_to_protected=%s bash_to_proj_written=%s hook_to_protected=%s mock_tool_steps=%s\n' "$MB" "$MO" "$MH" "$M_STEPS" > "$OUT/payloads/$1/summary.txt"
  M_STATE=ok
}
mock_check mock-sandbox-on "$SB"; on_b="$MB"; on_o="$MO"; on_h="$MH"; on_state="$M_STATE"; on_steps="${M_STEPS:-0}"
mock_check mock-sandbox-off '{}'; off_b="$MB"; off_h="$MH"
mocktxt="[模拟模型佐证 mode:mock] sandbox 开启:Bash 写 denyWrite 目录=$on_b、Bash 写项目目录=$on_o、hook 写 denyWrite 目录=$on_h(桩发出工具调用 $on_steps 次);对照无 sandbox:Bash 写=$off_b、hook 写=$off_h。"
sandbox_works=unknown
if [ "$on_state" = ok ] && [ "${on_steps:-0}" -ge 1 ]; then
  if [ "$on_b" = blocked ] && [ "$on_o" = yes ]; then sandbox_works=yes; else sandbox_works=no; fi
fi
obs="托管 SessionStart 与 UserPromptSubmit 的探针先记录 stdin,再执行 echo hook > /home/agent/protected/from-hook(目录属 agent,所以失败只可能来自沙箱)。[sandbox.enabled:true + failIfUnavailable:true + denyWrite=/home/agent/protected] 触发事件:${ev_on:-无};写入记录:${w_on:-无};from-hook 文件存在=$f_on;stderr:${err_on:-空}。[对照:无 sandbox 段] 写入记录:${w_off:-无};文件存在=$f_off。$mocktxt"
if [ -z "$ev_on" ]; then
  finish unverifiable none "沙箱配置下客户端没有触发任何事件(可能因 bwrap 不可用而拒绝启动),无法判断 hook 是否在沙箱内。$obs"
elif [ "$f_off" != yes ]; then
  finish unverifiable none "对照组(无 sandbox 段)写入也失败,探针本身有问题。$obs"
elif [ "$sandbox_works" = no ]; then
  finish unverifiable none "模拟模型佐证显示沙箱在本容器里没有拦住 Bash 的写入,无法证明沙箱确实在工作,所以 hook 能写入不构成 A20 的证据。$obs"
elif [ "$f_on" = yes ] && echo "$w_on" | grep -q 'SessionStart ok' && echo "$w_on" | grep -q 'UserPromptSubmit ok'; then
  finish pass none "$obs"
else
  finish fail none "$obs 【契约影响】hook 在沙箱内运行:T1 的代起设计与 Ballast 自身写 state/audit 都要重做,P2-RUNNER/P2-HOOK 暂停并升级为 architecture §12 契约变更。"
fi
