#!/usr/bin/env bash
# tpcds-load-and-bench.sh — Load TPC-DS SF=1 data and run queries on goopg.
# Assumes goopg is already running, data files are already generated,
# and schema is already loaded.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

TPCDS_TOOLS="${REPO_ROOT}/third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools"
TPCDS_DIR="${REPO_ROOT}/third-party/tpcds-postgres"
TPCDS_DATA_DIR="${RUNTIME_DIR}/tpcds-data"
TPCDS_RESULTS_DIR="${RUNTIME_DIR}/tpcds-results"
TPCDS_QUERY_DIR="${TPCDS_DATA_DIR}/queries"
SCALE="${TPCDS_SCALE:-1}"
PER_QUERY_TIMEOUT="${TPCDS_TIMEOUT:-600}"
BENCH_DB="${TPCDS_DB:-postgres}"

mkdir -p "${TPCDS_DATA_DIR}" "${TPCDS_RESULTS_DIR}" "${TPCDS_QUERY_DIR}"

log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { echo "FATAL: $*" >&2; exit 1; }

PG="psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_SUPERUSER} -d ${BENCH_DB}"

# ---- 0. Ensure goopg is ready -------------------------------------
pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || die "goopg not running"

# ---- 1. Re-create schema if needed --------------------------------
log "Ensuring TPC-DS schema..."
for table in call_center catalog_page catalog_returns catalog_sales customer \
    customer_address customer_demographics date_dim dbgen_version \
    household_demographics income_band inventory item promotion reason \
    ship_mode store store_returns store_sales time_dim warehouse \
    web_page web_returns web_sales web_site; do
    ${PG} -c "TRUNCATE ${table}" 2>/dev/null || true
done

# ---- 2. Load all data tables --------------------------------------
log "Loading TPC-DS data..."
TABLE_COUNT=0
for dat in "${TPCDS_TOOLS}"/*.dat; do
    table=$(basename "$dat" .dat)
    # Convert pipe-delimited to tab-delimited with \N for NULLs
    python3 "${REPO_ROOT}/scripts/convert_tpcds.py" "$dat" > "${TPCDS_DATA_DIR}/${table}.tsv"

    # Fix customer encoding
    [[ "$table" == "customer" ]] && python3 "${TPCDS_DIR}/fix_encoding.py" --filename="${TPCDS_DATA_DIR}/${table}.tsv" 2>/dev/null || true

    if ${PG} -c "COPY ${table} FROM '${TPCDS_DATA_DIR}/${table}.tsv'" >/dev/null 2>&1; then
        log "  ${table}: loaded"
        TABLE_COUNT=$((TABLE_COUNT + 1))
    else
        log "  ${table}: FAILED"
    fi
done
log "Loaded ${TABLE_COUNT}/25 tables"

# ---- 3. ANALYZE ---------------------------------------------------
log "Running ANALYZE..."
for table in call_center catalog_page catalog_returns catalog_sales customer \
    customer_address customer_demographics date_dim dbgen_version \
    household_demographics income_band inventory item promotion reason \
    ship_mode store store_returns store_sales time_dim warehouse \
    web_page web_returns web_sales web_site; do
    ${PG} -c "ANALYZE ${table}" 2>/dev/null || log "  ANALYZE ${table} failed"
done
log "ANALYZE complete"

# ---- 4. Generate queries ------------------------------------------
log "Generating TPC-DS queries..."
cd "${TPCDS_TOOLS}"
./dsqgen -DIRECTORY ../query_templates -INPUT ../query_templates/templates.lst \
    -VERBOSE Y -QUALIFY Y -DIALECT netezza -SCALE "${SCALE}" -OUTPUT_DIR "${TPCDS_QUERY_DIR}" 2>&1 | tail -1
cd "${REPO_ROOT}"
log "Queries generated to ${TPCDS_QUERY_DIR}"

# ---- 5. Split queries ---------------------------------------------
log "Splitting query_0.sql..."
QUERY_BLOB="${TPCDS_QUERY_DIR}/query_0.sql"
if [[ -f "${QUERY_BLOB}" ]]; then
    python3 "${TPCDS_DIR}/split_sqls.py" 2>/dev/null || true
    if [[ -d /cqueries ]]; then
        cp /cqueries/query*.sql "${TPCDS_QUERY_DIR}/" 2>/dev/null && log "Copied split queries" || true
    fi
    # Manual split if split_sqls.py didn't work
    if [[ ! -f "${TPCDS_QUERY_DIR}/query1.sql" ]]; then
        awk 'BEGIN{RS="\n\n\n+"; n=1} NF>0{printf "%s", $0 > sprintf("'"${TPCDS_QUERY_DIR}"'/query%d.sql", n); close(sprintf("'"${TPCDS_QUERY_DIR}"'/query%d.sql", n)); n++}' "${QUERY_BLOB}"
        log "Manual split done"
    fi
fi

# ---- 6. Run all queries -------------------------------------------
log "Running TPC-DS queries (timeout=${PER_QUERY_TIMEOUT}s per query)..."
RESULTS="${TPCDS_RESULTS_DIR}/results.txt"
echo "# TPC-DS SF=${SCALE} on goopg — $(date -Iseconds)" > "${RESULTS}"
echo "# query|status|elapsed_s|rows|error" >> "${RESULTS}"

TOTAL_TIME=0
OK=0
FAIL=0
TIMEOUT_COUNT=0

for q in $(seq 1 99); do
    QFILE="${TPCDS_QUERY_DIR}/query${q}.sql"
    if [[ ! -f "${QFILE}" ]]; then
        log "  Q${q}: SKIP (no query file)"
        continue
    fi

    log "  Q${q}: running..."
    START=$SECONDS
    QOUT=$(timeout "${PER_QUERY_TIMEOUT}" ${PG} \
        -c "SET max_parallel_workers_per_gather = 4;" \
        -f "${QFILE}" 2>&1) || true
    ELAPSED=$((SECONDS - START))

    # Check for goopg crash and restart
    if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        log "    goopg CRASHED! Restarting..."
        "${REPO_ROOT}/bench/tpcds/server.sh" start sf1 2>&1 | tail -1
        pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || die "goopg restart failed"
    fi

    if [[ ${ELAPSED} -ge ${PER_QUERY_TIMEOUT} ]]; then
        STATUS="TIMEOUT"
        ROWS=0
        ERR="timeout after ${PER_QUERY_TIMEOUT}s"
        TIMEOUT_COUNT=$((TIMEOUT_COUNT + 1))
    elif echo "${QOUT}" | grep -qi "ERROR\|FATAL\|PANIC"; then
        ERR=$(echo "${QOUT}" | grep -i "ERROR\|FATAL\|PANIC" | head -2 | tr '\n' '; ' | sed 's/|/ /g' | cut -c1-200)
        ROWS=0
        STATUS="ERROR"
        FAIL=$((FAIL + 1))
    elif echo "${QOUT}" | grep -q "(.*rows\?)"; then
        ROWS=$(echo "${QOUT}" | grep -oP '\(\d+ rows?\)' | tail -1 | grep -oP '\d+' || echo "?")
        STATUS="OK"
        OK=$((OK + 1))
    else
        ROWS=$(echo "${QOUT}" | wc -l)
        STATUS="OK?"
        OK=$((OK + 1))
    fi

    TOTAL_TIME=$((TOTAL_TIME + ELAPSED))
    echo "Q${q}|${STATUS}|${ELAPSED}|${ROWS}|${ERR:-}" >> "${RESULTS}"
    log "    Q${q}: ${STATUS} elapsed=${ELAPSED}s rows=${ROWS}"
    echo "${QOUT}" > "${TPCDS_RESULTS_DIR}/q${q}_output.txt"
done

# ---- 7. Summary ---------------------------------------------------
log "=== DONE ==="
log "OK: ${OK}  ERROR: ${FAIL}  TIMEOUT: ${TIMEOUT_COUNT}  Total: ${TOTAL_TIME}s"
log "Results: ${RESULTS}"
