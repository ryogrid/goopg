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
# Arms (M0145-0008 legacy-deletion slice 1: HEAD-vs-staged). The baseline arm
# runs an engine built from BASELINE_REV (default HEAD) and the candidate arm
# an engine built from this checkout, which the stamp's dirty_code rule
# forces to equal the index — so the fire set is the set of plans the STAGED
# CHANGE moves, on the shipped pipeline. Before the redesign both arms ran
# one binary and differed only in GOOPG_JOINTREE_PIPELINE (legacy vs
# jointree); after the cutover that measured the retired pipeline, not the
# change being committed, and it could not survive the knob's deletion
# (M0145-0008 slice 2 deleted it; slice 3 dropped the per-arm JOINTREE
# variables).
#   BASELINE_REV   git revision whose `go.mod go.sum cmd internal` build the
#                  baseline engine (default HEAD; resolved once and recorded
#                  in <outdir>/<label>-baseline-rev.txt, which a
#                  FIRESET_RESUME=1 run reuses). `worktree` = the candidate's
#                  own binary, for an env-file A/B on one engine; that mode
#                  refuses an A/A (no env file on either arm).
#   FIRESET_KEEP_CLONES  1 = keep the per-arm TPC-DS clone datadirs (3.3 GB
#                  each at SF1) after the corpus; default 0 deletes them.
#
# Defaults cover the task requirement: CORPORA="tpcds-sf025 tpcds-sf1",
# BASELINE_REV=HEAD. `tpch` is a supported corpus
# (M0145-0021b) but deliberately NOT a default — its fires execute at SF1
# through tpch-acceptance-arm.sh, so it is opt-in:
# `CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh …`. Either arm may source a local
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

# Gate stamp (M0145-0021a): the commit-msg hook requires a PASS stamp for
# commits in the fire-set scope (AGENT.md G9), so every exit writes
# tmp/gate-stamps/tpcds-fireset.json — 0 → PASS, a capture preflight
# failure (jointree-parity-capture.sh exits 3 on a missing/HOLD source
# datadir) → SKIP-BLOCKED, anything else → FAIL. GATE_STAMP_DIR is
# honoured like the other gates so a sub-invocation can redirect it.
# shellcheck source=lib/gate-stamp.sh
source "${ROOT}/scripts/lib/gate-stamp.sh"

[[ $# -eq 2 ]] || { echo "usage: $0 <label> <outdir>" >&2; exit 2; }

# Arm the stamp AFTER arg validation — a usage error must not clobber a
# fresh PASS stamp (a stray invocation would otherwise force a ~15 min
# re-run). Every later exit writes tmp/gate-stamps/tpcds-fireset.json:
# 0 → PASS, capture-preflight rc 3 → SKIP-BLOCKED, else FAIL.
trap 'rc=$?; \
  case "${rc}" in 3) GATE_STAMP_REASON="${GATE_STAMP_REASON:-capture preflight failed (missing or HOLD source datadir)}" ;; esac; \
  gate_stamp_write tpcds-fireset "$(gate_stamp_result_for_rc "${rc}" 3)" ""' EXIT
LABEL="$1"
OUTDIR="$2"
mkdir -p "${OUTDIR}"

CORPORA="${CORPORA:-tpcds-sf025 tpcds-sf1}"
BASELINE_REV="${BASELINE_REV:-HEAD}"
FIRESET_TIMEOUT="${FIRESET_TIMEOUT:-600}"
FIRESET_RESUME="${FIRESET_RESUME:-0}"
FIRESET_BATCH_SIZE="${FIRESET_BATCH_SIZE:-0}"
FIRESET_KEEP_CLONES="${FIRESET_KEEP_CLONES:-0}"

[[ "${FIRESET_RESUME}" =~ ^[01]$ ]] || { echo "FATAL: FIRESET_RESUME must be 0 or 1" >&2; exit 2; }
[[ "${FIRESET_BATCH_SIZE}" =~ ^[0-9]+$ ]] || { echo "FATAL: FIRESET_BATCH_SIZE must be a non-negative integer" >&2; exit 2; }
[[ "${FIRESET_KEEP_CLONES}" =~ ^[01]$ ]] || { echo "FATAL: FIRESET_KEEP_CLONES must be 0 or 1" >&2; exit 2; }
# One engine and no env file on either side is an A/A: every plan matches,
# the fire set is empty and the gate would PASS having compared nothing.
if [[ "${BASELINE_REV}" == worktree && -z "${BASELINE_ENV_FILE:-}" && -z "${CANDIDATE_ENV_FILE:-}" ]]; then
    echo "FATAL: BASELINE_REV=worktree with no env file on either arm is an A/A comparison" >&2
    exit 2
fi

# Engine images. Both are lane-private (never a shared server's image) and
# every capture below runs with NO_BUILD=1 against them, so the two arms
# differ exactly by the source tree they were built from.
BIN_DIR="${ROOT}/tmp/fireset-bin"
CANDIDATE_BIN="${BIN_DIR}/${LABEL}-candidate"
mkdir -p "${BIN_DIR}"
( cd "${ROOT}" && go build -o "${CANDIDATE_BIN}" ./cmd/goopg ) || { echo "FATAL: candidate build failed" >&2; exit 4; }

rev_file="${OUTDIR}/${LABEL}-baseline-rev.txt"
if [[ "${BASELINE_REV}" == worktree ]]; then
    BASELINE_BIN="${CANDIDATE_BIN}"
    baseline_sha=worktree
else
    # A resumed run keeps the revision the captures were taken with, even if
    # HEAD moved in between.
    if [[ "${FIRESET_RESUME}" == 1 && -s "${rev_file}" ]]; then
        baseline_sha="$(head -1 "${rev_file}")"
    else
        baseline_sha="$(git -C "${ROOT}" rev-parse --verify "${BASELINE_REV}^{commit}")" || {
            echo "FATAL: BASELINE_REV ${BASELINE_REV} does not name a commit" >&2; exit 2; }
    fi
    BASELINE_BIN="${BIN_DIR}/${LABEL}-baseline"
    baseline_src="${ROOT}/tmp/fireset-src/${LABEL}-baseline"
    rm -rf "${baseline_src}"
    mkdir -p "${baseline_src}"
    # cmd/goopg's in-module dependency closure is cmd/ + internal/ (go:embed
    # files included); the generated parser is checked in, so no generation
    # step is needed.
    git -C "${ROOT}" archive --format=tar "${baseline_sha}" -- go.mod go.sum cmd internal \
        | tar -x -C "${baseline_src}" || { echo "FATAL: cannot export ${baseline_sha}" >&2; exit 4; }
    ( cd "${baseline_src}" && go build -o "${BASELINE_BIN}" ./cmd/goopg ) || {
        echo "FATAL: baseline build (${baseline_sha}) failed" >&2; exit 4; }
    rm -rf "${baseline_src}"
fi
printf '%s\n' "${baseline_sha}" >"${rev_file}"
echo "FIRE-SET: baseline=${baseline_sha} ($(sha256sum "${BASELINE_BIN}" | cut -c1-12)) candidate=worktree ($(sha256sum "${CANDIDATE_BIN}" | cut -c1-12))"

# The TPC-H execution half runs tpch-acceptance-arm.sh, which under NO_BUILD=1
# also expects its runner image to exist. The runner is a client, so one
# worktree build serves both arms.
if [[ " ${CORPORA} " == *" tpch "* ]]; then
    export RUNNER_BIN="${BIN_DIR}/${LABEL}-tpch-runner"
    ( cd "${ROOT}" && go build -o "${RUNNER_BIN}" ./cmd/tpch-runner ) || { echo "FATAL: tpch-runner build failed" >&2; exit 4; }
fi

run_arm() {
    local arm="$1" env_file="$2" bin="$3"
    shift 3
    (
        if [[ -n "${env_file}" ]]; then
            [[ -r "${env_file}" ]] || { echo "FATAL: unreadable ${arm} env file: ${env_file}" >&2; exit 2; }
            # shellcheck source=/dev/null
            source "${env_file}"
        fi
        GOOPG_BIN="${bin}" NO_BUILD=1 FIRESET_TIMEOUT="${FIRESET_TIMEOUT}" "$@"
    )
}

# remove_clone <dir> — drop a finished arm's private clone unless it still
# has a live postmaster (never delete a running server's datadir).
remove_clone() {
    local dir="$1" pid
    [[ -d "${dir}" ]] || return 0
    if [[ -f "${dir}/postmaster.pid" ]]; then
        pid="$(head -1 "${dir}/postmaster.pid" 2>/dev/null || true)"
        if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
            echo "FIRE-SET: keeping ${dir} (live postmaster ${pid})" >&2
            return 0
        fi
    fi
    rm -rf "${dir}"
}

derive_fires() {
    local baseline_plans="$1" candidate_plans="$2" diff_out="$3"
    # The caller reads us through $( ), where `set -e` does NOT propagate:
    # without `|| return` a tpcds-plan-diff.py failure (incl. its exit-2
    # unknown/empty-capture guard) would leave ${diff_out} truncated, awk
    # would still succeed on it, and the caller would read "" as "no fires"
    # — a vacuous PASS on a broken diff. Fail the function so the gate
    # exits non-zero (and stamps FAIL).
    python3 "${DIFF}" "${baseline_plans}" "${candidate_plans}" >"${diff_out}" || return
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
    # tpch (M0145-0021b): the derivation half needs nothing new — the TPC-H
    # capture writes the same `=== Qn` blocks tpcds-plan-diff.py already
    # parses — and the execution half is routed to the acceptance arm inside
    # jointree-parity-capture.sh. It is NOT in CORPORA's default: the TPC-H
    # lane's fires execute at SF1 against the shared load, so it is opt-in.
    case "${corpus}" in tpcds-sf025|tpcds-sf1|tpch) ;; *) echo "FATAL: unsupported corpus: ${corpus}" >&2; exit 2 ;; esac
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
        run_arm baseline "${BASELINE_ENV_FILE:-}" "${BASELINE_BIN}" \
            "${CAPTURE}" "${corpus}" "${baseline_label}" "${corpus_dir}"
        run_arm candidate "${CANDIDATE_ENV_FILE:-}" "${CANDIDATE_BIN}" \
            "${CAPTURE}" "${corpus}" "${candidate_label}" "${corpus_dir}"
    fi

    diff_out="${corpus_dir}/${LABEL}-${corpus}-fires.diff.txt"
    fires="$(derive_fires "${corpus_dir}/${baseline_label}.plans.txt" "${corpus_dir}/${candidate_label}.plans.txt" "${diff_out}")"
    printf '%s\n' "${fires}" >"${corpus_dir}/${LABEL}-${corpus}-fires.txt"
    clone_seed="$(printf '%s' "${LABEL}-${corpus}" | sha256sum | cut -c1-12)"
    # The four private clones jointree-parity-capture.sh makes for this
    # corpus (tmp/<clone label>-data-<corpus>); the TPC-H lane clones inside
    # its arm scripts, so none of these exist for it.
    corpus_clones=(
        "${ROOT}/tmp/${baseline_label}-data-${corpus}"
        "${ROOT}/tmp/${candidate_label}-data-${corpus}"
        "${ROOT}/tmp/fireset-${clone_seed}-b-data-${corpus}"
        "${ROOT}/tmp/fireset-${clone_seed}-c-data-${corpus}"
    )
    # The capture clones are done once the fire set is derived; dropping them
    # here keeps the SF1 peak at two clones (execution), not four.
    if [[ "${FIRESET_KEEP_CLONES}" != 1 ]]; then
        for dir in "${corpus_clones[@]}"; do remove_clone "${dir}"; done
    fi
    if [[ -z "${fires}" ]]; then
        echo "FIRE-SET: corpus=${corpus} fires=none (plan A/B had no changed query)"
        continue
    fi

    baseline_status="${corpus_dir}/${baseline_label}.status.txt"
    candidate_status="${corpus_dir}/${candidate_label}.status.txt"
    if [[ "${FIRESET_RESUME}" != 1 ]]; then
        : >"${baseline_status}"
        : >"${candidate_status}"
    fi
    baseline_pending="$(missing_fires "${fires}" "${baseline_status}")"
    candidate_pending="$(missing_fires "${fires}" "${candidate_status}")"
    if [[ -n "${baseline_pending}" ]]; then
        run_arm baseline "${BASELINE_ENV_FILE:-}" "${BASELINE_BIN}" \
            env FIRESET_QUERIES="${baseline_pending}" FIRESET_STATUS_OUT="${baseline_status}" FIRESET_SKIP_CAPTURE=1 CLONE_LABEL="fireset-${clone_seed}-b" \
            "${CAPTURE}" "${corpus}" "${baseline_label}-execute" "${corpus_dir}"
    fi
    if [[ -n "${candidate_pending}" ]]; then
        run_arm candidate "${CANDIDATE_ENV_FILE:-}" "${CANDIDATE_BIN}" \
            env FIRESET_QUERIES="${candidate_pending}" FIRESET_STATUS_OUT="${candidate_status}" FIRESET_SKIP_CAPTURE=1 CLONE_LABEL="fireset-${clone_seed}-c" \
            "${CAPTURE}" "${corpus}" "${candidate_label}-execute" "${corpus_dir}"
    fi
    if [[ "${FIRESET_KEEP_CLONES}" != 1 ]]; then
        for dir in "${corpus_clones[@]}"; do remove_clone "${dir}"; done
    fi
    python3 "${STATUS}" --fires "${fires}" --baseline "${baseline_status}" --candidate "${candidate_status}"
done
