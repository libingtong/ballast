#!/usr/bin/env bash
# lower-sandbox-merge:见 docs/p2-design.md §6.4。观测手段:③ 模拟模型(mode:"mock")让客户端真实执行
# Bash/Write 工具调用,从文件系统结果判断各键是否生效;项目层顶层键用 none 模式 + 标记文件。
source /m0b/lib.sh
CASE="$CASE_DIR"
SUMMARY="$OUT/payloads/summary"; mkdir -p "$SUMMARY"

# ---- 公共夹具 --------------------------------------------------------------
start_dockersock() {
  python3 - <<'PY' >/dev/null 2>&1 &
import socket, os
p = '/var/run/docker.sock'
if os.path.lexists(p): os.remove(p)
s = socket.socket(socket.AF_UNIX); s.bind(p); os.chmod(p, 0o666); s.listen(8)
while True:
    c, _ = s.accept(); c.close()
PY
  sleep 0.5
}
prep_world() { # prep_world [managed-file|none]
  reset_world
  mkdir -p /home/agent/outside /home/agent/.ballast/evidence /home/agent/markers /usr/local/libexec/ballast /etc/claude-code/ballast
  if [ "${1:-}" = none ]; then
    rm -rf /etc/claude-code
  else
    cp "${1:-/m0b/fixtures/managed-t1.json}" /etc/claude-code/managed-settings.json
    chown -R root:root /etc/claude-code; chmod 755 /etc/claude-code; chmod 644 /etc/claude-code/managed-settings.json
  fi
  cat > /usr/local/bin/npm <<'NPM'
#!/bin/sh
# 假 npm:被 Claude 自己用 `npm root -g` 调用时(沙箱外)不记录;`npm run x` 时记录能否写受保护路径。
[ "$1" = run ] || { echo /usr/local/lib/node_modules; exit 0; }
(echo x > /home/agent/.ballast/evidence/npm) 2>/dev/null && w=yes || w=no
(echo x > /home/agent/outside/npm) 2>/dev/null && o=yes || o=no
echo "npm_write_protected=$w npm_write_outside=$o" >> /home/agent/proj/npm-result.txt
NPM
  chmod 755 /usr/local/bin/npm
  cat > /home/agent/proj/probe-test.sh <<'SH'
r=/home/agent/proj/result.txt
{
 (echo x > /home/agent/outside/a) 2>/dev/null && echo outside_write=yes || echo outside_write=no
 (echo x > /home/agent/.ballast/evidence/b) 2>/dev/null && echo protected_write=yes || echo protected_write=no
 python3 -c "import socket;s=socket.socket(socket.AF_UNIX);s.connect('/var/run/docker.sock')" 2>/dev/null && echo unix_sock=yes || echo unix_sock=no
 echo "pid1=$(cat /proc/1/comm 2>/dev/null)"
 echo "proc_pids=$(ls /proc | grep -c '^[0-9][0-9]*$')"
 python3 - <<'PY' 2>/dev/null || echo http_status=error
import urllib.request
try:
    r = urllib.request.urlopen('http://example.com/', timeout=5); print('http_status=%s' % r.status)
except Exception as e:
    print('http_status=%s' % getattr(e, 'code', type(e).__name__))
PY
} > $r
SH
  chown -R agent:agent /home/agent/outside /home/agent/.ballast /home/agent/markers /home/agent/proj
}

# ---- 沙箱键 × 层 -----------------------------------------------------------
key_json() {
  case "$1" in
    excludedCommands)      echo '{"sandbox":{"excludedCommands":["npm"]}}' ;;
    excludedCommandsPrefix) echo '{"sandbox":{"excludedCommands":["npm:*"]}}' ;;
    allowUnixSockets)      echo '{"sandbox":{"network":{"allowUnixSockets":["/var/run/docker.sock"]}}}' ;;
    allowAllUnixSockets)   echo '{"sandbox":{"network":{"allowAllUnixSockets":true}}}' ;;
    allowWrite)            echo '{"sandbox":{"filesystem":{"allowWrite":["/home/agent"]}}}' ;;
    weakerNested)          echo '{"sandbox":{"enableWeakerNestedSandbox":true}}' ;;
    allowedDomains)        echo '{"sandbox":{"network":{"allowedDomains":["example.com"]}}}' ;;
    additionalDirectories) echo '{"permissions":{"additionalDirectories":["/home/agent"]}}' ;;
  esac
}
SB_KEYS=(excludedCommands excludedCommandsPrefix allowUnixSockets allowAllUnixSockets allowWrite weakerNested allowedDomains additionalDirectories)
declare -A RES   # RES[<variant>]="outside=.. protected=.. sock=.. pid1=.. http=.. npm=.. write=.."
mock_variant() { # mock_variant <name> <layer> <json> [managed-file]
  local name="$1" layer="$2" json="$3"
  prep_world "${4:-}"
  case "$layer" in
    none) ;;
    user) put_user "$json" ;;
    project) put_project "$json" ;;
    local) put_local "$json" ;;
    claudejson-global) printf '%s\n' "$json" > /home/agent/.claude.json; chown agent:agent /home/agent/.claude.json ;;
    trusted-project) put_project "$json"; jq -n '{projects: {"/home/agent/proj": {hasTrustDialogAccepted: true}}}' > /home/agent/.claude.json; chown agent:agent /home/agent/.claude.json ;;
    claudejson-project) jq -n --argjson s "$json" '{projects: {"/home/agent/proj": $s}}' > /home/agent/.claude.json; chown agent:agent /home/agent/.claude.json ;;
  esac
  chown -R agent:agent /home/agent/.claude /home/agent/proj 2>/dev/null
  start_mock "$(steps_script "$(bash_step 'sh /home/agent/proj/probe-test.sh')" "$(bash_step 'npm run x')" '{"tool":"Write","input":{"file_path":"/home/agent/outside/w.txt","content":"hi"}}')" || { RES[$name]="mock-down"; return; }
  RUN_PROMPT="go"
  run_claude mock 90 --permission-mode acceptEdits --allowedTools Bash
  local r="$PROJ/result.txt" o sock out prot pid1 http npm w
  o="$(grep -s '^outside_write=' "$r" | cut -d= -f2)"; prot="$(grep -s '^protected_write=' "$r" | cut -d= -f2)"
  sock="$(grep -s '^unix_sock=' "$r" | cut -d= -f2)"; pid1="$(grep -s '^pid1=' "$r" | cut -d= -f2)"
  http="$(grep -s '^http_status=' "$r" | cut -d= -f2)"
  local pp; pp="$(grep -s '^proc_pids=' "$r" | cut -d= -f2)"
  npm="$(grep -s '^npm_write_protected=' "$PROJ/npm-result.txt" | tail -1 | sed -E 's/^npm_write_protected=([a-z]+).*/\1/')"
  local npo; npo="$(grep -s '^npm_write_protected=' "$PROJ/npm-result.txt" | tail -1 | sed -E 's/.*npm_write_outside=([a-z]+).*/\1/')"
  [ -f /home/agent/outside/w.txt ] && w=yes || w=no
  RES[$name]="outside=${o:-?} protected=${prot:-?} sock=${sock:-?} pid1=${pid1:-?} proc_pids=${pp:-?} http=${http:-?} npm_protected=${npm:-?} npm_outside=${npo:-?} write_tool_outside=$w steps=$(mock_steps_done)"
  echo "$name: ${RES[$name]}" >> "$SUMMARY/sandbox-matrix.txt"
}

start_dockersock
: > "$SUMMARY/sandbox-matrix.txt"
mock_notrust_home() { :; }
mock_variant baseline none '{}'
for layer in user project local; do
  for k in "${SB_KEYS[@]}"; do
    mock_variant "$layer/$k" "$layer" "$(key_json "$k")"
  done
done
# excludedCommands 的补充:托管层没有这个键时低层写入是否生效(区分"被托管层覆盖"与"被忽略")
jq 'del(.sandbox.excludedCommands)' /m0b/fixtures/managed-t1.json > /tmp/managed-noexcl.json
for layer in user project local; do
  mock_variant "$layer/excludedCommands(托管层未设该键)" "$layer" "$(key_json excludedCommands)" /tmp/managed-noexcl.json
done
# 阳性对照 1:托管层 allowUnsandboxedCommands:true 时用户层 excludedCommands 是否生效(证明测试手段能观测到 excludedCommands)
jq '.sandbox.allowUnsandboxedCommands = true' /m0b/fixtures/managed-t1.json > /tmp/managed-allowunsb.json
mock_variant "对照/excludedCommandsPrefix(托管 allowUnsandboxedCommands:true,用户层)" user "$(key_json excludedCommandsPrefix)" /tmp/managed-allowunsb.json
mock_variant "对照/excludedCommandsPrefix(托管 allowUnsandboxedCommands:true,项目层)" project "$(key_json excludedCommandsPrefix)" /tmp/managed-allowunsb.json
# 为什么托管层存在时项目/本地层的沙箱键没有效果?逐个去掉托管层里的收紧键,看哪一个让它们恢复生效
jq 'del(.allowManagedPermissionRulesOnly)' /m0b/fixtures/managed-t1.json > /tmp/managed-no-permonly.json
jq 'del(.allowManagedHooksOnly)' /m0b/fixtures/managed-t1.json > /tmp/managed-no-hooksonly.json
jq 'del(.allowManagedPermissionRulesOnly) | del(.allowManagedHooksOnly)' /m0b/fixtures/managed-t1.json > /tmp/managed-no-only.json
jq 'del(.sandbox.filesystem) | del(.sandbox.excludedCommands)' /m0b/fixtures/managed-t1.json > /tmp/managed-sandbox-minimal.json
jq -n '{sandbox:{enabled:true}}' > /tmp/managed-only-enabled.json
jq -n '{sandbox:{enabled:true,allowUnsandboxedCommands:false}}' > /tmp/managed-enabled-nounsb.json
jq -n '{sandbox:{enabled:true,failIfUnavailable:true}}' > /tmp/managed-enabled-failif.json
jq -n '{allowManagedHooksOnly:true, hooks:{SessionStart:[{hooks:[{type:"command",command:"/opt/probe/probe.sh managed"}]}]}}' > /tmp/managed-no-sandbox-block.json
for mv in no-permonly no-hooksonly no-only sandbox-minimal only-enabled enabled-nounsb enabled-failif no-sandbox-block; do
  for layer in project local; do
    for k in allowWrite allowAllUnixSockets excludedCommandsPrefix; do
      kj="$(key_json "$k")"
      [ "$mv" = no-sandbox-block ] && kj="$(jq '.sandbox.enabled = true' <<<"$kj")"
      mock_variant "托管变体:$mv/$layer/$k" "$layer" "$kj" "/tmp/managed-$mv.json"
    done
  done
done
# 阳性对照 1b:没有托管层时,用户层自开沙箱 + excludedCommands(npm 应在沙箱外运行,能写会话目录外)
mock_variant "对照/excludedCommands(无托管层,用户层)" user '{"sandbox":{"enabled":true,"excludedCommands":["npm"]}}' none
mock_variant "对照/excludedCommandsPrefix(无托管层,用户层)" user '{"sandbox":{"enabled":true,"excludedCommands":["npm:*"]}}' none
mock_variant "对照/无 excludedCommands(无托管层,用户层)" user '{"sandbox":{"enabled":true}}' none
# 阳性对照 2:没有托管层时,各层自己开沙箱并写 allowWrite 是否生效(区分"被托管层压制"与"客户端不接受该层的这个键")
SBON='{"sandbox":{"enabled":true,"filesystem":{"allowWrite":["/home/agent"]}}}'
mock_variant "对照/allowWrite(无托管层,用户层)" user "$SBON" none
mock_variant "对照/allowWrite(无托管层,项目层)" project "$SBON" none
mock_variant "对照/allowWrite(无托管层,本地层)" local "$SBON" none
mock_variant "对照/allowWrite(无托管层,项目层,工作区已信任)" trusted-project "$SBON" none
mock_variant "对照/allowWrite(托管 T1,项目层,工作区已信任)" trusted-project "$(key_json allowWrite)"
# ~/.claude.json 能否承载 sandbox 键(全局或按项目)
mock_variant "claude.json/全局 excludedCommands" claudejson-global "$(key_json excludedCommands)"
mock_variant "claude.json/按项目 excludedCommands" claudejson-project "$(jq -n --argjson s "$(key_json excludedCommands)" '$s')"
mock_variant "claude.json/全局 allowWrite" claudejson-global "$(key_json allowWrite)"

echo "$(date +%T) 沙箱键测量完成" >> "$RUNLOG"
source "$CASE/part2.sh"
