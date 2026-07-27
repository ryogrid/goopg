#!/usr/bin/env bash
# Run TPC-DS queries on PostgreSQL for reference comparison.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

QDIR="${RUNTIME_DIR}/tpcds-data/queries"
RDIR="${RUNTIME_DIR}/tpcds-results"
RF="${RDIR}/pg_results.txt"
TO="${1:-120}"
PG_P="${PG_PREFIX}/bin/psql -h ${TPCDS_HOST} -p ${TPCDS_PG_PORT} -U ${TPCDS_PG_USER} -d ${TPCDS_PG_DB}"

mkdir -p "${RDIR}"
echo "# TPC-DS SF=1 on PG 18.3 — $(date -Iseconds)" > "${RF}"
echo "# query|status|elapsed_s|rows|error" >> "${RF}"

OK=0; FAIL=0; TO_CNT=0; TT=0
for q in $(seq 1 99); do
    QF="${QDIR}/query${q}.sql"
    [[ -f "$QF" ]] || continue
    START=$SECONDS
    QOUT=$(timeout "${TO}" ${PG_P} -f "$QF" 2>&1) && QEX=0 || QEX=$?
    ELAPSED=$((SECONDS - START))
    if [[ $QEX -eq 124 ]]; then
        S="TIMEOUT"; ROWS=0; ERR="timeout after ${TO}s"; TO_CNT=$((TO_CNT+1))
    elif echo "$QOUT" | grep -qi "ERROR"; then
        S="ERROR"; ROWS=0; FAIL=$((FAIL+1))
        ERR=$(echo "$QOUT" | grep -i "ERROR" | head -1 | tr -d '|' | cut -c1-120)
    elif echo "$QOUT" | grep -q "(.*rows\?)"; then
        S="OK"; OK=$((OK+1))
        ROWS=$(echo "$QOUT" | grep -oP '\(\d+ rows?\)' | tail -1 | grep -oP '\d+' || echo "?")
        ERR=""
    else
        S="OK"; ROWS=$(echo "$QOUT" | wc -l); OK=$((OK+1)); ERR=""
    fi
    TT=$((TT+ELAPSED))
    echo "Q${q}|${S}|${ELAPSED}|${ROWS}|${ERR}" >> "${RF}"
    printf "Q%-3s %-7s %5ss %6s\n" "$q" "$S" "$ELAPSED" "$ROWS"
done
echo "=== DONE: OK=$OK FAIL=$FAIL TO=$TO_CNT Total=${TT}s ==="
