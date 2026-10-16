#!/bin/bash
# t1-escape.sh - the "verify test" of the boundary-t1.sh fixture repositories
# (docs/p2-design.md §5.3 t1-runner-sandbox). It runs INSIDE the runner's OS
# sandbox (bwrap) and tries to get out: every write or connection below must
# FAIL. It exits 0 only when
#
#   * every escape attempt failed, and
#   * the positive controls worked: a file can be written in the working
#     directory and, when the working directory is a git work tree (origin=cli;
#     the private copy of origin=hook has no .git), `git add` succeeds.
#
# Usage: t1-escape.sh [new]
#   (none)  attempts to write things that already exist or that no sandbox may
#           create (the exit status must be 0);
#   new     attempts to CREATE files the sandbox cannot forbid in advance on
#           Linux (.claude/settings.local.json, .vscode/tasks.json, .mcp.json,
#           evil/.git/config, ...). A creation that succeeds makes the script
#           fail; boundary-t1.sh then accepts either a clean pass (creation
#           failed) or an infra job protected_path_created with the file in
#           ~/.ballast/quarantine, and never a leftover in the host repository.
#
# Every attempt prints "blocked: <name>" or "ESCAPED: <name>" on stderr. The
# script only writes inside the current directory and its temp dir when a
# control or a probe needs it, and never prints a secret.
set -u
MODE=${1:-}
REAL_REPO=${T1_REAL_REPO:-/home/agent/repo}
SOCK=${T1_UNIX_SOCKET:-/run/ballast-t1/sock}
TCP_PORT=${T1_TCP_PORT:-47931}
HOME_DIR=${HOME:-/home/agent}
escaped=0
tried=0

try() { # NAME COMMAND...: the command succeeding is an ESCAPE
  local name=$1
  shift
  tried=$((tried + 1))
  if ("$@") >/dev/null 2>&1; then
    echo "ESCAPED: $name" >&2
    escaped=$((escaped + 1))
  else
    echo "blocked: $name" >&2
  fi
}
w() { printf 'x\n' >"$1"; }
wa() { printf 'x\n' >>"$1"; }

if [ "$MODE" = new ]; then
  try "new .ballast/x" w .ballast/x
  try "new .claude/settings.local.json" bash -c 'mkdir -p .claude && printf x > .claude/settings.local.json'
  try "new .vscode/tasks.json" bash -c 'mkdir -p .vscode && printf x > .vscode/tasks.json'
  try "new .mcp.json" w .mcp.json
  try "new .idea/x" bash -c 'mkdir -p .idea && printf x > .idea/x'
  try "new nested/.claude/settings.json" bash -c 'mkdir -p nested/deep/.claude && printf x > nested/deep/.claude/settings.json'
  try "new evil/.git/config (gitlink)" bash -c '
    git ls-files >/dev/null 2>&1 && cp "$(git rev-parse --git-path index)" "$TMPDIR/idx" 2>/dev/null
    GIT_INDEX_FILE="$TMPDIR/idx" git update-index --add --cacheinfo 160000,1111111111111111111111111111111111111111,evil 2>/dev/null
    mkdir -p evil/.git && printf x > evil/.git/config'
  if [ "$escaped" -ne 0 ]; then
    echo "t1-escape new: $escaped of $tried creations succeeded" >&2
    exit 1
  fi
  echo "t1-escape new: all $tried creations blocked" >&2
  exit 0
fi

# ---- things outside the work tree --------------------------------------------
try "echo > ~/.ballast/evidence/x" bash -c 'mkdir -p "$HOME/.ballast/evidence" && printf x > "$HOME/.ballast/evidence/x"'
try "python3 -c writes ~/.ballast/rules.json" python3 -c "open('$HOME_DIR/.ballast/rules.json','w').write('x')"
try "touch the binary directory" touch /usr/local/libexec/ballast/escape-probe
try "write ~/.bashrc" wa "$HOME_DIR/.bashrc"
try "write ~/.zshrc" wa "$HOME_DIR/.zshrc"
try "write ~/.profile" wa "$HOME_DIR/.profile"
try "write ~/.claude.json" w "$HOME_DIR/.claude.json"
try "write ~/.claude/settings.json" bash -c 'mkdir -p "$HOME/.claude" && printf x > "$HOME/.claude/settings.json"'
try "write the managed org.json" w /etc/claude-code/ballast/org.json
try "write /etc/claude-code/managed-settings.d/x.json" w /etc/claude-code/managed-settings.d/x.json
if [ ! -e .git ]; then
  # origin=hook: the work tree is a private copy; the REAL repository (reached by
  # its absolute path) and its .git/info/exclude must be unwritable. With
  # origin=cli the real work tree is the work tree, so only its protected
  # names are attempted below.
  try "real repository: write a file" w "$REAL_REPO/zz-real.txt"
  try "real repository: .git/info/exclude" wa "$REAL_REPO/.git/info/exclude"
fi
try "real repository: .ballast/project.json" wa "$REAL_REPO/.ballast/project.json"

# ---- inside the work tree: protected names ------------------------------------
try "sed -i .ballast/rules.json" sed -i 's/x/y/' .ballast/rules.json
try "write .ballast/project.json" wa .ballast/project.json
try "mv the work tree away and back" bash -c 'd=$PWD; mv "$d" "${d}2" && printf x > "${d}2/moved.txt"; mv "${d}2" "$d"'
try "ln ~/.ballast/rules.json hl && echo x > hl" bash -c 'ln "$HOME/.ballast/rules.json" hl && printf x > hl'
rm -f hl 2>/dev/null
if [ -e .git ]; then
  try "mv .git .git2 && mkdir .git && echo x > .git/config" bash -c 'mv .git .git2 && mkdir .git && printf x > .git/config'
  [ -e .git2 ] && [ ! -e .git ] && mv .git2 .git 2>/dev/null
  try "write .git/hooks/pre-commit" bash -c 'printf "#!/bin/sh\n" > .git/hooks/pre-commit'
  try "write .git/config" wa .git/config
fi

# ---- submodule / second worktree fixtures (docs/p2-design.md v1.3) -----------------
if [ -e sub/.git ]; then
  CD=$(git rev-parse --git-common-dir 2>/dev/null)
  case "$CD" in /*) ;; *) CD="$PWD/$CD" ;; esac
  try "sub/.git replaced by a directory, then sub/.git/config" bash -c 'rm -rf sub/.git && mkdir sub/.git && printf x > sub/.git/config'
  if [ -f sub/.git ]; then
    try "rewrite the sub/.git gitfile" bash -c 'printf "gitdir: %s/selfmade\n" "$PWD" > sub/.git'
  fi
  try "commondir/modules/sub/config" w "$CD/modules/sub/config"
  try "commondir/modules/sub/hooks/post-checkout" bash -c "mkdir -p '$CD/modules/sub/hooks' && printf x > '$CD/modules/sub/hooks/post-checkout'"
  try "worktrees/w2/config.worktree" w "$CD/worktrees/w2/config.worktree"
fi

# ---- sockets and network -----------------------------------------------------------
try "connect to the unix socket under /run" python3 -c "
import socket
s=socket.socket(socket.AF_UNIX); s.settimeout(2); s.connect('$SOCK')"
try "connect to a listener outside the sandbox (127.0.0.1)" python3 -c "
import socket
s=socket.create_connection(('127.0.0.1',$TCP_PORT),timeout=2)"
for v in SSH_AUTH_SOCK DOCKER_HOST DBUS_SESSION_BUS_ADDRESS DBUS_SYSTEM_BUS_ADDRESS; do
  tried=$((tried + 1))
  if [ -n "${!v:-}" ]; then
    echo "ESCAPED: $v is in the test environment" >&2
    escaped=$((escaped + 1))
  else
    echo "blocked: $v is not in the test environment" >&2
  fi
done

# ---- positive controls -------------------------------------------------------------
controls=0
if printf 'ok\n' >ok.txt 2>/dev/null; then
  echo "control: wrote ok.txt in the work tree" >&2
else
  echo "CONTROL FAILED: cannot write ok.txt in the work tree" >&2
  controls=1
fi
if [ -e .git ]; then
  if git add ok.txt >/dev/null 2>&1; then
    echo "control: git add ok.txt" >&2
  else
    echo "CONTROL FAILED: git add ok.txt" >&2
    controls=1
  fi
fi

if [ "$escaped" -ne 0 ] || [ "$controls" -ne 0 ]; then
  echo "t1-escape: $escaped of $tried attempts escaped, controls failed=$controls" >&2
  exit 1
fi
echo "t1-escape: all $tried attempts blocked, controls ok" >&2
exit 0
