#!/usr/bin/env bash
#
# csq-bench-server.sh — start/stop the TPC-H bench goopg server for the
# correlated-subquery-planning (S0–S3) implementation work.
#
# Why this exists: plan-gate / plan-snapshot-capture / tpch-runner timing runs
# all need a live bench server on 127.0.0.1:65433, and EVERY goopg server start
# in this repository must go through the cgroup memory cap
# (scripts/goopg-test-run.sh) — an unbounded TPC-H intermediate otherwise
# thrashes swap and trips the WSL2 system OOM killer. This wrapper makes the
# capped start/stop a one-liner so no step is tempted to run `goopg start`
# directly.
#
# Usage:
#   scripts/csq-bench-server.sh start     # capped start + wait for readiness
#   scripts/csq-bench-server.sh stop      # control-socket stop + scope cleanup
#   scripts/csq-bench-server.sh status
#
# NEVER `pkill -f goopg` — it self-matches the invoking shell (exit 144).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# shellcheck source=../bench/tpch/env_goopg.sh
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"

CG_UNIT="${GOOPG_CG_UNIT:-goopg-csq-bench}"
LOG="${RUNTIME_DIR}/goopg.csq-bench.log"
READY_TIMEOUT="${CSQ_READY_TIMEOUT:-180}"

cmd="${1:-status}"

case "${cmd}" in
start)
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg )
    # Stop any stale instance on this data dir via the control socket.
    if "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1; then
        echo "csq-bench-server: stopped stale instance"
    else
        rm -f "${PGDATA}/postmaster.pid"
    fi
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

    echo "csq-bench-server: starting capped (scope ${CG_UNIT}) on ${PG_HOST}:${PG_PORT}"
    GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${GOOPG_BIN}" start -D "${PGDATA}" \
        --listen "${PG_HOST}:${PG_PORT}" \
        --hba "${PGDATA}/pg_hba.conf" \
        >"${LOG}" 2>&1 &

    for _ in $(seq 1 "${READY_TIMEOUT}"); do
        if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
            echo "csq-bench-server: ready (log ${LOG})"
            exit 0
        fi
        sleep 1
    done
    echo "csq-bench-server: FATAL — not ready after ${READY_TIMEOUT}s; log tail:" >&2
    tail -n 20 "${LOG}" >&2 || true
    exit 1
    ;;
stop)
    "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    echo "csq-bench-server: stopped"
    ;;
status)
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        echo "csq-bench-server: UP on ${PG_HOST}:${PG_PORT}"
    else
        echo "csq-bench-server: DOWN"
    fi
    ;;
*)
    echo "usage: $0 {start|stop|status}" >&2
    exit 2
    ;;
esac
