#!/usr/bin/env bash
# 重跑 M0a 全部取证用例。会启动真实 claude -p 会话(haiku,少量 token)。
# 用法: BALLAST_PROBE_OUT=/some/tmp/dir scripts/probe/run-m0a.sh
set -uo pipefail
: "${BALLAST_PROBE_OUT:?需要设置 BALLAST_PROBE_OUT}"
R="$(cd "$(dirname "$0")" && pwd)/run-case.sh"
W="$BALLAST_PROBE_OUT/work"
$R bash-ok 'Bash(echo:*)' 'Run exactly this shell command with the Bash tool: echo hello-ballast . Then reply DONE.'
$R bash-exit1 'Bash(sh -c:*)' "Run exactly this shell command with the Bash tool: sh -c 'echo out; echo err >&2; exit 3' . Do not retry. Then reply DONE."
mkdir -p "$W/grep-exit1" && echo hello > "$W/grep-exit1/f.txt"
$R grep-exit1 'Bash(grep:*)' 'Run exactly this shell command with the Bash tool: grep zzz-no-such-text f.txt . Do not retry. Then reply DONE.'
$R bash-startfail 'Bash(nonexistent-ballast-cmd:*)' 'Run exactly this shell command with the Bash tool: nonexistent-ballast-cmd --x . Do not retry. Then reply DONE.'
for c in exit1-json-deny updatedinput-no-allow updatedinput-allow warn-additionalcontext; do
  $R "$c" 'Bash(touch:*)' 'Run exactly this shell command with the Bash tool: touch probe_a.txt . Do not retry and do not try alternatives. Then reply DONE.'
done
$R stop-systemmessage '' 'Reply with exactly the word: hello'
$R stop-cap '' 'Reply with exactly the word: hello'
mkdir -p "$W/parallel-posttooluse" && for i in 1 2 3; do echo $i > "$W/parallel-posttooluse/f$i.txt"; done
$R parallel-posttooluse 'Read' 'In ONE single assistant message, emit three Read tool calls at once (parallel tool use) for f1.txt, f2.txt and f3.txt. Do not read them one by one. Then reply DONE.' --model sonnet
$R subagent 'Agent,Task,Bash(echo:*)' 'Use the Agent (Task) tool to launch a general-purpose subagent whose job is to run the shell command "echo from-subagent" with Bash and report the output. Then reply DONE.'
$R compact-seed '' 'Reply with exactly the word: seeded'
$R compact '' '/compact' --resume "$(jq -r .session_id "$BALLAST_PROBE_OUT/compact-seed.result.json")"
