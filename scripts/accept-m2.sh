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
(cd "$REPO_ROOT" && gocmd test -count=1 ./internal/snapshot ./internal/evidence ./internal/project ./internal/runner) || rc=$?
if [ "$rc" = 0 ]; then ok "go test snapshot evidence project runner"; else bad "go test snapshot evidence project runner" "exit $rc"; fi

run_child "freshness.sh" freshness.sh
run_child "matrix.sh --mode stub" matrix.sh --mode stub
if [ "${BALLAST_ACCEPT_MATRIX_REAL:-0}" = 1 ]; then
  run_child "matrix.sh --mode real" matrix.sh --mode real
else
  skip "matrix.sh --mode real" "own CI job; BALLAST_ACCEPT_MATRIX_REAL=1 runs it here"
fi
run_bench "bench: git commit check (1k and 10k files)" -scenario commit

finish
