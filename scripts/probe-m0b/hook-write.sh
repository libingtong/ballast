#!/bin/sh
# hooks-run-outside-sandbox 用的探针:先记录 stdin(同 probe.sh),再尝试写 $PROBE_WRITE_TARGET,
# 把结果追加到 /tmp/probe/write.log(一行:<event> ok|fail <errno 文本>)。
# 用法:hook-write.sh <label> <target-file>
label="${1:-managed}"
target="${2:?target}"
mkdir -p /tmp/probe 2>/dev/null
input="$(cat)"
ev="$(printf '%s' "$input" | jq -r '.hook_event_name // "unknown"' 2>/dev/null)"
[ -n "$ev" ] || ev=unknown
ns="$(date +%s%N)"
printf '%s' "$input" > "/tmp/probe/${label}-${ev}-${ns}.json"
if err="$(sh -c "echo hook > '$target'" 2>&1)"; then
  echo "$ev ok" >> /tmp/probe/write.log
else
  echo "$ev fail $err" >> /tmp/probe/write.log
fi
exit 0
