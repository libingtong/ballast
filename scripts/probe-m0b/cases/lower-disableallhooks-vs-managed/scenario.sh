#!/usr/bin/env bash
source /m0b/lib.sh
obs=""; ok=1
for layer in user project local; do
  reset_world
  put_managed "$(hooks_json managed)"
  for l in user project local; do
    j="$(hooks_json "$l")"
    [ "$l" = "$layer" ] && j="$(merge_json "$j" '{"disableAllHooks":true}')"
    case "$l" in user) put_user "$j";; project) put_project "$j";; local) put_local "$j";; esac
  done
  run_claude none 20
  save_payloads "$layer"
  ss="$(probe_labels SessionStart)"; up="$(probe_labels UserPromptSubmit)"
  extra=""
  [ "$ss" = managed ] && [ "$up" = managed ] && extra="(其余非托管层的 hook 也未被调用:一个低层的 disableAllHooks 关闭全部非托管 hook)"
  obs="$obs[${layer} 层 disableAllHooks:true] SessionStart 调用的层:[${ss:-无}];UserPromptSubmit:[${up:-无}]${extra}。"
  echo " $ss " | grep -q " managed " || ok=0
  echo " $up " | grep -q " managed " || ok=0
  echo " $ss $up " | grep -q " $layer " && ok=0
done
obs="四层(managed/user/project/local)都配探针,依次让 user、project、local 中的一层设 disableAllHooks:true,none 模式。$obs"
if [ "$ok" = 1 ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
