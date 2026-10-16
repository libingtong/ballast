#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# accept-m4.sh - milestone M4 core (install, uninstall, org template, doctor):
#
#   * unit tests of internal/install
#   * install-e2e.sh (also under a ~300 byte TMPDIR): all four clients - round trip with an empty diff,
#     idempotence, the user's own hooks survive, a missing original file,
#     refusal under a managed deployment and on unparsable configuration,
#     backup cap
#   * `ballast org render` prints the T1 template as ONE pure JSON document
#     (the managed-settings fragment; --part org-json prints org.json
#     alone) and refuses --write for non-admins; `ballast doctor` reports
#     the tier honestly
#
# The container-based T1 boundary tests (boundary-t1.sh / boundary-t0.sh) are
# P2 in the plan and are not part of this script. No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init accept-m4
shared_binaries

sandbox_new gotest
section "unit tests"
rc=0
(cd "$REPO_ROOT" && gocmd test -count=1 ./internal/install) || rc=$?
if [ "$rc" = 0 ]; then ok "go test ./internal/install"; else bad "go test ./internal/install" "exit $rc"; fi

run_child "install-e2e.sh" install-e2e.sh

# Regression: backup file names used to embed the whole config path, so a deep
# TMPDIR (or CLAUDE_CONFIG_DIR) pushed them past NAME_MAX and install failed
# with ENAMETOOLONG. Run the whole install e2e again under a ~300 byte path.
section "deep TMPDIR"
if [ "$IS_WINDOWS" = 1 ]; then
  skip "install-e2e.sh under a deep TMPDIR" "MAX_PATH on Windows"
else
  deep_tmp="$TMP_ROOT/deep"
  for _i in 1 2 3; do deep_tmp="$deep_tmp/$(printf 'd%.0s' $(seq 1 90))"; done
  mkdir -p "$deep_tmp"
  saved_tmpdir="${TMPDIR-}"
  export TMPDIR="$deep_tmp"
  run_child "install-e2e.sh (TMPDIR of ${#deep_tmp} bytes)" install-e2e.sh
  if [ -n "$saved_tmpdir" ]; then export TMPDIR="$saved_tmpdir"; else unset TMPDIR; fi
fi

sandbox_new org
section "org render"
bl org render --binary /usr/local/libexec/ballast/ballast
expect_rc "org render" 0
check_true "the template is JSON" "$ACCUTIL" json valid "$BL_OUT_FILE"
check_eq "allowManagedHooksOnly" "$("$ACCUTIL" json get "$BL_OUT_FILE" allowManagedHooksOnly)" "true"
check_eq "sandbox.enabled" "$("$ACCUTIL" json get "$BL_OUT_FILE" sandbox.enabled)" "true"
check_eq "sandbox.failIfUnavailable" "$("$ACCUTIL" json get "$BL_OUT_FILE" sandbox.failIfUnavailable)" "true"
check_eq "allowManagedPermissionRulesOnly" "$("$ACCUTIL" json get "$BL_OUT_FILE" allowManagedPermissionRulesOnly)" "true"
check_int_ge "one managed hook per Claude Code event" "$("$ACCUTIL" json len "$BL_OUT_FILE" hooks)" 9
check_eq "permissions.deny uses the // absolute form" "$("$ACCUTIL" json get "$BL_OUT_FILE" permissions.deny.4)" "Edit(//usr/local/libexec/ballast/**)"
check_false "no Write(...) / NotebookEdit(...) permission entries" grep -Eq '"(Write|NotebookEdit)\(' "$BL_OUT_FILE"
check_no_file "printing the template wrote no managed settings" "$SB_MANAGED/managed-settings.d"
cp "$BL_OUT_FILE" "$TMP_ROOT/org-render-default.json"
bl org render --binary /usr/local/libexec/ballast/ballast --part managed-settings
expect_rc "org render --part managed-settings" 0
check_eq "--part managed-settings is the default" "$(cat "$BL_OUT_FILE")" "$(cat "$TMP_ROOT/org-render-default.json")"
bl org render --part org-json
expect_rc "org render --part org-json" 0
check_true "org.json is a single JSON document" "$ACCUTIL" json valid "$BL_OUT_FILE"
check_eq "org.json tier" "$("$ACCUTIL" json get "$BL_OUT_FILE" tier)" "T1"
check_eq "org.json runner_sandbox" "$("$ACCUTIL" json get "$BL_OUT_FILE" runner_sandbox)" "require"
bl org render --part bogus
expect_rc "an unknown --part is a usage error (2)" 2
bl org render --write
expect_rc "--write without --i-am-admin is refused (4)" 4
if [ "$(id -u 2>/dev/null || echo 1)" != 0 ]; then
  bl org render --write --i-am-admin
  expect_rc "--write --i-am-admin without root is refused (4)" 4
  check_no_file "and nothing was written" "$SB_MANAGED/managed-settings.d"
fi
bl org render --client codex
expect_rc "org render for a client without a managed template (2)" 2

section "doctor"
bl doctor
expect_rc "doctor on a machine with nothing installed only warns" 0
expect_out_has "doctor states the tier" "层级: T0"
expect_out_has "and that CI is the final arbiter" "CI 是最终裁判"

finish
