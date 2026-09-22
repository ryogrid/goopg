#!/usr/bin/env bash
# M0145-0021: private-lane TPC-DS fire-set execution gate.
#
# For every requested corpus, capture a baseline/candidate plan A/B, derive
# the fire set from the current changed-plan ids, then execute only those ids
# on fresh private clones. The status comparator makes a new candidate timeout
# fatal, while retaining inherited timeouts as evidence rather than pretending
# they passed.
#
# Usage: scripts/tpcds-fireset-gate.sh <label> <outdir>
#
# Defaults cover the task requirement: CORPORA="tpcds-sf025 tpcds-sf1",
# BASELINE_JOINTREE=0, CANDIDATE_JOINTREE=1. Either arm may source a local
# shell-assignment file through BASELINE_ENV_FILE or CANDIDATE_ENV_FILE before
# launching its clone; that makes the template usable for firewall, estimation,
# and cost-model experiments without exporting a candidate-only setting into
# the baseline arm.
# FIRESET_RESUME=1 reuses existing plan captures and appends only missing query
# status records. FIRESET_BATCH_SIZE=N bounds an invocation to N missing ids;
# it deliberately leaves the comparator incomplete/nonzero until every fire
# has a baseline and candidate status.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CAPTURE="${ROOT}/scripts/jointree-parity-capture.sh"
DIFF="${ROOT}/scripts/tpcds-plan-diff.py"
STATUS="${ROOT}/scripts/tpcds-fireset-status.py"

[[ $# -eq 2 ]] || { echo "usage: $0 <label> <outdir>" >&2; exit 2; }
LABEL="$1"
OUTDIR="$2"
mkdir -p "${OUTDIR}"

CORPORA="${CORPORA:-tpcds-sf025 tpcds-sf1}"
BASELINE_JOINTREE="${BASELINE_JOINTREE:-0}"
CANDIDATE_JOINTREE="${CANDIDATE_JOINTREE:-1}"
FIRESET_TIMEOUT="${FIRESET_TIMEOUT:-600}"
FIRESET_RESUME="${FIRESET_RESUME:-0}"
FIRESET_BATCH_SIZE="${FIRESET_BATCH_SIZE:-0}"

for value in "${BASELINE_JOINTREE}" "${CANDIDATE_JOINTREE}"; do
    [[ "${value}" =~ ^[0-9]+$ ]] || {
        echo "FATAL: jointree arm values must be unsigned integers" >&2
        exit 2
    }
done
[[ "${FIRESET_RESUME}" =~ ^[01]$ ]] || { echo "FATAL: FIRESET_RESUME must be 0 or 1" >&2; exit 2; }
[[ "${FIRESET_BATCH_SIZE}" =~ ^[0-9]+$ ]] || { echo "FATAL: FIRESET_BATCH_SIZE must be a non-negative integer" >&2; exit 2; }

run_arm() {
    local arm="$1" env_file="$2" jointree="$3"
    shift 3
    (
        if [[ -n "${env_file}" ]]; then
            [[ -r "${env_file}" ]] || { echo "FATAL: unreadable ${arm} env file: ${env_file}" >&2; exit 2; }
            # shellcheck source=/dev/null
            source "${env_file}"
        fi
        JOINTREE="${jointree}" FIRESET_TIMEOUT="${FIRESET_TIMEOUT}" "$@"
    )
}

derive_fires() {
    local baseline_plans="$1" candidate_plans="$2" diff_out="$3"
    python3 "${DIFF}" "${baseline_plans}" "${candidate_plans}" >"${diff_out}"
    awk '/^changed \([0-9]+\):/ { for (i = 3; i <= NF; i++) { sub(/^Q/, "", $i); if ($i !~ /^[0-9]+$/) exit 2; printf "%s%s", sep, $i; sep="," } } END { print "" }' "${diff_out}"
}

missing_fires() {
    local fires="$1" status_file="$2"
    local completed=""
    [[ -f "${status_file}" ]] && completed="$(awk '/^Q[0-9]+ (PASS|TIMEOUT|ERROR)$/ { sub(/^Q/, "", $1); printf "%s%s", sep, $1; sep="," }' "${status_file}")"
    python3 - "${fires}" "${completed}" "${FIRESET_BATCH_SIZE}" <<'PY'
import sys
fires = [int(q) for q in sys.argv[1].split(',') if q]
done = {int(q) for q in sys.argv[2].split(',') if q}
limit = int(sys.argv[3])
pending = [q for q in fires if q not in done]
if limit:
    pending = pending[:limit]
print(','.join(map(str, pending)))
PY
}

for corpus in ${CORPORA}; do
    case "${corpus}" in tpcds-sf025|tpcds-sf1) ;; *) echo "FATAL: unsupported corpus: ${corpus}" >&2; exit 2 ;; esac
    corpus_dir="${OUTDIR}/${corpus}"
    mkdir -p "${corpus_dir}"
    baseline_label="${LABEL}-${corpus}-baseline"
    candidate_label="${LABEL}-${corpus}-candidate"

    if [[ "${FIRESET_RESUME}" == 1 ]]; then
        [[ -s "${corpus_dir}/${baseline_label}.plans.txt" && -s "${corpus_dir}/${candidate_label}.plans.txt" ]] || {
            echo "FATAL: FIRESET_RESUME requires both prior plan captures for ${corpus}" >&2
            exit 2
        }
    else
        run_arm baseline "${BASELINE_ENV_FILE:-}" "${BASELINE_JOINTREE}" \
            "${CAPTURE}" "${corpus}" "${baseline_label}" "${corpus_dir}"
        run_arm candidate "${CANDIDATE_ENV_FILE:-}" "${CANDIDATE_JOINTREE}" \
            "${CAPTURE}" "${corpus}" "${candidate_label}" "${corpus_dir}"
    fi

    diff_out="${corpus_dir}/${LABEL}-${corpus}-fires.diff.txt"
    fires="$(derive_fires "${corpus_dir}/${baseline_label}.plans.txt" "${corpus_dir}/${candidate_label}.plans.txt" "${diff_out}")"
    printf '%s\n' "${fires}" >"${corpus_dir}/${LABEL}-${corpus}-fires.txt"
    if [[ -z "${fires}" ]]; then
        echo "FIRE-SET: corpus=${corpus} fires=none (plan A/B had no changed query)"
        continue
    fi

    baseline_status="${corpus_dir}/${baseline_label}.status.txt"
    candidate_status="${corpus_dir}/${candidate_label}.status.txt"
    clone_seed="$(printf '%s' "${LABEL}-${corpus}" | sha256sum | cut -c1-12)"
    if [[ "${FIRESET_RESUME}" != 1 ]]; then
        : >"${baseline_status}"
        : >"${candidate_status}"
    fi
    baseline_pending="$(missing_fires "${fires}" "${baseline_status}")"
    candidate_pending="$(missing_fires "${fires}" "${candidate_status}")"
    if [[ -n "${baseline_pending}" ]]; then
        run_arm baseline "${BASELINE_ENV_FILE:-}" "${BASELINE_JOINTREE}" \
            env FIRESET_QUERIES="${baseline_pending}" FIRESET_STATUS_OUT="${baseline_status}" FIRESET_SKIP_CAPTURE=1 CLONE_LABEL="fireset-${clone_seed}-b" \
            "${CAPTURE}" "${corpus}" "${baseline_label}-execute" "${corpus_dir}"
    fi
    if [[ -n "${candidate_pending}" ]]; then
        run_arm candidate "${CANDIDATE_ENV_FILE:-}" "${CANDIDATE_JOINTREE}" \
            env FIRESET_QUERIES="${candidate_pending}" FIRESET_STATUS_OUT="${candidate_status}" FIRESET_SKIP_CAPTURE=1 CLONE_LABEL="fireset-${clone_seed}-c" \
            "${CAPTURE}" "${corpus}" "${candidate_label}-execute" "${corpus_dir}"
    fi
    python3 "${STATUS}" --fires "${fires}" --baseline "${baseline_status}" --candidate "${candidate_status}"
done
