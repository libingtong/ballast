#!/usr/bin/env bash
source /m0b/lib.sh
obs=""
listed() { # listed:输出 claude mcp list 里出现的 server 名(空格分隔)
  ( cd "$PROJ" && as_agent timeout 40 claude mcp list 2>&1 ) > /tmp/mcp-list.txt
  grep -E '^[A-Za-z0-9_.-]+: ' /tmp/mcp-list.txt | cut -d: -f1 | tr '\n' ' ' | sed 's/ $//'
}
setup() { # setup <managed-json|"">
  reset_world
  as_agent claude mcp add --scope user other-srv -- /bin/true >/dev/null 2>&1
  as_agent claude mcp add --scope user allowed-srv -- /bin/true >/dev/null 2>&1
  # 项目层 .mcp.json 里再放一个(项目共享 server)
  printf '{"mcpServers":{"project-srv":{"command":"/bin/true","args":[]}}}\n' > "$PROJ/.mcp.json"; chown agent:agent "$PROJ/.mcp.json"
  [ -n "$1" ] && put_managed "$1"
  return 0
}
setup ""
l_none="$(listed)"; save_text none list.txt /tmp/mcp-list.txt
setup '{"allowedMcpServers":[{"serverName":"allowed-srv"}]}'
l_allow="$(listed)"; save_text allowed list.txt /tmp/mcp-list.txt
setup '{"allowedMcpServers":[]}'
l_empty="$(listed)"; save_text allowed-empty list.txt /tmp/mcp-list.txt
setup '{"deniedMcpServers":[{"serverName":"other-srv"}]}'
l_deny="$(listed)"; save_text denied list.txt /tmp/mcp-list.txt
setup '{"allowedMcpServer":[{"serverName":"allowed-srv"}]}'
l_typo="$(listed)"; save_text typo-key list.txt /tmp/mcp-list.txt
setup '{"allowManagedMcpServersOnly":true,"allowedMcpServers":[{"serverName":"allowed-srv"}]}'
l_only="$(listed)"; save_text managed-only list.txt /tmp/mcp-list.txt
obs="用户层 other-srv 与 allowed-srv(claude mcp add --scope user),项目层 .mcp.json 的 project-srv。claude mcp list 输出的 server:[无托管配置 => ${l_none:-无}];[allowedMcpServers=[allowed-srv] => ${l_allow:-无}];[allowedMcpServers=[] => ${l_empty:-无}];[deniedMcpServers=[other-srv] => ${l_deny:-无}];[拼错的键 allowedMcpServer => ${l_typo:-无}];[allowManagedMcpServersOnly:true + allowedMcpServers=[allowed-srv] => ${l_only:-无}]。白名单条目的形状 {\"serverName\":\"...\"}。"
if [ "$l_allow" = "allowed-srv" ] && [ -z "$l_empty" ] && ! echo " $l_deny " | grep -q " other-srv " && echo " $l_typo " | grep -q " other-srv "; then
  finish pass none "$obs" '{"managed-settings":["allowedMcpServers","deniedMcpServers","allowManagedMcpServersOnly"]}'
else
  finish fail none "$obs" '{"managed-settings":["allowedMcpServers","deniedMcpServers","allowManagedMcpServersOnly"]}'
fi
