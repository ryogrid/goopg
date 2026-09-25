#!/usr/bin/env bash
# check-stats-epoch.sh — turn the `# stats-epoch: …` line M0137-0002 stamps
# into a CHECKED step instead of a passive artefact field (M0137-0006).
#
# WHY. A values sweep re-samples statistics and opens a new epoch; R120 §6
# attributed two apparent "worsenings" in a flag-OFF/flag-ON A/B to drift
# rather than the flag, and left a standing rule — "re-take the OFF baseline;
# all A/B numbers same-epoch" — that no tool enforced
# (METHODOLOGY3/03-process-retrospective.md "Stats-epoch drift"). Same-code
# ANALYZE drift was separately measured at 1.31x on Q9 (R128's three
# committed captures), wider than some effects the programme was claiming.
# This script is the enforcement: it fails loudly, with the two epoch values
# printed, instead of leaving "same epoch" a thing a human remembers to check.
#
# Both scripts/capture-tpch.sh / scripts/capture-tpcds.sh (via
# scripts/lib/capture-stamp.sh) and cmd/estimate-audit (M0137-0006) write
# this line with the SAME fingerprint formula — sha256 over
# "relname|n_live_tup\n" rows from pg_stat_user_tables, ordered by relname,
# first 16 hex chars — so an OFF capture and an ON capture are comparable
# across either tool, and even across the two tools.
#
# Usage:
#   scripts/check-stats-epoch.sh <file> <file> [<file> ...]
#
# Exit codes:
#   0  every file's stats-epoch is present and identical
#   1  a mismatch, or an UNKNOWN(...) epoch, was found (the actual check)
#   2  operational failure (missing file, no stats-epoch line at all — not a
#      capture-stamp.sh/estimate-audit artefact)
#
# This tool is REPORT+VERDICT: unlike pg-plan-parity-diff.py (which always
# exits 0 and leaves the mismatch budget to a companion test), a stats-epoch
# mismatch is never an expected/tracked outcome — it means the two captures
# being compared do not share a baseline, so the exit code is the signal a
# calling script or CI step should act on.
set -uo pipefail

if [[ $# -lt 2 ]]; then
    echo "usage: $0 <file> <file> [<file> ...]" >&2
    echo "  (need at least two capture artefacts to compare a stats epoch across)" >&2
    exit 2
fi

# _extract_epoch <file> — the first `# stats-epoch: <value>` line's <value>,
# or empty if the file has none at all (an operational failure, not a
# mismatch: the file simply isn't a stamped capture-tpch.sh/capture-tpcds.sh
# or estimate-audit artefact).
_extract_epoch() {
    local file="$1"
    sed -n 's/^# stats-epoch: //p' "${file}" | head -1
}

declare -a files=("$@")
declare -a epochs=()
status=0

for file in "${files[@]}"; do
    if [[ ! -f "${file}" ]]; then
        echo "check-stats-epoch: ${file}: no such file" >&2
        exit 2
    fi
    epoch="$(_extract_epoch "${file}")"
    if [[ -z "${epoch}" ]]; then
        echo "check-stats-epoch: ${file}: no '# stats-epoch: ' line found" \
             "(not a capture-tpch.sh/capture-tpcds.sh/estimate-audit artefact?)" >&2
        exit 2
    fi
    epochs+=("${epoch}")
done

# An UNKNOWN epoch cannot be asserted equal to anything — the stamping tool
# already explained why it couldn't compute one; surface that instead of a
# false MATCH or a false MISMATCH.
for i in "${!files[@]}"; do
    if [[ "${epochs[$i]}" == UNKNOWN\(* ]]; then
        echo "check-stats-epoch: ${files[$i]}: stats-epoch is ${epochs[$i]}" \
             "— cannot verify this artefact's stats epoch at all" >&2
        status=1
    fi
done
if [[ ${status} -ne 0 ]]; then
    exit 1
fi

reference="${epochs[0]}"
mismatch=0
for i in "${!files[@]}"; do
    if [[ "${epochs[$i]}" != "${reference}" ]]; then
        mismatch=1
    fi
done

if [[ ${mismatch} -ne 0 ]]; then
    echo "check-stats-epoch: STATS-EPOCH MISMATCH — these captures do not share a baseline:" >&2
    for i in "${!files[@]}"; do
        echo "  ${files[$i]}: ${epochs[$i]}" >&2
    done
    echo "  Statistics were (re-)sampled between these captures (a values sweep / ANALYZE" >&2
    echo "  ran in between). Re-take the OFF baseline so both arms share one epoch before" >&2
    echo "  trusting any diff between them (rule: METHODOLOGY3/03-process-retrospective.md" >&2
    echo "  'Stats-epoch drift', enforced here per M0137-0006)." >&2
    exit 1
fi

echo "check-stats-epoch: MATCH — ${#files[@]} artefacts share stats-epoch ${reference}"
exit 0
