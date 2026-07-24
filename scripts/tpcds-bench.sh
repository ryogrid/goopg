#!/usr/bin/env bash
# tpcds-bench.sh — TPC-DS SF=1 benchmark runner for goopg.
# Adapted from third-party/tpcds-postgres/tpcds_generator.sh.
# All sudo usage removed; goopg lifecycle managed via csq-bench-server.sh pattern.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# shellcheck source=../bench/tpch/env_goopg.sh
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"

# ---- Configuration ------------------------------------------------
SCALE="${TPCDS_SCALE:-1}"
TPCDS_DIR="${REPO_ROOT}/third-party/tpcds-postgres"
TPCDS_TOOLS="${TPCDS_DIR}/DSGen-software-code-3.2.0rc1/tools"
TPCDS_DATA_DIR="${RUNTIME_DIR}/tpcds-data"
TPCDS_RESULTS_DIR="${RUNTIME_DIR}/tpcds-results"
TPCDS_QUERY_DIR="${TPCDS_DATA_DIR}/queries"
PER_QUERY_TIMEOUT="${TPCDS_TIMEOUT:-600}"

mkdir -p "${TPCDS_DATA_DIR}" "${TPCDS_RESULTS_DIR}" "${TPCDS_QUERY_DIR}"

# ---- Helper functions ---------------------------------------------
die() { echo "FATAL: $*" >&2; exit 1; }

log() { echo "[$(date +%H:%M:%S)] $*"; }

# ---- 1. Build goopg -----------------------------------------------
log "Building goopg..."
go build -o "${GOOPG_BIN}" ./cmd/goopg || die "goopg build failed"

# ---- 2. Start goopg -----------------------------------------------
log "Starting goopg (port ${PG_PORT})..."
"${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
rm -f "${PGDATA}/postmaster.pid"
systemctl --user stop goopg-csq-bench.scope >/dev/null 2>&1 || true
systemctl --user reset-failed goopg-csq-bench.scope >/dev/null 2>&1 || true

GOOPG_CG_UNIT="goopg-csq-bench" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${PGDATA}" \
    --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" \
    >"${RUNTIME_DIR}/goopg.tpcds.log" 2>&1 &
GOOPG_PID=$!

for _ in $(seq 1 180); do
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        log "goopg ready (PID ${GOOPG_PID})"
        break
    fi
    sleep 1
done
pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || die "goopg not ready after 180s"

# ---- 3. Load DDL schema (into postgres database, bypassing per-DB scoping gap) -
log "Loading TPC-DS schema into postgres database..."
SCHEMA_ERRORS="${TPCDS_RESULTS_DIR}/schema_errors.txt"
psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d postgres \
    -f "${TPCDS_TOOLS}/tpcds.sql" 2>"${SCHEMA_ERRORS}" || log "Schema load had errors (see ${SCHEMA_ERRORS})"
SCHEMA_ERR_COUNT=$(wc -l < "${SCHEMA_ERRORS}")
log "Schema loaded (${SCHEMA_ERR_COUNT} error lines)"

# ---- 5. Generate SF=1 data ----------------------------------------
log "Generating TPC-DS SF=${SCALE} data..."
cd "${TPCDS_TOOLS}"
./dsdgen -FORCE -VERBOSE -SCALE "${SCALE}" 2>&1 | tail -5
cd "${REPO_ROOT}"

# ---- 6. Load data tables ------------------------------------------
log "Loading data into goopg..."
TABLE_COUNT=0
for dat in "${TPCDS_TOOLS}"/*.dat; do
    table=$(basename "$dat" .dat)
    log "  Loading ${table}..."
    # Convert pipe-delimited to tab-delimited (goopg COPY uses PG TEXT format).
    # Strip trailing pipe, convert | to \t, empty fields → \N (PG NULL).
    python3 -c "
import sys
for line in open(sys.argv[1]):
    line = line.rstrip('\n\r')
    if line.endswith('|'):
        line = line[:-1]
    fields = [f if f != '' else '\\\\N' for f in line.split('|')]
    print('\t'.join(fields))
" "$dat" > "${TPCDS_DATA_DIR}/${table}.tsv"
    # Fix encoding for customer table
    if [[ "$table" == "customer" ]]; then
        python3 "${TPCDS_DIR}/fix_encoding.py" --filename="${TPCDS_DATA_DIR}/${table}.tsv" 2>/dev/null || true
    fi
    # TRUNCATE (clear any previous data)
    psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d postgres \
        -c "TRUNCATE ${table}" 2>/dev/null || true
    # Use server-side COPY (goopg supports COPY FROM 'file')
    psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d postgres \
        -c "COPY ${table} FROM '${TPCDS_DATA_DIR}/${table}.tsv'" 2>&1 || log "    WARNING: load failed for ${table}"
    TABLE_COUNT=$((TABLE_COUNT + 1))
done
log "Loaded ${TABLE_COUNT} tables"

# ---- 7. ANALYZE all tables ----------------------------------------
log "Running ANALYZE..."
ANALYZE_TABLES=$(grep -oP 'CREATE TABLE \K\w+' "${TPCDS_TOOLS}/tpcds.sql" | tr '\n' ' ')
for t in ${ANALYZE_TABLES}; do
    psql -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d postgres \
        -c "ANALYZE ${t}" 2>/dev/null || log "  ANALYZE ${t} failed"
done
log "ANALYZE complete"

# ---- 8. Generate queries via dsqgen --------------------------------
log "Generating TPC-DS queries..."
cd "${TPCDS_TOOLS}"
./dsqgen -DIRECTORY ../query_templates -INPUT ../query_templates/templates.lst \
    -VERBOSE Y -QUALIFY Y -DIALECT netezza -SCALE "${SCALE}" -OUTPUT_DIR "${TPCDS_QUERY_DIR}" 2>&1 | tail -3
cd "${REPO_ROOT}"
log "Queries generated to ${TPCDS_QUERY_DIR}"

# ---- 9. Split query_0.sql into individual files -------------------
log "Splitting queries..."
python3 "${TPCDS_DIR}/split_sqls.py" 2>&1 || true
# split_sqls.py writes to /cqueries/ — copy to our query dir
if [[ -d /cqueries ]]; then
    cp /cqueries/query*.sql "${TPCDS_QUERY_DIR}/" 2>/dev/null && log "Copied split queries" || log "No split queries found"
else
    # Fall back: split manually
    log "Splitting query_0.sql manually..."
    QUERY_FILE="${TPCDS_QUERY_DIR}/query_0.sql"
    if [[ -f "${QUERY_FILE}" ]]; then
        awk 'BEGIN{RS="\n\n\n+"; n=1} NF>0{printf "%s", $0 > sprintf("'"${TPCDS_QUERY_DIR}"'/query%d.sql", n); close(sprintf("'"${TPCDS_QUERY_DIR}"'/query%d.sql", n)); n++}' "${QUERY_FILE}"
        log "Manual split complete"
    fi
fi

# ---- 10. Create query-fixing helper --------------------------------
fix_pg_query() {
    # Apply the same fixes as split_sqls.py for PG compatibility
    local qnum=$1
    local f="${TPCDS_QUERY_DIR}/query${qnum}.sql"
    [[ -f "$f" ]] || return 1
    # Query 30: fix column name
    [[ $qnum -eq 30 ]] && sed -i 's/c_last_review_date_sk/c_last_review_date/g' "$f"
    # Queries with 'days' keyword issue
    local qdays_list="5 12 16 20 21 32 37 40 77 80 82 92 94 95 98"
    for qd in $qdays_list; do
        [[ $qnum -eq $qd ]] && sed -i 's/ days//g' "$f"
    done
    # Queries needing subquery wrapper
    local loch_list="36 70 86"
    for ql in $loch_list; do
        [[ $qnum -eq $ql ]] && { echo "select * from ("; cat "$f"; echo ") as sub"; } > "${f}.tmp" && mv "${f}.tmp" "$f"
    done
    return 0
}

# ---- 11. Run all queries ------------------------------------------
log "Running TPC-DS queries (timeout=${PER_QUERY_TIMEOUT}s)..."
RESULTS_FILE="${TPCDS_RESULTS_DIR}/results.txt"
echo "# TPC-DS SF=${SCALE} on goopg — $(date -Iseconds)" > "${RESULTS_FILE}"
echo "# query|status|elapsed_s|rows|error" >> "${RESULTS_FILE}"

TOTAL_QUERIES=0
PASS_COUNT=0
FAIL_COUNT=0
TOTAL_TIME=0

for q in $(seq 1 99); do
    fix_pg_query "$q" || true
    QFILE="${TPCDS_QUERY_DIR}/query${q}.sql"
    if [[ ! -f "${QFILE}" ]]; then
        log "  Q${q}: SKIP (no query file)"
        echo "Q${q}|SKIP|0|0|no query file" >> "${RESULTS_FILE}"
        continue
    fi

    TOTAL_QUERIES=$((TOTAL_QUERIES + 1))
    log "  Q${q}: running..."

    START_TIME=$SECONDS
    RESULT=$(timeout "${PER_QUERY_TIMEOUT}" psql \
        -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" -d postgres \
        -c "SET max_parallel_workers_per_gather = 4;" \
        -f "${QFILE}" 2>&1) || true
    ELAPSED=$((SECONDS - START_TIME))

    # Check if goopg is still running; restart if needed
    if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        log "    goopg died during Q${q}! Restarting..."
        "${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
        rm -f "${PGDATA}/postmaster.pid"
        GOOPG_CG_UNIT="goopg-csq-bench" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
            "${GOOPG_BIN}" start -D "${PGDATA}" \
            --listen "${PG_HOST}:${PG_PORT}" \
            --hba "${PGDATA}/pg_hba.conf" \
            >"${RUNTIME_DIR}/goopg.tpcds.log" 2>&1 &
        for _ in $(seq 1 30); do
            pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 && break
            sleep 1
        done
        log "    goopg restarted"
    fi

    # Determine success/failure
    if echo "${RESULT}" | grep -qi "ERROR\|FATAL\|PANIC"; then
        ERR_MSG=$(echo "${RESULT}" | grep -i "ERROR\|FATAL\|PANIC" | head -3 | tr '\n' '; ' | sed 's/|/ /g')
        ROWS=0
        STATUS="FAIL"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    elif echo "${RESULT}" | grep -q "(.*rows\?)"; then
        ROWS=$(echo "${RESULT}" | grep -oP '\(\d+ rows?\)' | tail -1 | grep -oP '\d+' || echo "?")
        STATUS="OK"
        PASS_COUNT=$((PASS_COUNT + 1))
    elif [[ ${ELAPSED} -ge ${PER_QUERY_TIMEOUT} ]]; then
        ROWS=0
        ERR_MSG="timeout after ${PER_QUERY_TIMEOUT}s"
        STATUS="TIMEOUT"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    else
        ROWS=$(echo "${RESULT}" | wc -l)
        STATUS="OK"
        PASS_COUNT=$((PASS_COUNT + 1))
    fi

    TOTAL_TIME=$((TOTAL_TIME + ELAPSED))
    echo "Q${q}|${STATUS}|${ELAPSED}|${ROWS}|${ERR_MSG:-}" >> "${RESULTS_FILE}"
    log "  Q${q}: ${STATUS} elapsed=${ELAPSED}s rows=${ROWS}"
    echo "${RESULT}" > "${TPCDS_RESULTS_DIR}/q${q}_output.txt"
done

# ---- 12. Summary --------------------------------------------------
log "=============================================="
log "TPC-DS SF=${SCALE} benchmark complete"
log "Total queries: ${TOTAL_QUERIES}"
log "Pass: ${PASS_COUNT}  Fail: ${FAIL_COUNT}"
log "Total elapsed: ${TOTAL_TIME}s"
log "Results: ${RESULTS_FILE}"
log "Outputs: ${TPCDS_RESULTS_DIR}/"
log "=============================================="
