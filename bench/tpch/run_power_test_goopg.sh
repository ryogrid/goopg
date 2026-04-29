#!/usr/bin/env bash
# Drive HammerDB to run the TPC-H power test (Q1..Q22) against a
# running goopg cluster previously loaded by build_schema_goopg.sh.
# Mirrors run_power_test.sh.
#
# Pre-conditions:
#   - goopg must be up and reachable on $PG_HOST:$PG_PORT.
#   - The TPC-H schema must already be loaded.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env_goopg.sh
source "${SCRIPT_DIR}/env_goopg.sh"

if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
    echo "goopg is not reachable at ${PG_HOST}:${PG_PORT}; run setup_goopg.sh first." >&2
    exit 1
fi

if [[ ! -x "${HAMMERDB_HOME}/hammerdbcli" ]]; then
    echo "HammerDB not found at ${HAMMERDB_HOME}; extract HammerDB-5.0 at repo root." >&2
    exit 1
fi

cd "${HAMMERDB_HOME}"

ts="$(date +%Y%m%d-%H%M%S)"
log_file="${LOG_DIR}/run_goopg_${ts}.log"
echo "Running TPC-H power test against goopg; log: ${log_file}"

./hammerdbcli tcl auto "${SCRIPT_DIR}/tcl/run_power_test.tcl" 2>&1 | tee "${log_file}"

echo "Power test complete (or errored — check ${log_file})."
