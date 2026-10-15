#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# hotreload.sh - plan B4: rule edits take effect on the NEXT hook run, with no
# restart of anything. Every step is a real hook process.
#
#   1  create / edit / retype / replace the user layer file
#   2  same-size in-place edit (a size-only cache key would miss it)
#   3  generation changes and the change summary on the next prompt
#   4  repo layer wins over the user layer; edits and removal are seen
#   5  org layer is immutable and shadows lower layers; edits are seen
#   6  an invalid edit falls back to last-known-good and is reported loudly
#   7  an invalid layer with no last-known-good counts as empty
#   8  the on-disk cache is only an optimization
#
# No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init hotreload
build_ballast
sandbox_new hotreload

USER_RULES="$SB_HOME/.ballast/rules.json"
ORG_RULES="$SB_MANAGED/ballast/rules.json"
CWD="$(native "$SB_WORK")"
TOOL_N=0

# rules_file ID ACTION MESSAGE: a one-rule RuleFile that acts on `frob`.
rules_file() {
  printf '{"version":1,"rules":[{"id":"%s","action":"%s","event":"PreToolUse","match":{"bash":{"cmd":"frob"}},"message":"%s","inject":"Never run frob (%s).","examples":[{"name":"hit","expect":"hit","input":{"tool_name":"Bash","tool_input":{"command":"frob x"}}},{"name":"miss","expect":"miss","input":{"tool_name":"Bash","tool_input":{"command":"ls"}}}]}]}\n' \
    "$1" "$2" "$3" "$1"
}

# frob [CWD] [SESSION]: one PreToolUse of `frob` (a fresh tool_use_id each time).
frob() {
  local cwd=${1:-$CWD} sess=${2:-hr}
  TOOL_N=$((TOOL_N + 1))
  bl_in "$(payload_bash "$sess" "hr$TOOL_N" frob "$cwd")" hook --client claude-code --event PreToolUse
}

generation() {
  bl config show --json
  "$ACCUTIL" json get "$BL_OUT_FILE" generation
}

prompt() { # SESSION
  bl_in "{\"session_id\":\"$1\",\"cwd\":\"$CWD\",\"prompt\":\"hello\"}" hook --client claude-code --event UserPromptSubmit
}

session_start() { # SESSION [CWD]
  bl_in "{\"session_id\":\"$1\",\"cwd\":\"${2:-$CWD}\",\"source\":\"startup\"}" hook --client claude-code --event SessionStart
}

section "1. the user layer"
frob
expect_allow "no rule file: frob passes"
G0="$(generation)"
check_ne "an empty configuration still has a generation" "$G0" ""

rules_file hot-a deny "hot a says no" >"$USER_RULES"
frob
expect_deny "a new rules.json is enforced by the very next hook"
expect_out_has "the decision names rule hot-a" "[hot-a] hot a says no"
G1="$(generation)"
check_ne "creating the file changes the generation" "$G1" "$G0"

size_before="$(wc -c <"$USER_RULES" | tr -d ' ')"
rules_file hot-b deny "hot a says no" >"$USER_RULES"
size_after="$(wc -c <"$USER_RULES" | tr -d ' ')"
check_eq "premise: the second edit has the same byte size" "$size_after" "$size_before"
frob
expect_deny "same-size in-place edit is seen (id hot-a -> hot-b)"
expect_out_has "the decision now names hot-b" "[hot-b]"
G2="$(generation)"
check_ne "a same-size edit changes the generation" "$G2" "$G1"

rules_file hot-b warn "warn only" >"$USER_RULES"
frob
expect_rc "retyped to warn: still exit 0" 0
expect_out_lacks "retyped to warn: no deny any more" "permissionDecision"
expect_out_has "retyped to warn: advice is delivered as context" "additionalContext"
G3="$(generation)"
check_ne "retyping changes the generation" "$G3" "$G2"

rules_file hot-b deny "replaced by rename" >"$USER_RULES.new"
mv "$USER_RULES.new" "$USER_RULES"
frob
expect_deny "a file replaced by rename (new inode) is seen"
expect_out_has "the renamed-in rule's message is used" "replaced by rename"

section "2. change summary on the next prompt of a running session"
session_start sess1
expect_rc "SessionStart carries the full rule text" 0
expect_out_has "SessionStart injected the current rules" "Never run frob (hot-b)"
prompt sess1
expect_out_lacks "no summary while nothing changed" "Ballast rules changed"
rules_file hot-c deny "third rule" >"$USER_RULES"
prompt sess1
expect_out_has "after an edit the next prompt reports the change" "Ballast rules changed"
expect_out_has "the summary lists the added rule" "+ hot-c: added"
expect_out_has "the summary lists the removed rule" "- hot-b: removed"
prompt sess1
expect_out_lacks "the summary is shown once" "Ballast rules changed"

section "3. repo layer wins over the user layer"
REPO="$SB_WORK/repo"
git_init "$REPO"
write_file "$REPO/a.txt" "a
"
gitc "$REPO" add -A
gitc "$REPO" commit -q -m init
REPO_N="$(native "$REPO")"
mkdir -p "$REPO/.ballast"
rules_file hot-c log "repo says log" >"$REPO/.ballast/rules.json"
frob "$REPO_N"
expect_allow "inside the repo the repo object wins entirely (log is silent)"
frob "$CWD"
expect_deny "outside the repo the user layer still denies"
rules_file hot-c deny "repo says no" >"$REPO/.ballast/rules.json"
frob "$REPO_N"
expect_deny "an edited repo rule is enforced by the next hook"
expect_out_has "with the repo layer's message" "repo says no"
rm -f "$REPO/.ballast/rules.json"
frob "$REPO_N"
expect_deny "deleting the repo file falls back to the user layer"
expect_out_has "with the user layer's message" "third rule"

section "4. org layer is immutable"
mkdir -p "$(dirname "$ORG_RULES")"
rules_file hot-c deny "ORG decides" >"$ORG_RULES"
rules_file hot-c log "repo tries to soften" >"$REPO/.ballast/rules.json"
frob "$REPO_N"
expect_deny "an org rule cannot be overridden by repo or user rules of the same id"
expect_out_has "the org message is the one shown" "ORG decides"
bl_dir "$REPO" config show --explain
expect_rc "config show --explain" 0
expect_out_has "the shadowed lower-layer rules are listed with the reason" "shadowed by the org layer"
rules_file hot-c deny "ORG decides again" >"$ORG_RULES"
frob "$REPO_N"
expect_out_has "an org edit is seen by the next hook" "ORG decides again"
rm -f "$ORG_RULES" "$REPO/.ballast/rules.json"
frob "$REPO_N"
expect_out_has "without the org file the user layer is back" "third rule"

section "5. an invalid edit keeps the last-known-good rules"
printf '{ this is not json' >"$USER_RULES"
frob
expect_deny "the hook keeps enforcing the previous valid rules"
expect_out_has "from the last-known-good copy" "third rule"
expect_err_has "and says so loudly on stderr" "last-known-good"
bl config show
expect_rc "config show reports the invalid layer (exit 1)" 1
expect_out_has "config show names the lkg state" "lkg"
session_start sess2
expect_out_has "the next SessionStart shows a config_invalid notice" "invalid"
check_int_ge "config_invalid is audited when the notice is shown" "$("$ACCUTIL" jsonl count "$BALLAST_HOME/audit" type=config_invalid)" 1
rules_file hot-d deny "fixed again" >"$USER_RULES"
frob
expect_deny "the repaired file is enforced at once"
expect_out_has "with its new content" "fixed again"
bl config show
expect_rc "config show is clean again" 0

section "6. an invalid layer without last-known-good counts as empty"
printf '{ never valid' >"$REPO/.ballast/rules.json"
frob "$REPO_N"
expect_deny "the other layers keep working"
expect_out_has "the user rule still decides" "fixed again"
expect_err_has "the broken repo layer is reported on stderr" "invalid"
bl_dir "$REPO" config show
expect_rc "config show exits 1" 1
rm -f "$REPO/.ballast/rules.json"

section "7. the cache is only an optimization"
check_file "the stat cache exists" "$BALLAST_HOME/cache/stat.json"
rm -rf "$BALLAST_HOME/cache"
frob
expect_deny "decisions are unchanged after deleting the whole cache directory"
printf 'garbage' >"$BALLAST_HOME/cache/stat.json"
frob
expect_deny "decisions are unchanged with a corrupt stat cache"
rules_file hot-e deny "after cache damage" >"$USER_RULES"
frob
expect_out_has "edits are still noticed with a damaged cache" "after cache damage"

section "8. the audit log"
bl audit lint
expect_rc "audit lint" 0

finish
