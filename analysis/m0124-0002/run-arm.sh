#!/usr/bin/env bash
#
# run-arm.sh — one TPC-H 22-query stream for ONE arm of the M0124-0002
# retroactive A/B (design: docs/design/0124-0002-retroactive-tpch-plan-gate-discharge.md).
#
#   usage: analysis/m0124-0002/run-arm.sh <A|B> <rep>
#
# Arm A = HEAD with 9740fce9's internal/planner/bushy.go hunks reverted
#         (its internal/executor/expr.go bounds check STAYS — reverting it
#         returns the Q8 crash and confounds the arm).
# Arm B = HEAD unmodified.
#
# Both binaries are pre-built by the driving loop into tmp/goopg-arm{A,B}-bin;
# this script only owns the run protocol, which must be identical per arm:
#
#   - fresh server per stream (server age is held constant — a server that
#     just ran a timeout query sits at GOMEMLIMIT and thrashes GC, which
#     mimics a regression: "sweep-tail collapse")
#   - GOGC=100 + GOMEMLIMIT=12GiB (Q21 draws a host-level OOM at 18GiB)
#   - S-cold by necessity: `ANALYZE <table>` inside db `tpch` errors
#     "relation does not exist" (ledger row `bench-reorg ANALYZE-scope`),
#     so S-warm is unreachable on this cluster.
#   - 600 s per-query budget, server default parallel degree.
#
set -euo pipefail

ARM="${1:?usage: run-arm.sh <A|B> <rep>}"
REP="${2:?usage: run-arm.sh <A|B> <rep>}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
BENCH_DIR="${REPO_ROOT}/bench/tpch"

# env_goopg.sh sets PGDATA/PG_HOST/PG_PORT/GOOPG_BIN and puts the bundled
# libpq bin dir on PATH. Override the GC knobs AFTER sourcing: it defaults
# to GOGC=off, which is not this experiment's configuration.
# shellcheck source=../../bench/tpch/env_goopg.sh
source "${BENCH_DIR}/env_goopg.sh"
export GOGC=100
export GOMEMLIMIT=12GiB

ARM_BIN="${REPO_ROOT}/tmp/goopg-arm${ARM}-bin"
RUNNER_BIN="${REPO_ROOT}/tmp/tpch-retro-runner"
[[ -x "${ARM_BIN}" ]]    || { echo "missing ${ARM_BIN}" >&2; exit 1; }
[[ -x "${RUNNER_BIN}" ]] || { echo "missing ${RUNNER_BIN}" >&2; exit 1; }

OUT_DIR="${SCRIPT_DIR}/runs"
mkdir -p "${OUT_DIR}"
TAG="arm${ARM}-rep${REP}"
SRV_LOG="${OUT_DIR}/${TAG}.server.log"
RUN_LOG="${OUT_DIR}/${TAG}.stream.txt"
CG_UNIT="goopg-tpch-retro"

# --- stop anything stale on this data dir -----------------------------------
# NEVER `pkill -f goopg`: it self-matches the invoking shell (exit 144).
"${ARM_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || rm -f "${PGDATA}/postmaster.pid"
systemctl --user stop "${CG_UNIT}.scope"        >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

# --- fresh capped server ----------------------------------------------------
echo "== ${TAG}: starting ${ARM_BIN} on ${PG_HOST}:${PG_PORT} (GOGC=${GOGC} GOMEMLIMIT=${GOMEMLIMIT})"
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${ARM_BIN}" start -D "${PGDATA}" \
    --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" \
    >"${SRV_LOG}" 2>&1 &
server_pid=$!

cleanup() {
    "${ARM_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope"         >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap cleanup EXIT

ready=0
for _ in $(seq 1 180); do
    kill -0 "${server_pid}" 2>/dev/null || break
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        ready=1; break
    fi
    sleep 1
done
[[ "${ready}" == 1 ]] || { echo "${TAG}: server never became ready"; tail -20 "${SRV_LOG}"; exit 1; }

# --- target resolution ------------------------------------------------------
# Since the per-DB catalog work goopg persists CREATE DATABASE, so the tables
# live in a durable `tpch` database and tpch@tpch survives a restart. Keep the
# legacy postgres@postgres fallback for a cluster that predates that.
GATE_DB="${TPCH_DB}"; GATE_USER="${TPCH_USER}"; GATE_PASS="${TPCH_PASS}"
if ! PGDATABASE="${GATE_DB}" PGUSER="${GATE_USER}" PGPASSWORD="${GATE_PASS}" \
        psql -h "${PG_HOST}" -p "${PG_PORT}" -tA -c 'select 1 from lineitem limit 1' >/dev/null 2>&1; then
    GATE_DB="postgres"; GATE_USER="${PG_SUPERUSER}"; GATE_PASS="${PG_SUPERUSER_PASS}"
fi
echo "== ${TAG}: data target = ${GATE_USER}@${GATE_DB}"

# --- the stream -------------------------------------------------------------
start_epoch="$(date +%s)"
"${RUNNER_BIN}" \
    --host="${PG_HOST}" --port="${PG_PORT}" \
    --db="${GATE_DB}" --user="${GATE_USER}" --password="${GATE_PASS}" \
    --per-query-timeout=600s 2>&1 | tee "${RUN_LOG}"
end_epoch="$(date +%s)"

echo "== ${TAG}: stream wall clock $(( end_epoch - start_epoch )) s" | tee -a "${RUN_LOG}"
