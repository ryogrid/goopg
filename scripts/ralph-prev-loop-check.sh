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
# The newest timestamped group is usually the CURRENTLY-RUNNING loop's own
# pair (the driver creates them before our first tool call), so the baseline
# rule inspects the SECOND-newest group — the previous loop.
#
# 2026-10-31 (P-5 fix, after the 07:16 kill incident): when NO live headless
# agent is running (the loop is stopped), the newest group is NOT the current
# loop's — it is the LAST, FINISHED loop's, and that is exactly the loop the
# kill hit. The old rule inspected only the second-newest pair and printed
# OK, hiding the mid-turn kill. Now: with no live agent, the newest group is
# checked as well.
#
# Caveats (all advisory-impact only):
#   - a still-running CONCURRENT second loop can sort second-newest and
#     falsely read as ANOMALY; the driver runs one loop at a time today.
#   - stems sort by name, so a cross-backend mix (devin_output_ > claude_output_
#     lexically) can misorder "second-newest"; the driver currently always
#     uses claude_output_.
#   - the live-agent probe treats any `opencode serve` daemon (including an
#     interactive user's shared service) as liveness: conservative, the
#     newest-group check is skipped in that case.
#
# Output (advisory only, exit 0 in every case):
#   OK      — the checked loop's log ends with a status block
#   ANOMALY — it does not; that loop probably died mid-turn
#   SKIP    — no loop logs exist to check
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
[ ${#stems[@]} -ge 1 ] || { echo "SKIP — no loop logs exist"; exit 0; }

check_stem() {
  # The status block is the FINAL thing a completed loop emits, so check only
  # the tail — a killed loop whose earlier stream merely mentions the marker
  # (an agent quoting the format) must still read as ANOMALY.
  # NOTE (2026-10-31): do NOT use `grep -q` here. It exits at the first match
  # and closes the pipe; `tail` then dies with SIGPIPE (status 141) and
  # `set -o pipefail` turns that into a non-zero pipeline status, so the same
  # log flipped between OK and ANOMALY run to run depending on where the
  # marker sat in the 40-line tail. `grep -c` reads all input, so no early
  # pipe close happens; we test the count.
  local count
  count=$(tail -n 40 "$logdir/${1}.log" "$logdir/${1}_stream.log" 2>/dev/null \
          | grep -c -- '---RALPH_STATUS---' || true)
  [[ "${count:-0}" -gt 0 ]]
}

# Live-agent probe: with a live headless agent, the newest group is that
# agent's own pair and only the second-newest group is "the previous loop".
# With no live agent (loop stopped), the newest group belongs to the last,
# FINISHED loop and must be checked too.
agent_live=false
for pat in 'opencode run' 'opencode serve' 'claude .* -p ' 'codex exec' 'devin .* --prompt-file'; do
  if pgrep -f "$pat" >/dev/null 2>&1; then agent_live=true; break; fi
done
# Test hook: force the stopped-loop code path even while agents run.
[[ "${RALPH_PREV_CHECK_FORCE_NEWEST:-}" == "1" ]] && agent_live=false

if [[ "$agent_live" == false ]]; then
  if check_stem "${stems[0]}"; then
    echo "OK — newest ${stems[0]}.* ends with a status block (no live agent found)"
  else
    echo "ANOMALY — newest ${stems[0]}.* ended without a ---RALPH_STATUS--- block; the LAST loop was killed mid-turn (no live agent found); its in-flight state is NOT in the baton — re-verify the working tree (git status) and the baton before resuming"
  fi
fi

if [[ ${#stems[@]} -ge 2 ]]; then
  prev="${stems[1]}"
  if check_stem "$prev"; then
    echo "OK — ${prev}.* ends with a status block"
  else
    echo "ANOMALY — ${prev}.* ended without a ---RALPH_STATUS--- block; the previous loop was probably killed mid-turn and its in-flight state may not be in the baton"
  fi
elif [[ "$agent_live" == true ]]; then
  echo "SKIP — only the running loop's log exists (no previous loop to check)"
fi
exit 0
