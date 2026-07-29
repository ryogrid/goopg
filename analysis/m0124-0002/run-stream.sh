#!/usr/bin/env bash
#
# run-stream.sh — one TPC-H 22-query stream against a nominated goopg binary.
#
# M0124-0002 (docs/design/0124-0002-retroactive-tpch-plan-gate-discharge.md §D2):
# both arms are built from HEAD and differ only in `9740fce9`'s bushy.go hunks,
# so the only thing that may vary between streams is the binary path. Every
# other knob — cluster, GC settings, timeout, cgroup scope — is fixed here so
# an A/B cannot drift in server age or configuration (the confound that burned
# a previous A/B on this programme).
#
# Usage: run-stream.sh <goopg-binary> <label> [outdir]
#
set -euo pipefail

BIN="${1:?usage: run-stream.sh <goopg-binary> <label> [outdir]}"
LABEL="${2:?usage: run-stream.sh <goopg-binary> <label> [outdir]}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
OUTDIR="${3:-${SCRIPT_DIR}}"

# shellcheck source=../../bench/tpch/env_goopg.sh
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"

# D2: GOGC=100 + GOMEMLIMIT=12GiB. Q21 drew a host-level OOM at 18 GiB and
# completes at these settings (CLAUDE.md). env_goopg.sh defaults GOGC=off, so
# this override is deliberate and must be identical on both arms.
export GOGC=100
export GOMEMLIMIT=12GiB

RUNNER_BIN="${REPO_ROOT}/tmp/tpch-retro-runner"
CG_UNIT="goopg-tpch-retro"
SRV_LOG="${OUTDIR}/server-${LABEL}.log"
OUT="${OUTDIR}/stream-${LABEL}.txt"
QUERY_TIMEOUT="${TPCH_RETRO_QUERY_TIMEOUT:-600s}"
STREAM_BUDGET="${TPCH_RETRO_STREAM_BUDGET:-1800}"
# Empty = all 22 (the designed stream). A comma list narrows the stream to the
# queries under question — used for the round-2 re-read of the one query whose
# round-1 move crossed the §D4.3 investigate band.
QUERIES="${TPCH_RETRO_QUERIES:-}"

mkdir -p "${OUTDIR}"

# Stop any stale instance on this data dir via the control socket.
# NEVER pkill -f: it self-matches the invoking shell (exit 144).
"${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || rm -f "${PGDATA}/postmaster.pid"
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

echo "=== stream ${LABEL}: bin=${BIN} ==="
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${BIN}" start -D "${PGDATA}" \
    --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" \
    >"${SRV_LOG}" 2>&1 &
server_pid=$!

cleanup() {
    "${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap cleanup EXIT

ready=0
for _ in $(seq 1 120); do
    kill -0 "${server_pid}" 2>/dev/null || break
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        ready=1; break
    fi
    sleep 1
done
if [[ "${ready}" -ne 1 ]]; then
    echo "FATAL — goopg (${LABEL}) did not become ready; tail of ${SRV_LOG}:" >&2
    tail -n 20 "${SRV_LOG}" >&2 || true
    exit 1
fi

# Target resolution: the Makefile plan defaults (tpch@tpch) are correct after
# the ef4a65a5 rebuild, but keep the spotcheck's postgres@postgres fallback so
# a role/db-persistence regression degrades to a named target rather than a
# silent empty baseline.
GATE_DB="${TPCH_DB}"; GATE_USER="${TPCH_USER}"; GATE_PASS="${TPCH_PASS}"
if ! PGDATABASE="${GATE_DB}" PGUSER="${GATE_USER}" PGPASSWORD="${GATE_PASS}" \
     psql -h "${PG_HOST}" -p "${PG_PORT}" -tA -c 'select 1 from lineitem limit 1' >/dev/null 2>&1; then
    GATE_DB="postgres"; GATE_USER="${PG_SUPERUSER}"; GATE_PASS="${PG_SUPERUSER_PASS}"
fi
echo "target=${GATE_USER}@${GATE_DB}"

{
    echo "# stream ${LABEL}"
    echo "# bin=${BIN}"
    echo "# target=${GATE_USER}@${GATE_DB} GOGC=${GOGC} GOMEMLIMIT=${GOMEMLIMIT} per-query-timeout=${QUERY_TIMEOUT}"
    echo "# started=$(date -Is)"
} >"${OUT}"

stream_start=$SECONDS
set +e
timeout "${STREAM_BUDGET}" "${RUNNER_BIN}" \
    --host="${PG_HOST}" --port="${PG_PORT}" \
    --db="${GATE_DB}" --user="${GATE_USER}" --password="${GATE_PASS}" \
    --queries="${QUERIES}" \
    --per-query-timeout="${QUERY_TIMEOUT}" 2>&1 | tee -a "${OUT}"
rc=${PIPESTATUS[0]}
set -e
stream_elapsed=$((SECONDS - stream_start))

{
    echo "# runner_rc=${rc}"
    echo "# stream_elapsed_s=${stream_elapsed}"
    echo "# finished=$(date -Is)"
} >>"${OUT}"
echo "=== stream ${LABEL} done in ${stream_elapsed}s (rc=${rc}) -> ${OUT} ==="
