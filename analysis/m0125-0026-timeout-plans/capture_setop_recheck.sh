#!/usr/bin/env bash
# M0125-0026 step 1 — capture plain EXPLAIN for the timeout class on both
# engines. NOTHING here executes a query: `EXPLAIN ANALYZE` is FORBIDDEN on
# goopg for this set (these are precisely the queries that do not finish), and
# on PG it is unnecessary because plain estimates suffice for classification.
#
# Usage: capture.sh <arm-dir> goopg|pg
#   capture.sh goopg-warm     goopg     # goopg default: warm stats + relsize 2
#   capture.sh goopg-relsize0 goopg     # server started GOOPG_RELSIZE_FALLBACK=0
#   capture.sh pg             pg
#
# Query set (18) = the warm gate's 12 hard members
#   (Q5 Q8 Q14 Q30 Q31 Q35 Q54 Q64 Q65 Q71 Q78 Q81)
# + the four the relation-size fallback ANSWERS (Q10 Q47 Q67 Q69, contrast set)
# + Q72, the one query in either benchmark family the fallback COSTS time
# + Q18, the warm-stats regression filed as M0125-0033.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 1
REPO_ROOT="$PWD"
. bench/tpcds/env_tpcds.sh

ARM="${1:?arm dir}"
ENGINE="${2:?goopg|pg}"
OUT="analysis/m0125-0026-timeout-plans/${ARM}"
mkdir -p "$OUT"

case "$ENGINE" in
goopg) PSQL=(psql -h "$TPCDS_HOST" -p "$SF05_PORT" -U "$TPCDS_SUPERUSER" -d postgres) ;;
pg) PSQL=(psql -h "$TPCDS_HOST" -p "$TPCDS_PG_PORT" -U "$TPCDS_PG_USER" -d "$SF05_PG_DB") ;;
*)
    echo "unknown engine $ENGINE" >&2
    exit 2
    ;;
esac

QUERIES="5 8 14 18 54 67 71"

for n in $QUERIES; do
    src="${TPCDS_QUERY_DIR}/query${n}.sql"
    [ -r "$src" ] || {
        echo "missing $src" >&2
        exit 1
    }
    # Split on the statement terminator and prefix each statement with EXPLAIN.
    # Only query14 has two statements; the rest are single-statement files.
    sql=$(awk 'BEGIN{RS=";"} {gsub(/^[ \t\r\n]+/,""); if (length($0)>0) print "EXPLAIN\n" $0 ";"}' "$src")
    {
        echo "-- M0125-0026 capture  arm=${ARM}  engine=${ENGINE}  query=${n}"
        echo "-- source: ${TPCDS_QUERY_DIR#"$REPO_ROOT"/}/query${n}.sql"
        printf -- '-- %s\n' "$(printf '%s ' "${PSQL[@]}")"
    } >"${OUT}/q${n}.txt"
    # 120 s is a plan-time guard only; a plain EXPLAIN that needs longer is
    # itself a finding (recorded as EXPLAIN-TIMEOUT in the file).
    if ! timeout 120 "${PSQL[@]}" -X -q -P pager=off -v ON_ERROR_STOP=0 \
        -c "$sql" >>"${OUT}/q${n}.txt" 2>&1; then
        rc=$?
        echo "-- CAPTURE-FAILED rc=${rc} (124 = EXPLAIN itself exceeded 120 s)" >>"${OUT}/q${n}.txt"
    fi
    echo "  q${n}: $(wc -l <"${OUT}/q${n}.txt") lines"
done
echo "arm ${ARM} done -> ${OUT}"
