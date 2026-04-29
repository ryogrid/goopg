#!/usr/bin/env bash
# Stop the local PostgreSQL cluster started by setup_pg.sh.
# Safe to run repeatedly: returns 0 even when nothing is running.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

if [[ ! -f "${PGDATA}/postmaster.pid" ]]; then
    echo "No running cluster at ${PGDATA}"
    exit 0
fi

if pg_ctl -D "${PGDATA}" status >/dev/null 2>&1; then
    pg_ctl -D "${PGDATA}" -m fast -w stop
else
    # Stale postmaster.pid (process gone but pidfile remains).
    echo "Stale postmaster.pid found, removing"
    rm -f "${PGDATA}/postmaster.pid"
fi
