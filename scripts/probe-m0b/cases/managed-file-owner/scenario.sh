#!/usr/bin/env bash
source /m0b/lib.sh
obs=""
N=0
try() { # try <name> <setup-cmd>;结果放在全局 N(不用子 shell,obs 才能累积)
  reset_world
  put_managed "$(hooks_json managed SessionStart)"
  eval "$2"
  run_claude none 20
  save_payloads "$1"
  N="$(probe_count managed SessionStart)"
  obs="$obs[$1] 托管 SessionStart 探针调用 $N 次。"
}
try root-owned-0644 true; n_root="$N"
allread=1
try file-owned-by-agent 'chown agent:agent /etc/claude-code/managed-settings.json'; [ "$N" -ge 1 ] || allread=0
try file-world-writable 'chmod 0666 /etc/claude-code/managed-settings.json'; [ "$N" -ge 1 ] || allread=0
try dir-owned-by-agent 'chown agent:agent /etc/claude-code'; [ "$N" -ge 1 ] || allread=0
note=""
[ "$allread" = 1 ] && note=" 结论:客户端不检查托管文件的属主与权限,Ballast 的层级判定必须自己检查(tier 条件 4、5)。"
obs="对照 root 所有 0644 / 文件属 agent / 文件 0666 / 目录属 agent,none 模式,SessionStart 单事件。$obs$note"
# 对照组必须读取;其余只记录
if [ "${n_root:-0}" -ge 1 ] 2>/dev/null; then finish pass none "$obs"; else finish fail none "$obs"; fi
