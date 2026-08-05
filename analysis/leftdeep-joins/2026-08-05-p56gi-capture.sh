#!/usr/bin/env bash
# M0127-P5.6-g-i: EXPLAIN-only capture over ALL 99 TPC-DS SF0.5 queries.
# Every statement in a file is EXPLAIN-prefixed (Q14/23/24/39 hold two), so
# nothing is ever executed for real.
set -uo pipefail
REPO=/home/ryo/work/goopg/goopg
cd "$REPO"
source bench/tpcds/env_tpcds.sh
BIN="$1"; OUT="$2"; CG="$3"
GOOPG_PSQL="psql -h ${TPCDS_HOST} -p ${SF05_PORT} -U ${TPCDS_SUPERUSER} -d postgres"

"$BIN" stop -D "$SF05_GOOPG_DATA" >/dev/null 2>&1 || true
sleep 2
hba=()
[[ -f "${SF05_GOOPG_DATA}/pg_hba.conf" ]] && hba=(--hba "${SF05_GOOPG_DATA}/pg_hba.conf")
GOOPG_CG_UNIT="$CG" "$REPO/scripts/goopg-test-run.sh" "$BIN" start \
    -D "$SF05_GOOPG_DATA" --listen "127.0.0.1:${SF05_PORT}" "${hba[@]}" \
    >/tmp/ds05_planall_${CG}.log 2>&1 &
for i in $(seq 1 180); do
    pg_isready -h 127.0.0.1 -p "$SF05_PORT" -U "$PG_SUPERUSER" >/dev/null 2>&1 && break
    sleep 1
done
pg_isready -h 127.0.0.1 -p "$SF05_PORT" -U "$PG_SUPERUSER" >/dev/null 2>&1 || { echo "server did not start"; exit 1; }

: > "$OUT"
for q in $(seq 1 99); do
    f="$TPCDS_QUERY_DIR/query$q.sql"
    [[ -f "$f" ]] || continue
    echo "===== Q$q =====" >> "$OUT"
    python3 - "$f" > /tmp/ds05_explain_all.sql <<'PY'
import sys
src = open(sys.argv[1]).read()
for stmt in src.split(';'):
    if stmt.strip():
        print("EXPLAIN " + stmt.strip() + ";")
PY
    timeout 180 ${GOOPG_PSQL} -f /tmp/ds05_explain_all.sql >> "$OUT" 2>&1 || echo "(explain rc=$?)" >> "$OUT"
done
"$BIN" stop -D "$SF05_GOOPG_DATA" >/dev/null 2>&1 || true
sleep 2
echo "done -> $OUT"
