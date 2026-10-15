#!/usr/bin/env bash
# 用法: run-case.sh <case_id> <allowedTools> <prompt> [额外 claude 参数...]
# 在隔离的临时项目目录里跑一次 claude -p,只加载 project/local 设置与探针 hook。
set -euo pipefail
case_id="$1"; tools="$2"; prompt="$3"; shift 3
here="$(cd "$(dirname "$0")" && pwd)"
: "${BALLAST_PROBE_OUT:?需要设置 BALLAST_PROBE_OUT}"
work="${BALLAST_PROBE_WORK:-$BALLAST_PROBE_OUT/work}/$case_id"
mkdir -p "$work"
settings="$BALLAST_PROBE_OUT/settings.json"
if [ ! -f "$settings" ]; then
  jq -n --arg cmd "$here/probe.sh" '
    {hooks: ( ["SessionStart","UserPromptSubmit","PreToolUse","PostToolUse","PostToolUseFailure","PostToolBatch","Stop","SubagentStart","SubagentStop","PreCompact","PostCompact","SessionEnd"]
      | map({key: ., value: [{matcher: "*", hooks: [{type: "command", command: $cmd, timeout: 10}]}]}) | from_entries)}' > "$settings"
fi
cd "$work"
BALLAST_PROBE_CASE="$case_id" claude -p "$prompt" \
  --model haiku --setting-sources project,local --settings "$settings" \
  --allowedTools "$tools" --output-format json "$@" > "$BALLAST_PROBE_OUT/$case_id.result.json" 2> "$BALLAST_PROBE_OUT/$case_id.stderr" || echo "claude exit=$?" >> "$BALLAST_PROBE_OUT/$case_id.stderr"
ls "$work"
