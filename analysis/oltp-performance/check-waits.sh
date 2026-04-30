#!/bin/bash
set -e
PORT=15701
SCALE=3
REPO=$(cd "$(dirname "$0")/../.." && pwd)
PG_BIN="$REPO/postgres/local_install/bin"
GOOPG="$REPO/bin/goopg"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export PGPASSWORD=""

TMPDIR=$(mktemp -d)
RESULT="$REPO/analysis/oltp-performance"
trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" EXIT

$GOOPG init -D "$TMPDIR/data" 2>/dev/null
$GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" > /dev/null 2>&1 &
sleep 3

echo "=== init scale=$SCALE ==="
$PG_BIN/pgbench -i -s $SCALE -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1

wait_snapshot() {
    local label=$1
    # Get wait events (COALESCE not available in v0, use CASE)
    $PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atqc "
        SELECT '$label',
               CASE WHEN wait_event_type = '' THEN 'none' ELSE wait_event_type END,
               CASE WHEN wait_event = '' THEN 'none' ELSE wait_event END,
               count(*)
        FROM pg_catalog.pg_stat_activity
        WHERE state = 'active'
        GROUP BY 2,3
        ORDER BY count(*) DESC" 2>&1 | tee -a "$RESULT/waits2.csv"
}

echo "=== Benchmark: simple update (-N) ==="
$PG_BIN/pgbench -c 4 -t 500 -h 127.0.0.1 -p $PORT -U postgres postgres -N &
PGBENCH_PID=$!
sleep 5
wait_snapshot "update-N-4"
sleep 5
wait_snapshot "update-N-4"
wait $PGBENCH_PID 2>/dev/null || true
echo "pgbench result:"
$PG_BIN/pgbench -c 4 -t 500 -h 127.0.0.1 -p $PORT -U postgres postgres -N 2>&1 | grep -E "^tps|^latency" || true

echo "=== Benchmark: default TPC-B ==="
$PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atqc \
    "DROP TABLE IF EXISTS pgbench_accounts,pgbench_branches,pgbench_history,pgbench_tellers CASCADE" 2>/dev/null || true
$PG_BIN/pgbench -i -s $SCALE -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1

$PG_BIN/pgbench -c 4 -t 500 -h 127.0.0.1 -p $PORT -U postgres postgres &
PGBENCH_PID=$!
sleep 5
wait_snapshot "default-4"
sleep 5
wait_snapshot "default-4"
wait $PGBENCH_PID 2>/dev/null || true
echo "pgbench result:"
$PG_BIN/pgbench -c 4 -t 500 -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | grep -E "^tps|^latency" || true

echo "=== Done ==="
cat "$RESULT/waits2.csv" 2>/dev/null || echo "no wait events"
