#!/usr/bin/env bash
source /m0b/lib.sh
reset_world
put_managed "$(merge_json "$(hooks_json managed)" '{"disableAllHooks":true}')"
put_user    "$(hooks_json user)"
put_project "$(hooks_json project)"
put_local   "$(hooks_json local)"
run_claude none 20
save_payloads none
ss="$(probe_labels SessionStart)"; up="$(probe_labels UserPromptSubmit)"
obs="四层都配探针,托管层 disableAllHooks:true,none 模式(退出码 $RC)。SessionStart 调用的层:[${ss:-无}];UserPromptSubmit:[${up:-无}]。"
if [ -z "$ss" ] && [ -z "$up" ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
