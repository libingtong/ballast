#!/usr/bin/env bash
# 由 run.sh 在不同的 --security-opt 组合下各运行一次:以 agent 身份跑 §3.4 的最小 bwrap 命令。
# 输出 OK 或 bwrap 的第一行错误。
out="$(runuser -u agent -- bwrap --die-with-parent --new-session --unshare-pid --unshare-net \
  --ro-bind / / --dev /dev --proc /proc --tmpfs /tmp --tmpfs /run -- true 2>&1 | head -1)"
if [ -z "$out" ]; then echo OK; else echo "FAIL: $out"; fi
