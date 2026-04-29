#!/usr/bin/env bash
# Drive HammerDB to run the TPC-H power test (Q1..Q22) against the
# already-loaded `tpch` database. Per-query timings stream to stdout
# and into bench/tpch/logs/run_<timestamp>.log.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
    echo "PostgreSQL is not reachable at ${PG_HOST}:${PG_PORT}; run setup_pg.sh first." >&2
    exit 1
fi

# Quick sanity check: ensure the build phase has populated the schema.
# Use PGPASSWORD scoped to this single command so we connect as the tpch
# role (env.sh sets the superuser password globally).
row_count=$(PGPASSWORD="${TPCH_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" \
    -U "${TPCH_USER}" -d "${TPCH_DB}" \
    -tAc "SELECT count(*) FROM supplier" 2>/dev/null || echo 0)
if [[ "${row_count}" -eq 0 ]]; then
    echo "TPC-H schema looks empty (supplier count=${row_count}); run build_schema.sh first." >&2
    exit 1
fi
echo "Detected supplier count = ${row_count}; expected $((10000 * TPCH_SCALE_FACT))."

cd "${HAMMERDB_HOME}"

ts="$(date +%Y%m%d-%H%M%S)"
log_file="${LOG_DIR}/run_${ts}.log"
echo "Running TPC-H power test; log: ${log_file}"

./hammerdbcli tcl auto "${SCRIPT_DIR}/tcl/run_power_test.tcl" 2>&1 | tee "${log_file}"

if [[ -f "${TMP}/pg_tproch_jobid" ]]; then
    echo "HammerDB job id: $(cat "${TMP}/pg_tproch_jobid")"
fi
echo "Power test complete."
