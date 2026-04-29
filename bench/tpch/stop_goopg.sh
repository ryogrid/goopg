#!/usr/bin/env bash
# Idempotent goopg stop. Mirrors stop_pg.sh but speaks `goopg stop`
# (which routes through the control socket) instead of pg_ctl.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env_goopg.sh
source "${SCRIPT_DIR}/env_goopg.sh"

if [[ ! -d "${PGDATA}" ]]; then
    echo "No data directory at ${PGDATA}; nothing to stop."
    exit 0
fi

if "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1; then
    echo "goopg stopped."
else
    # If the process is already dead, just clean up the pidfile so
    # the next setup_goopg.sh doesn't refuse to start.
    rm -f "${PGDATA}/postmaster.pid"
    echo "goopg was not running (cleaned stale pidfile if any)."
fi
