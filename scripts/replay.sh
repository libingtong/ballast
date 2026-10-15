#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# replay.sh - plan M3 replay acceptance over LOCAL transcripts.
#
# Reads the transcript list from $BALLAST_HOME/replay-set.txt (default
# $HOME/.ballast/replay-set.txt; one path per line, "#" comments allowed, a
# leading ~/ means $HOME). The transcripts are private and never part of the
# repository; they are only READ, and this script prints sizes and counts,
# never paths or contents.
#
#   * file missing            -> explicit SKIP, exit 0
#                                (BALLAST_REQUIRE_REPLAY=1: failure, exit 1)
#   * at least 3 transcripts, at least one larger than 10 MB
#   * every report: valid JSON in the frozen replay Report shape
#   * peak memory of `ballast replay` < 200 MB (where the OS reports it)
#   * at least 20 labels in $BALLAST_HOME/labels, every one resolving to a
#     sample id (of the right rule) of the reports
#
# HOME and BALLAST_HOME are used as given because sample ids hash the
# "~/"-relative transcript path; every other client/config location is
# redirected to a temporary directory. No arguments.
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
lib_init replay
build_ballast
sandbox_new replay keep-home

SET="$BALLAST_HOME/replay-set.txt"
if [ ! -f "$SET" ]; then
  if [ "${BALLAST_REQUIRE_REPLAY:-0}" = 1 ]; then
    bad "replay set" "\$BALLAST_HOME/replay-set.txt does not exist and BALLAST_REQUIRE_REPLAY=1"
  else
    skip "replay" "\$BALLAST_HOME/replay-set.txt does not exist (list >= 3 local transcripts there; BALLAST_REQUIRE_REPLAY=1 turns this into a failure)"
  fi
  finish
fi

OUT="${BALLAST_REPLAY_OUT:-$TMP_ROOT/replay-out}"
mkdir -p "$OUT"
rm -f "$OUT"/*.json "$OUT"/*.err "$OUT"/*.stats

section "the replay set"
PATHS_FILE="$TMP_ROOT/set.clean"
grep -v '^[[:space:]]*\(#.*\)\{0,1\}$' "$SET" | sed 's/\r$//' >"$PATHS_FILE" || true
TOTAL="$(wc -l <"$PATHS_FILE" | tr -d ' ')"
check_int_ge "the set lists at least 3 transcripts" "$TOTAL" 3
big=0
i=0
while IFS= read -r line; do
  i=$((i + 1))
  # shellcheck disable=SC2088  # a literal leading ~/ is the documented syntax
  case $line in '~/'*) p="$HOME/${line#\~/}" ;; *) p=$line ;; esac
  if [ -f "$p" ]; then
    size="$(wc -c <"$p" | tr -d ' ')"
    note "transcript #$i: $((size / 1024)) KiB"
    [ "$size" -gt 10485760 ] && big=$((big + 1))
  fi
done <"$PATHS_FILE"
check_int_ge "at least one transcript is larger than 10 MB" "$big" 1

section "replay every transcript (read-only)"
replay_reports "$PATHS_FILE" "$OUT"

REPORTS=()
n=0
while [ "$n" -lt "$REPLAY_COUNT" ]; do
  n=$((n + 1))
  idx="$(printf '%02d' "$n")"
  [ -f "$OUT/$idx.json" ] || continue
  if msg="$("$ACCUTIL" replay-check "$OUT/$idx.json" 2>&1)"; then
    ok "transcript #$n: the report matches the Report schema"
    note "#$n: $msg"
  else
    bad "transcript #$n: the report matches the Report schema" "$msg"
  fi
  REPORTS+=("$OUT/$idx.json")
  rss="$(stat_value "$OUT/$idx.stats" rss_kb)"
  ms="$(stat_value "$OUT/$idx.stats" ms)"
  if [ "$rss" -gt 0 ]; then
    check_int_le "transcript #$n: peak memory $((rss / 1024)) MB (< 200 MB), ${ms} ms" "$rss" 204799
  else
    skip "transcript #$n: peak memory" "this OS does not report the child's peak RSS to the script"
  fi
done

section "human labels"
if [ "${#REPORTS[@]}" -gt 0 ]; then
  if msg="$("$ACCUTIL" labels-check "$BALLAST_HOME/labels" 20 "${REPORTS[@]}" 2>&1)"; then
    ok "at least 20 labels, all resolving to sample ids of the reports ($msg)"
  else
    bad "at least 20 labels, all resolving to sample ids of the reports" "$msg"
  fi
else
  bad "labels" "no report to resolve labels against"
fi

finish
