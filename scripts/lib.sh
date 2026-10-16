#!/usr/bin/env bash
# Shared helpers of the acceptance scripts (scripts/*.sh). Source it, do not
# execute it:
#
#   source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
#   lib_init regress
#   ... checks ...
#   finish
#
# Conventions
#   * Runs on macOS (bash 3.2), Linux and Windows Git Bash: no associative
#     arrays, no mapfile, no GNU-only flags.
#   * Everything happens under one mktemp directory ($TMP_ROOT). Before any
#     ballast process starts, sandbox_new redirects HOME, USERPROFILE,
#     BALLAST_HOME, BALLAST_INSTALL_ROOT, BALLAST_MANAGED_DIR,
#     CLAUDE_CONFIG_DIR, CODEX_HOME, HERMES_HOME, XDG_*, GIT_CONFIG_GLOBAL, ...
#     into it and guard_sandbox refuses to run otherwise. Real clients
#     (claude, codex, cursor, hermes) are never started and nothing goes to
#     the network (GOPROXY=off).
#   * Checks never abort the script; they are counted, and finish exits 1 when
#     any of them failed. Unexpected command failures abort (set -e).
#   * Binaries are reused when BALLAST_BIN / BALLAST_FAULT_BIN / ACCUTIL are
#     exported (the accept-m*.sh scripts build once and share).
# shellcheck shell=bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts; globals are read by the callers

if [ -n "${BALLAST_LIB_LOADED:-}" ]; then
  return 0
fi
BALLAST_LIB_LOADED=1

set -euo pipefail

LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$LIB_DIR/.." && pwd)"

PASS_N=0
FAIL_N=0
SKIP_N=0
SCRIPT_NAME="script"
TMP_ROOT=""
IS_WINDOWS=0
EXE=""
_RUN_N=0

# ---------------------------------------------------------------- output ---

say() { printf '%s\n' "$*"; }
section() { printf '\n== %s\n' "$*"; }
note() { printf '   %s\n' "$*"; }

ok() {
  PASS_N=$((PASS_N + 1))
  printf 'ok   %s\n' "$1"
}

bad() {
  FAIL_N=$((FAIL_N + 1))
  printf 'FAIL %s\n' "$1"
  if [ "$#" -gt 1 ]; then
    shift
    printf '     %s\n' "$@"
  fi
}

# skip NAME REASON: an explicit, counted SKIP (never silent).
skip() {
  SKIP_N=$((SKIP_N + 1))
  printf 'SKIP %s: %s\n' "$1" "$2"
}

# skip_or_fail NAME REASON: SKIP locally, FAIL when CI=true (a missing tool
# chain must not make CI green).
skip_or_fail() {
  if is_ci; then
    bad "$1" "$2 (CI=true: a missing prerequisite counts as a failure)"
  else
    skip "$1" "$2"
  fi
}

is_ci() { [ "${CI:-}" = "true" ] || [ "${CI:-}" = "1" ]; }

# die MSG: harness failure (not an assertion), aborts at once.
die() {
  printf 'ERROR %s: %s\n' "$SCRIPT_NAME" "$*" >&2
  exit 2
}

# finish: print the summary line and exit 1 when a check failed.
finish() {
  printf '\nSUMMARY script=%s pass=%d fail=%d skip=%d\n' "$SCRIPT_NAME" "$PASS_N" "$FAIL_N" "$SKIP_N"
  if [ "$FAIL_N" -gt 0 ]; then
    printf 'RESULT %s: FAILED\n' "$SCRIPT_NAME"
    exit 1
  fi
  printf 'RESULT %s: OK\n' "$SCRIPT_NAME"
  exit 0
}

# ------------------------------------------------------------ assertions ---

# check_eq NAME GOT WANT
check_eq() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "got:  $2" "want: $3"; fi
}

# check_ne NAME GOT UNWANTED
check_ne() {
  if [ "$2" != "$3" ]; then ok "$1"; else bad "$1" "value must differ from: $3"; fi
}

# check_contains NAME HAYSTACK NEEDLE (fixed string)
check_contains() {
  case "$2" in
    *"$3"*) ok "$1" ;;
    *) bad "$1" "missing: $3" "in: $(printf '%s' "$2" | head -c 600)" ;;
  esac
}

# check_lacks NAME HAYSTACK NEEDLE
check_lacks() {
  case "$2" in
    *"$3"*) bad "$1" "must not contain: $3" "in: $(printf '%s' "$2" | head -c 600)" ;;
    *) ok "$1" ;;
  esac
}

# check_true NAME CMD...: ok when the command succeeds.
check_true() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then ok "$name"; else bad "$name" "command failed: $*"; fi
}

# check_false NAME CMD...: ok when the command fails.
check_false() {
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then bad "$name" "command unexpectedly succeeded: $*"; else ok "$name"; fi
}

check_file() { if [ -f "$2" ]; then ok "$1"; else bad "$1" "missing file: $2"; fi; }
check_no_file() { if [ ! -e "$2" ]; then ok "$1"; else bad "$1" "must not exist: $2"; fi; }

# ncpu: the number of CPUs this machine offers (1 when unknown).
ncpu() {
  local n=""
  if command -v nproc >/dev/null 2>&1; then
    n="$(nproc 2>/dev/null || true)"
  fi
  if [ -z "$n" ] && command -v sysctl >/dev/null 2>&1; then
    n="$(sysctl -n hw.ncpu 2>/dev/null || true)"
  fi
  if [ -z "$n" ]; then n="${NUMBER_OF_PROCESSORS:-}"; fi
  case $n in '' | *[!0-9]*) n=1 ;; esac
  printf '%s' "$n"
}

# check_int_le NAME GOT MAX / check_int_ge NAME GOT MIN
check_int_le() { if [ "$2" -le "$3" ]; then ok "$1"; else bad "$1" "$2 > $3"; fi; }
check_int_ge() { if [ "$2" -ge "$3" ]; then ok "$1"; else bad "$1" "$2 < $3"; fi; }

# expect_rc NAME WANT: compares the exit code of the last bl/hook call.
expect_rc() {
  if [ "$BL_RC" = "$2" ]; then
    ok "$1"
  else
    bad "$1" "exit $BL_RC, want $2" "stdout: $(printf '%s' "$BL_OUT" | head -c 500)" "stderr: $(printf '%s' "$BL_ERR" | head -c 500)"
  fi
}

# expect_out_empty NAME: stdout of the last call has no byte at all.
expect_out_empty() {
  if [ ! -s "$BL_OUT_FILE" ]; then ok "$1"; else bad "$1" "stdout not empty: $(head -c 400 "$BL_OUT_FILE")"; fi
}

expect_out_has() { check_contains "$1" "$BL_OUT" "$2"; }
expect_out_lacks() { check_lacks "$1" "$BL_OUT" "$2"; }
expect_err_has() { check_contains "$1" "$BL_ERR" "$2"; }
expect_err_lacks() { check_lacks "$1" "$BL_ERR" "$2"; }

# expect_deny NAME: the last call produced a deny decision on stdout.
expect_deny() {
  if [ "$BL_RC" = 0 ] && printf '%s' "$BL_OUT" | grep -q '"permissionDecision":"deny"'; then
    ok "$1"
  else
    bad "$1" "want exit 0 and a deny decision" "exit $BL_RC" "stdout: $(printf '%s' "$BL_OUT" | head -c 400)" "stderr: $(printf '%s' "$BL_ERR" | head -c 400)"
  fi
}

# expect_allow NAME: exit 0 and empty stdout (the "no decision" protocol).
expect_allow() {
  if [ "$BL_RC" = 0 ] && [ ! -s "$BL_OUT_FILE" ]; then
    ok "$1"
  else
    bad "$1" "want exit 0 and empty stdout" "exit $BL_RC" "stdout: $(printf '%s' "$BL_OUT" | head -c 400)" "stderr: $(printf '%s' "$BL_ERR" | head -c 400)"
  fi
}

# --------------------------------------------------------------- platform ---

# native PATH: the form a native Windows program understands (C:/x); the
# identity elsewhere. Used for every path that is written into JSON or passed
# to ballast.
native() {
  # CYGPATH_BIN is resolved once (lib_init), so native still works while a test
  # runs with an emptied PATH (install-e2e.sh checks "client not installed").
  local cyg="${CYGPATH_BIN:-}"
  if [ -z "$cyg" ] && [ "$IS_WINDOWS" = 1 ]; then
    cyg="$(command -v cygpath 2>/dev/null || true)"
  fi
  if [ "$IS_WINDOWS" = 1 ] && [ -n "$cyg" ]; then
    "$cyg" -m "$1"
  else
    printf '%s' "$1"
  fi
}

# gocmd ARGS...: the go tool with a throw-away HOME. The Go caches keep their
# real location (GOCACHE, GOMODCACHE, GOPATH are exported by lib_init), but the
# go command's own per-user files (telemetry counters) must not be written into
# whatever HOME the caller has.
gocmd() {
  local h="${TMP_ROOT:?}/gohome"
  mkdir -p "$h"
  if [ "$IS_WINDOWS" = 1 ]; then
    APPDATA="$(native "$h")" LOCALAPPDATA="$(native "$h")" HOME="$h" USERPROFILE="$(native "$h")" go "$@"
  else
    HOME="$h" XDG_CONFIG_HOME="$h/.config" go "$@"
  fi
}

# jesc TEXT: escape backslashes and double quotes for a JSON string.
jesc() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

# ------------------------------------------------------------------ init ---

lib_init() {
  SCRIPT_NAME="${1:-script}"
  case "$(uname -s 2>/dev/null || echo unknown)" in
    MINGW* | MSYS* | CYGWIN*)
      IS_WINDOWS=1
      EXE=".exe"
      ;;
  esac
  if [ "$IS_WINDOWS" = 1 ]; then
    CYGPATH_BIN="$(command -v cygpath 2>/dev/null || true)"
    export CYGPATH_BIN
  fi
  need_cmd go
  need_cmd git
  # Capture the Go caches before HOME is redirected, so building keeps working
  # offline from the already populated module cache.
  GOCACHE="$(go env GOCACHE)"
  GOMODCACHE="$(go env GOMODCACHE)"
  GOPATH="$(go env GOPATH)"
  export GOCACHE GOMODCACHE GOPATH
  # Never reach the network; never let go switch toolchains or edit go.mod.
  export GOPROXY=off GOTOOLCHAIN=local

  local base="${TMPDIR:-/tmp}"
  base="${base%/}"
  TMP_ROOT="$(mktemp -d "$base/ballast-accept.XXXXXX")"
  mkdir -p "$TMP_ROOT/run" "$TMP_ROOT/bin" "$TMP_ROOT/files"
  : >"$TMP_ROOT/empty"
  trap '_cleanup' EXIT
  export TMP_ROOT
  build_tools
}

_cleanup() {
  local rc=$?
  # Detached children (gc, runners) may still be finishing; give them a moment.
  if [ -n "$TMP_ROOT" ] && [ -d "$TMP_ROOT" ]; then
    if [ "${BALLAST_KEEP_TMP:-0}" = 1 ]; then
      printf 'kept %s\n' "$TMP_ROOT" >&2
    else
      rm -rf "$TMP_ROOT" 2>/dev/null || { sleep 1; rm -rf "$TMP_ROOT" 2>/dev/null || true; }
    fi
  fi
  return "$rc"
}

# mktmp NAME: a fresh path (file not created) under $TMP_ROOT/files.
mktmp() {
  _RUN_N=$((_RUN_N + 1))
  printf '%s/files/%s.%d' "$TMP_ROOT" "$1" "$_RUN_N"
}

# ----------------------------------------------------------------- build ---

build_tools() {
  if [ -n "${ACCUTIL:-}" ] && [ -x "$ACCUTIL" ]; then
    return 0
  fi
  ACCUTIL="$TMP_ROOT/bin/accutil$EXE"
  (cd "$REPO_ROOT" && gocmd build -trimpath -o "$ACCUTIL" ./scripts/tools/accutil) || die "cannot build scripts/tools/accutil"
  export ACCUTIL
}

# build_ballast: the production binary (BALLAST_BIN).
build_ballast() {
  if [ -n "${BALLAST_BIN:-}" ] && [ -x "$BALLAST_BIN" ]; then
    BL_BIN="$BALLAST_BIN"
    return 0
  fi
  BALLAST_BIN="$TMP_ROOT/bin/ballast$EXE"
  (cd "$REPO_ROOT" && gocmd build -trimpath -o "$BALLAST_BIN" ./cmd/ballast) || die "cannot build ballast"
  export BALLAST_BIN
  BL_BIN="$BALLAST_BIN"
}

# build_ballast_fault: a binary with fault injection (-tags ballast_fault).
build_ballast_fault() {
  if [ -n "${BALLAST_FAULT_BIN:-}" ] && [ -x "$BALLAST_FAULT_BIN" ]; then
    return 0
  fi
  BALLAST_FAULT_BIN="$TMP_ROOT/bin/ballast-fault$EXE"
  (cd "$REPO_ROOT" && gocmd build -trimpath -tags ballast_fault -o "$BALLAST_FAULT_BIN" ./cmd/ballast) || die "cannot build the fault-injection ballast"
  export BALLAST_FAULT_BIN
}

# ---------------------------------------------------------------- sandbox ---

# sandbox_new NAME [keep-home]: create $TMP_ROOT/sb-NAME and point every
# environment variable that could reach real user state into it. Exports
# SB_ROOT, SB_HOME, SB_WORK, SB_MANAGED and the redirected variables.
#
# With keep-home (replay.sh only) HOME, USERPROFILE and BALLAST_HOME stay what
# the caller passed in (default BALLAST_HOME=$HOME/.ballast): replay reads the
# user's transcripts and replay-set.txt and sample ids hash the "~/"-relative
# transcript path. Everything else is still redirected, and ballast replay
# writes nothing under HOME or BALLAST_HOME.
sandbox_new() {
  local name=$1 keep=${2:-}
  SB_ROOT="$TMP_ROOT/sb-$name"
  rm -rf "$SB_ROOT"
  SB_HOME="$SB_ROOT/home"
  SB_WORK="$SB_ROOT/work"
  SB_MANAGED="$SB_ROOT/managed"
  mkdir -p "$SB_HOME" "$SB_WORK" "$SB_MANAGED"

  export HOME USERPROFILE BALLAST_HOME BALLAST_INSTALL_ROOT BALLAST_MANAGED_DIR
  export CLAUDE_CONFIG_DIR CODEX_HOME HERMES_HOME XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME
  export GIT_CONFIG_GLOBAL GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT GIT_OPTIONAL_LOCKS
  export GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL
  if [ "$keep" = keep-home ]; then
    ALLOW_REAL_HOME=1
    ALLOW_REAL_BALLAST_HOME=1
    export ALLOW_REAL_HOME ALLOW_REAL_BALLAST_HOME
    : "${HOME:?HOME must be set}"
    : "${USERPROFILE:=$HOME}"
    : "${BALLAST_HOME:=$HOME/.ballast}"
    # Fake config roots for everything else; nothing is created in the real HOME.
    BALLAST_INSTALL_ROOT="$(native "$SB_HOME")"
  else
    mkdir -p "$SB_HOME/.ballast"
    HOME="$(native "$SB_HOME")"
    USERPROFILE="$HOME"
    BALLAST_HOME="$(native "$SB_HOME/.ballast")"
    BALLAST_INSTALL_ROOT="$HOME"
  fi
  BALLAST_MANAGED_DIR="$(native "$SB_MANAGED")"
  CLAUDE_CONFIG_DIR="$(native "$SB_HOME/.claude")"
  CODEX_HOME="$(native "$SB_HOME/.codex")"
  HERMES_HOME="$(native "$SB_HOME/.hermes")"
  XDG_CONFIG_HOME="$(native "$SB_HOME/.config")"
  XDG_DATA_HOME="$(native "$SB_HOME/.local/share")"
  XDG_CACHE_HOME="$(native "$SB_HOME/.cache")"
  XDG_STATE_HOME="$(native "$SB_HOME/.local/state")"
  GIT_CONFIG_GLOBAL="$(native "$SB_HOME/.gitconfig")"
  GIT_CONFIG_NOSYSTEM=1
  GIT_TERMINAL_PROMPT=0
  GIT_OPTIONAL_LOCKS=0
  GIT_AUTHOR_NAME="ballast-accept"
  GIT_AUTHOR_EMAIL="accept@example.invalid"
  GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME"
  GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
  : >"$SB_HOME/.gitconfig"
  if [ "$IS_WINDOWS" = 1 ]; then
    export APPDATA LOCALAPPDATA
    APPDATA="$(native "$SB_HOME/AppData/Roaming")"
    LOCALAPPDATA="$(native "$SB_HOME/AppData/Local")"
    mkdir -p "$SB_HOME/AppData/Roaming" "$SB_HOME/AppData/Local"
  fi
  unset BALLAST_FAULT GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_PREFIX \
    GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_CONFIG_SYSTEM GIT_EXEC_PATH_OVERRIDE \
    ACC_STUB_LOG ACC_STUB_EXIT 2>/dev/null || true
  if [ "$keep" != keep-home ]; then
    # A fresh gc.stamp keeps SessionStart from spawning the detached
    # `ballast gc`, which would race the removal of the sandbox. The scripts
    # that test that mechanism remove the stamp themselves.
    : >"$SB_HOME/.ballast/gc.stamp"
    chmod 700 "$SB_HOME/.ballast" 2>/dev/null || true
  fi
}

# guard_sandbox: abort unless every location ballast could write to is under
# $TMP_ROOT. replay.sh keeps the caller's HOME on purpose (ALLOW_REAL_HOME=1:
# sample ids hash the "~/"-relative transcript path) and its BALLAST_HOME is
# the caller's (ALLOW_REAL_BALLAST_HOME=1); nothing else may point outside.
guard_sandbox() {
  local var val
  for var in BALLAST_INSTALL_ROOT BALLAST_MANAGED_DIR CLAUDE_CONFIG_DIR CODEX_HOME HERMES_HOME GIT_CONFIG_GLOBAL XDG_CONFIG_HOME; do
    eval "val=\${$var:-}"
    _under_tmp "$val" || die "refusing to run: $var=$val is not under $TMP_ROOT"
  done
  if [ "${ALLOW_REAL_HOME:-0}" != 1 ]; then
    _under_tmp "${HOME:-}" || die "refusing to run: HOME=${HOME:-} is not under $TMP_ROOT"
    _under_tmp "${USERPROFILE:-}" || die "refusing to run: USERPROFILE=${USERPROFILE:-} is not under $TMP_ROOT"
  fi
  if [ "${ALLOW_REAL_BALLAST_HOME:-0}" != 1 ]; then
    _under_tmp "${BALLAST_HOME:-}" || die "refusing to run: BALLAST_HOME=${BALLAST_HOME:-} is not under $TMP_ROOT"
  fi
  [ -z "${BALLAST_FAULT:-}" ] || [ -n "${BL_ALLOW_FAULT:-}" ] || die "BALLAST_FAULT is set outside fault.sh"
}

_under_tmp() {
  local v=$1 root
  [ -n "$v" ] || return 1
  root="$(native "$TMP_ROOT")"
  case "$v" in
    "$root"/* | "$TMP_ROOT"/*) return 0 ;;
  esac
  return 1
}

# --------------------------------------------------------------- running ---

# bl ARGS...        run ballast; sets BL_RC, BL_OUT, BL_ERR, BL_OUT_FILE, BL_ERR_FILE
# bl_in TEXT ARGS...  the same with TEXT on stdin
# Extra environment for one call: VAR=value bl ...
BL_BIN="${BALLAST_BIN:-}"
BL_RC=0
BL_OUT=""
BL_ERR=""
BL_OUT_FILE=""
BL_ERR_FILE=""

_bl_exec() {
  local stdin=$1
  shift
  [ -n "$BL_BIN" ] || die "no ballast binary: call build_ballast first"
  guard_sandbox
  _RUN_N=$((_RUN_N + 1))
  BL_OUT_FILE="$TMP_ROOT/run/$_RUN_N.out"
  BL_ERR_FILE="$TMP_ROOT/run/$_RUN_N.err"
  BL_RC=0
  "$ACCUTIL" run -timeout "${BL_TIMEOUT:-90s}" -stdin "$stdin" -stdout "$BL_OUT_FILE" -stderr "$BL_ERR_FILE" -- "$BL_BIN" "$@" || BL_RC=$?
  if [ "$BL_RC" = 125 ]; then
    die "accutil run failed for: $* ($(<"$BL_ERR_FILE"))"
  fi
  # Builtin reads: a test may run ballast with a PATH that has no cat.
  BL_OUT="$(<"$BL_OUT_FILE")"
  BL_ERR="$(<"$BL_ERR_FILE")"
}

bl() { _bl_exec "$TMP_ROOT/empty" "$@"; }

bl_in() {
  local data=$1 f
  shift
  f="$(mktmp stdin)"
  printf '%s' "$data" >"$f"
  _bl_exec "$f" "$@"
}

# bl_in_dir DIR TEXT ARGS...: like bl_in with DIR as the working directory.
bl_in_dir() {
  local dir=$1 data=$2 here
  shift 2
  here="$(pwd)"
  cd "$dir"
  bl_in "$data" "$@"
  cd "$here"
}

# bl_dir DIR ARGS...: ballast in another working directory.
bl_dir() {
  local dir=$1 here
  shift
  here="$(pwd)"
  cd "$dir"
  bl "$@"
  cd "$here"
}

# hook CLIENT EVENT JSON: one real hook process.
hook() {
  bl_in "$3" hook --client "$1" --event "$2"
}

# payload_bash SESSION TOOL_USE_ID COMMAND_JSON_ESCAPED [CWD]
# (the command must already be a valid JSON string body)
payload_bash() {
  local cwd=${4:-$(native "$SB_WORK")}
  printf '{"session_id":"%s","cwd":"%s","tool_name":"Bash","tool_use_id":"%s","tool_input":{"command":"%s"}}' \
    "$1" "$cwd" "$2" "$3"
}

# --------------------------------------------------------------------- git ---

# git_init DIR: a repository with a deterministic identity and no hooks of
# the user's.
git_init() {
  local dir=$1
  mkdir -p "$dir"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name "ballast-accept"
  git -C "$dir" config user.email "accept@example.invalid"
  git -C "$dir" config commit.gpgsign false
  git -C "$dir" config core.autocrlf false
  git -C "$dir" config core.fsmonitor false
}

# gitc DIR ARGS...: git -C DIR.
gitc() {
  local dir=$1
  shift
  git -C "$dir" "$@"
}

# write_file PATH CONTENT
write_file() {
  mkdir -p "$(dirname "$1")"
  printf '%s' "$2" >"$1"
}

# --------------------------------------------------------------- verify ---

# wait_status WANT_RC TIMEOUT_S ARGS...: poll `ballast verify --status ARGS`
# (in the current directory) until it exits WANT_RC. Leaves the last result
# in BL_*; returns 0 when reached, 1 otherwise.
wait_status() {
  local want=$1 limit=$2 i=0 max
  shift 2
  max=$((limit * 10))
  while [ "$i" -lt "$max" ]; do
    bl verify --status "$@"
    if [ "$BL_RC" = "$want" ]; then
      return 0
    fi
    # 6 = running, 11 = no record yet: keep waiting; anything else is final.
    if [ "$BL_RC" != 6 ] && [ "$BL_RC" != 11 ]; then
      return 1
    fi
    "$ACCUTIL" sleep 0.1
    i=$((i + 1))
  done
  return 1
}

# project_json_write FILE EXEMPT_JSON_ARRAY NAME:ARGV_JSON[:REQUIRED[:TIMEOUT]]...
# writes a project.json; each entry is name|argv-json|required|timeout_s.
project_json_write() {
  local file=$1 exempt=$2 entries="" e name argv req tmo first=1
  shift 2
  for e in "$@"; do
    name="${e%%|*}"
    e="${e#*|}"
    argv="${e%%|*}"
    e="${e#*|}"
    req="${e%%|*}"
    tmo="${e#*|}"
    [ "$first" = 1 ] || entries="$entries,"
    first=0
    entries="$entries{\"name\":\"$name\",\"argv\":$argv,\"required\":$req"
    if [ -n "$tmo" ] && [ "$tmo" != "$e" ]; then
      entries="$entries,\"timeout_s\":$tmo"
    fi
    entries="$entries}"
  done
  write_file "$file" "{\"version\":1,\"verify\":[$entries],\"exempt\":$exempt}
"
}

# ---------------------------------------------------------------- running ---

# run_child NAME SCRIPT [ARGS...]: run another acceptance script, show its
# output, fold its verdict into this script's counters.
run_child() {
  local name=$1 script=$2 out rc=0 line
  shift 2
  section "$name"
  out="$(mktmp child)"
  set +e
  bash "$LIB_DIR/$script" "$@" 2>&1 | tee "$out"
  rc=${PIPESTATUS[0]}
  set -e
  line="$(grep '^SUMMARY ' "$out" | tail -n 1 || true)"
  if [ -n "$line" ]; then
    local s
    s="$(printf '%s' "$line" | sed -n 's/.* skip=\([0-9][0-9]*\).*/\1/p')"
    SKIP_N=$((SKIP_N + ${s:-0}))
  fi
  if [ "$rc" = 0 ]; then
    ok "$name"
  else
    bad "$name" "$script exited $rc"
  fi
}

# run_go NAME ARGS...: a go command in the repository root.
run_go() {
  local name=$1 rc=0
  shift
  section "$name"
  (cd "$REPO_ROOT" && gocmd "$@") || rc=$?
  if [ "$rc" = 0 ]; then ok "$name"; else bad "$name" "go $* exited $rc"; fi
}

# shared_binaries: build once and export for the child scripts.
shared_binaries() {
  build_ballast
  export BALLAST_BIN ACCUTIL
}

# ----------------------------------------------------------------- replay ---

# replay_reports SETFILE OUTDIR: `ballast replay` (JSON, with sub-agents, the
# shipped default rule pack) over every transcript listed in SETFILE (one path
# per line; blank lines and #-comments ignored; a leading ~/ means $HOME).
# Writes OUTDIR/NN.json, NN.err and NN.stats (ms=, rss_kb=). Transcripts are
# only read. Sets REPLAY_COUNT. Output names the transcripts by number, never
# by path.
REPLAY_COUNT=0
replay_reports() {
  local set=$1 out=$2 line path n=0 idx rc rules
  rules="$(native "$REPO_ROOT")/rules/default.json"
  mkdir -p "$out"
  REPLAY_COUNT=0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case $line in '' | '#'*) continue ;; esac
    # shellcheck disable=SC2088  # a literal leading ~/ is the documented syntax
    case $line in
      '~/'*) path="$HOME/${line#\~/}" ;;
      *) path=$line ;;
    esac
    n=$((n + 1))
    idx="$(printf '%02d' "$n")"
    if [ ! -f "$path" ]; then
      bad "transcript #$n exists" "the file listed as entry $n of the replay set is missing"
      continue
    fi
    guard_sandbox
    rc=0
    "$ACCUTIL" run -timeout 1800s -stdout "$out/$idx.json" -stderr "$out/$idx.err" -stats "$out/$idx.stats" \
      -- "$BL_BIN" replay "$path" --format json --subagents --rules "$rules" || rc=$?
    if [ "$rc" = 0 ]; then
      ok "transcript #$n: ballast replay exits 0"
    else
      bad "transcript #$n: ballast replay exits 0" "exit $rc" "$(head -c 300 "$out/$idx.err")"
    fi
  done <"$set"
  REPLAY_COUNT=$n
}

# stat_value FILE KEY: the value of KEY=VALUE in a stats file (0 if absent).
stat_value() {
  local v
  v="$(sed -n "s/^$2=//p" "$1" 2>/dev/null | head -n 1)"
  printf '%s' "${v:-0}"
}

# ------------------------------------------------------------ accept helpers ---

# static_checks: formatting, build and vet for the host and the two other
# platforms (the contract's "must build everywhere" rule).
static_checks() {
  local out
  section "gofmt"
  out="$(cd "$REPO_ROOT" && gofmt -l ./internal ./cmd ./scripts 2>&1)" || true
  if [ -z "$out" ]; then ok "gofmt -l ./internal ./cmd ./scripts prints nothing"; else bad "gofmt -l prints nothing" "$out"; fi
  run_go "go build ./..." build ./...
  run_go "go vet ./..." vet ./...
  section "cross-platform vet"
  local goos rc
  for goos in windows linux darwin; do
    rc=0
    (cd "$REPO_ROOT" && GOOS="$goos" gocmd vet ./...) || rc=$?
    if [ "$rc" = 0 ]; then ok "GOOS=$goos go vet ./..."; else bad "GOOS=$goos go vet ./..." "exit $rc"; fi
  done
}

# run_bench NAME [ARGS...]: the plan's p95 gate through scripts/bench (the
# Go program of unit U12c). It is called the way that program documents:
# `go run ./scripts/bench -bin BIN -base DIR -root REPO [ARGS...]`, where BIN
# is the shared ballast binary, DIR a scratch directory under $TMP_ROOT
# (outside any git repository, bench creates it) and ARGS narrow the run
# (`-only REGEXP` on scenario ids, `-n`, `-warm`). Bench measures with warm-up
# 20 and 300 runs, p95 of ceil(0.95n), gate 30 ms, and exits non-zero when a
# gate fails or its arguments are wrong. Without scripts/bench the step is an
# explicit SKIP (FAIL under CI).
run_bench() {
  local name=$1 rc=0
  shift
  section "$name"
  if ! ls "$REPO_ROOT"/scripts/bench/*.go >/dev/null 2>&1; then
    skip_or_fail "$name" "scripts/bench is not part of this tree"
    return 0
  fi
  (cd "$REPO_ROOT" && gocmd run ./scripts/bench \
    -bin "$(native "$BALLAST_BIN")" -base "$(native "$TMP_ROOT/bench")" \
    -root "$(native "$REPO_ROOT")" "$@") || rc=$?
  if [ "$rc" = 0 ]; then ok "$name"; else bad "$name" "scripts/bench exited $rc"; fi
}
