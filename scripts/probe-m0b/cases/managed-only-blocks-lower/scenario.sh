#!/usr/bin/env bash
source /m0b/lib.sh
reset_world
put_managed "$(merge_json "$(hooks_json managed)" '{"allowManagedHooksOnly":true}')"
put_user    "$(hooks_json user)"
put_project "$(hooks_json project)"
put_local   "$(hooks_json local)"
run_claude none 20
save_payloads none
ss="$(probe_labels SessionStart)"; up="$(probe_labels UserPromptSubmit)"
obs="四层(managed/user/project/local)都配置 SessionStart 与 UserPromptSubmit 探针,托管层 allowManagedHooksOnly:true,none 模式(退出码 $RC)。SessionStart 被调用的层:[${ss:-无}];UserPromptSubmit 被调用的层:[${up:-无}]。"
if [ "$ss" = managed ] && [ "$up" = managed ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
