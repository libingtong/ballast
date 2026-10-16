#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# boundary-t0.sh - M4 boundary, T0 half (docs/p2-design.md §5.2). Three
# platforms, no model, no container, no credentials. No arguments.
#
# T0 can DETECT tampering but cannot PREVENT it. This script is both the
# test of what is detected and the executable register of what is not:
#
#   * a redirected sandbox (lib.sh: HOME, BALLAST_HOME, CLAUDE_CONFIG_DIR,
#     BALLAST_MANAGED_DIR, ... all in a temp dir; guard_sandbox refuses to run
#     otherwise), a freshly built ballast, `ballast install --client
#     claude-code`, the shipped default rules as the user rule file, a
#     temporary git repository with .ballast/project.json;
#   * scripts/boundary/t0-cases.json: every row is a synthetic payload fed to
#     the REAL `ballast hook` (driver: scripts/boundary/t0). "detected" rows
#     (docs/trust-model.md §2.1 "识别的写入") must leave a rule_id "no-tamper"
#     hit in the audit; "gap" rows (§2.1 "未覆盖的写法", classes 1-7, two or
#     more each) must NOT. A gap that becomes detected FAILS the script:
#     the coverage changed, update docs/trust-model.md §2.1;
#   * a hand-written forged pass record with an arbitrary S_end is still
#     stale (`verify --check` exits 10) because freshness needs S_end == C;
#   * the forger who first computes the true C and writes it into the record
#     is NOT stopped (`verify --check` exits 0): that is the T0 boundary, and
#     it is recorded here as a gap row, not hidden;
#   * ConfigChange audit: after install, synthetic ConfigChange payloads (the
#     fields of docs/facts/payloads/m0b/configchange-payload) leave
#     `config_change` audit lines, protected:true for the Claude settings
#     files, and the hook never blocks (stdout empty).
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init boundary-t0
build_ballast
sandbox_new t0
guard_sandbox

BIN_N="$(native "$BALLAST_BIN")"
REPO="$SB_WORK/repo"
CTL="$SB_WORK/ctl"
CTL_BIN="$(native "$ACCUTIL")"
FIXTURE="$LIB_DIR/boundary/t0-cases.json"
SAMPLES="$REPO_ROOT/docs/facts/payloads/m0b/configchange-payload/hang"

# ---------------------------------------------------------------- setup ---

section "setup: install, default rules, repository"
# A Claude config dir that exists makes the client detectable.
write_file "$CLAUDE_CONFIG_DIR/settings.json" '{}
'
bl install --client claude-code --binary "$BIN_N"
expect_rc "ballast install --client claude-code" 0
check_contains "ConfigChange is among the installed hooks" "$(cat "$CLAUDE_CONFIG_DIR/settings.json")" '"ConfigChange"'

# The default pack as the user rule file (no-tamper in shadow mode: action log,
# target deny - the audit holds a "hit" record with a would_* decision).
cp "$REPO_ROOT/rules/default.json" "$BALLAST_HOME/rules.json"
bl lint "$(native "$BALLAST_HOME/rules.json")"
expect_rc "the default rule pack lints clean" 0

mkdir -p "$CTL/test"
git_init "$REPO"
write_file "$REPO/a.go" "package a
"
write_file "$REPO/README.md" "# readme
"
mkdir -p "$REPO/.ballast"
project_json_write "$REPO/.ballast/project.json" '["**/*.md","docs/**"]' \
  "test|[\"$CTL_BIN\",\"ctl\",\"$(native "$CTL")/test\"]|true|"
gitc "$REPO" add -A
gitc "$REPO" commit -q -m init

# ----------------------------------------------------- detected / gap rows ---

section "detected and gap rows (scripts/boundary/t0-cases.json)"
DRIVER="$TMP_ROOT/bin/t0cases$EXE"
(cd "$REPO_ROOT" && gocmd build -trimpath -o "$DRIVER" ./scripts/boundary/t0) || die "cannot build scripts/boundary/t0"
cases_out="$(mktmp t0cases)"
rc=0
"$DRIVER" -bin "$BIN_N" -cases "$(native "$FIXTURE")" -repo "$(native "$REPO")" -tmp-root "$(native "$TMP_ROOT")" >"$cases_out" 2>&1 || rc=$?
detail=""
while IFS= read -r line || [ -n "$line" ]; do
  line="${line%$'\r'}"
  case $line in
    "ok   "*) ok "${line#ok   }" ;;
    "FAIL "*)
      bad "${line#FAIL }"
      detail="x"
      ;;
    "     "*) printf '%s\n' "$line" ;;
    T0CASES*) note "$line" ;;
    *) printf '%s\n' "$line" ;;
  esac
done <"$cases_out"
if [ "$rc" != 0 ] && [ -z "$detail" ]; then
  bad "the case driver exited $rc" "$(head -c 600 "$cases_out")"
fi
check_int_ge "the fixture ran at least 60 rows" "$(grep -c '^ok ' "$cases_out" || true)" 60
check_contains "gap class 1 (language runtimes) has rows" "$(cat "$cases_out")" "gap1-"
check_contains "gap class 7 (MCP) has rows" "$(cat "$cases_out")" "gap7-"

# ------------------------------------------------------- forged evidence ---

section "forged evidence: S_end must equal C"

latest_record() { # NAME: newest evidence record file of a verify name
  local f best="" bestbase="" base
  for f in "$BALLAST_HOME"/evidence/*/"$1"/*.json; do
    [ -f "$f" ] || continue
    base="$(basename "$f")"
    if [ -z "$bestbase" ] || [ "$base" \> "$bestbase" ]; then
      best="$f"
      bestbase="$base"
    fi
  done
  printf '%s' "$best"
}

oracle_tree() { # REPO: independent computation of C with git only (index, exempt filter)
  local repo=$1 idx tmp t
  idx="$(git -C "$repo" rev-parse --path-format=absolute --git-path index)"
  tmp="$(mktmp oracle-index)"
  cp "$idx" "$tmp"
  t="$(
    export GIT_INDEX_FILE="$tmp"
    git -C "$repo" rm --cached -r -q --ignore-unmatch -- ':(glob)**/*.md' ':(glob)docs/**' >/dev/null
    git -C "$repo" write-tree
  )"
  rm -f "$tmp"
  printf '%s' "$t"
}

# forge_record FROM TO_JOB TREE: copy a genuine record under a new (newest) job
# id with s_start = s_end = TREE (a "valid pass" for that tree).
forge_record() {
  local from=$1 job=$2 tree=$3 dest
  dest="$(dirname "$from")/$job.json"
  sed -E -e "s/\"job_id\":\"[^\"]*\"/\"job_id\":\"$job\"/" \
    -e "s/\"s_start\":\"[0-9a-f]+\"/\"s_start\":\"$tree\"/" \
    -e "s/\"s_end\":\"[0-9a-f]+\"/\"s_end\":\"$tree\"/" "$from" >"$dest"
  chmod 600 "$dest" 2>/dev/null || true
}

cd "$REPO"
bl verify test
expect_rc "ballast verify test starts a job" 0
job="$(printf '%s\n' "$BL_OUT" | sed -n 's/^job=\([^ ]*\).*/\1/p' | head -n 1)"
wait_status 0 60 "$job" || bad "the control verify did not pass" "$BL_OUT"
bl verify --check
expect_rc "control: right after a genuine verify the evidence is fresh (0)" 0

genuine="$(latest_record test)"
[ -n "$genuine" ] || die "no genuine evidence record was written"

# The agent changes a tracked file and stages it; the genuine pass is now stale.
printf '// tampered\n' >>"$REPO/a.go"
gitc "$REPO" add -u
bl verify --check
expect_rc "control: after an edit the genuine pass is stale (10)" 10

forge_record "$genuine" "20990101T000000-f0f0f0" "1111111111111111111111111111111111111111"
bl verify --check
expect_rc "a hand-written pass record with an arbitrary S_end is still stale (10)" 10

true_c="$(oracle_tree "$REPO")"
[ -n "$true_c" ] || die "cannot compute the oracle tree"
forge_record "$genuine" "20990101T000001-f1f1f1" "$true_c"
bl verify --check
if [ "$BL_RC" = 0 ]; then
  ok "GAP (T0 detects, cannot prevent): a forger who first computes the true C and writes it into the record passes --check (0)"
else
  bad "gap row changed: the record forged with the true C is no longer accepted (exit $BL_RC)" \
    "the T0 boundary moved: update docs/trust-model.md §2.1/§3 and this row"
fi

# ----------------------------------------------------------- ConfigChange ---

section "ConfigChange audit"
cc_files=0
for sample in "$SAMPLES"/*ConfigChange*.json; do
  [ -f "$sample" ] || continue
  cc_files=$((cc_files + 1))
  body="$(sed -e "s#<home>#$(native "$SB_HOME")#g" -e "s#<repo>#$(native "$REPO")#g" "$sample")"
  hook claude-code ConfigChange "$body"
  expect_allow "ConfigChange sample $(basename "$sample") never blocks (exit 0, empty stdout)"
done
check_int_ge "the M0b ConfigChange samples were found" "$cc_files" 1
n_all="$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=config_change)"
check_eq "one config_change audit line per sample" "$n_all" "$cc_files"
n_prot="$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=config_change detail.protected=true)"
check_int_ge "Claude settings files are audited as protected:true" "$n_prot" 1
check_eq "the audit carries the change source" \
  "$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=config_change detail.source=user_settings)" 1
check_true "every audit line is one complete JSON object" "$ACCUTIL" jsonl valid "$BALLAST_HOME/audit"

finish
