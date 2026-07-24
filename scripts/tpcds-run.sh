#!/usr/bin/env bash
# tpcds-run.sh — State 3→Results: Run TPC-DS queries against a loaded goopg instance.
#
# State 3 prerequisite: scripts/tpcds-load.sh has been run (data loaded + ANALYZE'd)
# Outcome: results in ${RUNTIME_DIR}/tpcds-results/results.txt
#
# Usage:
#   scripts/tpcds-run.sh              # run all 99 queries
#   scripts/tpcds-run.sh 14           # run only query 14
#   scripts/tpcds-run.sh 1,3,5-10     # run queries 1,3,5,6,7,8,9,10
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
source "${REPO_ROOT}/bench/tpch/env_goopg.sh"

TPCDS_QUERY_DIR="${RUNTIME_DIR}/tpcds-data/queries"
TPCDS_RESULTS_DIR="${RUNTIME_DIR}/tpcds-results"
BENCH_DB="${TPCDS_DB:-postgres}"
PER_QUERY_TIMEOUT="${TPCDS_TIMEOUT:-600}"

mkdir -p "${TPCDS_RESULTS_DIR}"

log() { echo "[$(date +%H:%M:%S)] $*"; }
die() { echo "FATAL: $*" >&2; exit 1; }

PG="psql -h ${PG_HOST} -p ${PG_PORT} -U ${PG_SUPERUSER} -d ${BENCH_DB}"
RESULTS="${TPCDS_RESULTS_DIR}/results.txt"
RESTART_CMD="${REPO_ROOT}/scripts/csq-bench-server.sh"

# ---- Prerequisites ------------------------------------------------
pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || \
    die "goopg not running"
[[ -f "${TPCDS_QUERY_DIR}/query1.sql" ]] || die "No query files — run scripts/tpcds-setup.sh first"

# ---- Resolve query list -------------------------------------------
if [[ $# -ge 1 ]]; then
    # Parse comma-separated list with ranges: e.g. "1,3,5-10"
    QLIST=()
    IFS=',' read -ra PARTS <<< "$1"
    for part in "${PARTS[@]}"; do
        if [[ "$part" =~ ^([0-9]+)-([0-9]+)$ ]]; then
            for ((q=${BASH_REMATCH[1]}; q<=${BASH_REMATCH[2]}; q++)); do
                QLIST+=("$q")
            done
        else
            QLIST+=("$part")
        fi
    done
else
    # All 1-99 that have query files
    QLIST=()
    for q in $(seq 1 99); do
        [[ -f "${TPCDS_QUERY_DIR}/query${q}.sql" ]] && QLIST+=("$q")
    done
fi

log "Running ${#QLIST[@]} TPC-DS queries (timeout=${PER_QUERY_TIMEOUT}s)..."
echo "# TPC-DS SF=1 on goopg — $(date -Iseconds)" > "${RESULTS}"
echo "# query|status|elapsed_s|rows|error" >> "${RESULTS}"

OK=0; FAIL=0; TIMEOUT_COUNT=0; TOTAL_TIME=0

for q in "${QLIST[@]}"; do
    QFILE="${TPCDS_QUERY_DIR}/query${q}.sql"
    [[ -f "$QFILE" ]] || { log "  Q${q}: SKIP (no file)"; continue; }

    log "  Q${q}: running..."
    START=$SECONDS
    QOUT=$(timeout "${PER_QUERY_TIMEOUT}" ${PG} \
        -c "SET max_parallel_workers_per_gather = 4;" \
        -f "$QFILE" 2>&1) && QEXIT=0 || QEXIT=$?
    ELAPSED=$((SECONDS - START))

    # Save full output
    echo "$QOUT" > "${TPCDS_RESULTS_DIR}/q${q}_output.txt"

    # Crash detection + restart
    if ! pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        log "    goopg CRASHED during Q${q}! Restarting..."
        ${RESTART_CMD} start 2>&1 | tail -1
        for _ in $(seq 1 30); do
            pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 && break
            sleep 1
        done
        pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1 || die "restart failed"
        log "    restarted"
    fi

    # Classify result
    if echo "$QOUT" | grep -qi "ERROR\|FATAL"; then
        ERR=$(echo "$QOUT" | grep -i "ERROR\|FATAL" | head -2 | tr '\n' '; ' | sed 's/|/ /g' | cut -c1-250)
        ROWS=0
        STATUS="ERROR"
        FAIL=$((FAIL + 1))
    elif echo "$QOUT" | grep -qi "PANIC"; then
        ERR="PANIC — goopg internal error"
        ROWS=0
        STATUS="PANIC"
        FAIL=$((FAIL + 1))
    elif echo "$QOUT" | grep -q "(.*rows\?)"; then
        ROWS=$(echo "$QOUT" | grep -oP '\(\d+ rows?\)' | tail -1 | grep -oP '\d+' || echo "?")
        STATUS="OK"
        OK=$((OK + 1))
    elif [[ ${ELAPSED} -ge ${PER_QUERY_TIMEOUT} ]]; then
        ROWS=0
        ERR="timeout after ${PER_QUERY_TIMEOUT}s"
        STATUS="TIMEOUT"
        TIMEOUT_COUNT=$((TIMEOUT_COUNT + 1))
    elif [[ $QEXIT -ne 0 ]] && echo "$QOUT" | grep -q "canceling statement"; then
        ROWS=0
        ERR="canceled"
        STATUS="CANCEL"
        FAIL=$((FAIL + 1))
    else
        ROWS=$(echo "$QOUT" | wc -l)
        STATUS="OK"
        OK=$((OK + 1))
    fi

    TOTAL_TIME=$((TOTAL_TIME + ELAPSED))
    echo "Q${q}|${STATUS}|${ELAPSED}|${ROWS}|${ERR:-}" >> "${RESULTS}"
    printf "    Q%-3s %-7s elapsed=%-5s rows=%-8s\n" "$q" "$STATUS" "${ELAPSED}s" "$ROWS"
done

# ---- Summary -------------------------------------------------------
TOTAL=${#QLIST[@]}
log "=============================================="
log "TPC-DS done.  OK: ${OK}  ERROR: ${FAIL}  TIMEOUT: ${TIMEOUT_COUNT}  Total: ${TOTAL_TIME}s"
log "Results: ${RESULTS}"
