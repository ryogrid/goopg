#!/bin/bash
set -e
SCALE=3
PORT=15698
PG_BIN="postgres/local_install/bin"
GOOPG="bin/goopg"
export LD_LIBRARY_PATH="postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export PGPASSWORD=""

TMPDIR=$(mktemp -d)
RESULT="analysis/oltp-performance"
trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" EXIT

echo "=== goopg init ==="
$GOOPG init -D "$TMPDIR/data" 2>/dev/null
echo "=== goopg start ==="
$GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" > /dev/null 2>&1 &
sleep 3

bench() {
    local label=$1 clients=$2 txns=$3 extra=$4
    echo "--- $label c=$clients ---"
    (
        for i in $(seq 1 999); do sleep 1
            $PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atqc \
                "SELECT '$label-$clients', COALESCE(wait_event_type,'none'), COALESCE(wait_event,'none'), count(*) FROM pg_catalog.pg_stat_activity WHERE backend_type='client_backend' AND state='active' GROUP BY wait_event_type,wait_event ORDER BY count(*) DESC" 2>/dev/null >> "$RESULT/waits.csv" || true
        done
    ) &
    local wp=$!
    $PG_BIN/pgbench -c $clients -t $txns -h 127.0.0.1 -p $PORT -U postgres postgres $extra 2>&1 | grep -E "^(tps|latency)"
    kill $wp 2>/dev/null; wait $wp 2>/dev/null || true
}

reinit() {
    $PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atqc \
        "DROP TABLE IF EXISTS pgbench_accounts,pgbench_branches,pgbench_history,pgbench_tellers CASCADE" 2>/dev/null || true
    $PG_BIN/pgbench -i -q -s $SCALE -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1
}

echo "=== pgbench -i (scale=$SCALE) ==="
reinit

echo "=== SELECT ONLY ==="
bench select-only 1 1000 "-S"
bench select-only 4 500  "-S"
bench select-only 16 250 "-S"

echo "=== SIMPLE UPDATE ==="
reinit
bench simple-update 1 200 "-N"
bench simple-update 4 100 "-N"
bench simple-update 16 50 "-N"

echo "=== DEFAULT TPC-B ==="
reinit
bench default 1 50 ""
bench default 4 50 ""
bench default 16 25 ""

echo "=== DONE ==="
