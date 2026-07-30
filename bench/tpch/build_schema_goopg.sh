#!/usr/bin/env bash
# Drive HammerDB to build the TPC-H schema (scale factor 1) against
# a running goopg cluster. Mirrors build_schema.sh.
#
# Pre-conditions:
#   - goopg must be up and reachable on $PG_HOST:$PG_PORT (run
#     setup_goopg.sh first).
#   - HammerDB-5.0 must be extracted at the repo root.
#
# This script reuses the same `tcl/build_schema.tcl` HammerDB
# script as the upstream-PG variant — connection settings come from
# environment variables, so the goopg run is configured purely via
# `env_goopg.sh`.

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

# HammerDB hardcodes ./scripts/... relative paths in its loaders.
cd "${HAMMERDB_HOME}"

ts="$(date +%Y%m%d-%H%M%S)"
log_file="${LOG_DIR}/build_goopg_${ts}.log"
echo "Building TPC-H schema against goopg; log: ${log_file}"

./hammerdbcli tcl auto "${SCRIPT_DIR}/tcl/build_schema.tcl" 2>&1 | tee "${log_file}"

echo "Schema build complete (or errored — check ${log_file})."

# M0125-0030: verify HammerDB's ANALYZE populated stats and checkpoint.
# HammerDB's buildschema internally runs a "GATHERING SCHEMA STATISTICS" step
# that calls ANALYZE on each table. Before M0125-0028 this step errored 42P01
# in per-DB databases; with -0028/-0029 it should succeed and the counts must
# survive a restart, so we verify here and make them durable.
PGPASSWORD="${PG_SUPERUSER_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d "${TPCH_DB}" -tA <<'SQL'
SELECT 'reltuples_check: ' ||
       CASE WHEN count(*) = 8 THEN 'OK (' || count(*)::text || '/8 tables have reltuples > 0)'
            ELSE 'FAIL (' || count(*)::text || '/8 tables have reltuples > 0)'
       END
FROM pg_class
WHERE relname IN ('lineitem','orders','partsupp','part','customer','supplier','nation','region')
  AND reltuples > 0;
SQL
echo "Running CHECKPOINT..."
PGPASSWORD="${PG_SUPERUSER_PASS}" psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d "${TPCH_DB}" -c "CHECKPOINT" >/dev/null 2>&1 \
    && echo "CHECKPOINT done" \
    || echo "CHECKPOINT failed (non-fatal)"
