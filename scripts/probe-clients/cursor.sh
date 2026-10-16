#!/usr/bin/env bash
# Cursor client probe (docs/p2-design.md section 7.3). Needs a Cursor login
# and a machine with Cursor / cursor-agent (the CI image and the dev
# container have neither), so every case stays "unverified" until a person
# runs it. Supplement command (the user runs it himself):
#
#   BALLAST_CURSOR_PROBE_HOME=$(mktemp -d) scripts/probe-clients/cursor.sh
#
# Cursor's configuration root (~/.cursor) has no override variable, so the
# probe uses a throwaway HOME: the script installs Ballast and a record-only
# probe hook into <probe home>/.cursor, and you start Cursor with
# HOME=<probe home> (and log in there yourself).
#
# Refusal: the script stops (exit 2) before writing anything unless
# BALLAST_CURSOR_PROBE_HOME is set, is an existing directory that is empty or
# was created by this script, and is neither the real home directory (passwd
# home or $HOME), nor an ancestor of it, nor inside one of its protected parts
# (.cursor .codex .claude .hermes .ballast ...), nor a system path. Running
# with the real HOME and no probe home is refused too: HOME is never used as a
# default.
#
# Commands: run (default) | prepare | checklist | mark ID | answer ID y|n |
#           collect | --self-test       (see codex.sh; the flow is identical)
# Environment: BALLAST_CURSOR_PROBE_HOME (required), BALLAST_BIN,
#   BALLAST_PROBE_CURSOR_VERSION, BALLAST_PROBE_FACTS_DIR (default
#   <repo>/docs/facts)
# Exit codes: 0 ok, 2 refusal / usage, 4 collect found nothing to record.
# shellcheck shell=bash
# shellcheck disable=SC2034  # the globals set here are read by the pc_cmd_* functions in lib.sh
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
# shellcheck source=lib.sh
. "$(dirname "$SELF")/lib.sh"
pc_init
trap pc_cleanup EXIT

PC_CLIENT=cursor
CURSOR_EVENTS="sessionStart beforeSubmitPrompt preToolUse beforeShellExecution beforeMCPExecution beforeReadFile postToolUse afterAgentResponse stop subagentStop"
RECHECK='BALLAST_CURSOR_PROBE_HOME=$(mktemp -d) scripts/probe-clients/cursor.sh'

usage() {
  cat <<'EOF'
用法: BALLAST_CURSOR_PROBE_HOME=<空目录> scripts/probe-clients/cursor.sh [命令]

命令:
  run (默认)      prepare + 按清单逐项引导 + collect
  prepare         安装 Ballast 与探针 hook 到一次性 HOME 的 .cursor,打印启动方式
  checklist       打印全部用例的清单
  mark <用例>      把后续 hook 记录归到该用例(非交互时手动推进)
  answer <用例> y|n  记录用户对该用例问题的回答
  collect         脱敏并生成 docs/facts/cursor-cases.json 与 payloads/cursor/
  --self-test     脚本自测(不启动 cursor,不联网,不需要凭据)

补测命令(在装有 Cursor / cursor-agent 的机器上,由用户执行并在一次性 HOME 里登录):
  BALLAST_CURSOR_PROBE_HOME=$(mktemp -d) scripts/probe-clients/cursor.sh
EOF
}

# ------------------------------------------------------------ environment ---

setup_env() {
  local ph=${BALLAST_CURSOR_PROBE_HOME:-}
  if [ -z "$ph" ]; then
    pc_die "BALLAST_CURSOR_PROBE_HOME 未设置: 本脚本不会把 HOME 当作默认值(HOME 可能是真实家目录)。补测命令: $RECHECK"
  fi
  pc_guard BALLAST_CURSOR_PROBE_HOME "$ph"
  PH="$(pc_canon "$ph")"
  pc_claim "$PH"
  CLIENT_HOME="$PH"
  BH="$PH/.ballast"
  PROBE="$PH/probe-clients/cursor"
  WORK="$PH/work"
  FACTS="${BALLAST_PROBE_FACTS_DIR:-$PC_REPO_ROOT/docs/facts}"
  VERSION="${BALLAST_PROBE_CURSOR_VERSION:-unknown}"
  # BALLAST_HOME is explicit so a value exported in the user's shell cannot
  # send the Ballast hooks outside the throwaway HOME.
  LAUNCH="cd $(pc_q "$WORK") && env -u CURSOR_API_KEY HOME=$(pc_q "$PH") BALLAST_HOME=$(pc_q "$BH") cursor-agent    # CLI;IDE: env -u CURSOR_API_KEY HOME=$(pc_q "$PH") BALLAST_HOME=$(pc_q "$BH") cursor $(pc_q "$WORK") 然后 Developer: Reload Window"
  EXEC=""
}

# --------------------------------------------------------------- commands ---

cmd_prepare() {
  setup_env
  pc_resolve_ballast "$PROBE"
  mkdir -p "$PROBE/out" "$PROBE/state" "$WORK" "$PH/.cursor" "$BH"
  [ -f "$PROBE/current-case" ] || printf '%s\n' session-start >"$PROBE/current-case"
  printf '%s\n' 'ballast probe line one' >"$WORK/README_PROBE.txt"
  printf '%s\n' 'BALLAST_PROBE_SECRET_LINE' >"$WORK/SECRET_PROBE.txt"
  pc_write_hook cursor "$PROBE"

  # HOME / BALLAST_INSTALL_ROOT are the probe home: ballast writes <probe
  # home>/.cursor/{hooks.json,rules/ballast.mdc} and nothing else.
  pc_run_ballast "$PH" "$BH" -- install --client cursor --binary "$PC_BALLAST"

  local cmds
  # shellcheck disable=SC2086  # the event list is word-split on purpose
  cmds="$(pc_py probe_cmds "$PROBE/hook.sh" $CURSOR_EVENTS)"
  pc_py cursor_merge "$PH/.cursor/hooks.json" "$cmds" '["stop","subagentStop"]'
  pc_say "探针 hook 已加入 $PH/.cursor/hooks.json(10 个事件,stop/subagentStop 带 loop_limit 10)"
  print_launch
}

print_launch() {
  pc_say ""
  pc_say "已准备好。用一次性 HOME 启动 Cursor,并在其中自行登录(登录凭据只会写进该目录,本脚本不读取):"
  pc_say "  LAUNCH: $LAUNCH"
  pc_say "注意: macOS 上 Cursor IDE 的数据目录可能不跟随 HOME【未核验】;能用 cursor-agent CLI 就用 CLI。"
  pc_say "探针记录目录: $PROBE/out"
}

# -------------------------------------------------------------- self-test ---

run_script() { bash "$SELF" "$@"; }

selftest() {
  local root
  root="$(mktemp -d "${TMPDIR:-/tmp}/ballast-probe-selftest.XXXXXX")"
  # shellcheck disable=SC2064  # expand $root now: it is local to this function
  trap "rm -rf '$root'; pc_cleanup" EXIT
  local real real_before real_after rc
  real="$(pc_realhome)"
  real_before="$(find "$real" -maxdepth 1 -mindepth 1 2>/dev/null | sort | cksum)"

  pc_selftest_guards "$root/guards"

  # ---- the script itself: real-environment detection and refusal ----
  mkdir -p "$root/refuse-ph" "$root/dirty-ph"
  : >"$root/dirty-ph/something"
  rc=0
  (unset BALLAST_CURSOR_PROBE_HOME; HOME="$real" run_script prepare) >/dev/null 2>&1 || rc=$?
  pc_t_eq "真实 HOME 且未设置探针目录: 拒绝(退出码 2)" "$rc" "2"
  rc=0
  (unset BALLAST_CURSOR_PROBE_HOME; HOME="$root/refuse-ph" run_script prepare) >/dev/null 2>&1 || rc=$?
  pc_t_eq "仅设置 HOME、未设置探针目录: 拒绝(HOME 不是默认值)" "$rc" "2"
  rc=0
  BALLAST_CURSOR_PROBE_HOME="$real" HOME="$real" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "探针目录=真实家目录: 拒绝(退出码 2)" "$rc" "2"
  rc=0
  BALLAST_CURSOR_PROBE_HOME="$real/.cursor" HOME="$real" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "探针目录=~/.cursor: 拒绝(退出码 2)" "$rc" "2"
  rc=0
  BALLAST_CURSOR_PROBE_HOME="$(dirname "$real")" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "探针目录=真实家目录的父目录: 拒绝(退出码 2)" "$rc" "2"
  rc=0
  BALLAST_CURSOR_PROBE_HOME="$root/dirty-ph" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "探针目录非空且无标记: 拒绝(退出码 2)" "$rc" "2"
  rc=0
  BALLAST_CURSOR_PROBE_HOME="relative" run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "探针目录是相对路径: 拒绝(退出码 2)" "$rc" "2"
  pc_t_eq "拒绝时未写入任何文件" \
    "$(find "$root/refuse-ph" -mindepth 1 | wc -l | tr -d ' '):$(find "$root/dirty-ph" -mindepth 1 | wc -l | tr -d ' ')" "0:1"

  if [ -z "${BALLAST_BIN:-}" ] && ! command -v go >/dev/null 2>&1; then
    pc_t_skip "端到端(prepare/hook/collect)" "没有 go 且未设置 BALLAST_BIN,无法得到 ballast 二进制"
  else
    selftest_e2e "$root"
  fi

  real_after="$(find "$real" -maxdepth 1 -mindepth 1 2>/dev/null | sort | cksum)"
  pc_t_eq "真实家目录的顶层条目在自测前后不变" "$real_after" "$real_before"
  pc_t_finish cursor.sh
}

selftest_e2e() {
  local root=$1 ph facts hook rc json cph
  ph="$root/e2e-ph"
  facts="$root/e2e-facts"
  mkdir -p "$ph" "$facts"
  # HOME deliberately stays the caller's real HOME: the script must work
  # from explicit paths and never write there.
  export BALLAST_CURSOR_PROBE_HOME="$ph" BALLAST_PROBE_FACTS_DIR="$facts"
  if [ -z "${BALLAST_BIN:-}" ]; then
    pc_resolve_ballast "$root/e2e-build"
    export BALLAST_BIN="$PC_BALLAST"
  fi

  rc=0
  run_script prepare >"$root/prepare.out" 2>&1 || rc=$?
  pc_t_eq "prepare 成功" "$rc" "0"
  cph="$(pc_canon "$ph")"
  pc_t_eq "LAUNCH 行显式带 HOME 与 BALLAST_HOME(不继承 shell 里已导出的值)" \
    "$(grep -F "LAUNCH:" "$root/prepare.out" | grep -cF "env -u CURSOR_API_KEY HOME=$(pc_q "$cph") BALLAST_HOME=$(pc_q "$cph/.ballast") cursor-agent")" "1"
  hook="$cph/probe-clients/cursor/hook.sh"
  pc_t_eq "hooks.json: version 1、10 个事件各有 Ballast 与探针条目、stop 带 loop_limit" \
    "$(python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1]))
h=d["hooks"]
print(d["version"],len(h),sorted({len(v) for v in h.values()}),
      all(any("hook.sh" in e["command"] for e in v) and any("--client cursor" in e["command"] for e in v) for v in h.values()),
      [e.get("loop_limit") for e in h["stop"]])' "$cph/.cursor/hooks.json")" \
    "1 10 [2] True [10, 10]"
  pc_t_eq "只在探针目录内写入(.cursor/.ballast/probe-clients/work 与标记)" \
    "$(find "$cph" -maxdepth 1 -mindepth 1 -exec basename {} \; | sort | tr '\n' ' ')" \
    ".ballast .ballast-probe .cursor probe-clients work "
  rc=0
  run_script prepare >/dev/null 2>&1 || rc=$?
  pc_t_eq "prepare 幂等: 再次运行成功且每个事件仍是 2 条" \
    "$rc:$(python3 -I -c 'import json,sys;print(sorted({len(v) for v in json.load(open(sys.argv[1]))["hooks"].values()}))' "$cph/.cursor/hooks.json")" "0:[2]"

  local work="$cph/work" err="$root/hook.err" hrc hout
  feed() { # EVENT JSON
    hrc=0
    hout="$(printf '%s' "$2" | "$hook" "$1" 2>"$err")" || hrc=$?
  }
  base() { # EVENT EXTRA
    printf '{"conversation_id":"conv-0f3e","generation_id":"gen-77","user_email":"alice.real@corp-example.com","cursor_version":"0.0.0","hook_event_name":"%s","workspace_roots":["%s"],"transcript_path":"%s/.cursor/projects/t.jsonl"%s}' \
      "$1" "$work" "$cph" "${2:-}"
  }
  run_script mark session-start
  feed sessionStart "$(base sessionStart ',"session_id":"sess-1"')"
  pc_t_eq "session-start: 注入 additional_context" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["additional_context"])')" \
    "The session probe token is BALLAST_PROBE_SESSION_9K4M."
  run_script answer session-start y

  run_script mark shell-ok
  feed beforeShellExecution "$(base beforeShellExecution ',"command":"echo ballast-probe-ok","cwd":""')"
  pc_t_eq "shell-ok: 无决策时 stdout 为空、退出码 0" "$hout:$hrc" ":0"
  feed postToolUse "$(base postToolUse ',"tool_name":"Shell","tool_input":{"command":"echo ballast-probe-ok"},"tool_output":"ballast-probe-ok"')"

  run_script mark shell-deny
  feed beforeShellExecution "$(base beforeShellExecution ',"command":"touch denied_shell.txt","cwd":""')"
  pc_t_eq "shell-deny: 输出 permission=deny 与两条消息" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;o=json.load(sys.stdin);print(o["permission"],o["user_message"],"|",o["agent_message"])')" \
    "deny ballast probe user message | ballast probe agent message"
  feed beforeShellExecution "$(base beforeShellExecution ',"command":"echo fine","cwd":""')"
  pc_t_eq "shell-deny: 其他命令不拦截" "$hout:$hrc" ":0"

  run_script mark read-deny
  feed beforeReadFile "$(base beforeReadFile ",\"file_path\":\"$work/SECRET_PROBE.txt\"")"
  pc_t_eq "read-deny: 读 SECRET_PROBE.txt 输出 deny" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["permission"])')" "deny"
  run_script answer read-deny n

  run_script mark prompt-context
  feed beforeSubmitPrompt "$(base beforeSubmitPrompt ',"prompt":"Reply with the prompt probe tokens you were given, nothing else."')"
  pc_t_eq "prompt-context: 两条注入通道各带不同令牌" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;o=json.load(sys.stdin);print("PA_3M7Q" in o["additional_context"],"PB_8W2L" in o["hookSpecificOutput"]["additionalContext"])')" "True True"
  run_script answer prompt-context y

  run_script mark stop-followup
  feed afterAgentResponse "$(base afterAgentResponse ',"text":"done"')"
  feed stop "$(base stop ',"status":"completed","loop_count":0')"
  pc_t_eq "stop-followup: 第一次 stop 输出 followup_message" \
    "$(printf '%s' "$hout" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["followup_message"])')" \
    "ballast probe: reply with the single word AGAIN"
  feed stop "$(base stop ',"status":"completed","loop_count":1')"
  pc_t_eq "stop-followup: 第二次 stop 不再续跑" "$hout:$hrc" ":0"
  run_script answer stop-followup y

  # ---- collect ----
  rc=0
  run_script collect >/dev/null 2>&1 || rc=$?
  pc_t_eq "collect 成功" "$rc" "0"
  json="$facts/cursor-cases.json"
  pc_t_eq "cursor-cases.json: 11 个用例,字段齐全,needs_credentials 带 recheck" \
    "$(python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1]))
need={"case_id","events","schema_keys","assertion","result","observation","evidence","mode","cursor_version"}
bad=[c["case_id"] for c in d if not need<=set(c)]
nr=[c["case_id"] for c in d if c["result"]=="needs_credentials" and "recheck" not in c]
print(len(d),bad,nr)' "$json")" "11 [] []"
  pc_t_eq "用例结果" \
    "$(python3 -I -c '
import json,sys
d={c["case_id"]:c["result"] for c in json.load(open(sys.argv[1]))}
print(",".join("%s=%s"%(k,d[k]) for k in ["session-start","shell-ok","shell-deny","read-deny","prompt-context","stop-followup","pretool-deny","subagent"]))' "$json")" \
    "session-start=pass,shell-ok=pass,shell-deny=pass,read-deny=pass,prompt-context=pass,stop-followup=pass,pretool-deny=needs_credentials,subagent=needs_credentials"
  local leak
  leak="$(grep -rlE "$(printf '%s' "$root" | sed 's/[][\.*^$/]/\\&/g')|conv-0f3e|gen-77|sess-1|alice.real|corp-example|$(id -un)" "$facts" 2>/dev/null || true)"
  pc_t_eq "输出目录不含临时路径、会话 ID、登录邮箱或本地用户名" "$leak" ""
  pc_t_eq "输出通过脚本自带的卫生/密钥扫描" \
    "$(for f in "$json" "$facts"/payloads/cursor/*/*.json; do pc_py check_text "$f" >/dev/null 2>&1 || echo "FAIL:$f"; done | tr '\n' ' ')" ""
  pc_t_eq "payload 已规范化(ID/路径占位符、afterAgentResponse.text 脱敏)" \
    "$(python3 -I -c '
import json,sys
d=json.load(open(sys.argv[1]))
print(d["conversation_id"],d["workspace_roots"][0],d["transcript_path"],d["text"])' "$facts/payloads/cursor/stop-followup/01-afterAgentResponse.json")" \
    "<conversation_id-1> <work> <transcript-path> <redacted len=4>"
  pc_t_eq "payload 里的 user_email 被替换成占位符" \
    "$(python3 -I -c '
import json,sys
print(json.load(open(sys.argv[1]))["user_email"])' "$facts/payloads/cursor/shell-ok/01-beforeShellExecution.json")" "<user_email>"

  # a refused summary must leave docs/facts untouched (no half-updated state):
  # a raw key that looks like an e-mail address ends up in schema_keys
  local snap_before snap_after
  snap_before="$(find "$facts" -type f -exec cksum {} + | sort | cksum)"
  printf '{"hook_event_name":"beforeShellExecution","bob@example.org":1}' >"$cph/probe-clients/cursor/out/shell-ok/99-zz.json"
  rc=0
  run_script collect >/dev/null 2>&1 || rc=$?
  snap_after="$(find "$facts" -type f -exec cksum {} + | sort | cksum)"
  pc_t_eq "整体扫描失败: 退出码 3 且 docs/facts 保持原样" "$rc:$([ "$snap_before" = "$snap_after" ] && echo same || echo changed)" "3:same"
  unset BALLAST_CURSOR_PROBE_HOME BALLAST_PROBE_FACTS_DIR
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
