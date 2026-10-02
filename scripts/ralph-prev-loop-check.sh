#!/usr/bin/env bash
# ralph-prev-loop-check.sh — preflight for the Ralph loop (owner add,
# 2026-10-02, from the 2026-10-02 progress review: an aborted loop — usage
# cap, crash, driver restart — leaves in-flight state that never reaches the
# baton, and the next loop has no way to notice).
#
# Loop logs come in a per-loop pair: `claude_output_<ts>.log` (the final
# result record, written at loop end) and `claude_output_<ts>_stream.log`
# (the live stream). A completed loop's `.log` carries the closing
# `---RALPH_STATUS---` block; a killed loop's ends mid-stream without one.
#
# The newest timestamped group is almost always the CURRENTLY-RUNNING loop's
# own pair (the driver creates them before our first tool call), so this
# script inspects the SECOND-newest group — the previous loop.
#
# Caveats (all advisory-impact only, verified 2026-10-02):
#   - a still-running CONCURRENT second loop can sort second-newest and
#     falsely read as ANOMALY; the driver runs one loop at a time today.
#   - stems sort by name, so a cross-backend mix (devin_output_ > claude_output_
#     lexically) can misorder "second-newest"; the driver currently always
#     uses claude_output_.
#
# Output (advisory only, exit 0 in every case):
#   OK      — the previous loop's log ends with a status block
#   ANOMALY — it does not; the previous loop probably died mid-turn
#   SKIP    — no previous-loop log exists
set -uo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"
logdir="$here/.ralph/logs"
[ -d "$logdir" ] || { echo "SKIP — no .ralph/logs directory"; exit 0; }

# Timestamped log stems, newest first (claude_output_YYYY-MM-DD_HH-MM-SS and
# devin_output_<ts> alike; the _stream suffix is stripped before uniq).
stems=()
while IFS= read -r s; do stems+=("$s"); done < <(
  ls "$logdir" 2>/dev/null \
    | sed -n 's/^\(claude_output_[0-9_-]*\)\(_stream\)\?\.log$/\1/p; s/^\(devin_output_[0-9_-]*\)\(_stream\)\?\.log$/\1/p' \
    | sort -r | uniq)
[ ${#stems[@]} -ge 2 ] || { echo "SKIP — fewer than two loop logs exist (no previous loop to check)"; exit 0; }

prev="${stems[1]}"
# The status block is the FINAL thing a completed loop emits, so check only
# the tail — a killed loop whose earlier stream merely mentions the marker
# (an agent quoting the format) must still read as ANOMALY.
if tail -n 40 "$logdir/${prev}.log" "$logdir/${prev}_stream.log" 2>/dev/null \
   | grep -q -- '---RALPH_STATUS---'; then
  echo "OK — ${prev}.* ends with a status block"
else
  echo "ANOMALY — ${prev}.* ended without a ---RALPH_STATUS--- block; the previous loop was probably killed mid-turn and its in-flight state may not be in the baton"
fi
exit 0
