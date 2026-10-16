# lower-sandbox-merge 第 2、3 部分(由 scenario.sh source):顶层键 × 层;项目目录解析;汇总。

# ---- 第 2 部分:项目层/本地层的顶层键(none 模式 + 标记文件)-------------------------
declare -A TOP   # TOP[<layer>/<key>]="生效|未生效|…"
top_variant() { # top_variant <layer> <key> <json> [mode] [timeout]
  local layer="$1" key="$2" json="$3" mode="${4:-none}" to="${5:-20}"
  prep_world
  rm -f /home/agent/markers/*
  case "$layer" in
    user) put_user "$json" ;;
    project) put_project "$json" ;;
    local) put_local "$json" ;;
    none) ;;
  esac
  if [ "$key" = enableAllProjectMcpServers ]; then
    jq -n '{mcpServers:{"probe-srv":{command:"sh",args:["-c","echo ran > /home/agent/markers/mcp; sleep 30"]}}}' > "$PROJ/.mcp.json"
    chown agent:agent "$PROJ/.mcp.json"
  fi
  chown -R agent:agent /home/agent/.claude "$PROJ" 2>/dev/null
  run_claude "$mode" "$to"
  sleep 1
  local v="未生效"
  case "$key" in
    env) cat "$PROBE_DIR"/managed-SessionStart-*.env 2>/dev/null | grep -q '^BALLAST_PROBE_ENV=1$' && v="生效(托管探针 hook 的环境里出现 BALLAST_PROBE_ENV=1)" ;;
    apiKeyHelper|statusLine|awsAuthRefresh|otelHeadersHelper)
      [ -f "/home/agent/markers/$key" ] && v="生效(标记文件已写入,命令被执行)" ;;
    enableAllProjectMcpServers)
      [ -f /home/agent/markers/mcp ] && v="生效(项目 .mcp.json 的 server 被自动批准并启动)" ;;
    enabledPlugins)
      ( cd "$PROJ" && as_agent timeout 30 claude plugin list 2>&1 ) > "/tmp/plugin-list-$layer.txt"
      if grep -q 'ballast-probe' "/tmp/plugin-list-$layer.txt"; then v="生效(claude plugin list 列出了该插件条目)"; else v="未生效(claude plugin list 没有该条目:$(head -c 120 "/tmp/plugin-list-$layer.txt" | tr '\n' ' '))"; fi
      save_text summary "plugin-list-$layer.txt" "/tmp/plugin-list-$layer.txt" ;;
  esac
  TOP["$layer/$key"]="$v"
  echo "$layer/$key [mode=$mode rc=$RC]: $v" >> "$SUMMARY/top-level-keys.txt"
}
: > "$SUMMARY/top-level-keys.txt"
marker() { echo "echo ran > /home/agent/markers/$1; echo dummy-not-a-credential"; }
for layer in project local; do
  top_variant "$layer" env '{"env":{"BALLAST_PROBE_ENV":"1"}}'
  top_variant "$layer" apiKeyHelper "$(jq -n --arg c "$(marker apiKeyHelper)" '{apiKeyHelper:$c}')"
  top_variant "$layer" statusLine "$(jq -n --arg c "$(marker statusLine)" '{statusLine:{type:"command",command:$c}}')"
  EXTRA_ENV=(CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=us-east-1 AWS_ACCESS_KEY_ID=dummy-not-a-credential AWS_SECRET_ACCESS_KEY=dummy-not-a-credential)
  top_variant "$layer" awsAuthRefresh "$(jq -n --arg c "$(marker awsAuthRefresh)" '{awsAuthRefresh:$c}')"
  EXTRA_ENV=()
  ENV_UNSET=(-u DISABLE_TELEMETRY -u CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC)
  EXTRA_ENV=(CLAUDE_CODE_ENABLE_TELEMETRY=1 OTEL_METRICS_EXPORTER=otlp OTEL_EXPORTER_OTLP_PROTOCOL=http/json OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:9)
  top_variant "$layer" otelHeadersHelper "$(jq -n --arg c "echo ran > /home/agent/markers/otelHeadersHelper; echo '{}'" '{otelHeadersHelper:$c}')"
  EXTRA_ENV=(); ENV_UNSET=()
  top_variant "$layer" enabledPlugins '{"enabledPlugins":{"ballast-probe@ghost-marketplace":true}}'
  top_variant "$layer" enableAllProjectMcpServers '{"enableAllProjectMcpServers":true}' blackhole 12
done
# 对照:不设 enableAllProjectMcpServers 时 .mcp.json 的 server 是否被自动启动
top_variant none enableAllProjectMcpServers '{}' blackhole 12
TOP_CTRL_MCP="${TOP[none/enableAllProjectMcpServers]}"
# 对照:没有 enabledPlugins 时的 claude plugin list
prep_world; ( cd "$PROJ" && as_agent timeout 30 claude plugin list 2>&1 ) > /tmp/plugin-list-baseline.txt
save_text summary plugin-list-baseline.txt /tmp/plugin-list-baseline.txt

# ---- 第 3 部分:项目目录解析(CLAUDE_PROJECT_DIR 与被加载的项目层)------------------------
DIRS="$SUMMARY/project-dir.txt"; : > "$DIRS"
dir_probe() { # dir_probe <name> <cwd>:当前 /home/agent 下已布置好的各层 hooks;记录被调用的 label 与 CLAUDE_PROJECT_DIR
  local name="$1" cwd="$2"
  rm -rf "$PROBE_DIR"; mkdir -p "$PROBE_DIR"; chmod 1777 "$PROBE_DIR"
  RUN_CWD="$cwd"; run_claude none 20; RUN_CWD=""
  local labels; labels="$(probe_labels SessionStart | tr ' ' '\n' | grep -v '^user$' | tr '\n' ' ' | sed 's/ $//')"
  local pd; pd="$(cat "$PROBE_DIR"/user-SessionStart-*.env 2>/dev/null | grep '^CLAUDE_PROJECT_DIR=' | sort -u | tr '\n' ' ')"
  echo "[$name] 启动目录=${cwd//\/home\/agent/<home>} 被调用的项目层 label=[${labels:-无}] 用户层探针看到的 ${pd:-CLAUDE_PROJECT_DIR=(未设置)}" | tee -a "$DIRS" >/dev/null
  DIR_RESULTS="$DIR_RESULTS$(tail -1 "$DIRS")"$'\n'
}
DIR_RESULTS=""
reset_world   # 不装托管层:让项目层 hook 能运行
put_user "$(hooks_json user SessionStart)"
mkdir -p /home/agent/par/child/sub /home/agent/par/plain
chown -R agent:agent /home/agent/.claude
as_agent git -C /home/agent/par/child init -q .
for d in /home/agent/par /home/agent/par/child /home/agent/par/child/sub; do
  lbl="$(echo "${d#/home/agent/}" | tr '/' '-')"
  mkdir -p "$d/.claude"; hooks_json "p-$lbl" SessionStart > "$d/.claude/settings.json"
done
mkdir -p /home/agent/par/plain/.claude; hooks_json "p-plain" SessionStart > /home/agent/par/plain/.claude/settings.json
chown -R agent:agent /home/agent/par
dir_probe "子目录 sub 启动(git 根为 child,父目录 par 另有项目层)" /home/agent/par/child/sub
dir_probe "git 根 child 启动" /home/agent/par/child
dir_probe "非 git 目录 plain 启动,父目录 par 有项目层" /home/agent/par/plain
mkdir -p /home/agent/par/plain/deeper; chown agent:agent /home/agent/par/plain/deeper
dir_probe "非 git 目录 plain/deeper 启动(上两级有项目层)" /home/agent/par/plain/deeper
save_text summary project-dir.txt "$DIRS"

# ---- 第 4 部分:交互式 TUI(pty,blackhole 模式,观测手段 ②)--------------------------------
# 预置 customApiKeyResponses 与 hasCompletedOnboarding 跳过 key 确认和引导;项目层带 statusLine、
# apiKeyHelper、env 与一个托管 SessionStart 探针:工作区信任确认前后各看一次标记文件与探针。
TUI_TXT=""
PJ='{"statusLine":{"type":"command","command":"echo ran > /home/agent/markers/statusLine; echo sl"},"apiKeyHelper":"echo ran > /home/agent/markers/apiKeyHelper; echo dummy-not-a-credential","env":{"BALLAST_PROBE_ENV":"1"},"sandbox":{"excludedCommands":["npm"],"filesystem":{"allowWrite":["/home/agent"]}}}'
# 两次独立运行:A) 只等待 8 秒(停在信任对话框);B) 接受信任后再等待,并取 /sandbox 屏幕。
tui_gate() { # tui_gate <name> <accept:yes|no> <managed:yes|no>
  prep_world "$([ "$3" = yes ] && echo '' || echo none)"
  [ "$3" = yes ] && put_managed "$(jq '.hooks.SessionStart = [{"hooks":[{"type":"command","command":"/opt/probe/probe.sh managed"}]}]' /m0b/fixtures/managed-t1.json)"
  printf '%s\n' '{"hasCompletedOnboarding":true,"theme":"dark","customApiKeyResponses":{"approved":["mmy-not-a-credential"],"rejected":[]}}' > /home/agent/.claude.json
  chown agent:agent /home/agent/.claude.json
  put_project "$PJ"; rm -f /home/agent/markers/*
  # 无托管层时项目层自带一个 SessionStart 探针,用来观察 hook 是否在信任前运行
  [ "$3" = yes ] || put_project "$(jq '. + {hooks: {SessionStart: [{hooks: [{type:"command", command:"/opt/probe/probe.sh project"}]}]}}' <<<"$PJ")"
  chown -R agent:agent /home/agent/proj /home/agent/markers; mkdir -p "$PROBE_DIR"; chmod 1777 "$PROBE_DIR"
  local steps=(wait:8 snap:dialog)
  [ "$2" = yes ] && steps+=(send:'\x1b[B' send:'\r' wait:8 snap:after-accept send:/sandbox send:'\r' wait:3 send:'\x1b[C' wait:1 send:'\x1b[C' wait:2 snap:sandbox-cmd)
  as_agent env ANTHROPIC_API_KEY=dummy-not-a-credential ANTHROPIC_BASE_URL=http://127.0.0.1:9 TERM=xterm-256color \
    python3 /m0b/fixtures/tui.py /home/agent/proj "${steps[@]}" > /tmp/tui-$1.json 2>/tmp/tui-$1.err
  jq -r '.snaps | to_entries[] | "=== \(.key)\n\(.value)"' /tmp/tui-$1.json > /tmp/tui-$1.txt
  save_text summary "tui-$1.txt" /tmp/tui-$1.txt
  TUI_MARKERS="$(ls /home/agent/markers 2>/dev/null | tr '\n' ' ' | sed 's/ $//')"
  TUI_EVENTS="$(probe_events | tr '\n' ' ' | sed 's/ $//')"
  TUI_DIALOG="$(jq -r '.snaps.dialog' /tmp/tui-$1.json | tr -d '\n ' | grep -c 'trustthisfolder')"
}
TUI_TXT="交互式 TUI(pty,blackhole 端点,预置 key 确认;项目层同时带 statusLine、apiKeyHelper(此处 key 来自环境变量,helper 不会被取用)、env 与 sandbox.excludedCommands/allowWrite):"
for cfg in yes no; do
  tui_gate "gate-before-managed-$cfg" no "$cfg"
  tb_m="${TUI_MARKERS:-无}"; tb_e="${TUI_EVENTS:-无}"; td="$TUI_DIALOG"
  tui_gate "gate-after-managed-$cfg" yes "$cfg"
  ta_m="${TUI_MARKERS:-无}"; ta_e="${TUI_EVENTS:-无}"
  TUI_TXT="$TUI_TXT [托管层=$cfg] 停在工作区信任对话框(对话框出现=$([ "$td" -ge 1 ] && echo 是 || echo 否))时:标记文件=[$tb_m],探针事件=[$tb_e];接受信任后:标记文件=[$ta_m],事件=[$ta_e]。"
done
TUI_TXT="$TUI_TXT /sandbox 本地命令的屏幕见 payloads/summary/tui-gate-after-managed-*.txt。"

# ---- 汇总 ---------------------------------------------------------------------
rget() { echo "${RES[$1]:-}" | tr ' ' '\n' | grep "^$2=" | head -1 | cut -d= -f2; }
effective() { # effective <variant-name> <key>:该键是否改变了生效的沙箱行为(相对 baseline)
  local n="$1"
  case "$2" in
    excludedCommands|excludedCommandsPrefix) [ "$(rget "$n" npm_outside)" = yes ] || [ "$(rget "$n" npm_protected)" = yes ] ;;
    allowUnixSockets|allowAllUnixSockets) [ "$(rget "$n" sock)" = yes ] ;;
    allowWrite)            [ "$(rget "$n" outside)" = yes ] ;;
    weakerNested)          [ "$(rget "$n" proc_pids)" != "$(rget baseline proc_pids)" ] ;;
    allowedDomains)        [ "$(rget "$n" http)" != "$(rget baseline http)" ] ;;
    additionalDirectories) [ "$(rget "$n" write_tool_outside)" = yes ] ;;
  esac
}
verdict_of() { # verdict_of <variant> <key>
  if [ -z "${RES[$1]:-}" ] || [ "${RES[$1]}" = mock-down ] || [ "$(rget "$1" steps)" = 0 ]; then echo "未观测"; return; fi
  case "$2" in excludedCommands|excludedCommandsPrefix)
    if [ "$(rget "$1" npm_protected)" = "?" ]; then echo "命令未执行(被拒)"; return; fi ;;
  esac
  if effective "$1" "$2"; then echo 生效; else echo 无效果; fi
}
# unobs:某键因脚本/桩故障根本没有数据(RES 为空、桩宕机、桩没发步骤)⇒ 整个用例降为 unverifiable。
# partial:enableWeakerNestedSandbox 的指示器(/proc 进程数)在本容器里没有区分度,"无效果"不能当作"已观测":
# 记为"未观测(指示器无区分度)"并保持保守(条件 6 不放宽该键)。这是有意的"部分观测 pass",见 case.json assertion。
breach=0; unobs=0; partial=0
rows=""
for k in "${SB_KEYS[@]}"; do
  cells=""
  for layer in user project local; do
    n="$layer/$k"; v="$(verdict_of "$n" "$k")"
    if [ "$k" = weakerNested ] && [ "$v" = 无效果 ]; then v="未观测(指示器无区分度)"; partial=1; fi
    case "$v" in 未观测) unobs=1 ;; esac   # 精确匹配:只有"没有数据"才算;带括号说明的已知指示器缺口走 partial
    [ "$(rget "$n" protected)" = yes ] && { breach=1; v="$v(且受保护路径被写入!)"; }
    cells="$cells$layer=$v "
  done
  rows="$rows[${k}: ${cells% }]"
done
for n in "${!RES[@]}"; do
  case "$n" in *无托管层*|*only-enabled*|*enabled-nounsb*|*enabled-failif*|*no-sandbox-block*|*sandbox-minimal*) continue ;; esac   # 这些变体的托管层没有 denyWrite,写入受保护路径是预期
  [ "$(rget "$n" protected)" = yes ] && breach=1
done
bl="${RES[baseline]:-未运行}"
ctrl=""
ctrl="$ctrl[阳性对照(无托管层,用户层自开沙箱):excludedCommands:[\"npm\"] 时 npm run 在沙箱外运行=$(verdict_of '对照/excludedCommands(无托管层,用户层)' excludedCommands);excludedCommands:[\"npm:*\"]=$(verdict_of '对照/excludedCommandsPrefix(无托管层,用户层)' excludedCommandsPrefix)(裸名 \"npm\" 只匹配精确命令 npm,不匹配 npm run x;前缀写法是 \"npm:*\")]"
ctrl="$ctrl[托管 allowUnsandboxedCommands:true 时 excludedCommands:[\"npm:*\"] 用户层=$(verdict_of '对照/excludedCommandsPrefix(托管 allowUnsandboxedCommands:true,用户层)' excludedCommandsPrefix) 项目层=$(verdict_of '对照/excludedCommandsPrefix(托管 allowUnsandboxedCommands:true,项目层)' excludedCommandsPrefix)]"
ctrl="$ctrl[无托管层、各层自开沙箱的 allowWrite:用户层=$(verdict_of '对照/allowWrite(无托管层,用户层)' allowWrite) 项目层=$(verdict_of '对照/allowWrite(无托管层,项目层)' allowWrite) 本地层=$(verdict_of '对照/allowWrite(无托管层,本地层)' allowWrite) 项目层(工作区已信任)=$(verdict_of '对照/allowWrite(无托管层,项目层,工作区已信任)' allowWrite)]"
ctrl="$ctrl[托管 T1 + 项目层 allowWrite(工作区已信任)=$(verdict_of '对照/allowWrite(托管 T1,项目层,工作区已信任)' allowWrite)]"
sup=""
for mv in no-permonly no-hooksonly no-only sandbox-minimal only-enabled enabled-nounsb enabled-failif no-sandbox-block; do
  cells=""
  for layer in project local; do for k in allowWrite allowAllUnixSockets excludedCommandsPrefix; do
    cells="$cells$layer/$k=$(verdict_of "托管变体:$mv/$layer/$k" "$k") "
  done; done
  case "$mv" in
    no-permonly) what=allowManagedPermissionRulesOnly ;;
    no-hooksonly) what=allowManagedHooksOnly ;;
    no-only) what="allowManagedPermissionRulesOnly 与 allowManagedHooksOnly" ;;
    sandbox-minimal) what="sandbox.filesystem 与 sandbox.excludedCommands" ;;
    only-enabled) what="除 sandbox.enabled:true 外的全部键(托管层只剩这一个键)" ;;
    enabled-nounsb) what="除 sandbox.enabled:true 与 allowUnsandboxedCommands:false 外的全部键" ;;
    enabled-failif) what="除 sandbox.enabled:true 与 failIfUnavailable:true 外的全部键" ;;
    no-sandbox-block) what="整个 sandbox 段(项目/本地层自己写 sandbox.enabled:true;托管层只有 allowManagedHooksOnly 与探针 hook)" ;;
  esac
  sup="$sup[托管模板去掉 $what: ${cells% }]"
done
extra_excl=""
for layer in user project local; do extra_excl="$extra_excl$layer=$(verdict_of "$layer/excludedCommands(托管层未设该键)" excludedCommands) "; done
cjv() { verdict_of "$1" "$2"; }
cj="claude.json 全局 excludedCommands=$(cjv 'claude.json/全局 excludedCommands' excludedCommands);按项目 excludedCommands=$(cjv 'claude.json/按项目 excludedCommands' excludedCommands);全局 allowWrite=$(cjv 'claude.json/全局 allowWrite' allowWrite)"
toprows=""
for layer in project local; do
  for k in env apiKeyHelper statusLine awsAuthRefresh otelHeadersHelper enabledPlugins enableAllProjectMcpServers; do
    toprows="$toprows[$layer/$k: ${TOP[$layer/$k]:-未运行}]"
  done
done
mcpnote=""
echo "$TOP_CTRL_MCP" | grep -q '^生效' && mcpnote="(对照组没有该键时项目 .mcp.json 的 server 同样启动:-p 模式下该键没有可观察差异,项目 .mcp.json 在 -p 下无需批准即运行)"
obs="观测手段:③ 模拟模型(mode:mock)——桩让客户端真实执行 Bash(沙箱内运行测试脚本)与 Write 工具,从结果文件判断;托管层为 org render 的 T1 模板(excludedCommands:[]、allowUnsandboxedCommands:false、denyWrite 与 permissions.deny 含 **/ 形式、allowManagedHooksOnly、allowManagedPermissionRulesOnly)。① claude --debug 启动日志没有打印生效的沙箱配置(未用)。② 见第 4 部分。baseline(只有托管层): ${bl}。"
obs="$obs 沙箱键 × 层(相对 baseline 是否改变生效行为;weakerNested 以沙箱内可见的 /proc 进程数为指示,本容器里该指示器对此键没有区分度,无差异不等于无效果):${rows}。${ctrl}。逐个去掉托管层的收紧键后,项目层/本地层的沙箱键是否恢复生效(查找压制它们的托管键):${sup}。托管层不设 excludedCommands 时低层写入:${extra_excl% }。${cj}。"
obs="$obs 项目层/本地层顶层键(none 或 blackhole 模式 + 标记文件;statusLine 只在交互 TUI 里运行):${toprows}${mcpnote}。"
obs="$obs ${TUI_TXT}"
obs="$obs 项目目录解析(用户层探针记录 CLAUDE_PROJECT_DIR):$(printf '%s' "$DIR_RESULTS" | tr '\n' ' ')"
sk='{"sandbox":["excludedCommands","network.allowUnixSockets","network.allowAllUnixSockets","filesystem.allowWrite","enableWeakerNestedSandbox","network.allowedDomains"],"permissions":["additionalDirectories"],"top-level":["env","apiKeyHelper","statusLine","awsAuthRefresh","otelHeadersHelper","enabledPlugins","enableAllProjectMcpServers"]}'
if [ "$breach" = 1 ]; then
  finish fail mock "$obs 【契约影响】托管 denyWrite 保护的路径被低层配置击穿。" "$sk"
elif [ "$unobs" = 1 ]; then
  # 没有任何补测手段(case.json 无 recheck;场景脚本没有 credentials 分支),所以不写 needs_credentials
  finish unverifiable mock "部分键没有取得任何观测数据(脚本或桩故障)。$obs" "$sk"
elif [ "$partial" = 1 ]; then
  finish pass mock "[部分观测:enableWeakerNestedSandbox 因指示器无区分度记为未观测,tier.WeakenedSandboxKeys 对它保持保守;其余键全部观测] $obs" "$sk"
else
  finish pass mock "$obs" "$sk"
fi
