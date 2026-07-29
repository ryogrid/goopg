#!/bin/bash
# S2b — TPC-DS solo stage (ci/design/05-tpcds-stage.md §appendix).
#
# ISOLATION: same snapshot-copy pattern as stage-tpch.sh — the canonical
# data dir (bench/tpch/runtime_goopg/data, port 65433) is loop-owned, so
# the batch copies it to a private dir and runs its own server on port
# ${NIGHTLY_TPCDS_PORT:-65435}.
#
# The stage runs a subset of 61 TPC-DS queries that are known to pass
# (OK, non-zero rows, matching PG) on the current sf=1 dataset.  A
# spotcheck tripwire (Q3/Q98) gates the full sweep.
#
#   step 0  data precondition + snapshot copy (skip-with-note, exit 0)
#   step 1  fresh capped server on the copy + spotcheck tripwire
#   step 2  61-query sweep under NIGHTLY_TPCDS_BUDGET (7200s)
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

TPCDS_DIR="${RUN_DIR}/tpcds"
mkdir -p "${TPCDS_DIR}"

# Shared bench config (TPC-DS moved to bench/tpcds/ on 2026-07-27)
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"
set +e
SRC_DATA="${TPCDS_PGDATA}"      # bench/tpcds/runtime_goopg/data
SRC_PORT="${TPCDS_PORT}"        # 65436 — the TPC-DS SF=1 bench lane
RUN_PORT="${NIGHTLY_TPCDS_PORT:-65435}"
RUN_DATA="${REPO_ROOT}/tmp/goopg-nightly-tpcds-data"

BUDGET="${NIGHTLY_TPCDS_BUDGET:-7200}"
RESERVE=120
PER_Q_CAP=1200
CG_UNIT="goopg-nightly-tpcds"
SERVER_LOG="${TPCDS_DIR}/server.log"

# Qualifying queries (61 total — OK, non-zero rows, matches PG)
SWEEP_ORDER=(1 2 3 6 7 9 12 13 15 16 17 18 19 20 21 22 26 27 28 29 32 33 34 38 40 41 42 43 44 48 52 53 55 56 57 59 60 61 62 63 66 68 73 74 75 77 79 80 84 85 87 89 90 91 92 94 95 96 97 98 99)
if [[ -n "${NIGHTLY_TPCDS_QUERIES:-}" ]]; then
    IFS=',' read -ra SWEEP_ORDER <<< "${NIGHTLY_TPCDS_QUERIES}"
fi

# Spotcheck queries (must be fast and stable)
SPOTCHECK_QUERIES=(3 98)
SPOTCHECK_EXPECTED=(31 2531)

skip_stage() {
    progress "S2b" "tpcds SKIP: $*"
    stage_status tpcds "skip($1)"
    exit 0
}

# --- step 0a: source-data preconditions ---------------------------------------
[[ -s "${SRC_DATA}/PG_VERSION" ]] || skip_stage no-data "no initialised cluster at ${SRC_DATA}"
data_mb="$(du -sm "${SRC_DATA}" 2>/dev/null | awk '{print $1}')"
[[ -n "${data_mb}" && "${data_mb}" -ge 100 ]] || skip_stage no-data "data dir only ${data_mb:-0} MB"

if port_busy 127.0.0.1 "${RUN_PORT}"; then
    skip_stage port-busy "batch-reserved port ${RUN_PORT} unexpectedly held"
fi

# --- step 0b: snapshot copy of the canonical data dir --------------------------
copy_ok=0
for attempt in 1 2 3; do
    progress "S2b" "tpcds: waiting for canonical ${SRC_PORT} to be free (attempt ${attempt}/3)"
    if ! wait_port_free "${PG_HOST}" "${SRC_PORT}"; then
        skip_stage port-busy "canonical port ${SRC_PORT} busy for ${NIGHTLY_PORT_WAIT:-900}s"
    fi
    progress "S2b" "tpcds: copying ${SRC_DATA} -> ${RUN_DATA} (${data_mb} MB)"
    rm -rf "${RUN_DATA}"
    if ! cp -a "${SRC_DATA}" "${RUN_DATA}"; then
        rm -rf "${RUN_DATA}"
        skip_stage copy-failed "cp -a failed (disk space?)"
    fi
    if ! port_busy 127.0.0.1 "${SRC_PORT}"; then
        copy_ok=1
        break
    fi
    progress "S2b" "tpcds: server appeared on ${SRC_PORT} mid-copy — retrying"
done
[[ ${copy_ok} -eq 1 ]] || skip_stage port-busy "could not get an interference-free copy in 3 attempts"
rm -f "${RUN_DATA}/postmaster.pid"

# --- build server --------------------------------------------------------------
progress "S2b" "tpcds: building goopg"
mkdir -p "$(dirname "${GOOPG_BIN}")"
( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || { stage_status tpcds "fail(build)"; exit 1; }

stop_scope "${CG_UNIT}"

PPROF_ADDR="${NIGHTLY_TPCDS_PPROF_ADDR:-127.0.0.1:6161}"
progress "S2b" "tpcds: fresh server on ${PG_HOST}:${RUN_PORT} (unit=${CG_UNIT} pprof=${PPROF_ADDR})"
# GOOPG_PPROF_ADDR: private pprof port — see the same note in stage-tpch.sh.
GOOPG_CG_UNIT="${CG_UNIT}" GOOPG_MEM_HIGH=10G GOOPG_MEM_MAX=12G \
GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=9GiB GOOPG_PPROF_ADDR="${PPROF_ADDR}" \
    "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${RUN_DATA}" \
    --listen "${PG_HOST}:${RUN_PORT}" \
    --hba "${RUN_DATA}/pg_hba.conf" \
    > "${SERVER_LOG}" 2>&1 8>&- &
server_pid=$!

cleanup() {
    # Same bounded ladder as stage-tpch.sh — this stage runs the same TIMEOUT
    # -> killed-client -> leaked-backend shape and had the identical untimed
    # `wait`. See stop_goopg_server in lib/common.sh.
    STOP_PPROF_ADDR="${PPROF_ADDR}"
    STOP_DUMP_FILE="${TPCDS_DIR}/server-goroutines.txt"
    stop_goopg_server "${GOOPG_BIN}" "${RUN_DATA}" "${server_pid}"
    case "${STOP_RUNG}" in
        graceful|already-exited) ;;
        *) progress "S2b" "tpcds: server would NOT stop gracefully — escalated to ${STOP_RUNG} (leaked backend? see tpcds/server.log)" ;;
    esac
    stop_scope "${CG_UNIT}"
    rm -rf "${RUN_DATA}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

ready=0
for _ in $(seq 1 120); do
    kill -0 "${server_pid}" 2>/dev/null || break
    if pg_isready -h "${PG_HOST}" -p "${RUN_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        ready=1; break
    fi
    sleep 1
done
if [[ ${ready} -ne 1 ]]; then
    progress "S2b" "tpcds FAIL: server not ready in 120s"
    tail -n 20 "${SERVER_LOG}" || true
    stage_status tpcds "fail(startup)"
    exit 1
fi

# --- step 1: spotcheck tripwire ------------------------------------------------
progress "S2b" "tpcds: spotcheck Q3=${SPOTCHECK_EXPECTED[0]} Q98=${SPOTCHECK_EXPECTED[1]}"
spot_ok=1
QDIR="${TPCDS_QUERY_DIR}"
for i in 0 1; do
    qn="${SPOTCHECK_QUERIES[$i]}"
    expected="${SPOTCHECK_EXPECTED[$i]}"
    qf="${QDIR}/query${qn}.sql"
    out=$(timeout 120 psql -h "${PG_HOST}" -p "${RUN_PORT}" -U "${PG_SUPERUSER}" -d postgres -f "${qf}" 2>&1) || true
    rows=$(echo "${out}" | grep -oP '\(\d+ rows?\)' | tail -1 | grep -oP '\d+' || echo "0")
    if [[ "${rows}" != "${expected}" ]]; then
        spot_ok=0
        progress "S2b" "tpcds: spotcheck Q${qn} FAIL (got ${rows}, expected ${expected})"
    else
        progress "S2b" "tpcds: spotcheck Q${qn} PASS (${rows})"
    fi
done

if [[ ${spot_ok} -ne 1 ]]; then
    stage_status tpcds "fail(spotcheck)"
    exit 1
fi

# --- step 2: budget-bounded sweep ----------------------------------------------
TIMINGS="${TPCDS_DIR}/timings.csv"
echo "query,elapsed_s,rows,status" > "${TIMINGS}"
: > "${TPCDS_DIR}/run.log"
sweep_t0=${SECONDS}
sweep_failed=0
i=0
for qn in "${SWEEP_ORDER[@]}"; do
    i=$(( i + 1 ))
    elapsed_sweep=$(( SECONDS - sweep_t0 ))
    remaining=$(( BUDGET - elapsed_sweep ))
    if (( remaining <= RESERVE )); then
        progress "S2b" "tpcds: BUDGET EXHAUSTED (${elapsed_sweep}s) — Q${qn} and later marked not-run"
        for rest in "${SWEEP_ORDER[@]:$(( i - 1 ))}"; do
            echo "Q${rest},,,not-run(budget)" >> "${TIMINGS}"
        done
        sweep_failed=1
        break
    fi
    per_q=$(( remaining - RESERVE ))
    (( per_q > PER_Q_CAP )) && per_q=${PER_Q_CAP}

    qf="${QDIR}/query${qn}.sql"
    [[ -f "${qf}" ]] || { echo "Q${qn},,,not-run(missing-file)" >> "${TIMINGS}"; continue; }

    q_t0=${SECONDS}
    out=$(timeout "${per_q}" psql -h "${PG_HOST}" -p "${RUN_PORT}" -U "${PG_SUPERUSER}" -d postgres -f "${qf}" 2>&1) || true
    q_elapsed=$(( SECONDS - q_t0 ))
    printf '%s\n' "${out}" >> "${TPCDS_DIR}/run.log"

    if echo "${out}" | grep -qi "ERROR:"; then
        err=$(echo "${out}" | grep -i "ERROR:" | head -1 | tr -d '|' | cut -c1-120)
        echo "Q${qn},${q_elapsed},,error: ${err}" >> "${TIMINGS}"
        progress "S2b" "tpcds q${qn} ERROR after ${q_elapsed}s"
        sweep_failed=1
    elif (( q_elapsed >= per_q )); then
        echo "Q${qn},${q_elapsed},,timeout" >> "${TIMINGS}"
        progress "S2b" "tpcds q${qn} TIMEOUT after ${q_elapsed}s (cap ${per_q}s)"
        sweep_failed=1
    else
        rows=$(echo "${out}" | grep -oP '\(\d+ rows?\)' | tail -1 | grep -oP '\d+' || echo "?")
        echo "Q${qn},${q_elapsed},${rows},ok" >> "${TIMINGS}"
        progress "S2b" "tpcds q${qn} ok ${q_elapsed}s rows=${rows} (budget left $(( BUDGET - (SECONDS - sweep_t0) ))s)"
    fi
done

total=$(( SECONDS - sweep_t0 ))
progress "S2b" "tpcds: sweep done in ${total}s"
if [[ ${sweep_failed} -eq 0 ]]; then
    stage_status tpcds "pass"
    exit 0
fi
stage_status tpcds "fail(sweep)"
exit 1
