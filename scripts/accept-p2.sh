#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# accept-p2.sh - P2 integration acceptance (docs/p2-design.md §10). No
# arguments; any failed assertion exits non-zero. Three platforms; the parts
# that need Docker or a client are an explicit SKIP when the prerequisite is
# missing and a FAILURE when the matching BALLAST_REQUIRE_* variable is set
# (the Linux CI job is to set BALLAST_REQUIRE_DOCKER=1 once impl/ci-fix lands;
# docs/p2-design.md section 11).
#
#   1. go build, go vet (host and GOOS=windows), go test ./internal/archtest,
#      go test ./...
#   2. go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 60s
#   3. `ballast report --out` end to end: every exit code, and the file passes
#      report.Validate with seeded secrets / paths / ids absent
#   4. scripts/boundary-t0.sh
#   5. scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner, the
#      root-only TestBwrapPathDirectoryMustBeTrusted (BALLAST_DOCKER_USER=0:0)
#      and scripts/boundary-t1.sh (Docker)
#   6. go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json; the Hermes
#      cases (docs/facts/hermes-cases.json) through the checker's hygiene scan
#      and the adapter's evidence test (the M0b checker's case list is M0b's)
#   7. bench: scenario 12 (a faked-T1 canonical verify), 13a/13b/14 (forced redirect
#      lock) and the ConfigChange scenario, p95 <= 30ms, plus every earlier scenario
#   8. hygiene of the added artifacts: no local absolute paths, uids or secrets
#      (the CI patterns), and docs/clients.md says "触发时机未核验"
#
# Split-run switches for CI (each prints an explicit SKIP, never silent):
#   BALLAST_ACCEPT_SKIP_GOTEST=1   item 1's `go test ./...`
#   BALLAST_ACCEPT_SKIP_FUZZ=1     item 2
#   BALLAST_ACCEPT_SKIP_BENCH=1    item 7 (the caller runs scripts/bench.sh itself)
#   BALLAST_ACCEPT_SKIP_DOCKER=1   item 5 (the caller runs the Docker scripts itself)
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init accept-p2
shared_binaries

# ---------------------------------------------------------------- 1. build ---
static_checks
run_go "go test ./internal/archtest" test -count=1 ./internal/archtest
if [ "${BALLAST_ACCEPT_SKIP_GOTEST:-0}" = 1 ]; then
  skip "go test ./..." "BALLAST_ACCEPT_SKIP_GOTEST=1 (the caller runs it in its own step)"
else
  run_go "go test ./..." test -count=1 ./...
fi

# ----------------------------------------------------------------- 2. fuzz ---
section "redact fuzz (60s)"
if [ "${BALLAST_ACCEPT_SKIP_FUZZ:-0}" = 1 ]; then
  skip "go test ./internal/redact -fuzz FuzzString -fuzztime 60s" "BALLAST_ACCEPT_SKIP_FUZZ=1"
else
  run_go "fuzz FuzzString 60s" test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 60s
fi

# ------------------------------------------------------------ 3. report e2e ---
section "ballast report --out end to end"
REPORTCHECK="$TMP_ROOT/bin/reportcheck$EXE"
(cd "$REPO_ROOT" && gocmd build -trimpath -o "$REPORTCHECK" ./scripts/boundary/reportcheck) || die "cannot build scripts/boundary/reportcheck"
sandbox_new report
SECRET="ghp_$(printf 'A1b2C3d4E5%.0s' 1 2 3 4)"
mkdir -p "$BALLAST_HOME/audit"
month="$(date -u +%Y-%m)"
now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '%s\n' \
  "{\"v\":1,\"ts\":\"$now\",\"type\":\"hit\",\"session_id\":\"sess-SECRETID-1\",\"repo_inst\":\"repo-SECRETINST\",\"client\":\"claude-code\",\"tier\":\"T0\",\"rule_id\":\"read-before-edit\",\"rule_layer\":\"user\",\"decision\":\"deny\",\"matched\":\"token $SECRET\",\"reason\":\"see /home/jdoe/private/file.go\",\"latency_us\":2500}" \
  "{\"v\":1,\"ts\":\"$now\",\"type\":\"hit\",\"session_id\":\"sess-SECRETID-2\",\"repo_inst\":\"repo-SECRETINST\",\"client\":\"codex\",\"tier\":\"T0\",\"rule_id\":\"my-private-rule-name\",\"rule_layer\":\"repo\",\"decision\":\"log\",\"latency_us\":1800}" \
  "{\"v\":1,\"ts\":\"$now\",\"type\":\"diag\",\"session_id\":\"sess-SECRETID-1\",\"client\":\"claude-code\",\"tier\":\"T0\",\"detail\":{\"diag\":\"state_corrupt\"}}" \
  "{\"v\":1,\"ts\":\"$now\",\"type\":\"diag\",\"session_id\":\"sess-SECRETID-1\",\"client\":\"claude-code\",\"tier\":\"T0\",\"detail\":{\"diag\":\"not-a-frozen-name-/home/jdoe\"}}" \
  >"$BALLAST_HOME/audit/$month.jsonl"
OUT_R="$SB_ROOT/out/report.json"
bl report
expect_rc "report without --out (2)" 2
bl report --out "$(native "$OUT_R")" --since yesterday
expect_rc "report with a bad --since (2)" 2
check_no_file "a usage error wrote nothing" "$OUT_R"
bl report --out "$(native "$OUT_R")"
expect_rc "report --out writes the file (0)" 0
check_file "the report file exists" "$OUT_R"
check_true "the report passes report.Validate and holds no seeded id, path or secret" "$REPORTCHECK" \
  -forbid SECRETID -forbid SECRETINST -forbid "$SECRET" -forbid jdoe -forbid my-private-rule-name -forbid not-a-frozen-name "$(native "$OUT_R")"
check_contains "shipped rule ids appear verbatim" "$(cat "$OUT_R")" "read-before-edit"
bl report --out "$(native "$OUT_R")"
expect_rc "an existing file is refused without --force (4)" 4
bl report --out "$(native "$OUT_R")" --force --since 7d
expect_rc "--force overwrites (0)" 0
blocker="$SB_ROOT/blocker"
write_file "$blocker" "x"
bl report --out "$(native "$blocker")/report.json"
expect_rc "an I/O error (parent is a file) (5)" 5
# exit 1 (the privacy self-check failing) is a bug, never an input; the
# mapping is covered by TestReportWriteExit in internal/cli.
run_go "go test ./internal/cli -run 'TestReport|TestShippedRuleIDs'" test -count=1 ./internal/cli -run 'TestReport|TestShippedRuleIDs'

# ------------------------------------------------------------- 4. boundary-t0 ---
run_child "boundary-t0.sh" boundary-t0.sh

# --------------------------------------------------------------- 5. Docker ---
section "Docker: sandbox-docker.sh and boundary-t1.sh"
if [ "${BALLAST_ACCEPT_SKIP_DOCKER:-0}" = 1 ]; then
  skip "sandbox-docker.sh ./internal/sandbox ./internal/runner" "BALLAST_ACCEPT_SKIP_DOCKER=1"
  skip "boundary-t1.sh" "BALLAST_ACCEPT_SKIP_DOCKER=1"
else
  out="$(mktmp sandbox-docker)"
  rc=0
  bash "$LIB_DIR/sandbox-docker.sh" ./internal/sandbox ./internal/runner >"$out" 2>&1 || rc=$?
  tail -n 5 "$out"
  if [ "$rc" != 0 ]; then
    bad "scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner" "exit $rc"
  elif grep -q 'SKIP: Docker is not available' "$out"; then
    skip "scripts/sandbox-docker.sh" "Docker is not available (BALLAST_REQUIRE_DOCKER=1 makes this a failure)"
  else
    ok "scripts/sandbox-docker.sh ./internal/sandbox ./internal/runner"
  fi
  # The one test that needs root (it creates root-owned files) cannot run in the
  # uid-1000 pass above: run it once as root and require that it really ran.
  out_root="$(mktmp sandbox-docker-root)"
  rc=0
  BALLAST_DOCKER_USER=0:0 bash "$LIB_DIR/sandbox-docker.sh" ./internal/sandbox -run 'TestBwrapPathDirectoryMustBeTrusted' -v >"$out_root" 2>&1 || rc=$?
  tail -n 3 "$out_root"
  if grep -q 'SKIP: Docker is not available' "$out_root"; then
    skip "sandbox-docker.sh as root: TestBwrapPathDirectoryMustBeTrusted" "Docker is not available (BALLAST_REQUIRE_DOCKER=1 makes this a failure)"
  elif [ "$rc" = 0 ] && grep -q -- '--- PASS: TestBwrapPathDirectoryMustBeTrusted' "$out_root"; then
    ok "sandbox-docker.sh as root: TestBwrapPathDirectoryMustBeTrusted ran and passed (not skipped)"
  else
    bad "sandbox-docker.sh as root: TestBwrapPathDirectoryMustBeTrusted ran and passed" "exit $rc; $(grep -E -- '--- (SKIP|FAIL)' "$out_root" | head -n 2 | tr '\n' ' ')"
  fi
  run_child "boundary-t1.sh" boundary-t1.sh
fi

# ----------------------------------------------------- 6. M0b / Hermes cases ---
run_go "M0b cases: go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json" run ./scripts/probe-m0b/check docs/facts/m0b-cases.json
run_go "Hermes cases: hygiene scan (paths, secrets)" run ./scripts/probe-m0b/check --hygiene-only docs/facts/hermes-cases.json
run_go "Hermes cases: every Verified row is covered by a pass case" test -count=1 ./internal/adapter/hermes -run 'Evidence'

# -------------------------------------------------------------------- 7. bench ---
section "bench"
if [ "${BALLAST_ACCEPT_SKIP_BENCH:-0}" = 1 ]; then
  skip "bench (scenarios 12, 13a/b, 14a/b, ConfigChange and all earlier ones)" "BALLAST_ACCEPT_SKIP_BENCH=1 (the caller runs scripts/bench.sh itself)"
else
  run_bench "bench: P2 scenarios 12, 13a, 13b, 14a, 14b and ConfigChange (p95 <= 30ms)" -only '^(12|13a|13b|14a|14b|C-configchange)$'
  run_bench "bench: every earlier scenario still meets the budget with Locked in place" -only '^([1-9]|1[01]|5b|6[a-d]|8[bc]|C-.*)$'
fi

# ---------------------------------------------------------------- 8. hygiene ---
section "hygiene of the added artifacts"
cd "$REPO_ROOT"
if git rev-parse --git-dir >/dev/null 2>&1; then
  paths_hit="$(git grep --untracked -nIE '(^|[^A-Za-z:}])/Users/[a-z][A-Za-z0-9._-]*/(NewCode|Documents|Desktop|Library|\.claude|\.ballast)' -- . ':!internal/*_test.go' ':!.github/workflows/ci.yml' || true)"
  check_eq "no local absolute paths" "$paths_hit" ""
  uid_hit="$(git grep --untracked -nIE '[~]/NewCode|claude-[0-9]+-' -- . ':!.github/workflows/ci.yml' ':!scripts/accept-p2.sh' || true)"
  check_eq "no local directory layout or uid fragments" "$uid_hit" ""
  # The whole repository, not only docs/facts: a real id written back as a "must be flagged"
  # counter-example in a checker test is the same leak. The only exemption is the explicit
  # synthetic range 00000000-0000-4000-8000-0000000000xx (shaped like an id, belongs to nobody).
  sid_hit="$(git grep --untracked -nIE '"(session_id|prompt_id)" *: *"[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}"|/[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\.jsonl' -- . | grep -v '00000000-0000-4000-8000-0000000000' || true)"
  check_eq "no real client session ids anywhere in the repository (session_id / prompt_id are <session-N> / <prompt-N>; only the synthetic 00000000-0000-4000-8000-0000000000xx range is exempt)" "$sid_hit" ""
  secret_hit="$(git grep --untracked -nIE 'sk-ant-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|xox[abprs]-[A-Za-z0-9-]{10,}' -- . ':!internal/redact' ':!internal/audit' ':!internal/report' ':!internal/hook' ':!internal/*_test.go' ':!scripts/boundary-t0.sh' ':!scripts/accept-p2.sh' ':!scripts/probe-m0b' ':!schema/examples' || true)"
  check_eq "no credentials or secrets outside the redaction fixtures" "$secret_hit" ""
else
  skip "git grep hygiene" "not inside a git work tree"
fi
check_contains "docs/clients.md says the Hermes trigger timing is unverified" "$(cat "$REPO_ROOT/docs/clients.md")" "触发时机未核验"
check_contains "docs/trust-model.md registers the Hermes execute_code gap" "$(cat "$REPO_ROOT/docs/trust-model.md")" "execute_code"

finish
