#!/usr/bin/env bash
#
# with-server.sh — bring up one arm's goopg on the TPC-H bench cluster, run a
# command against it, then stop it. Used by M0124-0002 for the plan-snapshot
# half, where the instrument is EXPLAIN (cheap) rather than a timed stream.
#
# Usage: with-server.sh <goopg-binary> <label> <command...>
#
set -euo pipefail

BIN="${1:?usage: with-server.sh <goopg-binary> <label> <command...>}"
LABEL="${2:?usage: with-server.sh <goopg-binary> <label> <command...>}"
shift 2

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck source=../../bench/tpch/env_goopg.sh
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"
export GOGC=100
export GOMEMLIMIT=12GiB

CG_UNIT="goopg-tpch-retro"
SRV_LOG="${SCRIPT_DIR}/server-plan-${LABEL}.log"

"${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || rm -f "${PGDATA}/postmaster.pid"
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

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
echo "=== server up (${LABEL}, ${BIN}) ==="

cd "${REPO_ROOT}"
"$@"
