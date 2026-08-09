#!/bin/bash
# AI-20260810-011258-006 repro: pgbench TPC-B false-deadlock (40001) probe.
#
# Mirrors ci/batch/stages/stage-pgbench.sh's server plumbing at a cheaper size
# (s=20 c=100 T=60, standard workload only) and runs pgbench with
# --verbose-errors so the originating serialization errors are printed, plus
# GOOPG_DEBUG_WFG=1 so the server dumps the wait-for cycle behind each verdict.
set -uo pipefail
REPO_ROOT="${REPO_ROOT:-$PWD}"
OUT="${OUT:-${REPO_ROOT}/analysis/wfg-tpcb}"
SCALE="${SCALE:-20}"; CLIENTS="${CLIENTS:-100}"; THREADS="${THREADS:-20}"; T="${T:-60}"
PORT="${PORT:-5539}"
CG_UNIT="goopg-wfg-repro"
DATADIR="${REPO_ROOT}/tmp/goopg-wfg-repro-data-$$"

mkdir -p "${OUT}"
LOG="${OUT}/pgbench.log"; SERVER_LOG="${OUT}/server.log"
: > "${LOG}"; : > "${SERVER_LOG}"

PATH="${REPO_ROOT}/postgres/local_install/bin:${PATH}"
export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"

server_pid=""
cleanup() {
    [[ -n "${server_pid}" ]] && kill -KILL "${server_pid}" 2>/dev/null
    rm -rf "${DATADIR}"
}
trap cleanup EXIT

rm -rf "${DATADIR}"
( cd "${REPO_ROOT}" && go build -o bin/goopg-wfg ./cmd/goopg ) || exit 1
"${REPO_ROOT}/bin/goopg-wfg" init -D "${DATADIR}" >> "${SERVER_LOG}" 2>&1 || exit 1

GOOPG_DEBUG_WFG=1 GOOPG_CG_UNIT="${CG_UNIT}" GOOPG_MEM_HIGH=6G GOOPG_MEM_MAX=8G \
GOOPG_MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB \
    "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${REPO_ROOT}/bin/goopg-wfg" start -D "${DATADIR}" --listen "127.0.0.1:${PORT}" \
    >> "${SERVER_LOG}" 2>&1 &

PGPORT="${PORT}" timeout 60 bash -c \
    'until pg_isready -h 127.0.0.1 -U postgres -q; do sleep 1; done' || exit 1
server_pid="$(head -n1 "${DATADIR}/postmaster.pid" 2>/dev/null || true)"

pgbench -i -s "${SCALE}" -h 127.0.0.1 -p "${PORT}" -U postgres postgres >> "${LOG}" 2>&1 || exit 1
timeout -k 30 --signal=INT $(( T + 300 )) \
    pgbench -T "${T}" -c "${CLIENTS}" -j "${THREADS}" -P 10 --verbose-errors \
    -h 127.0.0.1 -p "${PORT}" -U postgres postgres >> "${LOG}" 2>&1
echo "--- summary ---"
grep -E 'number of (failed|transactions)|^tps|aborted' "${LOG}" | head -20
echo "--- WFG cycles: $(grep -c 'WFG deadlock' "${SERVER_LOG}") ---"
grep 'WFG deadlock' "${SERVER_LOG}" | head -20
