#!/usr/bin/env bash
# shellcheck disable=SC2164  # set -e is on: a failing cd aborts the script
# hermes.sh - offline Hermes evidence (docs/p2-design.md section 7.1, unit P2-HERMES).
#
# What it proves: with a real Hermes (a read-only copy of its source tree, run
# from a throw-away uv virtualenv) `hermes hooks test <event>` feeds the
# configured shell hooks the production-serialized payload, runs them, and
# prints the parsed Hermes wire shape. Ballast is installed with
# `ballast install --client hermes`; an extra probe hook records stdin next to
# it. What it does NOT prove: when a real agent fires which hook with which
# kwargs (needs a model session: the case "live-session-trigger-timing" is
# recorded as needs_credentials with a one-line recheck command).
#
# Usage:
#   bash scripts/probe-clients/hermes.sh            run every case, assert, print a summary
#   bash scripts/probe-clients/hermes.sh --write    also (re)write docs/facts/hermes-cases.json and
#                                                   docs/facts/payloads/hermes/** from this run
#
# Environment:
#   BALLAST_HERMES_SRC       Hermes source checkout (a git work tree). Default: ~/.hermes/hermes-agent
#                            if it is a git work tree. Only `git archive HEAD` reads it.
#   BALLAST_HERMES_PYTHON    interpreter request for uv (default 3.14, what Hermes' pinned deps need)
#   BALLAST_REQUIRE_HERMES=1 a missing prerequisite is a failure instead of a SKIP
#   BALLAST_KEEP_TMP=1       keep the temporary directory
#
# Isolation (docs/p2-design.md section 0): HOME, HERMES_HOME, BALLAST_HOME,
# BALLAST_INSTALL_ROOT, CLAUDE_CONFIG_DIR, CODEX_HOME, XDG_*, GIT_CONFIG_GLOBAL
# all point into one mktemp directory and Hermes runs under `env -i`, so no
# credential variable of the caller reaches it. The real ~/.hermes is never
# written, symlinked or copied (no installs/, auth.json or config.yaml); the
# source is read with `git archive`. Hermes' dependencies are installed into
# the throw-away venv (build time needs the network; `hooks test` does not call
# any model). Self-contained on purpose: no lib.sh.
# Needs: bash, git, go, uv, python3, tar; macOS/Linux only.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
FACTS="$ROOT/docs/facts"
# The commit the adapter fix was developed against: the baseline binary is
# built from it to record what the PRE-fix adapter did (design 7.1 step 3).
BASELINE_REV="cee8a4c963ce152a24e8da7b38cb289fa1b21ecc"
REAL_HOME="${HOME:-}"
WRITE=0
for a in "$@"; do
  case "$a" in
    --write) WRITE=1 ;;
    -h | --help) sed -n '2,36p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "hermes.sh: unknown argument: $a" >&2; exit 2 ;;
  esac
done

say() { printf '%s\n' "$*"; }
die() { printf 'ERROR hermes.sh: %s\n' "$*" >&2; exit 2; }

# need_or_skip REASON: a missing prerequisite is a SKIP (exit 0) unless
# BALLAST_REQUIRE_HERMES=1.
need_or_skip() {
  if [ "${BALLAST_REQUIRE_HERMES:-}" = 1 ]; then
    printf 'FAIL hermes.sh: %s (BALLAST_REQUIRE_HERMES=1)\n' "$1" >&2
    exit 1
  fi
  printf 'SKIP hermes.sh: %s\n' "$1"
  exit 0
}

# ---------------------------------------------------------------------------
# 1. Static check of the committed evidence (needs only python3).

static_check() {
  if ! command -v python3 >/dev/null 2>&1; then
    say "note: python3 not found; skipping the static check of the committed evidence"
    return 0
  fi
  python3 -I - "$FACTS" "${USER:-}" <<'PY'
import json, os, re, sys
facts, user = sys.argv[1], sys.argv[2]
errs = []
cases_path = os.path.join(facts, "hermes-cases.json")
if not os.path.exists(cases_path):
    print("static: docs/facts/hermes-cases.json does not exist yet"); sys.exit(1)
cases = json.load(open(cases_path, encoding="utf-8"))
need = ["case_id", "events", "schema_keys", "assertion", "result", "observation", "evidence", "client", "hermes_commit", "evidence_kind"]
seen = set()
for c in cases:
    cid = c.get("case_id", "?")
    for k in need:
        if k not in c:
            errs.append(f"{cid}: missing field {k}")
    if cid in seen:
        errs.append(f"{cid}: duplicate case_id")
    seen.add(cid)
    if c.get("client") != "hermes" or c.get("evidence_kind") != "offline-hooks-test":
        errs.append(f"{cid}: client/evidence_kind")
    if not re.fullmatch(r"[0-9a-f]{40}", str(c.get("hermes_commit", ""))):
        errs.append(f"{cid}: hermes_commit is not a full commit id")
    if c.get("result") not in ("pass", "fail", "unverifiable", "needs_credentials"):
        errs.append(f"{cid}: bad result {c.get('result')!r}")
    if c.get("result") == "needs_credentials" and not c.get("recheck"):
        errs.append(f"{cid}: needs_credentials without recheck")
    for ev in c.get("evidence", []):
        if not os.path.exists(os.path.join(facts, ev)):
            errs.append(f"{cid}: evidence path missing: {ev}")
pat = [re.compile(r"(^|[^A-Za-z:}])/Users/[a-z][A-Za-z0-9._-]*/"), re.compile(r"[~]/NewCode"), re.compile(r"claude-[0-9]+-"),
       re.compile(r"/home/[a-z][A-Za-z0-9._-]*/"), re.compile(r"/private/var/"), re.compile(r"/var/folders/")]
if user:
    pat.append(re.compile(r"\b" + re.escape(user) + r"\b"))
files = [cases_path, os.path.join(facts, "HERMES-CONCLUSIONS.md")]
for d, _, fs in os.walk(os.path.join(facts, "payloads", "hermes")):
    files += [os.path.join(d, f) for f in fs]
for f in files:
    if not os.path.exists(f):
        errs.append(f"missing {os.path.relpath(f, facts)}"); continue
    text = open(f, encoding="utf-8", errors="replace").read()
    for p in pat:
        m = p.search(text)
        if m:
            errs.append(f"hygiene: {os.path.relpath(f, facts)} matches {p.pattern!r}: {m.group(0)!r}")
concl = open(os.path.join(facts, "HERMES-CONCLUSIONS.md"), encoding="utf-8").read() if os.path.exists(os.path.join(facts, "HERMES-CONCLUSIONS.md")) else ""
for needle in ("待同步文本", "触发时机未核验", "execute_code"):
    if needle not in concl:
        errs.append(f"HERMES-CONCLUSIONS.md lacks {needle!r}")
if errs:
    print("static: FAILED"); print("\n".join("  " + e for e in errs)); sys.exit(1)
print(f"static: ok ({len(cases)} cases, {len(files)} files checked)")
PY
}

section() { printf '\n== %s\n' "$*"; }

section "committed evidence"
if [ "$WRITE" = 1 ]; then
  static_check || say "note: --write regenerates the evidence; the static check runs again afterwards"
else
  static_check || exit 1
fi

# ---------------------------------------------------------------------------
# 2. Prerequisites.

case "$(uname -s 2>/dev/null || echo unknown)" in
  MINGW* | MSYS* | CYGWIN*) need_or_skip "native Windows is not supported by this probe (macOS/Linux only)" ;;
esac
for t in git go uv python3 tar; do
  command -v "$t" >/dev/null 2>&1 || need_or_skip "required tool not found: $t"
done

SRC="${BALLAST_HERMES_SRC:-}"
if [ -z "$SRC" ] && [ -d "$REAL_HOME/.hermes/hermes-agent" ]; then SRC="$REAL_HOME/.hermes/hermes-agent"; fi
[ -n "$SRC" ] || need_or_skip "no Hermes source: set BALLAST_HERMES_SRC to a Hermes git work tree"
git -C "$SRC" rev-parse --is-inside-work-tree >/dev/null 2>&1 || need_or_skip "BALLAST_HERMES_SRC is not a git work tree"
HERMES_COMMIT="$(git -C "$SRC" rev-parse HEAD)"

# ---------------------------------------------------------------------------
# 3. Temporary root, guards, Hermes in a throw-away venv.

BASE_TMP="${TMPDIR:-/tmp}"
BASE_TMP="${BASE_TMP%/}"
WORK="$(mktemp -d "$BASE_TMP/ballast-hermes.XXXXXX")"
WORK="$(cd "$WORK" && pwd -P)"
cleanup() {
  local rc=$?
  if [ "${BALLAST_KEEP_TMP:-0}" = 1 ]; then printf 'kept %s\n' "$WORK" >&2; else rm -rf "$WORK" 2>/dev/null || true; fi
  return "$rc"
}
trap cleanup EXIT
mkdir -p "$WORK/bin" "$WORK/run" "$WORK/out" "$WORK/gohome"

# The Go caches keep their real location (read from the caller's go env); the
# go command's own per-user files go to a throw-away HOME.
GOCACHE="$(go env GOCACHE)"; GOMODCACHE="$(go env GOMODCACHE)"; GOPATH="$(go env GOPATH)"
export GOCACHE GOMODCACHE GOPATH GOPROXY=off GOTOOLCHAIN=local
gobuild() { (cd "$1" && shift && HOME="$WORK/gohome" XDG_CONFIG_HOME="$WORK/gohome/.config" go build -trimpath "$@"); }

section "build ballast"
BIN="$WORK/bin/ballast"
FAULT_BIN="$WORK/bin/ballast-fault"
BASE_BIN="$WORK/bin/ballast-baseline"
gobuild "$ROOT" -o "$BIN" ./cmd/ballast || die "cannot build ballast"
gobuild "$ROOT" -tags ballast_fault -o "$FAULT_BIN" ./cmd/ballast || die "cannot build the fault-injection ballast"
HAVE_BASELINE=0
if git -C "$ROOT" cat-file -e "$BASELINE_REV^{commit}" 2>/dev/null; then
  mkdir -p "$WORK/baseline-src"
  git -C "$ROOT" archive "$BASELINE_REV" | tar -x -C "$WORK/baseline-src"
  if gobuild "$WORK/baseline-src" -o "$BASE_BIN" ./cmd/ballast; then HAVE_BASELINE=1; fi
fi
say "ballast built (baseline commit available: $HAVE_BASELINE)"

section "hermes runtime (uv venv from a read-only copy of the source)"
mkdir -p "$WORK/hermes-src"
git -C "$SRC" archive HEAD | tar -x -C "$WORK/hermes-src"
VENV="$WORK/venv"
PYREQ="${BALLAST_HERMES_PYTHON:-3.14}"
uv venv --quiet --python "$PYREQ" "$VENV" >/dev/null 2>&1 || need_or_skip "uv cannot create a Python $PYREQ venv"
uv pip install --quiet --python "$VENV/bin/python" -e "$WORK/hermes-src" >"$WORK/uv-install.log" 2>&1 \
  || need_or_skip "cannot install Hermes' dependencies into the venv (network needed at build time): $(tail -n 2 "$WORK/uv-install.log" | tr '\n' ' ')"
HERMES="$VENV/bin/hermes"
PY="$VENV/bin/python"
[ -x "$HERMES" ] || need_or_skip "the venv has no hermes entry point"
say "hermes commit: $HERMES_COMMIT; python: $("$PY" -c 'import sys;print(".".join(map(str,sys.version_info[:3])))')"

# Probe hook: records stdin to $PROBE_OUT (inherited through Hermes' env).
PROBE="$WORK/bin/probe.sh"
cat >"$PROBE" <<'SH'
#!/bin/sh
cat > "${PROBE_OUT:?}"
exit 0
SH
chmod +x "$PROBE"

# Scripts that emulate a hook's exit-code / output behaviour (Hermes semantics).
mkscript() { # NAME BODY
  printf '#!/bin/sh\n%s\n' "$2" >"$WORK/bin/$1"
  chmod +x "$WORK/bin/$1"
}
mkscript exit2-stderr 'cat >/dev/null; echo "blocked by probe (exit 2)" >&2; exit 2'
mkscript exit1-json 'cat >/dev/null; printf "%s" "{\"decision\":\"block\",\"reason\":\"json despite exit 1\"}"; exit 1'
mkscript exit1-empty 'cat >/dev/null; exit 1'
mkscript dialect-hermes 'cat >/dev/null; printf "%s" "{\"action\":\"block\",\"message\":\"hermes dialect\"}"; exit 0'
mkscript dialect-claude 'cat >/dev/null; printf "%s" "{\"decision\":\"block\",\"reason\":\"claude dialect\"}"; exit 0'
mkscript garbage-stdout 'cat >/dev/null; echo "Traceback (most recent call last): boom"; exit 0'
mkscript continue-hermes 'cat >/dev/null; printf "%s" "{\"action\":\"continue\",\"message\":\"keep going\"}"; exit 0'

# ---------------------------------------------------------------------------
# 4. Per-case sandboxes and steps.

SAFE_PATH="$(dirname "$(command -v git)"):/usr/bin:/bin:/usr/sbin:/sbin"
RULES_PY="$WORK/mkrules.py"
cat >"$RULES_PY" <<'PY'
import json, sys
root, out, variant = sys.argv[1], sys.argv[2], sys.argv[3]
def load(n):
    return json.load(open(f"{root}/schema/examples/{n}", encoding="utf-8"))["rules"]
rules = load("02-no-long-sleep.json") + load("04-no-tamper.json")
if variant == "stop":
    rules.append({
        "id": "probe-stop-deny", "action": "deny", "event": "Stop",
        "match": {"text": {"source": "last_assistant_message", "regex": ["(?i)\\bdone\\b"]}},
        "message": "Not done until the tests have run: keep going.",
        "examples": [
            {"name": "claims done", "expect": "hit", "input": {"last_assistant_message": "All done."}},
            {"name": "still working", "expect": "miss", "input": {"last_assistant_message": "Still working on it."}},
        ],
    })
json.dump({"version": 1, "rules": rules}, open(out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
PY

# Environment of the current case (set by new_env).
E_ROOT=""; E_HOME=""; E_HH=""; E_BH=""; E_IR=""; E_WORK=""
CASES=()

guard_env() {
  local v
  for v in "$E_HOME" "$E_HH" "$E_BH" "$E_IR" "$E_WORK"; do
    case "$v" in
      "$WORK"/*) ;;
      *) die "sandbox guard: $v is not under the temporary root" ;;
    esac
  done
  [ "$E_HH" != "$REAL_HOME/.hermes" ] || die "sandbox guard: HERMES_HOME is the real one"
}

# new_env CASE RULES-VARIANT BINARY: fresh homes, rules.json, hermes config via `ballast install`.
new_env() {
  CASE="$1"
  E_ROOT="$WORK/run/$CASE/env"
  E_HOME="$E_ROOT/home"; E_HH="$E_ROOT/hermes-home"; E_BH="$E_ROOT/ballast-home"; E_IR="$E_ROOT/install-root"; E_WORK="$E_ROOT/repo"
  rm -rf "$E_ROOT"
  mkdir -p "$E_HOME" "$E_HH" "$E_BH" "$E_IR" "$E_WORK" "$E_ROOT/tmp" "$E_ROOT/cc" "$E_ROOT/cx"
  guard_env
  : >"$E_ROOT/gitconfig"
  CASES+=("$CASE")
  python3 -I "$RULES_PY" "$ROOT" "$E_BH/rules.json" "${2:-base}"
  if [ -n "${3:-}" ]; then
    run_ballast "$3" install --client hermes --binary "$3" --force >"$WORK/run/$CASE/install.out" 2>&1 \
      || die "ballast install failed in case $CASE: $(cat "$WORK/run/$CASE/install.out")"
    # The probe hook goes right after each event key, ahead of Ballast's own item.
    awk -v probe="$PROBE" '{print} /^  [a-z_]+:[ ]*$/ {print "    - command: \"" probe "\""; print "      timeout: 10"}' \
      "$E_HH/config.yaml" >"$E_HH/config.yaml.new" && mv "$E_HH/config.yaml.new" "$E_HH/config.yaml"
  fi
}

# envrun CMD...: run under env -i with the case's sandbox (extra vars: STEP_ENV array).
envrun() {
  guard_env
  env -i PATH="$SAFE_PATH" HOME="$E_HOME" HERMES_HOME="$E_HH" BALLAST_HOME="$E_BH" BALLAST_INSTALL_ROOT="$E_IR" \
    CLAUDE_CONFIG_DIR="$E_ROOT/cc" CODEX_HOME="$E_ROOT/cx" GIT_CONFIG_GLOBAL="$E_ROOT/gitconfig" GIT_CONFIG_NOSYSTEM=1 \
    XDG_CONFIG_HOME="$E_ROOT/home/.config" XDG_DATA_HOME="$E_ROOT/home/.local/share" XDG_CACHE_HOME="$E_ROOT/home/.cache" \
    TMPDIR="$E_ROOT/tmp" PYTHONUTF8=1 PYTHONDONTWRITEBYTECODE=1 LANG=en_US.UTF-8 \
    ${STEP_ENV[@]+"${STEP_ENV[@]}"} "$@"
}
STEP_ENV=()

run_ballast() { # BINARY args...
  local b=$1; shift
  (cd "$E_WORK" && envrun "$b" "$@")
}

# step NAME EVENT [hermes hooks test args...]: one `hermes hooks test` run.
# Saves raw.txt, stdin.payload (what the probe hook received) and the exact
# Ballast reply (direct.*): the same stdin replayed into the binary named by
# STEP_BIN against a snapshot of the state taken before the step.
STEP_BIN=""
step() {
  local name=$1 event=$2; shift 2
  local d="$WORK/run/$CASE/$name"
  mkdir -p "$d"
  cp -R "$E_BH" "$d/bh-snapshot"
  STEP_ENV=("PROBE_OUT=$d/stdin.payload" ${STEP_EXTRA_ENV[@]+"${STEP_EXTRA_ENV[@]}"})
  local rc=0
  (cd "$E_WORK" && envrun "$HERMES" hooks test "$event" "$@") >"$d/raw.txt" 2>"$d/raw.err" || rc=$?
  echo "$rc" >"$d/hermes.rc"
  if [ -n "$STEP_BIN" ] && [ -s "$d/stdin.payload" ]; then
    local save_bh="$E_BH"
    E_BH="$d/bh-snapshot"
    local drc=0
    (cd "$E_WORK" && envrun "$STEP_BIN" hook --client hermes --event "$event" <"$d/stdin.payload" >"$d/direct.stdout" 2>"$d/direct.stderr") || drc=$?
    echo "$drc" >"$d/direct.rc"
    E_BH="$save_bh"
  fi
  STEP_ENV=()
  rm -rf "$d/bh-snapshot"
}
STEP_EXTRA_ENV=()

# payload NAME JSON: write a --payload-file (JSON given with @HOME@ and @REPO@ placeholders).
payload() {
  local f="$WORK/run/$CASE/$1.payload.json"
  mkdir -p "$WORK/run/$CASE"
  printf '%s' "$2" | sed -e "s|@HOME@|$E_HOME|g" -e "s|@REPO@|$E_WORK|g" >"$f"
  PAYLOAD_FILE="$f"
}

section "cases"

# --- install-config: the installer writes the hooks, never fail_closed -------
new_env install-config base "$BIN"
STEP_BIN=""
(cd "$E_WORK" && envrun "$HERMES" hooks list) >"$WORK/run/$CASE/hooks-list.txt" 2>&1 || true
cp "$E_HH/config.yaml" "$WORK/run/$CASE/config.yaml"
cp "$WORK/run/$CASE/install.out" "$WORK/run/$CASE/install.txt"

# --- terminal-deny -----------------------------------------------------------
new_env terminal-deny base "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-terminal","args":{"command":"sleep 300"}}'
step current pre_tool_call --for-tool terminal --payload-file "$PAYLOAD_FILE"

# --- write-file-tamper -------------------------------------------------------
new_env write-file-tamper base "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-write","args":{"path":"@HOME@/.ballast/rules.json","content":"{}"}}'
step current pre_tool_call --for-tool write_file --payload-file "$PAYLOAD_FILE"

# --- patch-replace: pre-fix adapter vs current -------------------------------
for variant in baseline current; do
  if [ "$variant" = baseline ]; then
    [ "$HAVE_BASELINE" = 1 ] || continue
    new_env patch-replace-baseline base "$BASE_BIN"; STEP_BIN="$BASE_BIN"
  else
    new_env patch-replace base "$BIN"; STEP_BIN="$BIN"
  fi
  payload a '{"session_id":"p-patch-replace","args":{"mode":"replace","path":"@HOME@/.ballast/rules.json","old_string":"a","new_string":"b"}}'
  step "$variant" pre_tool_call --for-tool patch --payload-file "$PAYLOAD_FILE"
done

# --- patch-v4a: pre-fix adapter vs current -----------------------------------
for variant in baseline current; do
  if [ "$variant" = baseline ]; then
    [ "$HAVE_BASELINE" = 1 ] || continue
    new_env patch-v4a-baseline base "$BASE_BIN"; STEP_BIN="$BASE_BIN"
  else
    new_env patch-v4a base "$BIN"; STEP_BIN="$BIN"
  fi
  payload a '{"session_id":"p-patch-v4a","args":{"mode":"patch","patch":"*** Begin Patch\n*** Update File: src/ok.go\n@@\n-a\n+b\n*** Update File: @HOME@/.ballast/rules.json\n@@\n-x\n+y\n*** End Patch"}}'
  step "$variant" pre_tool_call --for-tool patch --payload-file "$PAYLOAD_FILE"
done

# --- execute-code: the documented gap ----------------------------------------
new_env execute-code base "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-exec","args":{"code":"open(\"@HOME@/.ballast/rules.json\", \"w\").write(\"{}\")"}}'
step current pre_tool_call --for-tool execute_code --payload-file "$PAYLOAD_FILE"

# --- prellm-first-and-second-turn --------------------------------------------
new_env prellm-turns base "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-turns","user_message":"hello","is_first_turn":true}'
step first pre_llm_call --payload-file "$PAYLOAD_FILE"
# The second turn must see the state the first one left: run the direct replay
# against the live state of this case (steps are sequential).
payload b '{"session_id":"p-turns","user_message":"and now?","is_first_turn":false}'
step second pre_llm_call --payload-file "$PAYLOAD_FILE"

# --- post-tool-call ----------------------------------------------------------
new_env post-tool-call base "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-post","args":{"command":"ls"},"result":"{\"output\": \"a.go\"}"}'
step current post_tool_call --for-tool terminal --payload-file "$PAYLOAD_FILE"

# --- session-lifecycle -------------------------------------------------------
new_env session-lifecycle base "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-life"}'
step start on_session_start --payload-file "$PAYLOAD_FILE"
step end on_session_end --payload-file "$PAYLOAD_FILE"

# --- pre-verify --------------------------------------------------------------
new_env pre-verify stop "$BIN"; STEP_BIN="$BIN"
payload a '{"session_id":"p-verify","final_response":"All done - the change is applied.","changed_paths":["src/app.tsx"],"attempt":0}'
step claims-done pre_verify --payload-file "$PAYLOAD_FILE"
payload b '{"session_id":"p-verify","final_response":"Still working on it.","changed_paths":["src/app.tsx"],"attempt":1}'
step still-working pre_verify --payload-file "$PAYLOAD_FILE"
step default-payload pre_verify

# --- fault-build -------------------------------------------------------------
new_env fault-build base "$FAULT_BIN"; STEP_BIN="$FAULT_BIN"
payload a '{"session_id":"p-fault","args":{"command":"sleep 300"}}'
STEP_EXTRA_ENV=("BALLAST_FAULT=decided:error")
step fault pre_tool_call --for-tool terminal --payload-file "$PAYLOAD_FILE"
STEP_EXTRA_ENV=()
step control pre_tool_call --for-tool terminal --payload-file "$PAYLOAD_FILE"

# --- hook behaviour semantics (probe scripts, no Ballast) ---------------------
semantic() { # NAME SCRIPT [extra yaml line]
  new_env "$1" base ""; STEP_BIN=""
  {
    printf 'hooks:\n  pre_tool_call:\n    - command: "%s"\n      timeout: 10\n' "$WORK/bin/$2"
    [ -z "${3:-}" ] || printf '      %s\n' "$3"
  } >"$E_HH/config.yaml"
  payload a '{"session_id":"p-sem","args":{"command":"echo hi"}}'
  step current pre_tool_call --for-tool terminal --payload-file "$PAYLOAD_FILE"
}
semantic exit2-blocks exit2-stderr
semantic exit1-json-still-parsed exit1-json
semantic exit1-empty-allows exit1-empty
semantic dialect-hermes dialect-hermes
semantic dialect-claude dialect-claude
semantic garbage-stdout-allows garbage-stdout
semantic missing-command-allows no-such-script
semantic missing-command-failclosed no-such-script "fail_closed: true"
semantic garbage-stdout-failclosed garbage-stdout "fail_closed: true"

new_env pre-verify-dialects base ""; STEP_BIN=""
printf 'hooks:\n  pre_verify:\n    - command: "%s"\n      timeout: 10\n' "$WORK/bin/continue-hermes" >"$E_HH/config.yaml"
payload a '{"session_id":"p-vd","final_response":"All done.","attempt":0}'
step hermes-dialect pre_verify --payload-file "$PAYLOAD_FILE"
printf 'hooks:\n  pre_verify:\n    - command: "%s"\n      timeout: 10\n' "$WORK/bin/dialect-claude" >"$E_HH/config.yaml"
step claude-dialect pre_verify --payload-file "$PAYLOAD_FILE"
printf 'hooks:\n  post_tool_call:\n    - command: "%s"\n      timeout: 10\n' "$WORK/bin/dialect-claude" >"$E_HH/config.yaml"
step post-tool-block-ignored post_tool_call --for-tool terminal --payload-file "$PAYLOAD_FILE"

# ---------------------------------------------------------------------------
# 5. Evaluate: python builds the sanitized artifacts and checks the assertions.

section "evaluate"
REPORT_PY="$WORK/report.py"
cat >"$REPORT_PY" <<'PY'
import argparse
import json
import os
import re
import shutil
import sys

ap = argparse.ArgumentParser()
ap.add_argument("--run", required=True)
ap.add_argument("--out", required=True)
ap.add_argument("--work", required=True)
ap.add_argument("--repo", required=True)
ap.add_argument("--bin", required=True)
ap.add_argument("--hermes-commit", required=True)
ap.add_argument("--have-baseline", default="0")
ap.add_argument("--baseline-rev", default="")
ap.add_argument("--user", default="")
ap.add_argument("--real-home", default="")
ap.add_argument("--python", default="")
A = ap.parse_args()
HAVE_BASELINE = A.have_baseline == "1"

# ---------------------------------------------------------------- sanitizing

WORKS = {A.work}
if A.work.startswith("/private/"):
    WORKS.add(A.work[len("/private"):])


def san(text):
    """Placeholders for every machine-specific string; timings are normalized."""
    for w in sorted(WORKS, key=len, reverse=True):
        e = re.escape(w)
        text = re.sub(e + r"/run/[^/\"\\]+/env/home", "<home>", text)
        text = re.sub(e + r"/run/[^/\"\\]+/env/repo", "<repo>", text)
        text = re.sub(e + r"/run/[^/\"\\]+/env/hermes-home", "<hermes-home>", text)
        text = re.sub(e + r"/run/[^/\"\\]+/env/ballast-home", "<ballast-home>", text)
        text = re.sub(e + r"/run/[^/\"\\]+/env/install-root", "<install-root>", text)
        text = re.sub(e + r"/run/[^/\"\\]+/env", "<sandbox>", text)
        text = text.replace(w + "/bin/ballast-baseline", "<ballast-baseline>")
        text = text.replace(w + "/bin/ballast-fault", "<ballast-fault>")
        text = text.replace(w + "/bin/ballast", "<ballast>")
        text = text.replace(w + "/bin/probe.sh", "<probe>")
        text = text.replace(w + "/bin", "<bin>")
        text = text.replace(w, "<tmp>")
    if A.repo:
        text = text.replace(A.repo, "<repo-root>")
    if A.real_home:
        text = text.replace(A.real_home, "<real-home>")
    if A.user:
        text = re.sub(r"\b" + re.escape(A.user) + r"\b", "<user>", text)
    text = re.sub(r"claude-[0-9]+-", "claude-<uid>-", text)
    text = re.sub(r"elapsed=[0-9.]+s", "elapsed=<t>s", text)
    return text


# ---------------------------------------------------------------- run access


def read(path, default=""):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return default


def parse_raw(text):
    """Split the `hermes hooks test` printout into one dict per fired hook."""
    hooks = []
    cur = None
    for line in text.splitlines():
        if line.startswith("  → "):
            cur = {"command": line[4:], "exit": None, "stdout": "", "stderr": "", "error": None,
                   "timed_out": False, "parsed": None, "has_parsed": False}
            hooks.append(cur)
            continue
        if cur is None:
            continue
        m = re.match(r"\s+exit=(-?\d+)\s+elapsed=", line)
        if m:
            cur["exit"] = int(m.group(1))
        elif line.startswith("      stdout: "):
            cur["stdout"] = line[len("      stdout: "):]
        elif line.startswith("      stderr: "):
            cur["stderr"] = line[len("      stderr: "):]
        elif line.startswith("      ✗ error: "):
            cur["error"] = line[len("      ✗ error: "):]
        elif line.startswith("      ✗ timed out"):
            cur["timed_out"] = True
        elif line.startswith("      parsed (Hermes wire shape): "):
            cur["parsed"] = json.loads(line[len("      parsed (Hermes wire shape): "):])
            cur["has_parsed"] = True
        elif line.startswith("      parsed: <none"):
            cur["parsed"] = None
            cur["has_parsed"] = True
    return hooks


class Step:
    def __init__(self, run_case, name):
        self.run_case, self.name = run_case, name
        self.dir = os.path.join(A.run, run_case, name)
        self.raw = read(os.path.join(self.dir, "raw.txt"))
        self.hooks = parse_raw(self.raw)
        self.stdin_text = read(os.path.join(self.dir, "stdin.payload"))
        try:
            self.stdin = json.loads(self.stdin_text) if self.stdin_text else None
        except ValueError:
            self.stdin = None
        self.hermes_rc = read(os.path.join(self.dir, "hermes.rc")).strip()
        self.direct_rc = read(os.path.join(self.dir, "direct.rc")).strip()
        self.direct_stdout = read(os.path.join(self.dir, "direct.stdout"))
        self.direct_stderr = read(os.path.join(self.dir, "direct.stderr"))

    @property
    def ballast(self):
        for h in self.hooks:
            if "hook --client hermes" in h["command"]:
                return h
        return None

    @property
    def only(self):
        return self.hooks[0] if len(self.hooks) == 1 else None

    @property
    def probe(self):
        for h in self.hooks:
            if h["command"].endswith("probe.sh"):
                return h
        return None

    def direct_json(self):
        try:
            return json.loads(self.direct_stdout) if self.direct_stdout.strip() else None
        except ValueError:
            return None


def step(run_case, name):
    s = Step(run_case, name)
    return s if os.path.isdir(s.dir) else None


# ------------------------------------------------------------ case plumbing

CASES = []  # result dicts, in order
ARTIFACTS = {}  # case_id -> list of (artifact name, text)


class Case:
    def __init__(self, cid, events, assertion):
        self.cid, self.events, self.assertion = cid, events, assertion
        self.fails, self.notes, self.schema_keys = [], [], {}
        self.result, self.recheck = None, None
        self.sources = []  # (run_case, step_name, artifact stem)

    def use(self, run_case, name, stem=None):
        s = step(run_case, name)
        if s is None:
            self.fails.append(f"missing step {run_case}/{name}")
            return None
        self.sources.append((s, stem or name))
        return s

    def check(self, cond, msg):
        if not cond:
            self.fails.append(msg)
        return bool(cond)

    def note(self, text):
        self.notes.append(text)

    def keys(self, label, obj):
        if isinstance(obj, dict):
            self.schema_keys[label] = sorted(obj.keys())

    def finish(self, observation, result="pass", recheck=None):
        self.result = "fail" if self.fails else result
        self.recheck = recheck
        CASES.append(self)
        self.observation = observation + ("" if not self.fails else " ASSERTION FAILURES: " + "; ".join(self.fails))
        art = []
        for s, stem in self.sources:
            if s.stdin is not None:
                art.append((f"{stem}.stdin", json.dumps(s.stdin, ensure_ascii=False, indent=2) + "\n"))
            art.append((f"{stem}.hooks-test.txt", s.raw))
            if s.direct_rc != "":
                art.append((f"{stem}.ballast-reply.txt",
                            f"exit: {s.direct_rc}\nstdout: {s.direct_stdout}\nstderr: {s.direct_stderr}\n"))
        ARTIFACTS[self.cid] = art + getattr(self, "extra_artifacts", [])


def stdin_shape(c, label, s):
    if s and s.stdin:
        c.keys(f"{label}.stdin", s.stdin)
        c.keys(f"{label}.stdin.extra", s.stdin.get("extra"))
        if isinstance(s.stdin.get("tool_input"), dict):
            c.keys(f"{label}.stdin.tool_input", s.stdin["tool_input"])


def blocked_by(c, s, rule, what):
    """Ballast blocked with a message from `rule`, Hermes parsed it as block, replay agrees."""
    b = s.ballast
    if not c.check(b is not None, f"{what}: no Ballast hook in the printout"):
        return
    c.check(b["exit"] == 0, f"{what}: ballast exit {b['exit']}")
    c.check(isinstance(b["parsed"], dict) and b["parsed"].get("action") == "block", f"{what}: parsed {b['parsed']!r}")
    msg = (b["parsed"] or {}).get("message", "")
    c.check(msg.startswith(f"[{rule}]"), f"{what}: message {msg!r}")
    d = s.direct_json()
    c.check(isinstance(d, dict) and d.get("decision") == "block" and d.get("reason") == msg,
            f"{what}: replayed stdout {s.direct_stdout!r}")
    c.check(s.direct_rc == "0", f"{what}: replay exit {s.direct_rc}")


def allowed(c, s, what, replay=True):
    b = s.ballast
    if not c.check(b is not None, f"{what}: no Ballast hook in the printout"):
        return
    c.check(b["exit"] == 0, f"{what}: ballast exit {b['exit']}")
    c.check(b["has_parsed"] and b["parsed"] is None, f"{what}: parsed {b['parsed']!r}")
    if replay:
        c.check(s.direct_rc == "0" and s.direct_stdout == "", f"{what}: replay rc={s.direct_rc} stdout={s.direct_stdout!r}")


def probe_saw(c, s, event, tool=None):
    if not c.check(s.stdin is not None, f"{event}: probe hook recorded no stdin"):
        return
    c.check(s.stdin.get("hook_event_name") == event, f"probe saw event {s.stdin.get('hook_event_name')!r}")
    if tool:
        c.check(s.stdin.get("tool_name") == tool, f"probe saw tool {s.stdin.get('tool_name')!r}")
    for k in ("hook_event_name", "tool_name", "tool_input", "session_id", "cwd", "profile", "extra"):
        c.check(k in s.stdin, f"stdin lacks key {k}")


# ------------------------------------------------------------------- cases

# 1. install-config ----------------------------------------------------------
c = Case("install-config", ["on_session_start", "pre_llm_call", "pre_tool_call", "post_tool_call", "pre_verify", "on_session_end"],
         "ballast install --client hermes writes one hook per installable native event into config.yaml (including pre_verify), never writes fail_closed, and hermes hooks list shows them as not allowlisted (consent is separate; hooks test does not need it)")
cfg = read(os.path.join(A.run, "install-config", "config.yaml"))
lst = read(os.path.join(A.run, "install-config", "hooks-list.txt"))
inst = read(os.path.join(A.run, "install-config", "install.txt"))
for ev in c.events:
    c.check(f"hook --client hermes --event {ev}\"" in cfg, f"config.yaml lacks the {ev} hook")
c.check("fail_closed" not in cfg and "failClosed" not in cfg, "the installer wrote fail_closed")
c.check(cfg.count("timeout: 30") == 6, "ballast hook entries are expected to carry timeout: 30")
n_ballast = sum(1 for l in lst.splitlines() if "hook --client hermes --event" in l)
n_unlisted = sum(1 for l in lst.splitlines() if "hook --client hermes --event" in l and "✗ not allowlisted" in l)
c.check(n_ballast == 6 and n_unlisted == 6, f"hooks list: {n_ballast} ballast hooks, {n_unlisted} not allowlisted")
c.check("--accept-hooks" in inst, "install output does not mention --accept-hooks")
c.extra_artifacts = [("config.yaml", cfg), ("hooks-list.txt", lst), ("install.txt", inst)]
c.finish("Six Ballast hooks (on_session_start, pre_llm_call, pre_tool_call, post_tool_call, pre_verify, on_session_end), "
         "timeout 30, no fail_closed. `hermes hooks list` reports every one as `not allowlisted`: running a real agent needs "
         "--accept-hooks (or the allowlist), while `hermes hooks test` fires hooks without consent. The installer prints the --accept-hooks hint.")

# 2. terminal-deny -------------------------------------------------------------
c = Case("terminal-deny", ["pre_tool_call"], "pre_tool_call + terminal running `sleep 300` with a deny rule: Hermes parses Ballast's output as a block carrying the rule reason")
s = c.use("terminal-deny", "current")
if s:
    probe_saw(c, s, "pre_tool_call", "terminal")
    c.check(s.stdin.get("tool_input") == {"command": "sleep 300"}, f"tool_input {s.stdin.get('tool_input')!r}")
    blocked_by(c, s, "no-long-sleep", "terminal")
    stdin_shape(c, "pre_tool_call", s)
c.finish("stdin = {hook_event_name, tool_name, tool_input (the model's args), session_id, cwd, profile, extra{task_id, tool_call_id}}; "
         "Ballast prints {\"decision\":\"block\",\"reason\":\"[no-long-sleep] ...\"} (exit 0) and Hermes parses it to {action:block, message:<reason>}.")

# 3. write-file-tamper ----------------------------------------------------------
c = Case("write-file-tamper", ["pre_tool_call"], "pre_tool_call + write_file into <home>/.ballast/rules.json is blocked by the no-tamper rule")
s = c.use("write-file-tamper", "current")
if s:
    probe_saw(c, s, "pre_tool_call", "write_file")
    blocked_by(c, s, "no-tamper", "write_file")
    stdin_shape(c, "pre_tool_call", s)
c.finish("write_file args {path, content}; the adapter maps it to Write and no-tamper (path **/.ballast/**) blocks.")

# 4. patch-replace ---------------------------------------------------------------
c = Case("patch-replace", ["pre_tool_call"], "pre_tool_call + patch (replace mode, args path/old_string/new_string) on <home>/.ballast/rules.json: the pre-fix adapter missed it, the fixed adapter blocks it (no-tamper)")
s = c.use("patch-replace", "current")
pre = None
if HAVE_BASELINE:
    pre = c.use("patch-replace-baseline", "baseline")
if s:
    probe_saw(c, s, "pre_tool_call", "patch")
    c.check(s.stdin["tool_input"].get("mode") == "replace", "payload is not replace mode")
    blocked_by(c, s, "no-tamper", "patch/replace (fixed adapter)")
    stdin_shape(c, "pre_tool_call", s)
pre_obs = "Pre-fix adapter binary was not available in this run."
if pre:
    b = pre.ballast
    missed = bool(b) and b["has_parsed"] and b["parsed"] is None and pre.direct_stdout == ""
    c.check(missed, f"expected the pre-fix adapter to MISS (premise of the fix), got parsed={b and b['parsed']!r}")
    pre_obs = ("Pre-fix adapter (commit " + A.baseline_rev[:10] + "): tool `patch` was not in the tool map, so no Edit-scoped rule saw it and nothing was blocked (parsed: none).")
c.finish(pre_obs + " Fixed adapter: patch => Edit with Targets=[path]; no-tamper blocks.")

# 5. patch-v4a ---------------------------------------------------------------------
c = Case("patch-v4a", ["pre_tool_call"], "pre_tool_call + patch (V4A mode, args mode=patch + patch envelope) touching src/ok.go and <home>/.ballast/rules.json: the pre-fix adapter missed it, the fixed adapter blocks it via the second target")
s = c.use("patch-v4a", "current")
pre = c.use("patch-v4a-baseline", "baseline") if HAVE_BASELINE else None
if s:
    probe_saw(c, s, "pre_tool_call", "patch")
    c.check(s.stdin["tool_input"].get("mode") == "patch" and "*** Begin Patch" in s.stdin["tool_input"].get("patch", ""), "payload is not a V4A envelope")
    blocked_by(c, s, "no-tamper", "patch/v4a (fixed adapter)")
    stdin_shape(c, "pre_tool_call", s)
pre_obs = "Pre-fix adapter binary was not available in this run."
if pre:
    b = pre.ballast
    missed = bool(b) and b["has_parsed"] and b["parsed"] is None and pre.direct_stdout == ""
    c.check(missed, f"expected the pre-fix adapter to MISS, got parsed={b and b['parsed']!r}")
    pre_obs = "Pre-fix adapter: not mapped, nothing blocked (parsed: none)."
c.finish(pre_obs + " Fixed adapter: every `*** Update/Add/Delete File:` (and Hermes' `*** Move File: a -> b`) in the envelope is a target; the second file triggers no-tamper.")

# 6. execute-code --------------------------------------------------------------------
c = Case("execute-code", ["pre_tool_call"], "pre_tool_call + execute_code writing <home>/.ballast/rules.json is NOT blocked (documented gap: no matcher sees which files a Python script writes)")
s = c.use("execute-code", "current")
if s:
    probe_saw(c, s, "pre_tool_call", "execute_code")
    c.check(set(s.stdin["tool_input"].keys()) == {"code"}, f"tool_input keys {sorted(s.stdin['tool_input'])}")
    allowed(c, s, "execute_code")
    stdin_shape(c, "pre_tool_call", s)
c.finish("execute_code args {code}; the tool keeps its raw name (rules can match it by name) but no-tamper cannot see the write, so the call is allowed. "
         "Registered in trust-model section 2.1 category 1 (see the pending-sync text in HERMES-CONCLUSIONS.md).")

# 7. prellm-turns --------------------------------------------------------------------
c = Case("prellm-turns", ["pre_llm_call"], "pre_llm_call with is_first_turn=true gets the full rule text as {\"context\"}; the next turn of the same session gets the short anchor")
a = c.use("prellm-turns", "first")
b2 = c.use("prellm-turns", "second")
ctx1 = ctx2 = ""
if a and b2:
    probe_saw(c, a, "pre_llm_call")
    c.check(a.stdin["extra"].get("is_first_turn") is True and a.stdin["extra"].get("user_message") == "hello", "first payload extra")
    c.check(b2.stdin["extra"].get("is_first_turn") is False, "second payload extra")
    p1, p2 = a.ballast["parsed"], b2.ballast["parsed"]
    c.check(isinstance(p1, dict) and set(p1) == {"context"}, f"first parsed {p1!r}")
    c.check(isinstance(p2, dict) and set(p2) == {"context"}, f"second parsed {p2!r}")
    ctx1, ctx2 = (p1 or {}).get("context", ""), (p2 or {}).get("context", "")
    c.check(ctx1.startswith("Ballast rules (generation ") and "[no-long-sleep]" in ctx1 and "[no-tamper]" in ctx1, "first context is not the full text")
    c.check(ctx2.startswith("Ballast: ") and len(ctx2) < len(ctx1), "second context is not the anchor")
    d1, d2 = a.direct_json(), b2.direct_json()
    c.check(d1 == p1 and d2 == p2, "replay differs from the parsed context")
    stdin_shape(c, "pre_llm_call", a)
    c.keys("pre_llm_call.stdin.extra(second)", b2.stdin.get("extra"))
c.finish("extra = {user_message, conversation_history, is_first_turn, model, platform}. First turn: {\"context\": \"Ballast rules (generation ...):\\n- [rule] ...\"}; "
         "second turn: {\"context\": \"Ballast: [rule] ...; ...\"} (anchor, shorter). The context key is the only thing Hermes reads from a non-gate event.")

# 8. post-tool-call -------------------------------------------------------------------
c = Case("post-tool-call", ["post_tool_call"], "post_tool_call: Ballast prints nothing, Hermes parses none, and the call is audited")
s = c.use("post-tool-call", "current")
if s:
    probe_saw(c, s, "post_tool_call", "terminal")
    allowed(c, s, "post_tool_call")
    c.check("result" in s.stdin["extra"] and "duration_ms" in s.stdin["extra"], "extra lacks result/duration_ms")
    stdin_shape(c, "post_tool_call", s)
state_files = []
sd = os.path.join(A.run, "post-tool-call", "env", "ballast-home", "state")
for d, _, fs in os.walk(sd):
    state_files += [os.path.join(d, f) for f in fs if f == "main.json"]
try:
    ring = json.loads(read(state_files[0]))["ring"] if state_files else []
except (ValueError, KeyError):
    ring = []
c.check(any(r.get("ev") == "PostToolUse" and r.get("tool") == "Bash" for r in ring), f"session state has no PostToolUse/Bash record: {ring!r}")
c.finish("extra = {task_id, tool_call_id, result (a JSON string), duration_ms}. Ballast replies with empty stdout (exit 0) and records the call in the session state "
         "(PostToolUse, tool normalized to Bash from `terminal`); the audit log only gets hit/diag/eval_summary records, a plain post_tool_call writes none.")

# 9. session-lifecycle ------------------------------------------------------------------
c = Case("session-lifecycle", ["on_session_start", "on_session_end"], "on_session_start and on_session_end: empty reply, parsed none")
s1 = c.use("session-lifecycle", "start")
s2 = c.use("session-lifecycle", "end")
for s, ev in ((s1, "on_session_start"), (s2, "on_session_end")):
    if s:
        probe_saw(c, s, ev)
        allowed(c, s, ev)
        stdin_shape(c, ev, s)
c.finish("on_session_start extra is {}; on_session_end extra = {task_id, turn_id, completed, failed, interrupted, turn_exit_reason, model, platform}. Both are lifecycle only: Ballast prints nothing.")

# 10. pre-verify -----------------------------------------------------------------------
c = Case("pre-verify", ["pre_verify"], "pre_verify carries the final answer in extra.final_response (attempt counts nudges); a Stop deny rendered as {decision:block,reason} is parsed by Hermes as {action:continue,message} (keep going)")
s1 = c.use("pre-verify", "claims-done")
s2 = c.use("pre-verify", "still-working")
s3 = c.use("pre-verify", "default-payload")
if s1 and s2 and s3:
    probe_saw(c, s1, "pre_verify")
    ex = s1.stdin["extra"]
    c.check(s1.stdin["tool_name"] is None and s1.stdin["tool_input"] is None, "pre_verify has no tool")
    for k in ("platform", "model", "coding", "attempt", "final_response", "changed_paths"):
        c.check(k in ex, f"pre_verify extra lacks {k}")
    c.check(ex.get("attempt") == 0 and "done" in ex.get("final_response", ""), "claims-done payload")
    for s, what in ((s1, "attempt 0"), (s3, "default hooks-test payload")):
        b = s.ballast
        c.check(b is not None and b["exit"] == 0, f"{what}: ballast exit")
        c.check(isinstance(b["parsed"], dict) and b["parsed"].get("action") == "continue"
                and b["parsed"].get("message", "").startswith("[probe-stop-deny]"), f"{what}: parsed {b and b['parsed']!r}")
        d = s.direct_json()
        c.check(isinstance(d, dict) and d.get("decision") == "block" and d.get("reason") == b["parsed"]["message"], f"{what}: replay {s.direct_stdout!r}")
    allowed(c, s2, "still-working (attempt 1, no match)")
    c.check(s2.stdin["extra"].get("attempt") == 1, "attempt field")
    stdin_shape(c, "pre_verify", s1)
audit = ""
for d, _, fs in os.walk(os.path.join(A.run, "pre-verify", "env", "ballast-home", "audit")):
    for f in fs:
        audit += read(os.path.join(d, f))
recs = [json.loads(l) for l in audit.splitlines() if l.strip()]
c.check(any(r.get("type") == "hit" and r.get("event") == "Stop" and r.get("native_event") == "pre_verify" and r.get("rule_id") == "probe-stop-deny" for r in recs),
        "audit has no Stop/pre_verify hit")
c.check(any(r.get("type") == "eval_summary" and r.get("native_event") == "pre_verify" for r in recs), "audit has no eval_summary flushed on pre_verify (Stop)")
c.finish("Hermes fires pre_verify once per turn after code edits. extra = {platform, model, coding, attempt, final_response, changed_paths}; no tool fields. "
         "The adapter maps it to Stop (last_assistant_message <- final_response, attempt>0 => stop_hook_active). A Stop deny prints {\"decision\":\"block\",\"reason\":R} "
         "and Hermes turns it into {action:\"continue\",message:R}: the agent keeps going (bounded by agent.max_verify_nudges, default 3). "
         "That is continue semantics, not a veto of the stop.")

# 11. pre-verify-dialects --------------------------------------------------------------
c = Case("pre-verify-dialects", ["pre_verify", "post_tool_call"], "pre_verify accepts both {action:continue,message} and the Claude Stop dialect {decision:block,reason}; a block answer on a non-blocking event (post_tool_call) is ignored")
s1 = c.use("pre-verify-dialects", "hermes-dialect")
s2 = c.use("pre-verify-dialects", "claude-dialect")
s3 = c.use("pre-verify-dialects", "post-tool-block-ignored")
if s1 and s2 and s3:
    c.check(s1.only and s1.only["parsed"] == {"action": "continue", "message": "keep going"}, f"hermes dialect: {s1.only and s1.only['parsed']!r}")
    c.check(s2.only and s2.only["parsed"] == {"action": "continue", "message": "claude dialect"}, f"claude dialect: {s2.only and s2.only['parsed']!r}")
    c.check(s3.only and s3.only["has_parsed"] and s3.only["parsed"] is None, f"post_tool_call block: {s3.only and s3.only['parsed']!r}")
c.finish("pre_verify: both dialects map to {action:continue}; Ballast uses the Claude one. post_tool_call: only a context directive is read, a block answer yields parsed none.")

# 12-17. exit-code / output semantics ------------------------------------------------------
c = Case("exit2-blocks", ["pre_tool_call"], "a hook that exits 2 blocks pre_tool_call, the stderr text is the message")
s = c.use("exit2-blocks", "current")
if s:
    o = s.only
    c.check(o and o["exit"] == 2 and o["parsed"] == {"action": "block", "message": "blocked by probe (exit 2)"}, f"{o and o['parsed']!r}")
c.finish("Exit code 2 on a blocking event => {action:block,message:<stderr>}, independent of stdout.")

c = Case("exit1-json-still-parsed", ["pre_tool_call"], "a hook that exits 1 but prints a block JSON is still parsed as a block (non-zero exits other than 2 still parse stdout)")
s = c.use("exit1-json-still-parsed", "current")
if s:
    o = s.only
    c.check(o and o["exit"] == 1 and o["parsed"] == {"action": "block", "message": "json despite exit 1"}, f"{o and o['parsed']!r}")
c.finish("Consequence for Ballast: its failure protocol (exit 1 with EMPTY stdout) is safe because empty stdout parses to nothing; a Ballast crash must never print a decision before exiting 1.")

c = Case("exit1-empty-allows", ["pre_tool_call"], "a hook that exits 1 with empty stdout contributes nothing (the call is allowed)")
s = c.use("exit1-empty-allows", "current")
if s:
    o = s.only
    c.check(o and o["exit"] == 1 and o["has_parsed"] and o["parsed"] is None, f"{o and o['parsed']!r}")
c.finish("Exit 1 + empty stdout => parsed none => allowed. This is Ballast's fail-open protocol (architecture section 4.2).")

c = Case("output-dialects", ["pre_tool_call"], "pre_tool_call accepts the Hermes dialect {action:block,message} and the Claude dialect {decision:block,reason}; both are translated to {action:block,message}")
s1 = c.use("dialect-hermes", "current", "hermes-dialect")
s2 = c.use("dialect-claude", "current", "claude-dialect")
if s1 and s2:
    c.check(s1.only and s1.only["parsed"] == {"action": "block", "message": "hermes dialect"}, f"hermes dialect {s1.only and s1.only['parsed']!r}")
    c.check(s2.only and s2.only["parsed"] == {"action": "block", "message": "claude dialect"}, f"claude dialect {s2.only and s2.only['parsed']!r}")
c.finish("Ballast keeps emitting the Claude dialect.")

c = Case("garbage-and-missing-fail-open", ["pre_tool_call"], "unparseable stdout and a missing hook command both fail OPEN by default")
s1 = c.use("garbage-stdout-allows", "current", "garbage-stdout")
s2 = c.use("missing-command-allows", "current", "missing-command")
if s1 and s2:
    c.check(s1.only and s1.only["has_parsed"] and s1.only["parsed"] is None, f"garbage stdout {s1.only and s1.only['parsed']!r}")
    c.check(s2.only and s2.only["error"] == "command not found" and s2.only["parsed"] is None, f"missing command {s2.only!r}")
c.finish("Default is fail open (spawn error, timeout, garbage stdout). Same as Claude's A7.")

c = Case("failclosed-semantics", ["pre_tool_call"], "with fail_closed: true a spawn error or garbage stdout BLOCKS; the installer must therefore never write fail_closed")
s1 = c.use("missing-command-failclosed", "current", "missing-command")
s2 = c.use("garbage-stdout-failclosed", "current", "garbage-stdout")
if s1 and s2:
    p1, p2 = s1.only and s1.only["parsed"], s2.only and s2.only["parsed"]
    c.check(isinstance(p1, dict) and p1.get("action") == "block" and "failed closed: command not found" in p1.get("message", ""), f"{p1!r}")
    c.check(isinstance(p2, dict) and p2.get("action") == "block" and "failed closed: unparseable stdout" in p2.get("message", ""), f"{p2!r}")
cfg_ok = "fail_closed" not in read(os.path.join(A.run, "install-config", "config.yaml"))
c.check(cfg_ok, "installer wrote fail_closed")
c.finish("fail_closed turns every Ballast failure (crash, timeout, missing binary after an uninstall) into a hard block of all tool calls. install-config shows the installer does not write it.")

# 18. fault-build -----------------------------------------------------------------------
c = Case("fault-build", ["pre_tool_call"], "a -tags ballast_fault build with BALLAST_FAULT=decided:error exits 1 with empty stdout; Hermes parses none (allow); without the variable the same build blocks")
s1 = c.use("fault-build", "fault")
s2 = c.use("fault-build", "control")
if s1 and s2:
    b = s1.ballast
    c.check(b and b["exit"] == 1 and b["has_parsed"] and b["parsed"] is None and b["stdout"] == "", f"fault run {b!r}")
    c.check("ballast:" in (b or {}).get("stderr", ""), "stderr does not start with ballast:")
    c.check(s1.direct_rc == "1" and s1.direct_stdout == "", f"replay rc={s1.direct_rc} stdout={s1.direct_stdout!r}")
    blocked_by(c, s2, "no-long-sleep", "control run")
c.finish("Fault => exit 1, stdout empty, stderr `ballast: fault injected at decided`; Hermes: parsed none, i.e. the tool call proceeds. Control run (no BALLAST_FAULT) blocks normally.")

# 19. needs credentials ---------------------------------------------------------------------
c = Case("live-session-trigger-timing", ["pre_tool_call", "pre_llm_call", "pre_verify", "on_session_start", "on_session_end", "post_tool_call"],
         "in a real Hermes session each hook fires at the expected moment with kwargs shaped like the hooks-test payloads, and a Ballast block really stops the tool call")
c.finish("hooks test proves serialization, output parsing and blocking semantics; it does not run an agent. Whether and when a live agent fires these hooks "
         "(including that a parsed block really prevents the tool from running and that pre_verify nudges are bounded by agent.max_verify_nudges) needs a model call, "
         "i.e. credentials the user logs in with inside a throw-away HERMES_HOME.",
         result="needs_credentials",
         recheck='HERMES_HOME="$(mktemp -d)" BALLAST_HOME="$(mktemp -d)" sh -c \'ballast install --client hermes --binary "$(command -v ballast)" --force && hermes chat --oneshot --accept-hooks -q "Run the shell command: sleep 300"\'  # log in to a provider inside that temporary HERMES_HOME first; expect the sleep to be refused with a [no-long-sleep] message; inspect $BALLAST_HOME/audit afterwards')

# ------------------------------------------------------------------- output

REQUIRED = ["install-config", "terminal-deny", "write-file-tamper", "patch-replace", "patch-v4a", "execute-code", "prellm-turns",
            "post-tool-call", "session-lifecycle", "pre-verify", "pre-verify-dialects", "exit2-blocks", "exit1-json-still-parsed",
            "exit1-empty-allows", "output-dialects", "garbage-and-missing-fail-open", "failclosed-semantics", "fault-build",
            "live-session-trigger-timing"]
got = [x.cid for x in CASES]
missing = [r for r in REQUIRED if r not in got]
if missing:
    print("FAIL missing cases:", missing)

out_cases = []
for x in CASES:
    out_cases.append({
        "case_id": x.cid,
        "events": x.events,
        "schema_keys": x.schema_keys,
        "assertion": x.assertion,
        "result": x.result,
        "observation": san(x.observation),
        "evidence": [f"payloads/hermes/{x.cid}/"],
        "client": "hermes",
        "hermes_commit": A.hermes_commit,
        "evidence_kind": "offline-hooks-test",
        "python": A.python,
        **({"recheck": x.recheck} if x.recheck else {}),
    })

os.makedirs(os.path.join(A.out, "payloads", "hermes"), exist_ok=True)
for x in CASES:
    d = os.path.join(A.out, "payloads", "hermes", x.cid)
    shutil.rmtree(d, ignore_errors=True)
    os.makedirs(d)
    if not ARTIFACTS.get(x.cid):
        with open(os.path.join(d, "README.txt"), "w", encoding="utf-8") as f:
            f.write("No payload: this case needs a real model session (see the recheck command in hermes-cases.json).\n")
    for name, text in ARTIFACTS.get(x.cid, []):
        with open(os.path.join(d, name), "w", encoding="utf-8") as f:
            f.write(san(text))
with open(os.path.join(A.out, "hermes-cases.json"), "w", encoding="utf-8") as f:
    json.dump(out_cases, f, ensure_ascii=False, indent=2)
    f.write("\n")

width = max(len(x.cid) for x in CASES)
bad = 0
for x in CASES:
    mark = {"pass": "ok  ", "needs_credentials": "NEED", "fail": "FAIL", "unverifiable": "UNV "}[x.result]
    print(f"{mark} {x.cid.ljust(width)}  {x.result}")
    if x.result == "fail":
        bad += 1
        for m in x.fails:
            print("       -", san(m))
print(f"\nSUMMARY cases={len(CASES)} pass={sum(1 for x in CASES if x.result == 'pass')} "
      f"needs_credentials={sum(1 for x in CASES if x.result == 'needs_credentials')} fail={bad} missing={len(missing)}")
sys.exit(1 if bad or missing else 0)
PY
OUT="$WORK/out"
"$PY" -I "$REPORT_PY" \
  --run "$WORK/run" --out "$OUT" --work "$WORK" --repo "$ROOT" --bin "$WORK/bin" \
  --hermes-commit "$HERMES_COMMIT" --have-baseline "$HAVE_BASELINE" --baseline-rev "$BASELINE_REV" \
  --user "${USER:-}" --real-home "$REAL_HOME" \
  --python "$("$PY" -c 'import sys;print(".".join(map(str,sys.version_info[:3])))')" \
  | tee "$WORK/report.txt"
rc=${PIPESTATUS[0]}

if [ "$rc" = 0 ] && [ "$WRITE" = 1 ]; then
  section "write docs/facts"
  mkdir -p "$FACTS/payloads"
  rm -rf "$FACTS/payloads/hermes"
  cp -R "$OUT/payloads/hermes" "$FACTS/payloads/hermes"
  cp "$OUT/hermes-cases.json" "$FACTS/hermes-cases.json"
  say "wrote docs/facts/hermes-cases.json and docs/facts/payloads/hermes/"
  section "committed evidence (after --write)"
  static_check || rc=1
fi
if [ "$rc" = 0 ] && [ "$WRITE" = 0 ]; then
  # Drift check: the live run must tell the same story as the committed file.
  python3 -I - "$OUT/hermes-cases.json" "$FACTS/hermes-cases.json" <<'PY' || rc=1
import json, sys
live = {c["case_id"]: c["result"] for c in json.load(open(sys.argv[1], encoding="utf-8"))}
kept = {c["case_id"]: c["result"] for c in json.load(open(sys.argv[2], encoding="utf-8"))}
if live != kept:
    print("FAIL drift between this run and docs/facts/hermes-cases.json (re-run with --write after reviewing):")
    for k in sorted(set(live) | set(kept)):
        if live.get(k) != kept.get(k):
            print(f"  {k}: live={live.get(k)} committed={kept.get(k)}")
    sys.exit(1)
print("ok   committed hermes-cases.json matches this run (case ids and results)")
PY
fi

if [ "$rc" = 0 ]; then printf '\nRESULT hermes.sh: OK\n'; else printf '\nRESULT hermes.sh: FAILED\n'; fi
exit "$rc"
