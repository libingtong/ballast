#!/usr/bin/env bash
source /m0b/lib.sh
reset_world
put_managed "$(hooks_json managed)"
put_user    "$(hooks_json user)"
put_project "$(hooks_json project)"
put_local   "$(hooks_json local)"
run_claude none 20
save_payloads none
ss="$(probe_labels SessionStart)"; up="$(probe_labels UserPromptSubmit)"
obs="四层都配置探针,托管层不设 allowManagedHooksOnly,none 模式(退出码 $RC)。SessionStart 调用(按启动时间)顺序:[${ss:-无}];UserPromptSubmit:[${up:-无}]。同一事件的多个 hook 是并发启动的,顺序只代表启动时间先后。"
ok=1
for l in managed user project local; do
  echo " $ss " | grep -q " $l " || ok=0
  echo " $up " | grep -q " $l " || ok=0
done
if [ "$ok" = 1 ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
