#!/usr/bin/env bash
# M0127-P5.9 — S5 acceptance driver: run ONE arm (fresh capped server, one
# sweep, server age 0 at sweep start) against the TPC-H SF1 cluster.
#
# usage: m0127-p59-arm.sh <arm-name> <out-file> [runner-args...]
# env:   PGSHAPED=1|0 (default 0), COLLAPSE=1|0 (default 0)
set -uo pipefail
ARM="$1"; OUT="$2"; shift 2
REPO_ROOT=/home/ryo/work/goopg/goopg
BIN="${REPO_ROOT}/tmp/goopg-m0127-p59-bin"
RUNNER="${REPO_ROOT}/tmp/tpch-runner-p59"
PGDATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
PG_HOST=127.0.0.1; PG_PORT=65433
CG_UNIT="goopg-m0127p59-${ARM}"
SRV_LOG="${REPO_ROOT}/tmp/m0127-p59-${ARM}.server.log"
export PATH="${REPO_ROOT}/postgres/local_install/bin:${PATH}"
export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export GOMEMLIMIT=12GiB GOGC=off GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0
export GOOPG_PGSHAPED_DP="${PGSHAPED:-0}"
export GOOPG_PGSHAPED_COLLAPSE="${COLLAPSE:-0}"

"${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${BIN}" start -D "${PGDATA}" --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" >"${SRV_LOG}" 2>&1 &
server_pid=$!
cleanup() {
    "${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap cleanup EXIT
ready=0
for _ in $(seq 1 180); do
    kill -0 "${server_pid}" 2>/dev/null || break
    pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U postgres >/dev/null 2>&1 && { ready=1; break; }
    sleep 1
done
[[ "${ready}" -eq 1 ]] || { echo "FATAL: ${ARM} not ready"; tail -20 "${SRV_LOG}"; exit 1; }
echo "# arm=${ARM} GOOPG_PGSHAPED_DP=${GOOPG_PGSHAPED_DP} GOOPG_PGSHAPED_COLLAPSE=${GOOPG_PGSHAPED_COLLAPSE}" >"${OUT}"
echo "# started $(date -Is)" >>"${OUT}"
"${RUNNER}" -host "${PG_HOST}" -port "${PG_PORT}" \
    -db tpch -user tpch -password tpch "$@" >>"${OUT}" 2>&1
echo "# finished $(date -Is)" >>"${OUT}"
echo "arm ${ARM} written: ${OUT} ($(wc -l <"${OUT}") lines)"
