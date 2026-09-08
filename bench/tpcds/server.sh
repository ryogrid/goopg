#!/usr/bin/env bash
#
# server.sh — start/stop/status for the TPC-DS benchmark servers.
#
# Manages three servers, all defined by bench/tpcds/env_tpcds.sh:
#   sf1   goopg, SF=1 cluster,   runtime_goopg/data,      port 65436
#   sf05  goopg, SF=0.5 cluster, runtime_goopg/data-sf05, port 65437
#   pg    PostgreSQL reference,  runtime/pgdata,          port 65438
#
# Every goopg start goes through the cgroup memory cap
# (scripts/goopg-test-run.sh) — an unbounded TPC-DS intermediate otherwise
# thrashes swap and trips the WSL2 system OOM killer. NEVER `pkill -f goopg`
# (it self-matches the invoking shell; exit 144).
#
# Modeled on scripts/csq-bench-server.sh (the TPC-H bench twin).
#
# Usage:
#   bench/tpcds/server.sh start  [sf1|sf05|pg]     # default sf1
#   bench/tpcds/server.sh stop   [sf1|sf05|pg|all]
#   bench/tpcds/server.sh status
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/env_tpcds.sh"

READY_TIMEOUT="${TPCDS_READY_TIMEOUT:-180}"

die() { echo "FATAL: $*" >&2; exit 1; }

goopg_target() {
    # sets DATA / PORT / SCOPE / LOG for sf1|sf05
    case "$1" in
    sf1)  DATA="${TPCDS_PGDATA}";     PORT="${TPCDS_PORT}"; SCOPE="goopg-tpcds";     LOG="${TPCDS_GOOPG_LOG}" ;;
    sf05) DATA="${SF05_GOOPG_DATA}";  PORT="${SF05_PORT}";  SCOPE="goopg-tpcds-sf05"; LOG="${SF05_LOG}" ;;
    *)    die "unknown goopg target '$1' (sf1|sf05)" ;;
    esac
}

goopg_stop() {
    goopg_target "$1"
    "${GOOPG_BIN}" stop -D "${DATA}" >/dev/null 2>&1 && echo "tpcds-server: $1 stopped" || {
        rm -f "${DATA}/postmaster.pid"
        echo "tpcds-server: $1 was not running"
    }
    systemctl --user stop "${SCOPE}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${SCOPE}.scope" >/dev/null 2>&1 || true
}

goopg_start() {
    goopg_target "$1"
    [[ -d "${DATA}" ]] || die "cluster missing: ${DATA} (see bench/tpcds/README.md for setup)"
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg )
    # Residency guard (2026-09-06). goopg turns shared_buffers into pool slots
    # as shared_buffers/8 (cmd/goopg/main.go poolSlotsFromGUC), and a
    # postgresql.conf that leaves it commented falls back to the 128MB GUC
    # BootVal = 16384 slots. Both TPC-DS clusters were in exactly that state
    # while their working sets are 1.1 GiB (SF0.5) and 2.2 GiB (SF1): measured,
    # store_sales alone is 232MB — 1.8x the whole pool — and two scans of it
    # produced 59522 reads and 43138 evictions, so nothing was ever resident.
    # The PG TPC-DS reference runs at 2GB, so this also silently made any
    # goopg-vs-PG TPC-DS timing a 16x unfair comparison. Warn rather than fail:
    # a values-only gate is still valid on a thrashing pool, a timing arm is not.
    if ! grep -qE '^[[:space:]]*shared_buffers[[:space:]]*=' "${DATA}/postgresql.conf" 2>/dev/null; then
        echo "tpcds-server: WARNING — shared_buffers is not set in ${DATA}/postgresql.conf;" >&2
        echo "tpcds-server:   the cluster will use the 128MB default (16384 slots) against a" >&2
        echo "tpcds-server:   >1 GiB working set. Values gates stay valid; do NOT publish timing." >&2
    fi
    goopg_stop "$1" >/dev/null
    local hba_arg=()
    [[ -f "${DATA}/pg_hba.conf" ]] && hba_arg=(--hba "${DATA}/pg_hba.conf")
    echo "tpcds-server: starting $1 capped (scope ${SCOPE}) on ${TPCDS_HOST}:${PORT}"
    GOOPG_CG_UNIT="${SCOPE}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${GOOPG_BIN}" start -D "${DATA}" \
        --listen "${TPCDS_HOST}:${PORT}" "${hba_arg[@]}" \
        >>"${LOG}" 2>&1 &
    local i
    for i in $(seq 1 "${READY_TIMEOUT}"); do
        if pg_isready -h "${TPCDS_HOST}" -p "${PORT}" -U "${TPCDS_SUPERUSER}" >/dev/null 2>&1; then
            echo "tpcds-server: $1 ready (log ${LOG})"
            return 0
        fi
        sleep 1
    done
    die "$1 did not become ready in ${READY_TIMEOUT}s — see ${LOG}"
}

pg_start() {
    [[ -d "${TPCDS_PG_DATA}" ]] || die "PG cluster missing: ${TPCDS_PG_DATA}"
    if pg_isready -h "${TPCDS_HOST}" -p "${TPCDS_PG_PORT}" >/dev/null 2>&1; then
        echo "tpcds-server: pg already running on :${TPCDS_PG_PORT}"
        return 0
    fi
    pg_ctl -D "${TPCDS_PG_DATA}" -l "${TPCDS_PG_LOG}" start
    local i
    for i in $(seq 1 60); do
        pg_isready -h "${TPCDS_HOST}" -p "${TPCDS_PG_PORT}" >/dev/null 2>&1 && {
            echo "tpcds-server: pg ready on :${TPCDS_PG_PORT} (log ${TPCDS_PG_LOG})"
            return 0
        }
        sleep 1
    done
    die "PostgreSQL did not become ready — see ${TPCDS_PG_LOG}"
}

pg_stop() {
    pg_ctl -D "${TPCDS_PG_DATA}" stop -m fast 2>/dev/null && echo "tpcds-server: pg stopped" \
        || echo "tpcds-server: pg was not running"
}

status_one() {
    local name="$1" port="$2"
    if pg_isready -h "${TPCDS_HOST}" -p "${port}" >/dev/null 2>&1; then
        echo "  ${name}  :${port}  UP"
    else
        echo "  ${name}  :${port}  down"
    fi
}

cmd="${1:-status}"
target="${2:-sf1}"
case "${cmd}" in
start)
    case "${target}" in
    pg) pg_start ;;
    *)  goopg_start "${target}" ;;
    esac
    ;;
stop)
    case "${target}" in
    pg)  pg_stop ;;
    all) goopg_stop sf1; goopg_stop sf05; pg_stop ;;
    *)   goopg_stop "${target}" ;;
    esac
    ;;
status)
    echo "tpcds-server status:"
    status_one "goopg sf1 " "${TPCDS_PORT}"
    status_one "goopg sf05" "${SF05_PORT}"
    status_one "postgres  " "${TPCDS_PG_PORT}"
    ;;
*)
    die "usage: server.sh {start|stop|status} [sf1|sf05|pg|all]"
    ;;
esac
