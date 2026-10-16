#!/usr/bin/env bash
source /m0b/lib.sh
reset_world
put_managed "$(hooks_json managed SessionStart UserPromptSubmit ConfigChange)"
put_user '{"cleanupPeriodDays":30}'
put_project '{"cleanupPeriodDays":30}'
put_local '{"cleanupPeriodDays":30}'
mkdir -p "$PROJ/.claude/skills/probe-skill"
printf -- '---\nname: probe-skill\ndescription: probe\n---\nv1\n' > "$PROJ/.claude/skills/probe-skill/SKILL.md"
mkdir -p "$HOME_AGENT/.claude/skills/user-skill"
printf -- '---\nname: user-skill\ndescription: probe\n---\nv1\n' > "$HOME_AGENT/.claude/skills/user-skill/SKILL.md"
chown -R agent:agent "$PROJ" "$HOME_AGENT/.claude"
run_claude_bg hang 150
wait_probe managed:UserPromptSubmit 30 || { stop_bg; finish fail hang "会话在 30 秒内没有走到 UserPromptSubmit,无法在会话存活期间改配置。"; exit 0; }
sleep 2
rows=""
step() { # step <name> <command...>:执行改动,等 6 秒看是否有新的 ConfigChange
  local name="$1"; shift
  local before; before="$(probe_events | grep -c '^managed:ConfigChange$')"
  "$@"
  if wait_new_probe managed:ConfigChange "$before" 6; then
    local latest; latest="$(ls -t "$PROBE_DIR"/managed-ConfigChange-*.json | head -1)"
    rows="$rows[$name => 触发,source=$(jq -r .source "$latest"),file_path=$(jq -r .file_path "$latest")]"
  else
    rows="$rows[$name => 未触发]"
  fi
}
edit_user()    { put_user '{"cleanupPeriodDays":31}'; }
edit_project() { put_project '{"cleanupPeriodDays":31}'; }
edit_local()   { put_local '{"cleanupPeriodDays":31}'; }
edit_userskill()    { printf -- '---\nname: user-skill\ndescription: probe\n---\nv2\n' > "$HOME_AGENT/.claude/skills/user-skill/SKILL.md"; chown agent:agent "$HOME_AGENT/.claude/skills/user-skill/SKILL.md"; }
edit_projskill()    { printf -- '---\nname: probe-skill\ndescription: probe\n---\nv2\n' > "$PROJ/.claude/skills/probe-skill/SKILL.md"; chown agent:agent "$PROJ/.claude/skills/probe-skill/SKILL.md"; }
edit_managed()      { put_managed "$(merge_json "$(hooks_json managed SessionStart UserPromptSubmit ConfigChange)" '{"cleanupPeriodDays":31}')"; }
edit_managed_d()    { put_managed '{"cleanupPeriodDays":32}' managed-settings.d/50-x.json; }
edit_claude_json()  { printf '{"mcpServers":{}}\n' > "$HOME_AGENT/.claude.json"; chown agent:agent "$HOME_AGENT/.claude.json"; }
step "改写用户层 settings.json" edit_user
step "改写项目层 settings.json" edit_project
step "改写本地层 settings.local.json" edit_local
step "改写用户 skill(SKILL.md)" edit_userskill
step "改写项目 skill(SKILL.md)" edit_projskill
step "改写托管 managed-settings.json" edit_managed
step "新增托管 managed-settings.d/50-x.json" edit_managed_d
step "改写 ~/.claude.json" edit_claude_json
stop_bg
save_payloads hang
n="$(probe_count managed ConfigChange)"
obs="hang 模式(本地监听器只 accept 不应答)下 claude -p 存活,走到 UserPromptSubmit 之后依次改动配置文件,托管层配 ConfigChange 探针(无 matcher)。$rows。ConfigChange 共 $n 次。"
if [ "$n" -ge 1 ]; then
  keys="$(cat "$PROBE_DIR"/managed-ConfigChange-*.json 2>/dev/null | jq -s -c 'map(keys)|add|unique')"
  obs="$obs ConfigChange payload 顶层键:$keys。"
  finish pass hang "$obs"
else
  finish fail hang "$obs"
fi
