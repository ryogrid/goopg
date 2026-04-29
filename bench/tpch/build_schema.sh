#!/usr/bin/env bash
# Drive HammerDB to build the TPC-H schema (scale factor 1) against
# the running local PostgreSQL cluster.
#
# Pre-conditions:
#   - PostgreSQL must be up and reachable on $PG_HOST:$PG_PORT.
#   - The bootstrap superuser ($PG_SUPERUSER) must exist with the
#     password configured in env.sh.
#
# HammerDB connects as the superuser, creates the tpch user/database
# if they don't already exist, and then loads the data.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
    echo "PostgreSQL is not reachable at ${PG_HOST}:${PG_PORT}; run setup_pg.sh first." >&2
    exit 1
fi

# HammerDB hardcodes ./scripts/... relative paths in its loaders, so
# we must cd into the install root before launching hammerdbcli.
cd "${HAMMERDB_HOME}"

ts="$(date +%Y%m%d-%H%M%S)"
log_file="${LOG_DIR}/build_${ts}.log"
echo "Building TPC-H schema; log: ${log_file}"

# `auto` mode runs the script and exits, so the build is one-shot.
./hammerdbcli tcl auto "${SCRIPT_DIR}/tcl/build_schema.tcl" 2>&1 | tee "${log_file}"

echo "Schema build complete."
