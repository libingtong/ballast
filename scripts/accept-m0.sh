#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# accept-m0.sh - milestone M0 (fact gathering and the Go performance gate).
#
#   * docs/facts/cases.json: every required case id has events, schema_keys,
#     assertion and a result (pass or unverifiable), its evidence directories
#     exist;
#   * every plan A-row still waiting for M0a has a conclusion row in
#     docs/facts/M0a-CONCLUSIONS.md;
#   * the M0c summary states a worst hook p95 not above 30 ms (otherwise stop
#     and report - the gate is not relaxed);
#   * the prototype (proto/) still builds and vets on every platform.
#
# No arguments. Reads the repository only.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init accept-m0
build_tools

section "M0a facts and M0c gate"
if out="$("$ACCUTIL" m0-check "$REPO_ROOT" 2>&1)"; then
  ok "docs/facts is complete"
  printf '%s\n' "$out" | sed 's/^/     /'
else
  bad "docs/facts is complete" "$out"
fi

section "the payload samples the cases cite"
n="$(find "$REPO_ROOT/docs/facts/payloads" -type f -name '*.json' | wc -l | tr -d ' ')"
check_int_ge "docs/facts/payloads holds the captured payload files" "$n" 100
bad_json=0
while IFS= read -r f; do
  "$ACCUTIL" json valid "$f" >/dev/null 2>&1 || { bad_json=$((bad_json + 1)); note "not JSON: ${f#"$REPO_ROOT"/}"; }
done < <(find "$REPO_ROOT/docs/facts/payloads" -type f -name '*.json')
check_eq "every payload file is valid JSON" "$bad_json" "0"

section "prototype"
run_go "go vet ./proto/..." vet ./proto/...
rc=0
(cd "$REPO_ROOT" && GOOS=windows gocmd vet ./proto/...) || rc=$?
if [ "$rc" = 0 ]; then ok "GOOS=windows go vet ./proto/..."; else bad "GOOS=windows go vet ./proto/..." "exit $rc"; fi

finish
