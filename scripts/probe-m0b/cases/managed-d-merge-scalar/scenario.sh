#!/usr/bin/env bash
source /m0b/lib.sh
obs=""
results=()   # 记录各变体的生效值
# effective_only <variant-name> <base-extra> <d10-extra> <d20-extra>:
# 三个托管文件各自带 extra(JSON 对象,可为空);用户层带 user 探针;
# 用户探针被调用 => allowManagedHooksOnly 实际为 false。
run_scalar() {
  local name="$1" b="$2" d10="$3" d20="$4"
  reset_world
  put_managed "$(merge_json "$(hooks_json managed)" "${b:-{\}}")"
  [ -n "$d10" ] && put_managed "$d10" managed-settings.d/10-a.json
  [ -n "$d20" ] && put_managed "$d20" managed-settings.d/20-b.json
  put_user "$(hooks_json user)"
  run_claude none 20
  save_payloads "$name"
  local ss; ss="$(probe_labels SessionStart)"
  local eff="false"
  echo " $ss " | grep -q " user " || eff="true"
  local envv=""
  envv="$(cat "$PROBE_DIR"/managed-SessionStart-*.env 2>/dev/null | grep '^BALLAST_PROBE_ENV=' | head -1 | cut -d= -f2)"
  echo "$eff|$envv|$ss"
}
T='{"allowManagedHooksOnly":true}'; F='{"allowManagedHooksOnly":false}'
r_a="$(run_scalar base-true-d10-false "$T" "$F" "")"
r_b="$(run_scalar base-false-d10-true "$F" "$T" "")"
r_c="$(run_scalar d10-true-d20-false "" "$T" "$F")"
r_d="$(run_scalar d10-false-d20-true "" "$F" "$T")"
r_e="$(run_scalar env-base-d10-d20 '{"env":{"BALLAST_PROBE_ENV":"base"}}' '{"env":{"BALLAST_PROBE_ENV":"d10"}}' '{"env":{"BALLAST_PROBE_ENV":"d20"}}')"
r_f="$(run_scalar env-d10-d20 '' '{"env":{"BALLAST_PROBE_ENV":"d10"}}' '{"env":{"BALLAST_PROBE_ENV":"d20"}}')"
r_g="$(run_scalar env-base-d10 '{"env":{"BALLAST_PROBE_ENV":"base"}}' '{"env":{"BALLAST_PROBE_ENV":"d10"}}' '')"
f() { echo "$1" | cut -d'|' -f"$2"; }
obs="allowManagedHooksOnly 冲突(true=用户探针不被调用,值为生效的 allowManagedHooksOnly):[基础 true / 10-a false => 生效 $(f "$r_a" 1)];[基础 false / 10-a true => $(f "$r_b" 1)];[10-a true / 20-b false => $(f "$r_c" 1)];[10-a false / 20-b true => $(f "$r_d" 1)]。env.BALLAST_PROBE_ENV 冲突(托管探针看到的值):[基础 base / 10-a d10 / 20-b d20 => $(f "$r_e" 2)];[10-a d10 / 20-b d20 => $(f "$r_f" 2)];[基础 base / 10-a d10 => $(f "$r_g" 2)]。"
# 判定:文档说法 = 后者胜且 .d 覆盖基础
ok=1
[ "$(f "$r_a" 1)" = false ] || ok=0     # .d(false)覆盖基础(true)
[ "$(f "$r_b" 1)" = true ] || ok=0
[ "$(f "$r_c" 1)" = false ] || ok=0     # 20 覆盖 10
[ "$(f "$r_d" 1)" = true ] || ok=0
[ "$(f "$r_e" 2)" = d20 ] || ok=0
if [ "$ok" = 1 ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
