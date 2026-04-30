#!/bin/bash
# run-benchmarks.sh — run inside repo root
set -e
PORT=${1:-15696}
PG_BIN="postgres/local_install/bin"
GOOPG="bin/goopg"
export LD_LIBRARY_PATH="postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export PGPASSWORD=""

TMPDIR=$(mktemp -d)
trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" EXIT

$GOOPG init -D "$TMPDIR/data" 2>/dev/null
$GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" > /dev/null 2>&1 &
sleep 3

bench() {
    local label=$1 clients=$2 txns=$3 extra=$4
    echo "--- $label: clients=$clients ---"
    $PG_BIN/pgbench -c $clients -t $txns -h 127.0.0.1 -p $PORT -U postgres postgres $extra 2>&1 | grep "^tps = "
}

echo "=== pgbench -i (scale=10) ==="
$PG_BIN/pgbench -i -q -s 10 -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1

echo "=== Select-only ==="
bench select-only 1 5000 "-S"
bench select-only 4 2000 "-S"
bench select-only 16 500 "-S"

$PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -c "DROP TABLE IF EXISTS pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers CASCADE" 2>/dev/null || true
$PG_BIN/pgbench -i -q -s 10 -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1

echo "=== Simple update (-N) ==="
bench simple-update 1 500 "-N"
bench simple-update 4 200 "-N"
bench simple-update 16 50 "-N"

$PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -c "DROP TABLE IF EXISTS pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers CASCADE" 2>/dev/null || true
$PG_BIN/pgbench -i -q -s 10 -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1

echo "=== Default TPC-B ==="
bench default 1 100 ""
bench default 4 100 ""
bench default 16 50 ""
