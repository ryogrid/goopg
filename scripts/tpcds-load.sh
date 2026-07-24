#!/usr/bin/env bash
# tpcds-load.sh — State 2→3: Load COPY-ready .tsv files into a running goopg.
#
# State 2 prerequisite: scripts/tpcds-setup.sh has been run (TSV + query files exist)
# State 3 outcome: TPC-DS tables loaded + ANALYZE'd in goopg
#
# Usage: scripts/tpcds-load.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"

TPCDS_TOOLS="${REPO_ROOT}/third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools"
TPCDS_DATA_DIR="${RUNTIME_DIR}/tpcds-data"
BENCH_DB="${TPCDS_DB:-postgres}"

log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { echo "FATAL: $*" >&2; exit 1; }

PG="psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_SUPERUSER} -d ${BENCH_DB} -v ON_ERROR_STOP=1"

# ---- Prerequisites ------------------------------------------------
[[ -f "${TPCDS_TOOLS}/tpcds.sql" ]] || die "Run scripts/tpcds-setup.sh first"
pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || \
    die "goopg not running — start it with: scripts/csq-bench-server.sh start"

# ---- Step 1: Create schema ----------------------------------------
log "Step 1/3: Creating TPC-DS schema..."
${PG} -f "${TPCDS_TOOLS}/tpcds.sql" 2>&1 | tail -3
log "  Schema created"

# ---- Step 2: Load data --------------------------------------------
log "Step 2/3: Loading data via COPY..."
OK=0
FAIL=0
for tsv in "${TPCDS_DATA_DIR}"/*.tsv; do
    table=$(basename "$tsv" .tsv)
    # Skip non-table files
    case "$table" in
        dbgen_version|catalog_returns|catalog_sales|customer|customer_address|customer_demographics|\
        date_dim|household_demographics|income_band|inventory|item|promotion|reason|ship_mode|\
        store|store_returns|store_sales|time_dim|warehouse|web_page|web_returns|web_sales|web_site|\
        call_center|catalog_page) ;;
        *) log "  skipping ${table} (not a TPC-DS table)"; continue ;;
    esac

    printf "  %-30s " "${table}..."

    # TRUNCATE if table has data, then COPY
    ${PG} -c "TRUNCATE ${table}" >/dev/null 2>&1 || true
    if ${PG} -c "COPY ${table} FROM '${tsv}'" >/dev/null 2>&1; then
        COUNT=$(${PG} -t -c "SELECT count(*) FROM ${table}" 2>/dev/null | tr -d ' ')
        echo "OK (${COUNT} rows)"
        OK=$((OK + 1))
    else
        echo "FAILED"
        FAIL=$((FAIL + 1))
    fi
done
log "  Loaded: ${OK} tables, Failed: ${FAIL}"

# ---- Step 3: ANALYZE ----------------------------------------------
log "Step 3/3: Running ANALYZE..."
for tsv in "${TPCDS_DATA_DIR}"/*.tsv; do
    table=$(basename "$tsv" .tsv)
    ${PG} -c "ANALYZE ${table}" >/dev/null 2>&1 || true
done
log "  ANALYZE complete"

# ---- Step 4: CHECKPOINT (flush WAL so restart doesn't need replay) ---
log "Step 4/4: Running CHECKPOINT..."
${PG} -c "CHECKPOINT" >/dev/null 2>&1 && log "  CHECKPOINT done" || log "  CHECKPOINT failed (non-fatal)"

log ""
log "=== LOAD COMPLETE ==="
log "Next: scripts/tpcds-run.sh   (run benchmark)"
log "  Or: scripts/tpcds-run.sh N  (run single query N)"
