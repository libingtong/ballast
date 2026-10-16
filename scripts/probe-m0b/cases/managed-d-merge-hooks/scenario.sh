#!/usr/bin/env bash
source /m0b/lib.sh
reset_world
put_managed "$(hooks_json managed)"
put_managed "$(hooks_json managed-d10)" managed-settings.d/10-a.json
put_managed "$(hooks_json managed-d20)" managed-settings.d/20-b.json
run_claude none 20
save_payloads none
ss="$(probe_labels SessionStart)"; up="$(probe_labels UserPromptSubmit)"
obs="基础文件 managed-settings.json(label managed)、managed-settings.d/10-a.json(managed-d10)、20-b.json(managed-d20)各配 SessionStart/UserPromptSubmit 探针,none 模式。SessionStart 调用(按启动时间):[${ss:-无}];UserPromptSubmit:[${up:-无}]。同一事件的 hook 并发启动,顺序只代表启动先后。"
ok=1
for l in managed managed-d10 managed-d20; do echo " $ss " | grep -q " $l " || ok=0; echo " $up " | grep -q " $l " || ok=0; done
if [ "$ok" = 1 ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
