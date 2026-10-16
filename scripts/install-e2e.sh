#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# install-e2e.sh - plan B12 / M4 install and uninstall, for each of the four
# clients (claude-code, codex, cursor, hermes), in a redirected HOME:
#
#   dry-run         writes nothing and prints a diff
#   round trip      install -> uninstall leaves every file byte-identical
#   idempotent      a second install changes no byte (and not the manifest)
#   user hooks      a hook the user already had and one added after the install
#                   both survive the uninstall; an entry the user edited is
#                   kept (exit 1, warning), the rest is removed
#   missing files   no client config at all: install with --force creates what
#                   it needs, uninstall removes it again
#   not detected    without --force a client that is not there is refused (2)
#   managed layer   a managed Ballast deployment refuses the user-level install
#   layouts         non-canonical JSON (tabs, CRLF, blank lines, compact, 0 bytes,
#                   a user's empty event array) keeps every foreign byte and
#                   round-trips byte for byte
#   broken config   unparsable configuration is refused (3) and never written
#   backups         at most 10 are kept, and they hold the original bytes
#   doctor          is happy afterwards
#
# No arguments; never touches a real client configuration.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init install-e2e
build_ballast

BIN_N="$(native "$BALLAST_BIN")"
EMPTY_PATH_DIR="$TMP_ROOT/empty-path"
mkdir -p "$EMPTY_PATH_DIR"

# ---- per-client facts ------------------------------------------------------

cfg_file() { # CLIENT -> the main configuration file
  case $1 in
    claude-code) echo "$SB_HOME/.claude/settings.json" ;;
    codex) echo "$SB_HOME/.codex/hooks.json" ;;
    cursor) echo "$SB_HOME/.cursor/hooks.json" ;;
    hermes) echo "$SB_HOME/.hermes/config.yaml" ;;
  esac
}

seed_client() { # CLIENT: a realistic pre-existing configuration with one foreign hook
  case $1 in
    claude-code)
      write_file "$(cfg_file claude-code)" '{
  "theme": "dark",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/bin/true"}]}
    ]
  }
}
'
      ;;
    codex)
      write_file "$(cfg_file codex)" '{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/true"}]}]}}
'
      write_file "$SB_HOME/.codex/config.toml" 'model = "gpt"

[features]
hooks = true
'
      ;;
    cursor)
      write_file "$(cfg_file cursor)" '{"version":1,"hooks":{"stop":[{"command":"echo hi"}]}}
'
      write_file "$SB_HOME/.cursor/rules/mine.mdc" '---
alwaysApply: true
---
my own rule
'
      ;;
    hermes)
      write_file "$(cfg_file hermes)" 'model: x
hooks:
  pre_tool_call:
    - command: "/usr/bin/true"
      timeout: 5
tail: 1
'
      ;;
  esac
}

foreign_marker() { # CLIENT: text identifying the user's pre-existing hook
  case $1 in cursor) echo "echo hi" ;; *) echo "/usr/bin/true" ;; esac
}

# add_user_hook CLIENT: the user edits the installed file and adds a hook of
# their own (re-encoding is fine: ownership is judged on each entry's hash).
add_user_hook() {
  local f
  f="$(cfg_file "$1")"
  case $1 in
    claude-code) "$ACCUTIL" json append "$f" hooks.PostToolUse '{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/bin/true"}]}' ;;
    codex) "$ACCUTIL" json append "$f" hooks.PostToolUse '{"hooks":[{"type":"command","command":"/usr/bin/true"}]}' ;;
    cursor) "$ACCUTIL" json append "$f" hooks.postToolUse '{"command":"/usr/bin/true"}' ;;
    hermes)
      awk -v ins='  on_session_end_user:\n    - command: "/usr/bin/true"\n      timeout: 5' \
        '{ print } /^hooks:/ && !done { print ins; done = 1 }' "$f" >"$f.new"
      mv "$f.new" "$f"
      ;;
  esac
}

# edit_owned_entry CLIENT: the user changes one entry Ballast installed.
edit_owned_entry() {
  local f
  f="$(cfg_file "$1")"
  case $1 in
    claude-code)
      awk '{ print } !d && /"command": ".*\/ballast(\.exe)?",$/ { print "            \"timeout\": 7,"; d = 1 }' "$f" >"$f.new"
      ;;
    codex) sed 's/"timeout":30/"timeout":31/' "$f" >"$f.new" ;;
    cursor) sed 's/--event sessionStart"/--event sessionStart # mine"/' "$f" >"$f.new" ;;
    hermes) awk '!d && /^      timeout: 30$/ { print "      timeout: 31"; d = 1; next } { print }' "$f" >"$f.new" ;;
  esac
  mv "$f.new" "$f"
}

edit_marker() { # CLIENT: text proving the edited entry is still there
  case $1 in
    claude-code) echo '"timeout": 7,' ;;
    codex) echo '"timeout":31' ;;
    cursor) echo '# mine' ;;
    hermes) echo 'timeout: 31' ;;
  esac
}

# owned_count FILE: how many Ballast hook commands the file holds.
owned_count() {
  local n
  n="$(grep -o -- '--client' "$1" 2>/dev/null | wc -l | tr -d ' ')" || n=0
  echo "${n:-0}"
}

# corrupt_client CLIENT: make the main file unparsable for Ballast.
corrupt_client() {
  case $1 in
    hermes) write_file "$(cfg_file hermes)" 'model: x
hooks: {pre_tool_call: []}
' ;;
    *) write_file "$(cfg_file "$1")" '{ this is not json' ;;
  esac
}

# ---- helpers ---------------------------------------------------------------

snap() { "$ACCUTIL" tree "$SB_HOME" .ballast >"$1"; }

# same_tree NAME A B: two tree listings are identical.
same_tree() {
  if cmp -s "$2" "$3"; then ok "$1"; else bad "$1" "$(diff "$2" "$3" | head -20)"; fi
}

differs_tree() {
  if cmp -s "$2" "$3"; then bad "$1" "the tree did not change"; else ok "$1"; fi
}

new_client_sandbox() { # CLIENT
  sandbox_new "ie-$1"
  cp "$REPO_ROOT/rules/default.json" "$BALLAST_HOME/rules.json"
}

install_c() { bl install --client "$1" --binary "$BIN_N" "${@:2}"; }

count_backups() {
  local n=0 d
  for d in "$BALLAST_HOME"/backups/*/; do
    [ -d "$d" ] && n=$((n + 1))
  done
  echo "$n"
}

# ---- the suite for one client ------------------------------------------------

client_suite() {
  local c=$1 before after1 f mid cfgsave i
  section "client $c: dry-run, round trip, idempotence"
  new_client_sandbox "$c"
  seed_client "$c"
  before="$(mktmp before)"
  after1="$(mktmp after1)"
  mid="$(mktmp mid)"
  snap "$before"
  f="$(cfg_file "$c")"
  cfgsave="$(mktmp seed-copy)"
  cp "$f" "$cfgsave"

  install_c "$c" --dry-run
  expect_rc "$c: install --dry-run" 0
  expect_out_has "$c: --dry-run prints a unified diff" "+++"
  snap "$mid"
  same_tree "$c: --dry-run wrote nothing" "$before" "$mid"
  check_no_file "$c: --dry-run left no manifest" "$BALLAST_HOME/install-manifest.json"

  install_c "$c"
  expect_rc "$c: install" 0
  snap "$after1"
  differs_tree "$c: install changed the client configuration" "$before" "$after1"
  check_file "$c: the manifest was written" "$BALLAST_HOME/install-manifest.json"
  check_contains "$c: hook commands carry the absolute binary path" "$(cat "$f")" "$(printf '%s' "$BIN_N" | sed 's/\\/\\\\/g')"
  check_contains "$c: a hook the user already had is still there" "$(cat "$f")" "$(foreign_marker "$c")"
  if [ "$c" != cursor ]; then
    check_contains "$c: ballast entries were added" "$(cat "$f")" "--client"
  fi

  cp "$BALLAST_HOME/install-manifest.json" "$mid.manifest"
  install_c "$c"
  expect_rc "$c: second install" 0
  expect_out_has "$c: second install reports no change" "无变化"
  snap "$mid"
  same_tree "$c: second install changed no byte" "$after1" "$mid"
  if cmp -s "$BALLAST_HOME/install-manifest.json" "$mid.manifest"; then ok "$c: second install left the manifest untouched"; else bad "$c: second install left the manifest untouched"; fi

  bl doctor
  expect_rc "$c: doctor is happy after the install" 0
  bl doctor --json
  check_eq "$c: doctor --json ok" "$("$ACCUTIL" json get "$BL_OUT_FILE" ok)" "true"

  uninstall_check "$c" "$before"

  section "client $c: backups"
  install_c "$c"
  expect_rc "$c: install for the backup check" 0
  # Backup directories are named by second plus a random suffix, so their sort
  # order is not their age: look for the original bytes in all of them.
  local bfile found=0
  for bfile in "$BALLAST_HOME"/backups/*/*; do
    case "$bfile" in
      *"-$(basename "$f")") if cmp -s "$bfile" "$cfgsave"; then found=1; fi ;;
    esac
  done
  if [ "$found" = 1 ]; then
    ok "$c: a backup holds the original bytes of the file install rewrote"
  else
    bad "$c: a backup holds the original bytes of the file install rewrote" "backups: $(find "$BALLAST_HOME/backups" -type f 2>&1 | wc -l | tr -d ' ') files"
  fi
  bl uninstall --client "$c"
  expect_rc "$c: uninstall after the backup check" 0
  i=0
  while [ "$i" -lt 12 ]; do
    install_c "$c" >/dev/null
    bl uninstall --client "$c"
    i=$((i + 1))
  done
  check_eq "$c: backups are capped at the newest 10 after 25 backing-up runs" "$(count_backups)" "10"

  section "client $c: users' own hooks"
  new_client_sandbox "$c"
  seed_client "$c"
  install_c "$c"
  expect_rc "$c: install" 0
  add_user_hook "$c"
  check_contains "$c: premise: the user's added hook is in the file" "$(cat "$f")" "/usr/bin/true"
  bl uninstall --client "$c"
  expect_rc "$c: uninstall" 0
  check_eq "$c: no Ballast entry is left" "$(owned_count "$f")" "0"
  local trues
  trues="$(grep -o '/usr/bin/true' "$f" | wc -l | tr -d ' ')"
  if [ "$c" = cursor ]; then
    check_eq "$c: the user's added hook survives the uninstall" "$trues" "1"
    check_contains "$c: the pre-existing hook survives too" "$(cat "$f")" "echo hi"
  else
    check_eq "$c: both the pre-existing and the added hook survive the uninstall" "$trues" "2"
  fi

  section "client $c: an entry the user edited is kept"
  new_client_sandbox "$c"
  seed_client "$c"
  install_c "$c"
  edit_owned_entry "$c"
  check_contains "$c: premise: the edit is in the file" "$(cat "$f")" "$(edit_marker "$c")"
  install_c "$c"
  expect_rc "$c: re-install leaves the user-edited entry alone" 0
  check_contains "$c: the edit survives the re-install" "$(cat "$f")" "$(edit_marker "$c")"
  bl uninstall --client "$c"
  expect_rc "$c: uninstall keeps the edited entry and says so (exit 1)" 1
  expect_err_has "$c: with a warning" "warning"
  check_contains "$c: the edited entry is still in the file" "$(cat "$f")" "$(edit_marker "$c")"
  check_eq "$c: every other Ballast entry was removed" "$(owned_count "$f")" "1"

  section "client $c: no configuration at all"
  new_client_sandbox "$c"
  before="$(mktmp before-empty)"
  snap "$before"
  PATH="$EMPTY_PATH_DIR" install_c "$c"
  expect_rc "$c: a client that is not there is refused without --force (2)" 2
  snap "$mid"
  same_tree "$c: the refused install wrote nothing" "$before" "$mid"
  install_c "$c" --force
  expect_rc "$c: install --force creates the files" 0
  snap "$after1"
  differs_tree "$c: files were created" "$before" "$after1"
  bl uninstall --client "$c"
  expect_rc "$c: uninstall" 0
  snap "$mid"
  if [ "$c" = codex ]; then
    # Documented exception: [features] hooks = true is never switched off.
    local residue
    residue="$(diff "$before" "$mid" | grep '^[<>]' | sed 's/^> [0-9a-f]\{64\} /> FILE /' | tr '\n' ';')" || true
    check_eq "$c: only the features.hooks switch remains (documented)" "$residue" "> dir .codex;> FILE .codex/config.toml;"
    check_lacks "$c: and it holds no Ballast text" "$(cat "$SB_HOME/.codex/config.toml")" "ballast"
    check_contains "$c: but keeps hooks = true" "$(cat "$SB_HOME/.codex/config.toml")" "hooks = true"
  else
    same_tree "$c: uninstall removed everything install created (empty diff, directories too)" "$before" "$mid"
  fi

  section "client $c: configuration Ballast cannot parse"
  new_client_sandbox "$c"
  corrupt_client "$c"
  before="$(mktmp before-corrupt)"
  snap "$before"
  install_c "$c"
  expect_rc "$c: install over unparsable configuration is refused (3)" 3
  snap "$mid"
  same_tree "$c: nothing was written" "$before" "$mid"
  new_client_sandbox "$c"
  seed_client "$c"
  install_c "$c"
  expect_rc "$c: install" 0
  corrupt_client "$c"
  snap "$before"
  bl uninstall --client "$c"
  expect_rc "$c: uninstall over unparsable configuration fails loudly (3)" 3
  snap "$mid"
  same_tree "$c: and changes nothing" "$before" "$mid"

  section "client $c: a managed Ballast deployment"
  new_client_sandbox "$c"
  seed_client "$c"
  write_file "$SB_MANAGED/managed-settings.json" '{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/local/libexec/ballast/ballast","args":["hook","--client","claude-code","--event","PreToolUse"]}]}]}}
'
  before="$(mktmp before-managed)"
  snap "$before"
  install_c "$c"
  if [ "$c" = claude-code ]; then
    expect_rc "$c: the user-level install is refused while the managed layer has Ballast (1)" 1
    snap "$mid"
    same_tree "$c: nothing was written" "$before" "$mid"
  else
    expect_rc "$c: the managed layer is Claude Code's; this client installs normally" 0
  fi
}

# uninstall_check CLIENT BEFORE_LISTING: uninstall and expect the pre-install tree.
uninstall_check() {
  local c=$1 before=$2 after
  after="$(mktmp after-uninstall)"
  bl_uninstall "$c"
  expect_rc "$c: uninstall" 0
  snap "$after"
  same_tree "$c: install -> uninstall leaves every file byte-identical (empty diff)" "$before" "$after"
}
bl_uninstall() { bl uninstall --client "$1"; }

for c in claude-code codex cursor hermes; do
  client_suite "$c"
done

# ---- client-specific extras ----------------------------------------------------

section "codex: --codex-trust round trip"
new_client_sandbox codex
seed_client codex
before="$(mktmp before-trust)"
snap "$before"
install_c codex
check_lacks "codex: trust entries are not written by default" "$(cat "$SB_HOME/.codex/config.toml")" "trusted_hash"
install_c codex --codex-trust
expect_rc "codex: install --codex-trust" 0
check_contains "codex: --codex-trust writes trust entries" "$(cat "$SB_HOME/.codex/config.toml")" "trusted_hash"
bl uninstall --client codex
expect_rc "codex: uninstall" 0
after="$(mktmp after-trust)"
snap "$after"
same_tree "codex: the trust round trip leaves no diff" "$before" "$after"

section "codex: a config.toml without a features.hooks key (documented residue)"
new_client_sandbox codex
seed_client codex
# The seed already enables hooks; replace it with a config that does not.
write_file "$SB_HOME/.codex/config.toml" 'model = "gpt"
'
cfg_before="$(cat "$SB_HOME/.codex/config.toml")"
install_c codex
expect_rc "codex: install without a features.hooks key" 0
check_contains "codex: install switched features.hooks on" "$(cat "$SB_HOME/.codex/config.toml")" "hooks = true"
bl uninstall --client codex
expect_rc "codex: uninstall" 0
cfg_after="$(cat "$SB_HOME/.codex/config.toml")"
# Contract (architecture 5.2): the switch Ballast turned on is never switched
# off again, because other hook sources may rely on it. This is the one
# documented exception to the empty round-trip diff.
check_eq "codex: the original text plus the features.hooks switch remain" "$cfg_after" "$cfg_before

[features]
hooks = true"
check_lacks "codex: and no Ballast text remains" "$cfg_after" "ballast"

section "cursor: the managed ballast.mdc"
new_client_sandbox cursor
seed_client cursor
install_c cursor
mdc="$SB_HOME/.cursor/rules/ballast.mdc"
check_file "cursor: ballast.mdc was created" "$mdc"
check_contains "cursor: it is always applied" "$(cat "$mdc")" "alwaysApply: true"
check_contains "cursor: it carries the rule text" "$(cat "$mdc")" "no-long-sleep"
check_file "cursor: the user's own rule file is untouched" "$SB_HOME/.cursor/rules/mine.mdc"
mdc_before="$(cat "$mdc")"
"$ACCUTIL" json append "$BALLAST_HOME/rules.json" rules '{"id":"extra-rule","action":"log","event":"PreToolUse","match":{"bash":{"cmd":"frobnicate"}},"message":"extra","inject":"Extra rule text.","examples":[{"name":"hit","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"frobnicate"}}},{"name":"miss","expect":"miss","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}}]}'
bl doctor
expect_out_has "cursor: doctor notices the stale ballast.mdc" "mdc"
install_c cursor
check_contains "cursor: re-install regenerates it from the current rules" "$(cat "$mdc")" "extra-rule"
check_ne "cursor: and the content really changed" "$(cat "$mdc")" "$mdc_before"
bl uninstall --client cursor
check_no_file "cursor: uninstall deletes the managed ballast.mdc" "$mdc"
check_file "cursor: but not the user's rule" "$SB_HOME/.cursor/rules/mine.mdc"

section "non-canonical JSON layouts: install inserts only its own entries, uninstall restores the bytes"
# Fixtures a user may well have: tabs, no space after the colon, blank lines
# between members, CRLF, no final newline, compact, padded, empty containers,
# and a 0-byte file. Install must keep every other byte; install followed by
# uninstall must give the file back byte for byte.
layout_case() { # CLIENT NAME CONTENT [MARKER: text that must survive the install verbatim]
  local c=$1 name=$2 content=$3 marker=${4:-} f saved
  new_client_sandbox "$c"
  f="$(cfg_file "$c")"
  write_file "$f" "$content"
  saved="$(mktmp layout-saved)"
  cp "$f" "$saved"
  install_c "$c"
  expect_rc "$c/$name: install" 0
  if cmp -s "$f" "$saved"; then bad "$c/$name: install changed the file" "unchanged"; else ok "$c/$name: install changed the file"; fi
  if [ -n "$marker" ]; then
    # compare as a file test: grep -F with CR/tab in the pattern, no echo mangling
    if LC_ALL=C grep -qF -- "$marker" "$f"; then ok "$c/$name: the user's own bytes are still there verbatim"; else bad "$c/$name: the user's own bytes are still there verbatim" "$(od -c "$f" | head -5)"; fi
  fi
  bl doctor
  expect_rc "$c/$name: doctor is happy" 0
  bl uninstall --client "$c"
  expect_rc "$c/$name: uninstall" 0
  if [ -e "$f" ] && cmp -s "$f" "$saved"; then
    ok "$c/$name: install -> uninstall restored the file byte for byte"
  else
    bad "$c/$name: install -> uninstall restored the file byte for byte" "$(od -c "$f" 2>&1 | head -8)"
  fi
}

for c in claude-code codex cursor; do
  case $c in cursor) ev=stop ver='"version":1,' ;; *) ev=Stop ver='' ;; esac
  layout_case "$c" zero-bytes ''
  layout_case "$c" tab-no-colon-space $'{\n\t"theme":"dark"\n}\n' $'{\n\t"theme":"dark",\n\t"'
  layout_case "$c" blank-line-between-members $'{\n  "theme": "dark",\n\n  "model": "opus"\n}\n' $'"theme": "dark",\n\n  "model": "opus",'
  layout_case "$c" crlf-no-final-newline $'{\r\n  "theme": "dark"\r\n}' $'{\r\n  "theme": "dark",\r\n  "'
  layout_case "$c" compact "{${ver}\"a\":1}" '"a":1,'
  layout_case "$c" padded "{ ${ver}\"a\" : 1 }"
  layout_case "$c" empty-object $'{ }\n'
  layout_case "$c" users-empty-event "{${ver}\"hooks\":{\"$ev\":[]}}" "\"$ev\":["
  layout_case "$c" inline-hooks-in-multiline-root $'{\n  "theme":"dark",\n  "hooks": {"Other":[]}\n}' $'"theme":"dark",\n  "hooks": {"Other":[],'
done

section "all four clients at once"
new_client_sandbox all
for c in claude-code codex cursor hermes; do seed_client "$c"; done
before="$(mktmp before-all)"
snap "$before"
bl install --client all --binary "$BIN_N"
expect_rc "install --client all" 0
bl doctor --json
check_eq "doctor --json ok with all four clients installed" "$("$ACCUTIL" json get "$BL_OUT_FILE" ok)" "true"
check_eq "doctor lists four clients" "$("$ACCUTIL" json len "$BL_OUT_FILE" clients)" "4"
bl uninstall --all
expect_rc "uninstall --all" 0
after="$(mktmp after-all)"
snap "$after"
same_tree "all: install -> uninstall --all leaves every file byte-identical" "$before" "$after"
bl uninstall --all
expect_rc "uninstall --all with nothing installed is a no-op" 0

section "usage errors write nothing"
new_client_sandbox usage
before="$(mktmp before-usage)"
mid="$(mktmp mid-usage)"
snap "$before"
bl install
expect_rc "install without --client (2)" 2
bl install --client nobody --binary "$BIN_N"
expect_rc "install --client with an unknown client (2)" 2
bl install --client claude-code --binary relative/path
expect_rc "install --binary with a relative path (2)" 2
bl uninstall --all --client codex
expect_rc "uninstall --all with --client (2)" 2
snap "$mid"
same_tree "nothing was written" "$before" "$mid"

finish
