#!/usr/bin/env bash
# Codex client probe (docs/p2-design.md section 7.2). Needs a Codex login, so
# it cannot run in CI: every case stays "unverified" until a person with a
# Codex account runs it. Supplement command (the user runs it himself):
#
#   CODEX_HOME=$(mktemp -d) BALLAST_HOME=$(mktemp -d) scripts/probe-clients/codex.sh
#
# What it does
#   prepare   installs Ballast (ballast install --client codex --codex-trust)
#             and a record-only probe hook into the throwaway CODEX_HOME, adds
#             trust entries for the probe hook, prints the launch command
#   run       (default) prepare, then walks through the checklist of prompts
#             you type into the Codex TUI, then collect
#   mark ID / answer ID y|n / collect / checklist
#             the same steps one at a time (for a non-interactive shell)
#   --self-test   no client, no network, no credentials: guards, hash vectors,
#             merge logic, the probe hook and the collect/sanitise pipeline
#
# What it never does: start codex (the codex on PATH may be a shell function
# that switches to a real account; the launch command runs the binary through
# `env`, which bypasses same-named shell functions and aliases, and clears
# OPENAI_API_KEY / CODEX_API_KEY so a key exported in the shell is not used),
# read auth.json / config.toml of a real home, or touch a real user directory.
# CODEX_HOME and BALLAST_HOME must be existing, empty (or previously created
# by this script) directories that are neither a real home nor inside one of
# its protected parts (.codex .claude .cursor .hermes .ballast ...); the check
# runs before anything is written.
#
# Environment
#   CODEX_HOME, BALLAST_HOME      required, see above
#   BALLAST_BIN                   optional, absolute path of a built ballast
#                                 (otherwise ./cmd/ballast is built)
#   BALLAST_PROBE_CODEX_VERSION   optional, recorded as codex_version
#   BALLAST_PROBE_FACTS_DIR       optional, default <repo>/docs/facts
#
# Exit codes: 0 ok, 2 refusal / usage, 4 collect found nothing to record.
# shellcheck shell=bash
# shellcheck disable=SC2034  # the globals set here are read by the pc_cmd_* functions in lib.sh
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
# shellcheck source=lib.sh
. "$(dirname "$SELF")/lib.sh"
pc_init
trap pc_cleanup EXIT

PC_CLIENT=codex
CODEX_EVENTS="SessionStart UserPromptSubmit PreToolUse PermissionRequest PostToolUse Stop"
RECHECK='CODEX_HOME=$(mktemp -d) BALLAST_HOME=$(mktemp -d) scripts/probe-clients/codex.sh'

usage() {
  cat <<'EOF'
用法: CODEX_HOME=<空目录> BALLAST_HOME=<空目录> scripts/probe-clients/codex.sh [命令]

命令:
  run (默认)      prepare + 按清单逐项引导 + collect
  prepare         安装 Ballast 与探针 hook 到临时 CODEX_HOME,打印启动命令
  checklist       打印全部用例的清单
  mark <用例>      把后续 hook 记录归到该用例(非交互时手动推进)
  answer <用例> y|n  记录用户对该用例问题的回答
  collect         脱敏并生成 docs/facts/codex-cases.json 与 payloads/codex/
  --self-test     脚本自测(不启动 codex,不联网,不需要凭据)

补测命令(自己登录临时 CODEX_HOME):
  CODEX_HOME=$(mktemp -d) BALLAST_HOME=$(mktemp -d) scripts/probe-clients/codex.sh
EOF
}

# ------------------------------------------------------------ environment ---

# setup_env: validate CODEX_HOME / BALLAST_HOME, claim them, derive paths.
# Everything before pc_claim is read-only.
setup_env() {
  local ch=${CODEX_HOME:-} bh=${BALLAST_HOME:-}
  [ -n "$ch" ] || pc_die "CODEX_HOME 未设置。补测命令: $RECHECK"
  [ -n "$bh" ] || pc_die "BALLAST_HOME 未设置。补测命令: $RECHECK"
  pc_guard CODEX_HOME "$ch"
  pc_guard BALLAST_HOME "$bh"
  pc_disjoint "$ch" "$bh"
  CH="$(pc_canon "$ch")" # Codex canonicalises CODEX_HOME; trust keys must use that form
  BH="$(pc_canon "$bh")"
  pc_claim "$CH"
  pc_claim "$BH"
  CLIENT_HOME="$CH"
  PROBE="$BH/probe-clients/codex"
  WORK="$PROBE/work"
  SANDBOX="$BH/sandbox-home"
  FACTS="${BALLAST_PROBE_FACTS_DIR:-$PC_REPO_ROOT/docs/facts}"
  VERSION="${BALLAST_PROBE_CODEX_VERSION:-unknown}"
  # BALLAST_HOME must be part of the command: the Ballast hooks written by
  # --codex-trust carry no environment, so without it they fall back to the
  # real ~/.ballast. env runs the binary directly, which already bypasses
  # same-named shell functions and aliases.
  LAUNCH="cd $(pc_q "$WORK") && env -u OPENAI_API_KEY -u CODEX_API_KEY CODEX_HOME=$(pc_q "$CH") BALLAST_HOME=$(pc_q "$BH") codex"
  EXEC="cd $(pc_q "$WORK") && env -u OPENAI_API_KEY -u CODEX_API_KEY CODEX_HOME=$(pc_q "$CH") BALLAST_HOME=$(pc_q "$BH") codex exec 'Run the shell command: echo ballast-probe-exec'"
}

cmd_prepare() {
  setup_env
  pc_resolve_ballast "$PROBE"
  mkdir -p "$PROBE/out" "$PROBE/state" "$WORK" "$SANDBOX"
  [ -f "$PROBE/current-case" ] || printf '%s\n' session-start >"$PROBE/current-case"
  pc_write_hook codex "$PROBE"

  pc_run_ballast "$SANDBOX" "$BH" CODEX_HOME="$CH" -- \
    install --client codex --codex-trust --binary "$PC_BALLAST"

  local cmds
  # shellcheck disable=SC2086  # the event list is word-split on purpose
  cmds="$(pc_py probe_cmds "$PROBE/hook.sh" $CODEX_EVENTS)"
  pc_py codex_merge "$CH/hooks.json" "$cmds"
  local added
  added="$(pc_py codex_trust "$CH/config.toml" "$CH/hooks.json" "$PROBE/hook.sh")"
  pc_say "探针 hook 已加入 hooks.json,新增 $added 条信任记录(哈希由本脚本按 Codex 源码算法计算)"
  local audit
  audit="$(pc_py trust_audit "$CH/config.toml" "$CH/hooks.json")"
  pc_say "信任核对(hooks.json 的每个 command hook 对 config.toml): $audit"
  case "$audit" in
    *"missing=0 mismatch=0") ;;
    *) pc_warn "有 hook 没有匹配的信任记录;启动后用 /hooks 查看,或在 TUI 里手动批准" ;;
  esac
  print_launch
}

print_launch() {
  pc_say ""
  pc_say "已准备好。在新终端启动 Codex(env 直接执行二进制,绕过同名 shell 函数/别名,不要用会切换到真实账号的包装):"
  pc_say "  LAUNCH: $LAUNCH"
  pc_say "exec-noninteractive 用例另开终端运行(同样固定 BALLAST_HOME,避免 hook 落到真实 ~/.ballast):"
  pc_say "  EXEC: $EXEC"
  pc_say "在该 TUI 里自行登录。本脚本不读取凭据;Codex 可能把凭据存进系统钥匙串而不是临时 CODEX_HOME【未核验】,取证结束后请自行登出。"
  pc_say "探针记录目录: $PROBE/out"
}

# -------------------------------------------------------------- self-test ---

# run_script ARGS...: run this script as a child with the caller's env.
run_script() { bash "$SELF" "$@"; }

selftest() {
  local root
  root="$(mktemp -d "${TMPDIR:-/tmp}/ballast-probe-selftest.XXXXXX")"
  # shellcheck disable=SC2064  # expand $root now: it is local to this function
  trap "rm -rf '$root'; pc_cleanup" EXIT
  local real real_before real_after
  real="$(pc_realhome)"
  real_before="$(ls -A "$real" 2>/dev/null | cksum)"

  pc_selftest_guards "$root/guards"

  # The refusal cases below hand real directories to `prepare`: only run them
  # once the guard is seen to refuse those directories on its own.
  pc_realdirs_refused "$real" "$real/.codex" || { pc_t_finish codex.sh; return 1; }

  # ---- the script itself refuses unsafe targets, before writing anything ----
  local rc fresh_bh="$root/refuse-bh"
  mkdir -p "$fresh_bh" "$root/refuse-ch"
  rc=0
  (unset CODEX_HOME; BALLAST_HOME="$fresh_bh" run_script prepare) >/dev/null 2>&1 || rc=$?
  pc_t_eq "CODEX_HOME 未设置: 退出码 2" "$rc" "2"
  rc=0
  (unset BALLAST_HOME; CODEX_HOME="$root/refuse-ch" run_script prepare) >/dev/null 2>&1 || rc=$?
  pc_t_eq "BALLAST_HOME 未设置: 退出码 2" "$rc" "2"
  rc=0
  CODEX_HOME="$real" BALLAST_HOME="$fresh_bh" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "CODEX_HOME=真实家目录: 退出码 2" "$rc" "2"
  rc=0
  CODEX_HOME="$root/refuse-ch" BALLAST_HOME="$real" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "BALLAST_HOME=真实家目录: 退出码 2" "$rc" "2"
  rc=0
  CODEX_HOME="$root/refuse-ch" BALLAST_HOME="$root/refuse-ch" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "CODEX_HOME 与 BALLAST_HOME 相同: 退出码 2" "$rc" "2"
  rc=0
  HOME="$real" CODEX_HOME="$real/.codex" BALLAST_HOME="$fresh_bh" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "CODEX_HOME=~/.codex: 退出码 2" "$rc" "2"
  pc_t_eq "拒绝时未写入任何文件(标记与探针目录都不存在)" \
    "$(find "$fresh_bh" "$root/refuse-ch" -mindepth 1 | wc -l | tr -d ' ')" "0"

  # ---- end to end with a real ballast binary and a synthetic session ----
  if [ -z "${BALLAST_BIN:-}" ] && ! command -v go >/dev/null 2>&1; then
    pc_t_skip "端到端(prepare/hook/collect)" "没有 go 且未设置 BALLAST_BIN,无法得到 ballast 二进制"
  else
    selftest_e2e "$root"
  fi

  real_after="$(ls -A "$real" 2>/dev/null | cksum)"
  pc_t_eq "真实家目录的顶层条目在自测前后不变" "$real_after" "$real_before"
  pc_t_finish codex.sh
}

selftest_e2e() {
  local root=$1 ch bh facts hook rc json hrc
  ch="$root/e2e-ch"
  bh="$root/e2e-bh"
  facts="$root/e2e-facts"
  mkdir -p "$ch" "$bh" "$facts"
  export CODEX_HOME="$ch" BALLAST_HOME="$bh" BALLAST_PROBE_FACTS_DIR="$facts"
  if [ -z "${BALLAST_BIN:-}" ]; then
    pc_resolve_ballast "$root/e2e-build"
    export BALLAST_BIN="$PC_BALLAST"
  fi

  rc=0
  run_script prepare >"$root/prepare.out" 2>&1 || rc=$?
  pc_t_eq "prepare 成功" "$rc" "0"
  local cch cbh
  cch="$(pc_canon "$ch")"
  cbh="$(pc_canon "$bh")"
  # The Ballast hooks carry no environment, so the printed commands must pin
  # BALLAST_HOME themselves or the hooks fall back to the real ~/.ballast.
  pc_t_eq "LAUNCH 行同时带规范化后的 CODEX_HOME 与 BALLAST_HOME,且不使用 command" \
    "$(grep -F "LAUNCH:" "$root/prepare.out" | grep -cF "env -u OPENAI_API_KEY -u CODEX_API_KEY CODEX_HOME=$(pc_q "$cch") BALLAST_HOME=$(pc_q "$cbh") codex")" "1"
  pc_t_eq "启动命令不含多余的 command 前缀(env 找不到 command 的平台会失败)" \
    "$(grep -cE "(LAUNCH|EXEC):.*env .* command codex" "$root/prepare.out" || true)" "0"
  pc_t_eq "EXEC 命令同样带 BALLAST_HOME" \
    "$(grep -F "EXEC:" "$root/prepare.out" | grep -cF "env -u OPENAI_API_KEY -u CODEX_API_KEY CODEX_HOME=$(pc_q "$cch") BALLAST_HOME=$(pc_q "$cbh") codex exec")" "1"
  hook="$cbh/probe-clients/codex/hook.sh"
  # Run the installed Ballast hook the way the LAUNCH line would (only
  # CODEX_HOME / BALLAST_HOME set) with a decoy HOME: state must land in the
  # throwaway BALLAST_HOME and nothing may appear under the decoy HOME.
  local decoy="$root/decoy-home" bcmd
  mkdir -p "$decoy"
  bcmd="$(python3 -I -c 'import json,sys
for g in json.load(open(sys.argv[1]))["hooks"]["PreToolUse"]:
    for h in g["hooks"]:
        if "hook.sh" not in h["command"]:
            print(h["command"])' "$cch/hooks.json" | head -1)"
  hrc=0
  printf '{"session_id":"s","turn_id":"t","transcript_path":"","cwd":"%s","hook_event_name":"PreToolUse","model":"m","permission_mode":"default","tool_name":"Bash","tool_input":{"command":"echo x"},"tool_use_id":"c"}' "$cbh" |
    env -i PATH="$PATH" HOME="$decoy" CODEX_HOME="$cch" BALLAST_HOME="$cbh" sh -c "$bcmd" >/dev/null 2>&1 || hrc=$?
  pc_t_eq "Ballast hook 按 LAUNCH 的环境运行: 状态写进临时 BALLAST_HOME,假 HOME 下没有 .ballast" \
    "$(ls -d "$cbh/cache" "$cbh/state" 2>/dev/null | wc -l | tr -d ' '):$(ls -A "$decoy" | wc -l | tr -d ' ')" "2:0"
  pc_t_eq "hooks.json: 6 个 Ballast + 6 个探针 command hook" \
    "$(python3 -I -c 'import json,sys;d=json.load(open(sys.argv[1]))["hooks"];print(sum(len(g["hooks"]) for v in d.values() for g in v), sum(1 for v in d.values() for g in v for h in g["hooks"] if "hook.sh" in h["command"]))' "$cch/hooks.json")" \
    "12 6"
  pc_t_eq "信任核对: 12 条全部匹配(Ballast 的哈希与脚本独立计算的一致)" \
    "$(pc_py trust_audit "$cch/config.toml" "$cch/hooks.json")" "ok=12 missing=0 mismatch=0"
  pc_t_eq "信任 key 全部使用规范化后的 CODEX_HOME($ch -> $cch)" \
    "$(grep -c "^\[hooks\.state\.'$cch/hooks\.json:" "$cch/config.toml")" "12"
  rc=0
  run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "prepare 幂等: 再次运行成功" "$rc" "0"
  pc_t_eq "prepare 幂等: 条目数与信任记录不变" \
    "$(python3 -I -c 'import json,sys;d=json.load(open(sys.argv[1]))["hooks"];print(sum(len(g["hooks"]) for v in d.values() for g in v))' "$cch/hooks.json") $(pc_py trust_audit "$cch/config.toml" "$cch/hooks.json")" \
    "12 ok=12 missing=0 mismatch=0"

  # ---- the probe hook: recording and scripted answers ----
  local work="$cbh/probe-clients/codex/work" sid="0f3e5a52-1111-4222-8333-444455556666" err hout
  err="$root/hook.err"
  feed() { # EVENT JSON -> sets hout, hrc
    hrc=0
    hout="$(printf '%s' "$2" | "$hook" "$1" 2>"$err")" || hrc=$?
  }
  base() { # EVENT EXTRA
    printf '{"session_id":"%s","turn_id":"turn-1","transcript_path":"%s/sessions/rollout.jsonl","cwd":"%s","hook_event_name":"%s","model":"gpt-x","permission_mode":"default"%s}' \
      "$sid" "$cch" "$work" "$1" "${2:-}"
  }
  run_script mark session-start
  feed SessionStart "$(base SessionStart ',"source":"startup"')"
  pc_t_eq "session-start: 注入 SessionStart 令牌" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["hookSpecificOutput"]["additionalContext"])')" \
    "The session probe token is BALLAST_PROBE_SESSION_9K4M."
  feed UserPromptSubmit "$(base UserPromptSubmit ',"prompt":"What is the session probe token you were given? Reply with the token only."')"
  feed Stop "$(base Stop ',"stop_hook_active":false,"last_assistant_message":"BALLAST_PROBE_SESSION_9K4M"')"
  run_script answer session-start y

  run_script mark bash-ok
  feed UserPromptSubmit "$(base UserPromptSubmit ',"prompt":"Run the shell command `echo ballast-probe-ok` and show me its output."')"
  feed PreToolUse "$(base PreToolUse ',"tool_name":"Bash","tool_input":{"command":"echo ballast-probe-ok"},"tool_use_id":"call_1"')"
  pc_t_eq "bash-ok: 无决策时 stdout 为空、退出码 0" "$hout:$hrc" ":0"
  feed PostToolUse "$(base PostToolUse ',"tool_name":"Bash","tool_input":{"command":"echo ballast-probe-ok"},"tool_response":"ballast-probe-ok\n","tool_use_id":"call_1"')"
  feed Stop "$(base Stop ',"stop_hook_active":false,"last_assistant_message":"ok"')"

  run_script mark bash-fail
  feed PreToolUse "$(base PreToolUse ',"tool_name":"Bash","tool_input":{"command":"sh -c exit 3"},"tool_use_id":"call_2"')"
  feed PostToolUse "$(base PostToolUse ',"tool_name":"Bash","tool_input":{"command":"sh -c exit 3"},"tool_response":"token sk-abcdefghijklmnopqrstuvwx0123 leaked","tool_use_id":"call_2"')"

  run_script mark pre-deny-json
  feed PreToolUse "$(base PreToolUse ',"tool_name":"Bash","tool_input":{"command":"touch denied_json.txt"},"tool_use_id":"call_3"')"
  pc_t_eq "pre-deny-json: 输出 permissionDecision=deny" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;o=json.load(sys.stdin)["hookSpecificOutput"];print(o["permissionDecision"],o["permissionDecisionReason"])')" \
    "deny ballast probe deny json"
  feed PreToolUse "$(base PreToolUse ',"tool_name":"Bash","tool_input":{"command":"echo other"},"tool_use_id":"call_4"')"
  pc_t_eq "pre-deny-json: 其他命令不拦截" "$hout:$hrc" ":0"

  run_script mark pre-deny-exit2
  feed PreToolUse "$(base PreToolUse ',"tool_name":"Bash","tool_input":{"command":"touch denied_exit2.txt"},"tool_use_id":"call_5"')"
  pc_t_eq "pre-deny-exit2: 退出码 2、原因在 stderr、stdout 为空" "$hrc:$hout:$(cat "$err")" "2::ballast probe exit2 block"

  run_script mark pre-exit1-deny-stdout
  feed PreToolUse "$(base PreToolUse ',"tool_name":"Bash","tool_input":{"command":"touch exit1_deny.txt"},"tool_use_id":"call_6"')"
  pc_t_eq "pre-exit1-deny-stdout: 退出码 1 且 stdout 是 deny JSON" "$hrc:$(printf '%s' "$hout" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["hookSpecificOutput"]["permissionDecision"])')" "1:deny"
  : >"$work/exit1_deny.txt"

  run_script mark stop-block
  feed Stop "$(base Stop ',"stop_hook_active":false')"
  pc_t_eq "stop-block: 第一次 Stop 输出 decision:block" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["decision"])')" "block"
  feed Stop "$(base Stop ',"stop_hook_active":true')"
  pc_t_eq "stop-block: 第二次 Stop 不再阻止" "$hout:$hrc" ":0"

  # ---- collect ----
  rc=0
  run_script collect >/dev/null 2>&1 || rc=$?
  pc_t_eq "collect 成功" "$rc" "0"
  json="$facts/codex-cases.json"
  pc_t_eq "codex-cases.json: 14 个用例,字段齐全" \
    "$(python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1]))
need={"case_id","events","schema_keys","assertion","result","observation","evidence","mode","codex_version"}
bad=[c["case_id"] for c in d if not need<=set(c)]
nr=[c["case_id"] for c in d if c["result"]=="needs_credentials" and "recheck" not in c]
print(len(d),bad,nr)' "$json")" "14 [] []"
  pc_t_eq "用例结果: 已喂数据的 pass/unverifiable 与未运行的 needs_credentials" \
    "$(python3 -I -c '
import json,sys
d={c["case_id"]:c["result"] for c in json.load(open(sys.argv[1]))}
print(",".join("%s=%s"%(k,d[k]) for k in ["session-start","bash-ok","bash-fail","pre-deny-json","pre-deny-exit2","pre-exit1-deny-stdout","stop-block","apply-patch","exec-noninteractive"]))' "$json")" \
    "session-start=pass,bash-ok=pass,bash-fail=pass,pre-deny-json=pass,pre-deny-exit2=pass,pre-exit1-deny-stdout=pass,stop-block=unverifiable,apply-patch=needs_credentials,exec-noninteractive=needs_credentials"
  pc_t_eq "含密钥样式的 payload 被扣留,观察里有记录" \
    "$(python3 -I -c '
import json,sys,os
d={c["case_id"]:c for c in json.load(open(sys.argv[1]))}
o=d["bash-fail"]["observation"]
print("已扣留" in o, sorted(os.listdir(sys.argv[2])))' "$json" "$facts/payloads/codex/bash-fail")" \
    "True ['01-PreToolUse.json']"
  # nothing local or secret-looking may be left in what collect wrote
  local leak
  leak="$(grep -rlE "$(printf '%s' "$root" | sed 's/[][\.*^$/]/\\&/g')|$sid|sk-abcdefghijkl|$(id -un)" "$facts" 2>/dev/null || true)"
  pc_t_eq "输出目录不含临时路径、会话 ID、密钥样式字符串或本地用户名" "$leak" ""
  pc_t_eq "输出通过脚本自带的卫生/密钥扫描" \
    "$(for f in "$json" "$facts"/payloads/codex/*/*.json; do pc_py check_text "$f" >/dev/null 2>&1 || echo "FAIL:$f"; done | tr '\n' ' ')" ""
  pc_t_eq "payload 已规范化(ID 占位符、路径占位符、自由文本脱敏)" \
    "$(python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1]))
print(d["session_id"],d["transcript_path"],d["cwd"],d["last_assistant_message"])' "$facts/payloads/codex/session-start/03-Stop.json")" \
    "<session_id-1> <transcript-path> <work> <redacted len=26>"

  # nothing recorded => no cases file is written
  local ch2="$root/e2e2-ch" bh2="$root/e2e2-bh" f2="$root/e2e2-facts"
  mkdir -p "$ch2" "$bh2" "$f2"
  CODEX_HOME="$ch2" BALLAST_HOME="$bh2" BALLAST_PROBE_FACTS_DIR="$f2" run_script prepare >/dev/null 2>&1
  rc=0
  CODEX_HOME="$ch2" BALLAST_HOME="$bh2" BALLAST_PROBE_FACTS_DIR="$f2" run_script collect >/dev/null 2>&1 || rc=$?
  pc_t_eq "未记录任何内容: collect 退出码 4、不生成 codex-cases.json" "$rc:$(ls -A "$f2" | tr '\n' ' ')" "4:"
}

# ------------------------------------------------------------------- main ---

main() {
  local cmd=${1:-run}
  [ "$#" -gt 0 ] && shift
  case "$cmd" in
    -h | --help | help) usage ;;
    --self-test | self-test) selftest ;;
    run) pc_cmd_run ;;
    prepare) cmd_prepare ;;
    checklist) pc_cmd_checklist ;;
    mark) pc_cmd_mark "$@" ;;
    answer) pc_cmd_answer "$@" ;;
    collect) pc_cmd_collect ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
