#!/bin/sh
# M0b 探针 hook(镜像内 /opt/probe/probe.sh,root 所有):
#   /opt/probe/probe.sh <label>
# 把 stdin 原样写到 /tmp/probe/<label>-<event>-<ns>.json,并把自身环境里与取证相关的
# 变量写到同名 .env;stdout 保持为空,exit 0。label 区分 managed、managed-d10、
# managed-d20、user、project、local 等层。不做任何其他事(写入探测用 hook-write.sh)。
mkdir -p /tmp/probe 2>/dev/null
label="${1:-unknown}"
input="$(cat)"
ev="$(printf '%s' "$input" | jq -r '.hook_event_name // "unknown"' 2>/dev/null)"
[ -n "$ev" ] || ev=unknown
ns="$(date +%s%N)"
printf '%s' "$input" > "/tmp/probe/${label}-${ev}-${ns}.json"
# 只留与取证有关的变量,避免把整个环境(含可能的凭据)落盘。
env | grep -E '^(CLAUDE_PROJECT_DIR|CLAUDE_CONFIG_DIR|BALLAST_PROBE_[A-Z_]*|PWD|HOME)=' | sort > "/tmp/probe/${label}-${ev}-${ns}.env"
exit 0
