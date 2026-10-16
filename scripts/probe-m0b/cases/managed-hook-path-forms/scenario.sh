#!/usr/bin/env bash
source /m0b/lib.sh
reset_world
mkdir -p "/opt/probe dir" /home/agent/bin /usr/local/bin
cp /opt/probe/probe.sh "/opt/probe dir/probe.sh"
# 每个目标脚本把自己的位置和第一个参数写进 label,从 label 判断实际执行的是哪个文件。
mk() { printf '#!/bin/sh\nexec /opt/probe/probe.sh "%s-${1:-noargs}"\n' "$2" > "$1"; chmod 755 "$1"; }
mk /usr/local/bin/m0b-bare     root-bare        # 系统(root 所有)位置
mk /home/agent/bin/m0b-bare    agentpath-bare   # agent 可写、在 PATH 最前
mk "$PROJ/m0b-rel.sh"          cwd-rel          # 会话目录(agent 所有)
mk /home/agent/m0b-home.sh     agenthome        # agent 家目录
chown -R agent:agent /home/agent/bin "$PROJ" /home/agent/m0b-home.sh
mkdir -p /etc/claude-code
cat > /etc/claude-code/managed-settings.json <<'J'
{"hooks":{"SessionStart":[{"hooks":[
 {"type":"command","command":"/opt/probe/probe.sh f01-abs"},
 {"type":"command","command":"/opt/probe/probe.sh","args":["f02-abs-args"]},
 {"type":"command","command":"m0b-bare f03-bare"},
 {"type":"command","command":"m0b-bare","args":["f04-bare-args"]},
 {"type":"command","command":"./m0b-rel.sh f05-rel"},
 {"type":"command","command":"./m0b-rel.sh","args":["f06-rel-args"]},
 {"type":"command","command":"~/m0b-home.sh f07-tilde"},
 {"type":"command","command":"~/m0b-home.sh","args":["f08-tilde-args"]},
 {"type":"command","command":"$HOME/m0b-home.sh f09-homevar"},
 {"type":"command","command":"$HOME/m0b-home.sh","args":["f10-homevar-args"]},
 {"type":"command","command":"/opt/probe dir/probe.sh f11-space-unquoted"},
 {"type":"command","command":"\"/opt/probe dir/probe.sh\" f12-space-quoted"},
 {"type":"command","command":"/opt/probe dir/probe.sh","args":["f13-space-args"]}
]}]}}
J
chown -R root:root /etc/claude-code; chmod 755 /etc/claude-code; chmod 644 /etc/claude-code/managed-settings.json
# agent 的 PATH:可写目录排在最前(模拟 shell rc 或环境被改)。
AGENT_PATH=/home/agent/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
run_claude none 20
save_payloads none
files="$(ls "$PROBE_DIR" | grep -E '\.json$' | sed -E 's/-SessionStart-[0-9]+\.json$//')"
rows=""
check() { # check <form-id> <desc> <expected-label>
  local id="$1" desc="$2" lab
  lab="$(echo "$files" | grep -E -- "(^|-)$id\$" | head -1)"
  if [ -n "$lab" ]; then
    where="${lab%$id}"; where="${where%-}"; [ -n "$where" ] || where="所写文件本身"
    rows="$rows$id($desc)=执行,解析到[$where];"
  else rows="$rows$id($desc)=未执行;"; fi
}
check f01-abs 绝对路径; check f02-abs-args 绝对路径+args; check f03-bare 裸名; check f04-bare-args 裸名+args
check f05-rel ./相对; check f06-rel-args ./相对+args; check f07-tilde '~/'; check f08-tilde-args '~/+args'
check f09-homevar '$HOME/'; check f10-homevar-args '$HOME/+args'
check f11-space-unquoted 含空格未加引号; check f12-space-quoted 含空格加引号; check f13-space-args 含空格+args
obs="托管层 SessionStart 配 13 种 command 写法,none 模式,agent 的 PATH 把可写目录 /home/agent/bin 排在最前(模拟被改的环境)。目标脚本的 label 标明实际执行的是哪个文件:root-bare=/usr/local/bin(系统位置),agentpath-bare=/home/agent/bin(agent 可写,即被劫持),cwd-rel=会话目录,agenthome=agent 家目录。结果:$rows"
a=0; b=0
echo "$files" | grep -qE -- '(^|-)f01-abs$' && echo "$files" | grep -qE -- '(^|-)f02-abs-args$' && echo "$files" | grep -qE -- '(^|-)f13-space-args$' && a=1
echo "$files" | grep -q -- 'agentpath-bare-f0[34]' && b=1
if [ "$a" = 1 ] && [ "$b" = 1 ]; then finish pass none "$obs"; else finish fail none "$obs"; fi
