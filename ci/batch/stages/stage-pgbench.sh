#!/bin/bash
# S1 Lane H step 2 — pgbench functional stage (ci/design/02 §A).
#
# Self-contained (NOT ralph-precommit-test.sh): the nightly parameters differ
# from the per-commit smoke — scale factor 50, 100 clients, 20 threads,
# 180 s per workload — and changing the shared precommit tool would alter the
# git-hook gate for every commit. Server plumbing mirrors
# scripts/ralph-precommit-test.sh (free-port probe, throwaway data dir, capped
# server, pinned PG 18.3 client tools, SIGKILL teardown).
#
# Workloads (unchanged trio): standard TPC-B, -N (simple update), -S (select
# only). Gate: 0 failed transactions on all three — TPS never gates.
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

SCALE="${NIGHTLY_PGBENCH_SCALE:-50}"
CLIENTS="${NIGHTLY_PGBENCH_CLIENTS:-100}"
THREADS="${NIGHTLY_PGBENCH_THREADS:-20}"
DURATION="${NIGHTLY_PGBENCH_T:-180}"
BASE_PORT="${NIGHTLY_PGBENCH_PORT:-5555}"
CG_UNIT="goopg-nightly-pgbench"
DATADIR="${REPO_ROOT}/tmp/goopg-nightly-pgbench-data-$$"
LOG="${RUN_DIR}/pgbench/pgbench.log"
SERVER_LOG="${RUN_DIR}/pgbench/server.log"

mkdir -p "${RUN_DIR}/pgbench"
progress "S1.H" "pgbench start (s=${SCALE} c=${CLIENTS} j=${THREADS} T=${DURATION}x3, unit=${CG_UNIT} high=6G max=8G)"

# Pinned PG 18.3 client tools (libpq must match — see precommit script).
if [[ -x "${REPO_ROOT}/postgres/local_install/bin/pgbench" ]]; then
    PATH="${REPO_ROOT}/postgres/local_install/bin:${PATH}"
    export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
fi
for tool in pgbench pg_isready; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        progress "S1.H" "pgbench SKIP: required tool '${tool}' not found"
        stage_status pgbench "skip(no-${tool})"
        exit 0
    fi
done

# Free-port probe (a stray server on a fixed port would false-pass the gate).
PORT=""
for cand in $(seq "${BASE_PORT}" $(( BASE_PORT + 50 ))); do
    if ! port_busy 127.0.0.1 "${cand}"; then PORT="${cand}"; break; fi
done
if [[ -z "${PORT}" ]]; then
    progress "S1.H" "pgbench FAIL: no free port in [${BASE_PORT},$(( BASE_PORT + 50 ))]"
    stage_status pgbench "fail(no-port)"
    exit 1
fi

server_pid=""
cleanup() {
    if [[ -n "${server_pid}" ]] && kill -0 "${server_pid}" 2>/dev/null; then
        kill -KILL "${server_pid}" 2>/dev/null || true
    fi
    stop_scope "${CG_UNIT}"
    rm -rf "${DATADIR}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM   # route signals through the EXIT trap; never resume mid-flow

rm -rf "${DATADIR}"
( cd "${REPO_ROOT}" && go build -o bin/goopg ./cmd/goopg ) || { stage_status pgbench "fail(build)"; exit 1; }
"${REPO_ROOT}/bin/goopg" init -D "${DATADIR}" >> "${SERVER_LOG}" 2>&1 || { stage_status pgbench "fail(init)"; exit 1; }

# 8>&- : the detached server must NOT inherit the orchestrator's run-lock fd
# (an orphan would pin the run lock — ci/design/06 §C).
GOOPG_CG_UNIT="${CG_UNIT}" GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB \
    "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${REPO_ROOT}/bin/goopg" start -D "${DATADIR}" --listen "127.0.0.1:${PORT}" \
    >> "${SERVER_LOG}" 2>&1 8>&- &

if ! PGPORT="${PORT}" timeout 30 bash -c \
        'until pg_isready -h 127.0.0.1 -U postgres -q; do sleep 1; done'; then
    progress "S1.H" "pgbench FAIL: server not ready in 30s (see pgbench/server.log)"
    stage_status pgbench "fail(startup)"
    exit 1
fi
server_pid="$(head -n1 "${DATADIR}/postmaster.pid" 2>/dev/null || true)"

# NOTE: no `exit` inside a { } group here — that would terminate the whole
# script (skipping status recording and the remaining workloads). Run each
# workload even if an earlier one failed: one bad workload should still yield
# the other two data points.
#
# Every pgbench invocation runs under an OUTER `timeout`: at c=100 a server
# bug can leave all clients hung on never-returning queries, and pgbench then
# ignores its own -T deadline forever (observed live: 0.0 tps for 56 min past
# the 180 s duration, 2026-07-06). INT makes pgbench abort clients and print
# partial results; KILL follows 30 s later if even that hangs.
rc=0
echo "== pgbench -i -s ${SCALE} ==" > "${LOG}"
if ! timeout -k 30 --signal=INT 1800 \
        pgbench -i -s "${SCALE}" -h 127.0.0.1 -p "${PORT}" -U postgres postgres >> "${LOG}" 2>&1; then
    progress "S1.H" "pgbench FAIL: -i -s ${SCALE} load failed or exceeded 1800s (see pgbench/pgbench.log)"
    stage_status pgbench "fail(load)"
    exit 1
fi
WL_CLAMP=$(( DURATION + 600 ))
for wl in "" "-N" "-S"; do
    echo "== pgbench ${wl:-standard} -T ${DURATION} -c ${CLIENTS} -j ${THREADS} (clamp ${WL_CLAMP}s) ==" >> "${LOG}"
    # shellcheck disable=SC2086
    if ! timeout -k 30 --signal=INT "${WL_CLAMP}" \
            pgbench -T "${DURATION}" -c "${CLIENTS}" -j "${THREADS}" -P 10 ${wl} \
            -h 127.0.0.1 -p "${PORT}" -U postgres postgres >> "${LOG}" 2>&1; then
        rc=1
        progress "S1.H" "pgbench workload '${wl:-standard}' FAILED (client abort / hang past ${WL_CLAMP}s clamp — see pgbench/pgbench.log)"
    fi
done

# Gate: rc AND 0 failed transactions across all three workloads.
failed_total=0
blocks=0
while read -r n; do
    failed_total=$(( failed_total + n ))
    blocks=$(( blocks + 1 ))
done < <(sed -n 's/^number of failed transactions: \([0-9]\+\).*/\1/p' "${LOG}")

if [[ ${rc} -eq 0 && ${failed_total} -eq 0 && ${blocks} -eq 3 ]]; then
    tps="$(sed -n 's/^tps = \([0-9.]*\).*/\1/p' "${LOG}" | paste -sd/ -)"
    progress "S1.H" "pgbench PASS (0 failed txns; tps ${tps:-?})"
    stage_status pgbench "pass"
    exit 0
fi
progress "S1.H" "pgbench FAIL (rc=${rc}, failed_txns=${failed_total}, workloads_completed=${blocks}/3)"
stage_status pgbench "fail"
exit 1
