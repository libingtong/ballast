#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# freshness.sh - plan B3 freshness suite, driven through a REAL git
# pre-commit hook that calls `ballast verify --check --commit`.
#
#   11 counter-examples  (evidence must be judged stale / missing / running,
#                         and the real `git commit` must be blocked, HEAD unmoved)
#    3 positive examples (the commit goes through)
#    S/C consistency     (the tree the runner records for the work tree and the
#                         tree --check computes for the commit agree with each
#                         other and with an independent git oracle)
#
# The verify command is `accutil ctl <dir>`: a scripted command steered by files
# (pass, fail, block until released), so no toolchain and no tracked-file edit is
# needed to change an outcome. No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init freshness
build_ballast
sandbox_new freshness

CTL_BIN="$(native "$ACCUTIL")"
JOB=""

# fresh_repo NAME [TIMEOUT_S] [WITH_LINT]: a committed repository with the
# pre-commit hook installed; leaves the shell inside it.
#   REPO = repo dir, CTL = control root (CTL/test, CTL/lint)
fresh_repo() {
  local name=$1 tmo=${2:-} lint=${3:-0}
  REPO="$SB_WORK/$name"
  CTL="$SB_WORK/ctl-$name"
  mkdir -p "$CTL/test" "$CTL/lint"
  git_init "$REPO"
  write_file "$REPO/a.go" "package a
"
  write_file "$REPO/b.go" "package a
"
  write_file "$REPO/README.md" "# readme
"
  write_file "$REPO/docs/guide.md" "guide
"
  mkdir -p "$REPO/.ballast"
  local test_entry ctl_n
  ctl_n="$(native "$CTL")"
  test_entry="test|[\"$CTL_BIN\",\"ctl\",\"$ctl_n/test\"]|true|$tmo"
  if [ "$lint" = 1 ]; then
    project_json_write "$REPO/.ballast/project.json" '["**/*.md","docs/**"]' "$test_entry" \
      "lint|[\"$CTL_BIN\",\"ctl\",\"$ctl_n/lint\"]|false"
  else
    project_json_write "$REPO/.ballast/project.json" '["**/*.md","docs/**"]' "$test_entry"
  fi
  gitc "$REPO" add -A
  gitc "$REPO" commit -q -m init
  write_file "$REPO/.git/hooks/pre-commit" "#!/bin/sh
exec \"$(native "$BALLAST_BIN")\" verify --check --commit
"
  chmod +x "$REPO/.git/hooks/pre-commit"
  cd "$REPO"
}

# start_verify [NAME...]: begin a background verify, set JOB.
start_verify() {
  bl verify "$@"
  JOB="$(printf '%s\n' "$BL_OUT" | sed -n 's/^job=\([^ ]*\).*/\1/p' | head -n 1)"
  [ "$BL_RC" = 0 ] && [ -n "$JOB" ] || die "verify did not start: rc=$BL_RC out=$BL_OUT err=$BL_ERR"
}

# wait_final JOB: poll until the job is no longer running; BL_RC = outcome status.
wait_final() {
  local i=0
  while [ "$i" -lt 400 ]; do
    bl verify --status "$1"
    if [ "$BL_RC" != 6 ] && [ "$BL_RC" != 11 ]; then
      return 0
    fi
    "$ACCUTIL" sleep 0.1
    i=$((i + 1))
  done
  die "job $1 did not finish: rc=$BL_RC"
}

# verify_pass: run verify test to a passing record.
verify_pass() {
  rm -f "$CTL/test/hold" "$CTL/test/exit"
  start_verify test
  wait_final "$JOB"
  [ "$BL_RC" = 0 ] || die "expected a passing verify, got status $BL_RC: $BL_OUT"
}

# check_rc NAME WANT [--worktree]: ballast verify --check must exit WANT.
check_rc() {
  local name=$1 want=$2
  shift 2
  bl verify --check "$@"
  expect_rc "$name" "$want"
}

# commit_try ARGS...: run git commit; sets C_RC, C_OUT, HEAD_BEFORE, HEAD_AFTER.
commit_try() {
  HEAD_BEFORE="$(git rev-parse HEAD)"
  C_RC=0
  C_OUT="$(git commit -q "$@" 2>&1)" || C_RC=$?
  HEAD_AFTER="$(git rev-parse HEAD)"
}

expect_blocked() {
  local name=$1
  shift
  commit_try "$@"
  if [ "$C_RC" != 0 ] && [ "$HEAD_BEFORE" = "$HEAD_AFTER" ]; then
    ok "$name: the real git commit is blocked by the pre-commit hook"
  else
    bad "$name: the real git commit must be blocked" "git commit exit $C_RC, HEAD moved: $([ "$HEAD_BEFORE" != "$HEAD_AFTER" ] && echo yes || echo no)" "$C_OUT"
  fi
}

expect_committed() {
  local name=$1
  shift
  commit_try "$@"
  if [ "$C_RC" = 0 ] && [ "$HEAD_BEFORE" != "$HEAD_AFTER" ]; then
    ok "$name: the commit goes through"
  else
    bad "$name: the commit must go through" "git commit exit $C_RC" "$C_OUT"
  fi
}

append() { printf '%s\n' "$2" >>"$1"; }

# ===========================================================================
section "counter-example 1: another session modifies a file after verify"
fresh_repo sc01
verify_pass
check_rc "SC01 control: right after verify the evidence is fresh" 0
append a.go "// edited by another session"
gitc "$REPO" add a.go
check_rc "SC01 staged edit after verify -> stale (10)" 10
check_rc "SC01 the work tree is stale too" 10 --worktree
expect_blocked "SC01" -m "x"

section "counter-example 2: git checkout / git reset after verify"
fresh_repo sc02
append a.go "// second commit"
gitc "$REPO" add a.go
commit_try --no-verify -m second
SECOND="$(git rev-parse HEAD)"
verify_pass
check_rc "SC02 control: evidence matches the second commit" 0
git reset -q --hard HEAD~1
check_rc "SC02 git reset --hard HEAD~1 -> stale (10)" 10
expect_blocked "SC02 after reset" --allow-empty -m "x"
git reset -q --hard "$SECOND"
check_rc "SC02 back on the verified commit -> fresh again" 0
git checkout -q --detach HEAD~1
check_rc "SC02 git checkout of another commit -> stale (10)" 10
expect_blocked "SC02 after checkout" --allow-empty -m "x"

section "counter-example 3: a file is modified while verify is running"
fresh_repo sc03
touch "$CTL/test/hold"
start_verify test
"$ACCUTIL" wait-file "$CTL/test/started" 20 || die "the verify command did not start"
append a.go "// edited while verify runs"
touch "$CTL/test/release"
wait_final "$JOB"
expect_rc "SC03 the command itself passed (status 0)" 0
bl verify --status "$JOB"
expect_out_has "SC03 but the pass is not valid evidence (S_start != S_end)" "tree changed while running"
bl verify --status --json "$JOB"
check_false "SC03 --json does not claim valid_pass either" "$ACCUTIL" json has "$BL_OUT_FILE" jobs.0.valid_pass
gitc "$REPO" add -u
check_rc "SC03 an invalid pass is no evidence -> stale (10, not_valid_pass)" 10
expect_err_has "SC03 the reason is named" "not_valid_pass"
expect_blocked "SC03" -m "x"

section "counter-example 4: a failing verify after a passing one"
fresh_repo sc04
verify_pass
check_rc "SC04 control: fresh after the pass" 0
printf '1' >"$CTL/test/exit"
start_verify test
wait_final "$JOB"
expect_rc "SC04 the second verify failed (status 1)" 1
check_rc "SC04 the latest record is a failure -> stale (10, not_valid_pass)" 10
expect_err_has "SC04 the reason is named" "not_valid_pass"
expect_blocked "SC04" --allow-empty -m "x"

section "counter-example 5: only a non-required verify passed"
fresh_repo sc05 "" 1
start_verify lint
wait_final "$JOB"
expect_rc "SC05 lint passed" 0
check_rc "SC05 the required entry has no evidence -> 11" 11
expect_blocked "SC05" --allow-empty -m "x"

section "counter-example 6: verified in worktree A, committing in worktree B"
fresh_repo sc06
verify_pass
check_rc "SC06 control: worktree A is fresh" 0
gitc "$REPO" worktree add -q "$SB_WORK/sc06-wtB" -b wtb
cd "$SB_WORK/sc06-wtB"
check_rc "SC06 worktree B (same content) has no evidence -> 11" 11
expect_blocked "SC06 in worktree B" --allow-empty -m "x"
cd "$REPO"

section "counter-example 7: git -C other commit"
fresh_repo sc07a
verify_pass
A_REPO="$REPO"
fresh_repo sc07b
B_REPO="$REPO"
B_CTL="$CTL"
cd "$A_REPO"
check_rc "SC07 control: repository A is fresh" 0
HEAD_BEFORE="$(git -C "$B_REPO" rev-parse HEAD)"
C_RC=0
C_OUT="$(git -C "$B_REPO" commit -q --allow-empty -m x 2>&1)" || C_RC=$?
if [ "$C_RC" != 0 ] && [ "$HEAD_BEFORE" = "$(git -C "$B_REPO" rev-parse HEAD)" ]; then
  ok "SC07 git -C ../other commit is judged by the OTHER repository's evidence and blocked"
else
  bad "SC07 git -C ../other commit must be blocked" "exit $C_RC" "$C_OUT"
fi
CTL="$B_CTL"
REPO="$B_REPO"
cd "$B_REPO"
verify_pass
HEAD_BEFORE="$(git rev-parse HEAD)"
if git -C "$B_REPO" commit -q --allow-empty -m x 2>/dev/null; then
  ok "SC07 control: after verifying the other repository its commit passes"
else
  bad "SC07 control: the other repository commits after its own verify"
fi

section "counter-example 8: verify times out"
fresh_repo sc08 1
touch "$CTL/test/hold"
start_verify test
wait_final "$JOB"
expect_rc "SC08 the job ends as timeout (status 3)" 3
check_rc "SC08 a timed-out verify is no evidence -> stale (10, not_valid_pass)" 10
expect_err_has "SC08 the reason is named" "not_valid_pass"
expect_blocked "SC08" --allow-empty -m "x"

section "counter-example 9: partial staging with a plain git commit"
fresh_repo sc09
append a.go "// staged"
append b.go "// not staged"
verify_pass
gitc "$REPO" add a.go
check_rc "SC09 staged a.go only -> the index differs from the verified tree (10)" 10
expect_blocked "SC09" -m "x"

section "counter-example 10: a new file is added after verify"
fresh_repo sc10
verify_pass
write_file "$REPO/new.go" "package a
"
check_rc "SC10 an untracked file is outside the snapshot (still fresh)" 0
gitc "$REPO" add new.go
check_rc "SC10 once added it is part of the commit tree -> stale (10)" 10
expect_blocked "SC10" -m "x"

section "counter-example 11: a job of the same name is still running"
fresh_repo sc11
verify_pass
touch "$CTL/test/hold"
start_verify test
"$ACCUTIL" wait-file "$CTL/test/started" 20 || die "the verify command did not start"
check_rc "SC11 running job -> 13, never fresh" 13
check_rc "SC11 also for the work tree" 13 --worktree
expect_blocked "SC11" --allow-empty -m "x"
touch "$CTL/test/release"
wait_final "$JOB"
expect_rc "SC11 the job finishes" 0
check_rc "SC11 after it finished the evidence is fresh" 0

# ===========================================================================
section "positive 1: only exempt files changed after verify"
fresh_repo p01
verify_pass
append README.md "more words"
append docs/guide.md "more guide"
gitc "$REPO" add README.md docs/guide.md
check_rc "P01 exempt-only change -> still fresh" 0
expect_committed "P01" -m "docs only"

section "positive 2: git add -u, then a plain git commit"
fresh_repo p02
append a.go "// edit"
verify_pass
check_rc "P02 index (without the edit) != verified tree -> stale before git add -u" 10
gitc "$REPO" add -u
check_rc "P02 after git add -u the index equals the verified tree" 0
expect_committed "P02" -m "all staged"

section "positive 3: git commit -a"
fresh_repo p03
append a.go "// edit"
verify_pass
expect_committed "P03 commit -a after verify" -a -m "commit all"
append b.go "// unverified edit"
expect_blocked "P03 control: commit -a of an unverified edit" -a -m "no evidence for this"

# ===========================================================================
section "S/C consistency: runner snapshot S, check snapshot C and a git oracle"

# oracle_tree REPO index|worktree: independent computation with git only (plan B3).
oracle_tree() {
  local repo=$1 mode=$2 idx tmp t
  idx="$(git -C "$repo" rev-parse --path-format=absolute --git-path index)"
  tmp="$(mktmp oracle-index)"
  cp "$idx" "$tmp"
  t="$(
    export GIT_INDEX_FILE="$tmp"
    if [ "$mode" = worktree ]; then git -C "$repo" add -u; fi
    git -C "$repo" rm --cached -r -q --ignore-unmatch -- ':(glob)**/*.md' ':(glob)docs/**' >/dev/null
    git -C "$repo" write-tree
  )"
  rm -f "$tmp"
  printf '%s' "$t"
}

# latest_record NAME: path of the newest evidence record of a verify name.
latest_record() {
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

fresh_repo sc_consistency
verify_pass
rec="$(latest_record test)"
S1="$("$ACCUTIL" json get "$rec" s_end)"
check_eq "clean tree: S_start == S_end" "$("$ACCUTIL" json get "$rec" s_start)" "$S1"
check_eq "clean tree: recorded S == git oracle (work tree)" "$S1" "$(oracle_tree "$REPO" worktree)"
check_eq "clean tree: recorded S == git oracle (index)" "$S1" "$(oracle_tree "$REPO" index)"
check_rc "clean tree: C == S, --check --commit is fresh" 0
check_rc "clean tree: and --check --worktree is fresh" 0 --worktree

append a.go "// dirty tracked edit, not staged"
append README.md "// exempt edit, not staged"
verify_pass
rec="$(latest_record test)"
S2="$("$ACCUTIL" json get "$rec" s_end)"
check_ne "a tracked edit changes S" "$S2" "$S1"
check_eq "dirty tree: recorded S == git oracle (add -u + exempt filter)" "$S2" "$(oracle_tree "$REPO" worktree)"
check_rc "dirty tree: --check --worktree computes the same S (fresh)" 0 --worktree
check_rc "dirty tree: the index (C) lacks the unstaged edit (stale)" 10
C_before="$(oracle_tree "$REPO" index)"
check_ne "dirty tree: oracle C != oracle S before staging" "$C_before" "$S2"
gitc "$REPO" add -u
check_eq "after git add -u the oracle C equals S" "$(oracle_tree "$REPO" index)" "$S2"
check_rc "after git add -u: C == S, --check --commit is fresh" 0

write_file "$REPO/README.md" "# readme, rewritten
"
write_file "$REPO/docs/guide.md" "guide, rewritten
"
verify_pass
rec="$(latest_record test)"
check_eq "editing only exempt files leaves S unchanged (one filter for S and C)" "$("$ACCUTIL" json get "$rec" s_end)" "$S2"

# ===========================================================================
section "the audit log"
cd "$SB_WORK"
bl audit lint
expect_rc "audit lint after the freshness suite" 0

finish
