#!/bin/bash
# perf-sweep.sh — sweep across shared_buffers sizes to identify buffer-pool effects
set -e
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
PG_BIN="$REPO/postgres/local_install/bin"
GOOPG="$REPO/bin/goopg"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export PGPASSWORD=""

bench() {
    local label=$1 sb=$2 clients=$3
    local port=$4

    TMPDIR=$(mktemp -d)
    trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" RETURN

    $GOOPG init -D "$TMPDIR/data" 2>/dev/null
    # Set shared_buffers
    echo "shared_buffers = $sb" >> "$TMPDIR/data/postgresql.conf"
    $GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$port" > /dev/null 2>&1 &
    sleep 3

    $PG_BIN/pgbench -i -q -s 3 -h 127.0.0.1 -p $port -U postgres postgres 2>&1 | tail -1
    echo "$label sb=$sb"
    $PG_BIN/pgbench -c $clients -t 200 -h 127.0.0.1 -p $port -U postgres postgres -N 2>&1 | grep -E "^tps|^latency"
    $PG_BIN/pgbench -c $clients -t 50 -h 127.0.0.1 -p $port -U postgres postgres 2>&1 | grep -E "^tps|^latency"

    kill %1 2>/dev/null; wait 2>/dev/null
}

echo "=== Shared buffer sweep ==="
# Test with tiny, default, and large shared_buffers
bench "sb-tiny"     "65536"     4 15810   # 64KB
bench "sb-small"    "16777216"  4 15811   # 16MB
bench "sb-default"  "134217728" 4 15812   # 128MB (default)
bench "sb-large"    "536870912" 4 15813   # 512MB

echo "=== GOMAXPROCS sweep ==="
for gmp in 1 2 4 8; do
    TMPDIR=$(mktemp -d)
    port=$((15820 + gmp))
    trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" RETURN

    $GOOPG init -D "$TMPDIR/data" 2>/dev/null
    $GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$port" > /dev/null 2>&1 &
    sleep 3

    $PG_BIN/pgbench -i -q -s 3 -h 127.0.0.1 -p $port -U postgres postgres 2>&1 | tail -1
    echo "GOMAXPROCS=$gmp"
    GOMAXPROCS=$gmp $PG_BIN/pgbench -c 4 -t 200 -h 127.0.0.1 -p $port -U postgres postgres -N 2>&1 | grep -E "^tps|^latency"
    GOMAXPROCS=$gmp $PG_BIN/pgbench -c 4 -t 50 -h 127.0.0.1 -p $port -U postgres postgres 2>&1 | grep -E "^tps|^latency"

    kill %1 2>/dev/null; wait 2>/dev/null
done
echo "=== Done ==="
