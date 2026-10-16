#!/usr/bin/env bash
# Shared helpers of scripts/probe-clients/codex.sh and cursor.sh (and ONLY
# those two: hermes.sh carries its own helpers, see docs/p2-design.md section 9).
# Source it, do not execute it.
#
# What the probe scripts do: install Ballast plus a record-only probe hook into
# a THROWAWAY client configuration root, print a checklist of prompts the user
# types into the real client (the user logs in there himself), then collect
# the recorded hook payloads, normalise them and write the cases file under
# docs/facts/. They never start codex / cursor / cursor-agent, never read an
# auth file, a keychain or an environment credential, and refuse to run when
# a directory they were pointed at is (or contains, or lives inside the
# protected parts of) a real home directory.
#
# Conventions: bash 3.2 compatible (no associative arrays, no mapfile, no
# ${var,,}); every Python helper lives in one module that is written into a
# private temp directory at load time and run with `python3 -I`.
# shellcheck shell=bash
# shellcheck disable=SC2034  # globals are read by codex.sh / cursor.sh

if [ -n "${PC_LIB_LOADED:-}" ]; then
  return 0
fi
PC_LIB_LOADED=1

set -euo pipefail

PC_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PC_REPO_ROOT="$(cd "$PC_LIB_DIR/../.." && pwd)"
PC_TMP=""
PC_PY=""
PC_T_PASS=0
PC_T_FAIL=0
PC_T_SKIP=0

# ----------------------------------------------------------------- output ---

pc_say() { printf '%s\n' "$*"; }
pc_warn() { printf '警告: %s\n' "$*" >&2; }
# pc_die MSG: refusal / usage error, exit code 2.
pc_die() {
  printf '拒绝/错误: %s\n' "$*" >&2
  exit 2
}

pc_cleanup() {
  if [ -n "$PC_TMP" ] && [ -d "$PC_TMP" ] && [ -z "${BALLAST_KEEP_TMP:-}" ]; then
    rm -rf "$PC_TMP"
  fi
}

# ----------------------------------------------------------- python module ---

# The module is a quoted heredoc: nothing in it is expanded by the shell.
pc_write_py() {
  cat >"$PC_PY" <<'PYEOF'
import collections
import getpass
import hashlib
import json
import os
import pwd
import re
import socket
import sys

MARKER = ".ballast-probe"
# Sub-paths of a real home that a probe directory may never be, or live inside.
PROTECTED_SUB = [".codex", ".claude", ".claude.json", ".cursor", ".hermes",
                 ".ballast", ".ssh", ".gnupg", ".config", ".local", ".aws",
                 ".kube", ".docker", "Library"]
# System locations (managed-settings roots and friends) never usable as a probe.
SYSTEM_DIRS = ["/etc", "/Library", "/System", "/usr", "/bin", "/sbin", "/root",
               "/var/root", "/opt"]
SECRET_PATTERNS = [
    re.compile(r"sk-[A-Za-z0-9_-]{16,}"),
    re.compile(r"eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}"),
    re.compile(r"(?i)bearer\s+[A-Za-z0-9._~+/=-]{12,}"),
    re.compile(r"(?i)\"?(access|refresh|id)_token\"?\s*[:=]"),
    re.compile(r"(?i)(api[_-]?key|secret|password)\"?\s*[:=]\s*\"?[A-Za-z0-9/+_=-]{12,}"),
    re.compile(r"-----BEGIN [A-Z ]*(PRIVATE KEY|CERTIFICATE)"),
    re.compile(r"gh[pousr]_[A-Za-z0-9]{20,}"),
    re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b"),
    re.compile(r"AIza[0-9A-Za-z_-]{35}"),
    re.compile(r"xox[baprs]-[A-Za-z0-9-]{10,}"),
    re.compile(r"glpat-[A-Za-z0-9_-]{20,}"),
    re.compile(r"github_pat_[A-Za-z0-9_]{20,}"),
]
HYGIENE_PATTERNS = [
    re.compile(r"/Users/[A-Za-z][A-Za-z0-9._-]*/"),
    re.compile(r"/home/[A-Za-z][A-Za-z0-9._-]*/"),
    re.compile(r"claude-[0-9]+-"),
    # per-user temp roots on macOS carry a machine-identifying hash
    re.compile(r"/(private/)?var/folders/"),
    re.compile(r"[~]/NewCode"),
    # logged-in account e-mail (Cursor puts user_email into every hook payload)
    re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}"),
]
EMAIL_RE = HYGIENE_PATTERNS[-1]


def die(msg, code=1):
    sys.stderr.write(msg + "\n")
    sys.exit(code)


def norm(p):
    return p.lower() if sys.platform in ("darwin", "win32") else p


def canon(p):
    return os.path.realpath(p)


def inside(child, parent):
    c, p = norm(child), norm(parent)
    return c == p or c.startswith(p.rstrip(os.sep) + os.sep)


def same(a, b):
    if norm(a) == norm(b):
        return True
    try:
        return os.path.samefile(a, b)
    except OSError:
        return False


def passwd_home():
    try:
        return pwd.getpwuid(os.getuid()).pw_dir
    except KeyError:
        return ""


def real_homes(exclude=None):
    """Canonical list of directories that count as a real home. $HOME is
    included unless it IS the directory being examined and is not the passwd
    home (the documented HOME=<probe home> usage)."""
    homes = []
    ph = passwd_home()
    if ph:
        homes.append(canon(ph))
    h = os.environ.get("HOME", "")
    if h and os.path.isabs(h):
        ch = canon(h)
        if not (exclude is not None and same(ch, exclude) and not (ph and same(ch, canon(ph)))):
            homes.append(ch)
    for x in os.environ.get("BALLAST_PROBE_EXTRA_PROTECTED", "").split(os.pathsep):
        if x:
            homes.append(canon(x))
    out = []
    for h in homes:
        if h not in out:
            out.append(h)
    return out


def guard(label, d, fresh=True):
    """Return None when d is an acceptable probe directory, else the reason."""
    if not d:
        return "%s 未设置" % label
    if not os.path.isabs(d):
        return "%s 必须是绝对路径: %s" % (label, d)
    if not os.path.isdir(d):
        return "%s 不是已存在的目录: %s" % (label, d)
    c = canon(d)
    if c == os.sep:
        return "%s 是根目录" % label
    for h in real_homes(exclude=c):
        if same(c, h):
            return "%s 是真实家目录" % label
        if inside(h, c):
            return "%s 包含真实家目录" % label
        for s in PROTECTED_SUB:
            for ps in {os.path.join(h, s), canon(os.path.join(h, s))}:
                if same(c, ps) or inside(c, ps):
                    return "%s 位于真实家目录的受保护路径 %s 内" % (label, s)
    for s in SYSTEM_DIRS:
        for sc in {s, canon(s)}:
            if inside(c, sc):
                return "%s 位于系统路径 %s 内" % (label, s)
    if fresh:
        entries = os.listdir(c)
        if entries and MARKER not in entries:
            return "%s 非空且不是由本探针创建的(缺少 %s 标记)" % (label, MARKER)
        if MARKER in entries:
            import stat
            if not stat.S_ISREG(os.lstat(os.path.join(c, MARKER)).st_mode):
                return "%s 的 %s 标记不是普通文件(符号链接或其他类型)" % (label, MARKER)
    return None


def disjoint(a, b):
    ca, cb = canon(a), canon(b)
    if same(ca, cb) or inside(ca, cb) or inside(cb, ca):
        return "两个目录不能相同或互相包含: %s / %s" % (a, b)
    return None


def claim(d):
    r = guard("probe dir", d)
    if r:
        die("refuse: " + r)
    write_nofollow(os.path.join(d, MARKER), "created by scripts/probe-clients; safe to delete\n")


def snake(ev):
    return re.sub(r"(?<!^)(?=[A-Z])", "_", ev).lower()


def trust_hash(event, command, timeout):
    """Codex hook trust identity (rust-v0.130.0 .. rust-v0.163.0, see
    docs/facts/codex-static.md): sha256 of the key-sorted compact JSON of the
    normalised {event_name, hooks:[handler]} group."""
    doc = {"event_name": event,
           "hooks": [{"type": "command", "command": command,
                      "timeout": timeout, "async": False}]}
    s = json.dumps(doc, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return "sha256:" + hashlib.sha256(s.encode("utf-8")).hexdigest()


def write_nofollow(path, text):
    """Create or truncate path without following a symlink planted there
    (O_NOFOLLOW; where the platform lacks it the plain open is all we have)."""
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC | getattr(os, "O_NOFOLLOW", 0)
    try:
        fd = os.open(path, flags, 0o644)
    except OSError as e:
        die("refuse: cannot write %s without following links: %s" % (os.path.basename(path), e.strerror))
    with os.fdopen(fd, "w") as f:
        f.write(text)


def write_atomic(path, text):
    tmp = path + ".probe-tmp"
    write_nofollow(tmp, text)
    os.replace(tmp, path)


def load_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except FileNotFoundError:
        return default


def codex_merge(path, cmds):
    doc = load_json(path, {})
    hooks = doc.setdefault("hooks", {})
    for ev in sorted(cmds):
        cmd = cmds[ev]
        groups = hooks.setdefault(ev, [])
        have = any(h.get("command") == cmd for g in groups for h in g.get("hooks", []))
        if not have:
            groups.append({"hooks": [{"type": "command", "command": cmd, "timeout": 30}]})
    write_atomic(path, json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


def toml_key(k):
    if "'" not in k and "\n" not in k:
        return "'" + k + "'"
    esc = k.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")
    return '"' + esc + '"'


def codex_trust(config, hooks_json, marker):
    """Append [hooks.state.<key>] tables that trust the probe's own entries."""
    doc = load_json(hooks_json, {})
    text = ""
    if os.path.exists(config):
        with open(config) as f:
            text = f.read()
    added = 0
    for ev, groups in sorted(doc.get("hooks", {}).items()):
        for g, grp in enumerate(groups):
            for h, hd in enumerate(grp.get("hooks", [])):
                cmd = hd.get("command", "")
                if hd.get("type") != "command" or marker not in cmd:
                    continue
                key = "%s:%s:%d:%d" % (hooks_json, snake(ev), g, h)
                header = "[hooks.state.%s]" % toml_key(key)
                if header in text:
                    continue
                if text and not text.endswith("\n"):
                    text += "\n"
                text += "\n%s\nenabled = true\ntrusted_hash = \"%s\"\n" % (
                    header, trust_hash(snake(ev), cmd, hd.get("timeout", 600)))
                added += 1
    write_atomic(config, text)
    return added


def probe_cmds(hook, events):
    import shlex
    return {ev: "%s %s" % (shlex.quote(hook), ev) for ev in events}


def trust_audit(config, hooks_json):
    """Cross-check every command handler of hooks.json against the
    [hooks.state.*] tables of config.toml. Returns (ok, missing, mismatch)."""
    doc = load_json(hooks_json, {})
    tables = {}
    cur = None
    if os.path.exists(config):
        with open(config) as f:
            for line in f:
                line = line.strip()
                m = re.match(r"^\[hooks\.state\.(.+)\]$", line)
                if m:
                    k = m.group(1)
                    if k.startswith("'") and k.endswith("'"):
                        k = k[1:-1]
                    elif k.startswith('"') and k.endswith('"'):
                        k = json.loads(k)
                    cur = k
                    tables[cur] = None
                    continue
                if line.startswith("["):
                    cur = None
                    continue
                m = re.match(r'^trusted_hash\s*=\s*"(.*)"$', line)
                if m and cur is not None:
                    tables[cur] = m.group(1)
    ok = missing = mismatch = 0
    for ev, groups in sorted(doc.get("hooks", {}).items()):
        for g, grp in enumerate(groups):
            for h, hd in enumerate(grp.get("hooks", [])):
                if hd.get("type") != "command":
                    continue
                key = "%s:%s:%d:%d" % (hooks_json, snake(ev), g, h)
                want = trust_hash(snake(ev), hd.get("command", ""), hd.get("timeout", 600))
                if key not in tables or tables[key] is None:
                    missing += 1
                elif tables[key] != want:
                    mismatch += 1
                else:
                    ok += 1
    return ok, missing, mismatch


def cursor_merge(path, cmds, loop_limit_events):
    doc = load_json(path, {})
    doc.setdefault("version", 1)
    hooks = doc.setdefault("hooks", {})
    for ev in sorted(cmds):
        lst = hooks.setdefault(ev, [])
        if not any(e.get("command") == cmds[ev] for e in lst):
            ent = {"command": cmds[ev]}
            if ev in loop_limit_events:
                ent["loop_limit"] = 10
            lst.append(ent)
    write_atomic(path, json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


# ----------------------------------------------------------- probe hook ---

HOOK_COMMON = r"""#!/bin/sh
# Ballast client probe hook (client=__CLIENT__): records its stdin under
# $P/out/<current case>/ and, for some cases, answers with a scripted decision.
# Record-only otherwise: empty stdout, exit 0.
P='__PROBE__'
EV=${1:-}
input=$(cat)
case_id=$(cat "$P/current-case" 2>/dev/null) || case_id=none
[ -n "$case_id" ] || case_id=none
if [ -z "$EV" ]; then
  EV=$(printf '%s' "$input" | sed -n 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"\([A-Za-z_]*\)".*/\1/p' | head -n 1)
fi
[ -n "$EV" ] || EV=unknown
d="$P/out/$case_id"
mkdir -p "$d" "$P/state"
if ts=$(python3 -I -c 'import time;print(time.time_ns())' 2>/dev/null) && [ -n "$ts" ]; then :; else ts="$(date +%s)$$"; fi
printf '%s\n' "$input" >"$d/$ts-$EV.json"
has() { printf '%s' "$input" | grep -q -- "$1"; }
once() { mkdir "$P/state/$1" 2>/dev/null; }
case "$case_id:$EV" in
"""

HOOK_CODEX = r"""session-start:SessionStart)
  printf '%s' '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"The session probe token is BALLAST_PROBE_SESSION_9K4M."}}' ;;
userprompt-context:UserPromptSubmit)
  printf '%s' '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"The user-prompt probe token is BALLAST_PROBE_UPS_5R8D."}}' ;;
pre-deny-json:PreToolUse)
  has denied_json && printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"ballast probe deny json"}}' ;;
pre-deny-legacy:PreToolUse)
  has denied_legacy && printf '%s' '{"decision":"block","reason":"ballast probe legacy block"}' ;;
pre-deny-exit2:PreToolUse)
  if has denied_exit2; then printf '%s\n' 'ballast probe exit2 block' >&2; exit 2; fi ;;
pre-exit1-deny-stdout:PreToolUse)
  if has exit1_deny; then
    printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"ballast probe exit1 deny"}}'
    exit 1
  fi ;;
permission-deny:PermissionRequest)
  printf '%s' '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"ballast probe permission deny"}}}' ;;
stop-block:Stop)
  once stop-block && printf '%s' '{"decision":"block","reason":"ballast probe: reply with the single word AGAIN"}' ;;
"""

HOOK_CURSOR = r"""session-start:sessionStart)
  printf '%s' '{"additional_context":"The session probe token is BALLAST_PROBE_SESSION_9K4M."}' ;;
prompt-context:beforeSubmitPrompt)
  printf '%s' '{"continue":true,"additional_context":"The prompt probe token A is BALLAST_PROBE_PA_3M7Q.","hookSpecificOutput":{"hookEventName":"beforeSubmitPrompt","additionalContext":"The prompt probe token B is BALLAST_PROBE_PB_8W2L."}}' ;;
shell-deny:beforeShellExecution)
  has denied_shell && printf '%s' '{"permission":"deny","user_message":"ballast probe user message","agent_message":"ballast probe agent message"}' ;;
pretool-deny:preToolUse)
  has denied_pre && printf '%s' '{"permission":"deny","user_message":"ballast probe user message","agent_message":"ballast probe agent message"}' ;;
read-deny:beforeReadFile)
  has SECRET_PROBE && printf '%s' '{"permission":"deny","user_message":"ballast probe user message","agent_message":"ballast probe agent message"}' ;;
stop-followup:stop)
  once stop-followup && printf '%s' '{"followup_message":"ballast probe: reply with the single word AGAIN"}' ;;
"""

HOOK_TAIL = r"""*) : ;;
esac
exit 0
"""


def render_hook(client, probe):
    body = {"codex": HOOK_CODEX, "cursor": HOOK_CURSOR}[client]
    text = HOOK_COMMON + body + HOOK_TAIL
    return text.replace("__CLIENT__", client).replace("__PROBE__", probe.replace("'", "'\\''"))


# ------------------------------------------------------------- case tables ---

LAUNCH_NOTE = "{LAUNCH}"

CODEX_CASES = [
    dict(id="session-start", title="启动与 SessionStart 注入",
         steps=["在新终端按上面的 LAUNCH 命令启动 Codex TUI(工作目录为 {WORK}),在临时 CODEX_HOME 下自行登录(脚本不读取凭据;客户端可能把凭据存进系统钥匙串【未核验】,取证结束后请自行登出)。",
                "若问是否信任该目录,选择信任。"],
         prompt="What is the session probe token you were given? Reply with the token only.",
         need=["SessionStart", "UserPromptSubmit", "Stop"],
         question=("模型回复了 BALLAST_PROBE_SESSION_9K4M 吗?", "y"),
         assertion="SessionStart 的 additionalContext 到达模型;payload 含 session_id/cwd/model/permission_mode/source/transcript_path"),
    dict(id="hooks-trust", title="/hooks 信任状态",
         steps=["在 TUI 中输入 /hooks,查看 Ballast 的 6 个 hook 与探针 hook 的状态。"],
         prompt=None, need=[],
         question=("所有 hook 都显示为已受信/已启用,且没有要求手动批准吗?", "y"),
         assertion="--codex-trust 写入的 Ballast 信任哈希与脚本为探针 hook 计算的哈希都被该 Codex 版本接受(哈希算法与 key 格式有效)"),
    dict(id="bash-ok", title="Bash 成功",
         steps=[], prompt="Run the shell command `echo ballast-probe-ok` and show me its output.",
         need=["UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"], tool=["Bash"],
         assertion="Bash 的 PreToolUse/PostToolUse 经同一 tool_name 与 tool_input.command 到达;记录 tool_response 形态"),
    dict(id="bash-fail", title="Bash 非 0 退出",
         steps=[], prompt="Run the shell command `sh -c 'echo probe-err >&2; exit 3'` and tell me its exit code.",
         need=["PreToolUse"], opt=["PostToolUse"], tool=["Bash"],
         assertion="记录非 0 退出时 PostToolUse 是否触发、tool_response 是否含数值退出码(静态依据: 字符串、无数值退出码)"),
    dict(id="apply-patch", title="apply_patch",
         steps=[], prompt="Use the apply_patch tool (not the shell) to create a file named probe_patch.txt in the current directory containing the single line hello.",
         need=["PreToolUse"], opt=["PostToolUse", "PermissionRequest"], tool=["apply_patch"],
         assertion="apply_patch 的 tool_name 与 tool_input 形状(静态依据: tool_name=apply_patch, tool_input.command=补丁全文)"),
    dict(id="pre-deny-json", title="PreToolUse deny(hookSpecificOutput)",
         steps=[], prompt="Run the shell command `touch denied_json.txt`.",
         need=["PreToolUse"], absent=["denied_json.txt"],
         assertion="permissionDecision=deny + 非空 permissionDecisionReason 阻止工具执行(工作目录里不出现 denied_json.txt)"),
    dict(id="pre-deny-legacy", title="PreToolUse deny(decision:block)",
         steps=[], prompt="Run the shell command `touch denied_legacy.txt`.",
         need=["PreToolUse"], absent=["denied_legacy.txt"],
         assertion="旧式 {decision:block, reason} 也阻止工具执行"),
    dict(id="pre-deny-exit2", title="PreToolUse exit 2",
         steps=[], prompt="Run the shell command `touch denied_exit2.txt`.",
         need=["PreToolUse"], absent=["denied_exit2.txt"],
         assertion="退出码 2 + stderr 原因阻止工具执行"),
    dict(id="pre-exit1-deny-stdout", title="PreToolUse exit 1 + deny stdout",
         steps=[], prompt="Run the shell command `touch exit1_deny.txt`.",
         need=["PreToolUse"], present=["exit1_deny.txt"],
         assertion="退出码 1 时不解析 stdout,工具照常执行(工作目录里出现 exit1_deny.txt);这是 Ballast 故障放行(exit 1 + 空 stdout)约定的前提"),
    dict(id="permission-record", title="PermissionRequest 记录",
         steps=["让 Codex 在执行非受信命令前询问: TUI 内 /approvals 或 /permissions 选最严格且会询问的模式(或带 -a untrusted 重新启动)。",
                "批准提示出现时选择拒绝。"],
         prompt="Run the shell command `touch perm_record.txt`.",
         need=["PreToolUse", "PermissionRequest"], absent=["perm_record.txt"],
         assertion="需要批准时触发 PermissionRequest,payload 的 tool_name/tool_input(含 description)形状"),
    dict(id="permission-deny", title="PermissionRequest deny",
         steps=["保持上一步的批准模式。"],
         prompt="Run the shell command `touch perm_deny.txt`.",
         need=["PermissionRequest"], absent=["perm_deny.txt"],
         question=("这次出现了批准提示吗?(预期 n: hook 直接拒绝,不弹提示)", "n"),
         assertion="hookSpecificOutput.decision.behavior=deny 直接拒绝,不再弹出批准提示"),
    dict(id="userprompt-context", title="UserPromptSubmit 注入",
         steps=[], prompt="Reply with the user-prompt probe token you were given, nothing else.",
         need=["UserPromptSubmit"],
         question=("模型回复了 BALLAST_PROBE_UPS_5R8D 吗?", "y"),
         assertion="UserPromptSubmit 的 additionalContext 到达模型"),
    dict(id="stop-block", title="Stop 续跑",
         steps=[], prompt="Reply with the single word: done",
         need=["Stop"], min_count={"Stop": 2},
         question=("模型在第一次回答后又继续回复了 AGAIN 吗?", "y"),
         assertion="Stop 的 decision:block + reason 让 agent 继续(续跑语义),第二次 Stop 的 stop_hook_active 为 true"),
    dict(id="exec-noninteractive", title="codex exec 是否触发 hook",
         steps=["另开终端,工作目录为 {WORK},用同一个临时 CODEX_HOME(已登录)运行:",
                "  {EXEC}"],
         prompt=None, need=[], observe_only=True,
         question=("命令正常执行完毕了吗?(n 表示因登录等原因没能运行)", None),
         assertion="记录非交互 codex exec 是否触发 hook(pinrule 称不触发;静态阅读未发现按交互与否的开关)"),
]

CURSOR_CASES = [
    dict(id="session-start", title="启动与 sessionStart 注入",
         steps=["在新终端按上面的 LAUNCH 命令启动 Cursor(IDE 需 Reload Window;CLI 在工作目录 {WORK} 下运行),在一次性 HOME 下自行登录(脚本不读取凭据;客户端可能把凭据存进系统钥匙串【未核验】,取证结束后请自行登出)。"],
         prompt="What is the session probe token you were given? Reply with the token only.",
         need=["sessionStart"],
         question=("模型回复了 BALLAST_PROBE_SESSION_9K4M 吗?", "y"),
         assertion="sessionStart 的 additional_context 到达模型;payload 字段(session_id/conversation_id/transcript_path 等)"),
    dict(id="hooks-loaded", title="hooks.json 被加载",
         steps=["在 Cursor 设置里查看 Hooks(或 Output 面板的 Hooks 日志),确认 Ballast 与探针的条目都已加载。"],
         prompt=None, need=[],
         question=("hooks 已加载,没有解析错误吗?", "y"),
         assertion="~/.cursor/hooks.json(version 1)的 Ballast 条目与探针条目被该 Cursor 版本接受"),
    dict(id="shell-ok", title="Shell 成功",
         steps=[], prompt="Run the shell command `echo ballast-probe-ok` and show me its output.",
         need=["beforeShellExecution"], opt=["preToolUse", "postToolUse", "afterAgentResponse", "stop", "beforeSubmitPrompt"],
         assertion="beforeShellExecution 的命令在 command 字段;记录 preToolUse/postToolUse 是否同时触发及 tool_name(假设: Shell)"),
    dict(id="shell-deny", title="beforeShellExecution deny",
         steps=[], prompt="Run the shell command `touch denied_shell.txt`.",
         need=["beforeShellExecution"], absent=["denied_shell.txt"],
         assertion="{permission:deny,user_message,agent_message} 阻止命令执行(工作目录里不出现 denied_shell.txt)"),
    dict(id="pretool-deny", title="preToolUse deny",
         steps=[], prompt="Run the shell command `touch denied_pre.txt`.",
         need=["preToolUse"], absent=["denied_pre.txt"],
         assertion="preToolUse 返回同一 deny 形状时阻止工具执行"),
    dict(id="read-file", title="beforeReadFile",
         steps=[], prompt="Read the file README_PROBE.txt in the current directory and tell me its first line.",
         need=["beforeReadFile"], opt=["preToolUse", "postToolUse"],
         assertion="beforeReadFile 的路径字段名(file_path 或 path)"),
    dict(id="read-deny", title="beforeReadFile deny",
         steps=[], prompt="Read the file SECRET_PROBE.txt in the current directory and print its first line.",
         need=["beforeReadFile"],
         question=("模型输出了 BALLAST_PROBE_SECRET_LINE 吗?(预期 n)", "n"),
         assertion="beforeReadFile deny 阻止读取,内容不进入模型"),
    dict(id="edit-file", title="编辑文件",
         steps=[], prompt="Create a file named probe_edit.txt in the current directory containing the single line hello.",
         need=["preToolUse"], opt=["postToolUse"],
         assertion="编辑走的 tool_name(假设: Write)与 tool_input 形状"),
    dict(id="prompt-context", title="beforeSubmitPrompt 注入",
         steps=[], prompt="Reply with the prompt probe tokens you were given, nothing else.",
         need=["beforeSubmitPrompt"],
         question=("模型回复里出现了哪些令牌? y = 只有 B(嵌套 hookSpecificOutput)或 A+B;n = 只有 A 或都没有", "y"),
         assertion="beforeSubmitPrompt 的注入通道: additional_context(A)与嵌套 hookSpecificOutput.additionalContext(B)哪个生效(Ballast 假设后者)"),
    dict(id="stop-followup", title="stop followup_message",
         steps=[], prompt="Reply with the single word: done",
         need=["stop", "afterAgentResponse"], min_count={"stop": 2},
         question=("agent 在第一次回答后又自动继续回复了 AGAIN 吗?", "y"),
         assertion="stop 返回 followup_message 让 agent 自动续跑(不能阻止停止),受 loop_limit 限制;afterAgentResponse 带 text"),
    dict(id="subagent", title="子 agent(Task)",
         steps=[], prompt="Use a subagent (the Task tool) to run the shell command `echo ballast-probe-sub` and report the output.",
         need=[], opt=["subagentStop", "preToolUse", "beforeShellExecution"],
         observe_only=True,
         assertion="记录子 agent 的 subagentStop 是否触发及 agent_id/subagent_id 字段、Task 的 tool_name"),
]

CASES = {"codex": CODEX_CASES, "cursor": CURSOR_CASES}
ID_KEY = re.compile(r"(^id$|_id$|Id$|^uuid$)")
TEXT_KEYS = {"last_assistant_message", "text", "user_message", "agent_message"}
# keys that carry the logged-in account: user_email, email, account, *_email, *_account
ACCOUNT_KEY = re.compile(r"(?i)(^|[_-])(e-?mail|account)$|^(e-?mail|account)[_-]")


def case_by_id(client, cid):
    for c in CASES[client]:
        if c["id"] == cid:
            return c
    return None


def expand(s, env):
    for k, v in env.items():
        s = s.replace("{" + k + "}", v)
    return s


def show(client, cid):
    c = case_by_id(client, cid)
    if not c:
        die("unknown case: " + cid)
    env = {"WORK": os.environ.get("PC_WORK", "<work>"),
           "LAUNCH": os.environ.get("PC_LAUNCH", "<launch>"),
           "EXEC": os.environ.get("PC_EXEC", "<exec>")}
    lines = ["== 用例 %s: %s" % (c["id"], c["title"])]
    for s in c.get("steps", []):
        lines.append("  - " + expand(s, env))
    if c.get("prompt"):
        lines.append("  在客户端中输入(原文):")
        lines.append("    " + c["prompt"])
    print("\n".join(lines))


def question(client, cid):
    c = case_by_id(client, cid)
    q = c.get("question") if c else None
    print(q[0] if q else "")


def ids(client):
    print("\n".join(c["id"] for c in CASES[client]))


# ------------------------------------------------------------- collection ---

class Sanitizer:
    def __init__(self, repls):
        seen = set()
        self.repls = []
        for old, new in sorted(repls, key=lambda r: -len(r[0])):
            if old and old not in seen and old != os.sep:
                seen.add(old)
                self.repls.append((old, new))
        self.ids = {}
        self.user = ""
        try:
            self.user = getpass.getuser()
        except Exception:
            pass
        # host names that identify this machine (full name and first label)
        self.hosts = []
        try:
            full = socket.gethostname()
        except Exception:
            full = ""
        for h in (full, full.split(".")[0]):
            if len(h) >= 3 and h.lower() not in ("localhost", "localdomain") and h not in self.hosts:
                self.hosts.append(h)

    def text(self, s):
        for old, new in self.repls:
            s = s.replace(old, new)
        return s

    def walk(self, obj, key=None, prompts=()):
        if isinstance(obj, dict):
            return {k: self.walk(v, k, prompts) for k, v in obj.items()}
        if isinstance(obj, list):
            return [self.walk(v, key, prompts) for v in obj]
        if isinstance(obj, str):
            if key == "transcript_path":
                return "<transcript-path>"
            if key and ACCOUNT_KEY.search(key):
                return "<%s>" % re.sub(r"[^a-z0-9_]", "_", key.lower())
            if key and ID_KEY.search(key):
                m = self.ids.setdefault((key, obj), "<%s-%d>" % (key, 1 + sum(1 for k in self.ids if k[0] == key)))
                return m
            if key in TEXT_KEYS:
                return "<redacted len=%d>" % len(obj)
            if key == "prompt" and obj not in prompts:
                return "<redacted len=%d>" % len(obj)
            return EMAIL_RE.sub("<email>", self.text(obj))
        return obj

    def check(self, text):
        """Return the reason a sanitised document must not be written, or None."""
        for p in SECRET_PATTERNS:
            if p.search(text):
                return "matches secret pattern " + p.pattern[:24]
        for p in HYGIENE_PATTERNS:
            if p.search(text):
                return "matches hygiene pattern " + p.pattern[:24]
        if self.user and len(self.user) >= 3 and re.search(r"(?<![A-Za-z0-9_])" + re.escape(self.user) + r"(?![A-Za-z0-9_])", text):
            return "contains the local user name"
        for h in self.hosts:
            if re.search(r"(?<![A-Za-z0-9_-])" + re.escape(h) + r"(?![A-Za-z0-9_-])", text, re.IGNORECASE):
                return "contains the local host name"
        return None


def event_of(path, obj):
    ev = obj.get("hook_event_name") if isinstance(obj, dict) else None
    if ev:
        return ev
    m = re.match(r"^[0-9]+-(.+)\.json$", os.path.basename(path))
    return m.group(1) if m else "unknown"


def summarize(payloads):
    keys = collections.OrderedDict()
    tools = collections.OrderedDict()
    resp = collections.OrderedDict()
    order = []
    for ev, obj in payloads:
        order.append(ev)
        if isinstance(obj, dict):
            keys.setdefault(ev, set()).update(obj.keys())
            tn = obj.get("tool_name")
            if isinstance(tn, str):
                tools.setdefault(ev, set()).add(tn)
                ti = obj.get("tool_input")
                if isinstance(ti, dict):
                    keys.setdefault("%s.tool_input(%s)" % (ev, tn), set()).update(ti.keys())
            tr = obj.get("tool_response")
            if tr is not None:
                resp.setdefault(ev, set()).add(type(tr).__name__ + (":" + ",".join(sorted(tr)) if isinstance(tr, dict) else ""))
    return order, {k: sorted(v) for k, v in keys.items()}, tools, resp


def collect(client, probe, facts_dir, work, version, rel_recheck):
    cases = CASES[client]
    outdir = os.path.join(probe, "out")
    answers = {}
    ap = os.path.join(probe, "answers.tsv")
    if os.path.exists(ap):
        with open(ap) as f:
            for line in f:
                parts = line.rstrip("\n").split("\t")
                if len(parts) >= 2:
                    answers[parts[0]] = parts[1]
    repls = []
    for raw, tag in ((work, "<work>"), (probe, "<probe-root>"),
                     (os.environ.get("PC_CLIENT_HOME", ""), "<client-home>"),
                     (os.environ.get("PC_BALLAST_HOME", ""), "<ballast-home>")):
        if raw:
            repls.append((raw, tag))
            repls.append((canon(raw), tag))
    for h in real_homes():
        repls.append((h, "<home>"))
    san = Sanitizer(repls)
    prompts = {c["prompt"] for c in cases if c.get("prompt")}
    payload_root = os.path.join(facts_dir, "payloads", client)
    results = []
    anything = False
    for c in cases:
        cid = c["id"]
        d = os.path.join(outdir, cid)
        files = sorted(f for f in os.listdir(d) if f.endswith(".json")) if os.path.isdir(d) else []
        files.sort(key=lambda f: (len(f.split("-", 1)[0]), f))
        payloads, bad, held = [], [], []
        for f in files:
            p = os.path.join(d, f)
            try:
                with open(p) as fh:
                    obj = json.load(fh)
            except ValueError:
                bad.append(f)
                continue
            payloads.append((event_of(p, obj), obj))
        san_docs = []
        for i, (ev, obj) in enumerate(payloads, 1):
            clean = san.walk(obj, None, prompts)
            text = json.dumps(clean, indent=2, ensure_ascii=False) + "\n"
            why = san.check(text)
            if why:
                held.append("%02d-%s: %s" % (i, ev, why))
            else:
                san_docs.append(("%02d-%s.json" % (i, ev), text))
        order, keys, tools, resp = summarize(payloads)
        obs_events = []
        for e in order:
            if e not in obs_events:
                obs_events.append(e)
        counts = collections.Counter(order)
        rec = collections.OrderedDict()
        rec["case_id"] = cid
        rec["events"] = obs_events
        rec["schema_keys"] = keys
        rec["assertion"] = c["assertion"]
        problems, missing = [], []
        for e in c.get("need", []):
            if e not in obs_events:
                missing.append(e)
        for e, n in c.get("min_count", {}).items():
            if counts.get(e, 0) < n:
                missing.append("%s x%d" % (e, n))
        if c.get("tool"):
            seen = set().union(*tools.values()) if tools else set()
            if not (seen & set(c["tool"])):
                missing.append("tool_name in %s" % "/".join(c["tool"]))
        for fn in c.get("absent", []):
            if os.path.exists(os.path.join(work, fn)):
                problems.append("%s 存在: 拦截未生效" % fn)
        for fn in c.get("present", []):
            if not os.path.exists(os.path.join(work, fn)):
                problems.append("%s 不存在: 工具被阻止了" % fn)
        ans = answers.get(cid)
        q = c.get("question")
        if q and ans is not None and q[1] is not None and ans != q[1]:
            problems.append("用户回答 %s,预期 %s" % (ans, q[1]))
        if q and ans is None and payloads:
            missing.append("用户回答")
        notes = []
        if order:
            notes.append("触发事件(按序): " + ",".join(order))
        for ev, names in tools.items():
            notes.append("%s.tool_name=%s" % (ev, "/".join(sorted(names))))
        for ev, forms in resp.items():
            notes.append("%s.tool_response=%s" % (ev, "/".join(sorted(forms))))
        if c.get("opt"):
            absent_opt = [e for e in c["opt"] if e not in obs_events]
            if absent_opt:
                notes.append("未触发(可选): " + ",".join(absent_opt))
        if q and ans is not None:
            notes.append("用户回答 %s: %s" % (ans, q[0]))
        if bad:
            notes.append("无法解析的 payload: " + ",".join(bad))
        if held:
            notes.append("已扣留(不入库): " + "; ".join(held))
        if problems:
            notes.append("偏差: " + "; ".join(problems))
        if missing:
            notes.append("缺少: " + ", ".join(missing))
        has_data = bool(payloads) or ans is not None
        if not has_data:
            result = "needs_credentials"
        elif held and not san_docs:
            result = "unverifiable"
        elif c.get("observe_only"):
            result = "unverifiable" if ans == "n" or (not payloads and ans is None) else "pass"
            if c["id"] == "exec-noninteractive":
                notes.append("hook 在 codex exec 下%s" % ("触发" if payloads else "未触发"))
        elif missing:
            result = "unverifiable"
        elif problems:
            result = "fail"
        else:
            result = "pass"
        if has_data:
            anything = True
        rec["result"] = result
        rec["mode"] = "credentials"
        rec["%s_version" % client] = version
        rec["observation"] = ";".join(notes) if notes else "未运行"
        rec["evidence"] = ["payloads/%s/%s/" % (client, cid)] if san_docs else []
        if result == "needs_credentials":
            rec["recheck"] = rel_recheck
        results.append((rec, san_docs))
    if not anything:
        return None
    # Render and scan the summary first: a refusal here must leave docs/facts
    # exactly as it was (no payload deleted, none rewritten).
    out = os.path.join(facts_dir, "%s-cases.json" % client)
    text = json.dumps([r for r, _ in results], indent=1, ensure_ascii=False) + "\n"
    why = san.check(text)
    if why:
        die("refuse to write %s: %s (nothing was changed)" % (out, why), 3)
    for rec, docs in results:
        cdir = os.path.join(payload_root, rec["case_id"])
        if os.path.isdir(cdir):
            for f in os.listdir(cdir):
                if re.match(r"^[0-9]{2}-.+\.json$", f):
                    os.remove(os.path.join(cdir, f))
        if docs:
            os.makedirs(cdir, exist_ok=True)
            for name, text_doc in docs:
                with open(os.path.join(cdir, name), "w") as f:
                    f.write(text_doc)
    os.makedirs(facts_dir, exist_ok=True)
    write_atomic(out, text)
    return out, collections.Counter(r["result"] for r, _ in results)


def main(argv):
    cmd = argv[1] if len(argv) > 1 else ""
    a = argv[2:]
    if cmd == "realhome":
        print(passwd_home())
    elif cmd == "canon":
        print(canon(a[0]))
    elif cmd == "guard":
        r = guard(a[0], a[1], fresh=(len(a) < 3 or a[2] == "fresh"))
        if r:
            die("refuse: " + r)
    elif cmd == "disjoint":
        r = disjoint(a[0], a[1])
        if r:
            die("refuse: " + r)
    elif cmd == "claim":
        claim(a[0])
    elif cmd == "trusthash":
        print(trust_hash(a[0], a[1], int(a[2])))
    elif cmd == "codex_merge":
        codex_merge(a[0], json.loads(a[1]))
    elif cmd == "codex_trust":
        print(codex_trust(a[0], a[1], a[2]))
    elif cmd == "probe_cmds":
        print(json.dumps(probe_cmds(a[0], a[1:])))
    elif cmd == "trust_audit":
        print("ok=%d missing=%d mismatch=%d" % trust_audit(a[0], a[1]))
    elif cmd == "cursor_merge":
        cursor_merge(a[0], json.loads(a[1]), set(json.loads(a[2])))
    elif cmd == "render_hook":
        sys.stdout.write(render_hook(a[0], a[1]))
    elif cmd == "ids":
        ids(a[0])
    elif cmd == "show":
        show(a[0], a[1])
    elif cmd == "question":
        question(a[0], a[1])
    elif cmd == "has_case":
        sys.exit(0 if case_by_id(a[0], a[1]) else 1)
    elif cmd == "collect":
        r = collect(a[0], a[1], a[2], a[3], a[4], a[5])
        if r is None:
            sys.exit(4)
        print("已写入 %s" % r[0])
        print("结果统计: " + ", ".join("%s=%d" % kv for kv in sorted(r[1].items())))
    elif cmd == "check_text":
        # exit 1 when the file would fail the secret / hygiene scan
        with open(a[0]) as f:
            t = f.read()
        s = Sanitizer([])
        why = s.check(t)
        if why:
            die(why)
    else:
        die("pclib: unknown command %r" % cmd, 2)


if __name__ == "__main__":
    main(sys.argv)
PYEOF
}

# ------------------------------------------------------------ bootstrap ---

# pc_init: private temp dir + Python module. Call once, first.
pc_init() {
  command -v python3 >/dev/null 2>&1 || pc_die "需要 python3(用于路径规范化、JSON 与脱敏)"
  PC_TMP="$(mktemp -d "${TMPDIR:-/tmp}/ballast-probe-lib.XXXXXX")"
  PC_PY="$PC_TMP/pclib.py"
  pc_write_py
  # The probe scripts never need credentials: drop them from our own
  # environment so no child process can inherit one.
  unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN OPENAI_API_KEY CODEX_API_KEY \
    CURSOR_API_KEY GITHUB_TOKEN GH_TOKEN 2>/dev/null || true
}

# pc_py ARGS...: run a pclib command.
pc_py() { python3 -I "$PC_PY" "$@"; }

# ----------------------------------------------------------------- guards ---

# pc_realhome: the passwd home of the current user, immune to $HOME overrides.
pc_realhome() { pc_py realhome; }

# pc_guard LABEL DIR [fresh|nofresh]: refuse (exit 2) unless DIR is a safe,
# existing, empty-or-marked probe directory.
pc_guard() {
  local msg
  if ! msg="$(pc_py guard "$@" 2>&1)"; then
    printf '%s\n' "$msg" >&2
    exit 2
  fi
}

# pc_guard_quiet LABEL DIR [mode]: like pc_guard but returns 1 instead of exiting.
pc_guard_quiet() { pc_py guard "$@" >/dev/null 2>&1; }

pc_disjoint() {
  local msg
  if ! msg="$(pc_py disjoint "$1" "$2" 2>&1)"; then
    printf '%s\n' "$msg" >&2
    exit 2
  fi
}

# pc_claim DIR: write the .ballast-probe marker (re-checks the guard first).
pc_claim() { pc_py claim "$1" || exit 2; }

# pc_canon PATH: symlink-resolved absolute path.
pc_canon() { pc_py canon "$1"; }

# ------------------------------------------------------- ballast handling ---

# pc_resolve_ballast OUTDIR: sets PC_BALLAST (absolute path). Reuses
# BALLAST_BIN when given, otherwise builds ./cmd/ballast into OUTDIR/bin.
pc_resolve_ballast() {
  local outdir=$1
  if [ -n "${BALLAST_BIN:-}" ]; then
    case "$BALLAST_BIN" in
      /*) ;;
      *) pc_die "BALLAST_BIN 必须是绝对路径" ;;
    esac
    [ -x "$BALLAST_BIN" ] || pc_die "BALLAST_BIN 不可执行: $BALLAST_BIN"
    PC_BALLAST="$BALLAST_BIN"
    return 0
  fi
  command -v go >/dev/null 2>&1 || pc_die "未找到 go,无法构建 ballast;请设置 BALLAST_BIN=<已构建二进制的绝对路径>"
  mkdir -p "$outdir/bin"
  (cd "$PC_REPO_ROOT" && go build -o "$outdir/bin/ballast" ./cmd/ballast) || pc_die "构建 ballast 失败"
  PC_BALLAST="$outdir/bin/ballast"
}

# pc_run_ballast SANDBOX BALLAST_HOME [VAR=VAL...] -- ARGS...
# Runs ballast with every home-like variable redirected into SANDBOX / the
# probe directories; nothing of the real environment leaks in.
pc_run_ballast() {
  local sandbox=$1 bhome=$2
  shift 2
  local extra=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
    extra+=("$1")
    shift
  done
  [ "$#" -gt 0 ] && shift
  mkdir -p "$sandbox"
  env \
    HOME="$sandbox" USERPROFILE="$sandbox" \
    BALLAST_HOME="$bhome" BALLAST_INSTALL_ROOT="$sandbox" \
    BALLAST_MANAGED_DIR="$sandbox/managed" \
    CLAUDE_CONFIG_DIR="$sandbox/.claude" CODEX_HOME="$sandbox/.codex" \
    HERMES_HOME="$sandbox/.hermes" \
    XDG_CONFIG_HOME="$sandbox/.config" XDG_DATA_HOME="$sandbox/.local/share" \
    XDG_STATE_HOME="$sandbox/.local/state" XDG_CACHE_HOME="$sandbox/.cache" \
    GIT_CONFIG_GLOBAL="$sandbox/gitconfig" GIT_CONFIG_NOSYSTEM=1 \
    ${extra[@]+"${extra[@]}"} \
    "$PC_BALLAST" "$@"
}

# ------------------------------------------------------------- probe hook ---

# pc_write_hook CLIENT PROBEDIR: writes PROBEDIR/hook.sh (mode 755).
pc_write_hook() {
  pc_py render_hook "$1" "$2" >"$2/hook.sh"
  chmod 755 "$2/hook.sh"
}

# pc_q STRING: single-quote STRING for a POSIX shell (used in printed commands).
pc_q() {
  local s=$1
  s=${s//\'/\'\\\'\'}
  printf "'%s'" "$s"
}

# ------------------------------------------------- generic probe commands ---
#
# Contract with the calling script (codex.sh / cursor.sh): it defines
#   setup_env      validates and claims the throwaway dirs, then sets
#                  PC_CLIENT (codex|cursor), PROBE, WORK, FACTS, VERSION,
#                  RECHECK, LAUNCH, EXEC, CLIENT_HOME, BH
#   cmd_prepare    installs Ballast and the probe hook
# and may call the pc_cmd_* functions below.

pc_need_prepared() {
  [ -x "$PROBE/hook.sh" ] || pc_die "尚未 prepare: 先运行 $0 prepare"
}

# --------------------------------------------------------------- commands ---

pc_cmd_checklist() {
  local id
  export PC_WORK="${WORK:-<work>}" PC_LAUNCH="${LAUNCH:-<launch>}" PC_EXEC="${EXEC:-<exec>}"
  for id in $(pc_py ids "$PC_CLIENT"); do
    pc_py show "$PC_CLIENT" "$id"
  done
}

pc_cmd_mark() {
  local id=${1:-}
  [ -n "$id" ] || pc_die "mark 需要用例 ID"
  pc_py has_case "$PC_CLIENT" "$id" || pc_die "未知用例: $id(见 checklist)"
  setup_env
  pc_need_prepared
  rm -rf "${PROBE:?}/state/$id"
  printf '%s\n' "$id" >"$PROBE/current-case"
}

pc_cmd_answer() {
  local id=${1:-} ans=${2:-}
  [ -n "$id" ] || pc_die "answer 需要用例 ID"
  case "$ans" in y | n) ;; *) pc_die "回答必须是 y 或 n" ;; esac
  pc_py has_case "$PC_CLIENT" "$id" || pc_die "未知用例: $id"
  setup_env
  pc_need_prepared
  local f="$PROBE/answers.tsv" tmp
  tmp="$PROBE/answers.tsv.tmp"
  if [ -f "$f" ]; then
    grep -v "^$id	" "$f" >"$tmp" || true
  else
    : >"$tmp"
  fi
  printf '%s\t%s\n' "$id" "$ans" >>"$tmp"
  mv "$tmp" "$f"
}

pc_cmd_collect() {
  setup_env
  pc_need_prepared
  export PC_CLIENT_HOME="$CLIENT_HOME" PC_BALLAST_HOME="$BH"
  local rc=0
  pc_py collect "$PC_CLIENT" "$PROBE" "$FACTS" "$WORK" "$VERSION" "$RECHECK" || rc=$?
  if [ "$rc" -eq 4 ]; then
    pc_say "没有记录到任何 payload 或回答: 不生成 $PC_CLIENT-cases.json,$PC_CLIENT 各单元格保持【未核验】。"
    pc_say "补测命令: $RECHECK"
    return 4
  fi
  return "$rc"
}

# ask_tty PROMPT: read one line from the controlling terminal.
ask_tty() {
  local a
  printf '%s' "$1" >/dev/tty
  read -r a </dev/tty || a=""
  printf '%s' "$a"
}

pc_cmd_run() {
  cmd_prepare
  if ! { : </dev/tty; } 2>/dev/null; then
    pc_say ""
    pc_say "当前没有可交互的终端。手动推进: 对每个用例依次执行"
    pc_say "  $0 mark <用例>   # 然后在客户端里输入清单里的提示"
    pc_say "  $0 answer <用例> y|n   # 有问题的用例"
    pc_say "最后执行 $0 collect。清单见: $0 checklist"
    return 0
  fi
  export PC_WORK="$WORK" PC_LAUNCH="$LAUNCH" PC_EXEC="$EXEC"
  local id q a
  for id in $(pc_py ids "$PC_CLIENT"); do
    pc_cmd_mark "$id"
    pc_say ""
    pc_py show "$PC_CLIENT" "$id"
    a="$(ask_tty "完成后按回车(输入 s 跳过该用例): ")"
    if [ "$a" = "s" ]; then continue; fi
    q="$(pc_py question "$PC_CLIENT" "$id")"
    if [ -n "$q" ]; then
      a="$(ask_tty "$q (y/n): ")"
      case "$a" in y | n) pc_cmd_answer "$id" "$a" ;; *) pc_say "  未回答,按未回答记录" ;; esac
    fi
  done
  printf '%s\n' none >"$PROBE/current-case"
  pc_say ""
  pc_cmd_collect
}


# ----------------------------------------------------- self-test plumbing ---

pc_t_ok() {
  PC_T_PASS=$((PC_T_PASS + 1))
  printf 'ok   %s\n' "$1"
}
pc_t_bad() {
  PC_T_FAIL=$((PC_T_FAIL + 1))
  printf 'FAIL %s\n' "$1"
  if [ "$#" -gt 1 ]; then
    shift
    printf '     %s\n' "$@"
  fi
}
pc_t_skip() {
  PC_T_SKIP=$((PC_T_SKIP + 1))
  printf 'SKIP %s: %s\n' "$1" "$2"
}
pc_t_eq() { # NAME GOT WANT
  if [ "$2" = "$3" ]; then pc_t_ok "$1"; else pc_t_bad "$1" "got:  $2" "want: $3"; fi
}
# pc_t_refuses NAME DIR [mode]: the guard must refuse DIR.
pc_t_refuses() {
  local name=$1 dir=$2 mode=${3:-fresh} msg
  if msg="$(pc_py guard probe "$dir" "$mode" 2>&1)"; then
    pc_t_bad "拒绝: $name" "守卫放行了 $dir"
  else
    case "$msg" in
      refuse:*) pc_t_ok "拒绝: $name" ;;
      *) pc_t_bad "拒绝: $name" "输出不是 refuse: $msg" ;;
    esac
  fi
}
pc_t_accepts() { # NAME DIR [mode]
  local name=$1 dir=$2 mode=${3:-fresh} msg
  if msg="$(pc_py guard probe "$dir" "$mode" 2>&1)"; then
    pc_t_ok "放行: $name"
  else
    pc_t_bad "放行: $name" "$msg"
  fi
}

# pc_realdirs_refused DIR...: precondition of the self-tests that hand a REAL
# directory to `prepare` and expect a refusal: the guard itself must refuse
# each DIR first (checked without writing anything). If it does not, the guard
# regressed and running `prepare` would write a marker into the real home, so
# the self-test records a failure and the caller must stop before that.
pc_realdirs_refused() {
  local d ok=1
  for d in "$@"; do
    if pc_py guard probe "$d" fresh >/dev/null 2>&1; then
      pc_t_eq "前置条件: 守卫拒绝真实目录 $(basename "$d")" "accepted" "refused"
      ok=0
    fi
  done
  [ "$ok" = 1 ] && pc_t_eq "前置条件: 守卫在 prepare 之前就拒绝全部真实目录" "refused" "refused"
  [ "$ok" = 1 ]
}

# pc_selftest_guards ROOT: guard / real-home / hash / merge tests shared by
# both scripts. ROOT is a fresh temp dir owned by the caller.
pc_selftest_guards() {
  local root=$1 fh="$1/fakehome" msg
  mkdir -p "$fh/.codex" "$fh/.claude" "$fh/.cursor" "$fh/.hermes" "$fh/.ballast" "$fh/scratch" "$fh/.codex/sub"
  ln -s "$fh" "$root/link-to-home"
  ln -s "$fh/.codex" "$root/link-to-codex"
  mkdir "$root/fresh" "$root/marked" "$root/dirty"
  : >"$root/marked/.ballast-probe"
  : >"$root/marked/auth-placeholder"
  : >"$root/dirty/something"
  : >"$root/afile"
  export BALLAST_PROBE_EXTRA_PROTECTED="$fh"

  pc_t_refuses "未设置" ""
  pc_t_refuses "相对路径" "relative/dir"
  pc_t_refuses "不存在的目录" "$root/nope"
  pc_t_refuses "普通文件" "$root/afile"
  pc_t_refuses "根目录" "/"
  pc_t_refuses "真实家目录(模拟)" "$fh"
  pc_t_refuses "真实 ~/.codex" "$fh/.codex"
  pc_t_refuses "真实 ~/.codex 的子目录" "$fh/.codex/sub"
  pc_t_refuses "真实 ~/.claude" "$fh/.claude"
  pc_t_refuses "真实 ~/.cursor" "$fh/.cursor"
  pc_t_refuses "真实 ~/.hermes" "$fh/.hermes"
  pc_t_refuses "真实 ~/.ballast" "$fh/.ballast"
  pc_t_refuses "指向家目录的符号链接" "$root/link-to-home"
  pc_t_refuses "指向 ~/.codex 的符号链接" "$root/link-to-codex"
  pc_t_refuses "包含真实家目录的祖先目录" "$root"
  pc_t_refuses "非空且无标记的目录" "$root/dirty"
  mkdir "$root/symmarker"
  ln -s "$root/afile" "$root/symmarker/.ballast-probe"
  pc_t_refuses "标记是符号链接的目录" "$root/symmarker"
  local real
  real="$(pc_realhome)"
  pc_t_refuses "passwd 家目录" "$real"
  local sub
  for sub in .codex .cursor .ballast .claude .hermes; do
    if [ -d "$real/$sub" ]; then
      pc_t_refuses "passwd 家目录/$sub" "$real/$sub"
    else
      pc_t_skip "passwd 家目录/$sub" "本机没有该目录(以模拟家目录的同名用例为准)"
    fi
  done
  if [ -n "${HOME:-}" ] && [ "$(pc_canon "$HOME")" = "$(pc_canon "$real")" ]; then
    pc_t_refuses "\$HOME(即真实家目录)" "$HOME"
  fi
  pc_t_refuses "系统路径 /etc" "/etc"
  pc_t_accepts "全新空目录" "$root/fresh"
  pc_t_accepts "带标记的非空目录" "$root/marked"
  pc_t_accepts "家目录内的非受保护目录" "$fh/scratch" nofresh

  # $HOME override: the real home is still found through the passwd database.
  msg="$(HOME="$root/fresh" pc_realhome)"
  if [ "$msg" != "$root/fresh" ] && [ -n "$msg" ]; then pc_t_ok "pc_realhome 不受 \$HOME 覆盖影响"; else pc_t_bad "pc_realhome 不受 \$HOME 覆盖影响" "got: $msg"; fi
  # HOME=<probe home> is the documented usage and must be accepted ...
  if msg="$(HOME="$root/fresh" pc_py guard probe "$root/fresh" fresh 2>&1)"; then
    pc_t_ok "HOME=<探针目录> 放行"
  else
    pc_t_bad "HOME=<探针目录> 放行" "$msg"
  fi
  # ... but HOME=<real home> pointing the probe at itself never is.
  if msg="$(HOME="$fh" pc_py guard probe "$fh" fresh 2>&1)"; then
    pc_t_bad "HOME=<真实家目录> 作为探针目录" "守卫放行了"
  else
    pc_t_ok "HOME=<真实家目录> 作为探针目录被拒绝"
  fi
  if msg="$(pc_py disjoint "$root/fresh" "$root/fresh/x" 2>&1)"; then
    pc_t_bad "两个目录互相包含" "放行了"
  else
    pc_t_ok "两个目录互相包含被拒绝"
  fi
  unset BALLAST_PROBE_EXTRA_PROTECTED

  # ---- trust hash: vectors shared with internal/install/hash_test.go ----
  pc_t_eq "信任哈希 pre_tool_use(与 Go 测试同一向量)" \
    "$(pc_py trusthash pre_tool_use '/opt/ballast/bin/ballast hook --client codex --event PreToolUse' 30)" \
    "sha256:4c52bb0133a2ba33b43c322753624ddde3c79f0579230fa261ce0d698626f419"
  pc_t_eq "信任哈希 stop(含空格与引号)" \
    "$(pc_py trusthash stop "'/opt/x y/ballast' hook --client codex --event Stop" 30)" \
    "sha256:be5c7ea31550a16ae9314f00d30f3b920929c3c17dacd79c5fcd1771fb06083a"
  pc_t_eq "信任哈希 session_start(非 ASCII、转义、U+2028)" \
    "$(pc_py trusthash session_start "$(printf '/opt/日本/ballast "q" \\ \t hook\342\200\250 <&>')" 5)" \
    "sha256:00c6207f6b4ce5f6704860af7c87f475a7358e56f20fc8b91e9b28ee12dcc729"

  # ---- hooks.json merge: foreign entries survive, merge is idempotent ----
  local hj="$root/hooks-merge.json"
  printf '%s\n' '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/true","timeout":5}]}],"Custom":[]}}' >"$hj"
  pc_py codex_merge "$hj" '{"Stop":"/p/hook.sh Stop","PreToolUse":"/p/hook.sh PreToolUse"}'
  pc_py codex_merge "$hj" '{"Stop":"/p/hook.sh Stop","PreToolUse":"/p/hook.sh PreToolUse"}'
  pc_t_eq "codex 合并: 保留他人条目、幂等" \
    "$(python3 -I -c 'import json,sys;d=json.load(open(sys.argv[1]))["hooks"];print(len(d["Stop"]),len(d["PreToolUse"]),d["Stop"][0]["hooks"][0]["command"],"Custom" in d)' "$hj")" \
    "2 1 /usr/bin/true True"
  local cj="$root/cursor-merge.json"
  printf '%s\n' '{"version":1,"hooks":{"stop":[{"command":"/usr/bin/true"}]}}' >"$cj"
  pc_py cursor_merge "$cj" '{"stop":"/p/hook.sh stop","preToolUse":"/p/hook.sh preToolUse"}' '["stop"]'
  pc_py cursor_merge "$cj" '{"stop":"/p/hook.sh stop","preToolUse":"/p/hook.sh preToolUse"}' '["stop"]'
  pc_t_eq "cursor 合并: 保留他人条目、幂等、stop 带 loop_limit" \
    "$(python3 -I -c 'import json,sys;d=json.load(open(sys.argv[1]));h=d["hooks"];print(d["version"],len(h["stop"]),h["stop"][1].get("loop_limit"),len(h["preToolUse"]),h["preToolUse"][0].get("loop_limit"))' "$cj")" \
    "1 2 10 1 None"

  # ---- the final scan rejects machine identifiers ----
  local hf="$root/hygiene.txt" hhost
  hhost="$(python3 -I -c 'import socket;print(socket.gethostname())')"
  chk() { printf '%s' "$1" >"$hf"; if pc_py check_text "$hf" >/dev/null 2>&1; then echo pass; else echo reject; fi; }
  pc_t_eq "卫生扫描: /var/folders 与 /private/var/folders 被拒绝,普通文本通过" \
    "$(chk '{"p":"/var/folders/ab/xyz/T/x"}') $(chk '{"p":"/private/var/folders/ab/xyz/T"}') $(chk '{"p":"<work>/a.txt"}')" "reject reject pass"
  pc_t_eq "卫生扫描: 邮箱地址被拒绝(含 user_email 字段),占位符与普通文本通过" \
    "$(chk '{"user_email":"alice.real@corp-example.com"}') $(chk '{"x":"mail bob@example.org now"}') $(chk '{"user_email":"<user_email>"}') $(chk '{"p":"a @ b"}')" "reject reject pass pass"
  case "$hhost" in
    "" | localhost | ??) pc_t_skip "卫生扫描: 主机名被拒绝" "本机主机名为空、localhost 或短于 3 个字符" ;;
    *) pc_t_eq "卫生扫描: 出现本机主机名被拒绝" "$(chk "{\"tool_response\":\"host $hhost up\"}")" "reject" ;;
  esac

  # ---- hygiene: this directory carries no local paths or user names ----
  local bad
  bad="$(grep -nE '(^|[^A-Za-z:}])/Users/[a-z][A-Za-z0-9._-]*/(NewCode|Documents|Desktop|Library|\.claude|\.ballast)|[~]/NewCode|claude-[0-9]+-' "$PC_LIB_DIR"/*.sh || true)"
  if [ -z "$bad" ]; then pc_t_ok "脚本不含本机绝对路径/uid 片段"; else pc_t_bad "脚本不含本机绝对路径/uid 片段" "$bad"; fi
}

# pc_t_finish NAME: print the summary, exit 1 on any failure.
pc_t_finish() {
  printf '\nSUMMARY self-test=%s pass=%d fail=%d skip=%d\n' "$1" "$PC_T_PASS" "$PC_T_FAIL" "$PC_T_SKIP"
  if [ "$PC_T_FAIL" -gt 0 ]; then
    printf 'RESULT %s self-test: FAILED\n' "$1"
    return 1
  fi
  printf 'RESULT %s self-test: OK\n' "$1"
}
