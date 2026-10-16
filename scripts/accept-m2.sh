#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# accept-m2.sh - milestone M2 (evidence), from the plan's milestone table:
#
#   unit tests of snapshot, evidence, project, runner
#   freshness.sh        11 counter-examples, 3 positives, S/C consistency
#   matrix.sh --mode stub   9 stacks x {success, failure} with stub toolchains
#                           (the real-toolchain mode is its own CI job; set
#                           BALLAST_ACCEPT_MATRIX_REAL=1 to run it here too)
#   commit bench        the `git commit` check scenarios (1k / 10k files)
#
# No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init accept-m2
shared_binaries

sandbox_new gotest
section "unit tests"
rc=0
# -timeout 30m: the snapshot package takes 3-9 minutes on a hosted Windows runner
# (git and Defender on thousands of small files), close to go test's 10 minute default.
(cd "$REPO_ROOT" && gocmd test -timeout 30m -count=1 ./internal/snapshot ./internal/evidence ./internal/project ./internal/runner) || rc=$?
if [ "$rc" = 0 ]; then ok "go test snapshot evidence project runner"; else bad "go test snapshot evidence project runner" "exit $rc"; fi

run_child "freshness.sh" freshness.sh
run_child "matrix.sh --mode stub" matrix.sh --mode stub
if [ "${BALLAST_ACCEPT_MATRIX_REAL:-0}" = 1 ]; then
  run_child "matrix.sh --mode real" matrix.sh --mode real
else
  skip "matrix.sh --mode real" "own CI job; BALLAST_ACCEPT_MATRIX_REAL=1 runs it here"
fi
# Scenario 6 of the plan's B10 plus its `commit -a` variants: 6a/6b are
# `git commit` on 1k/10k files (C), 6c/6d `git commit -a` (S, worktree snapshot).
run_bench "bench: git commit check (1k and 10k files)" -only '^6[abcd]$'

finish
