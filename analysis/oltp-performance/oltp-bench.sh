#!/bin/bash
# oltp-bench.sh — run focused pgbench benchmarks and collect wait-event data
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DATA_DIR="${1:-/tmp/goopg-perf-data}"
PORT="${2:-15693}"
PG_BIN="$REPO_ROOT/postgres/local_install/bin"
LD_LIB="${REPO_ROOT}/postgres/local_install/lib"
GOOPG="$REPO_ROOT/bin/goopg"
RESULT="$REPO_ROOT/analysis/oltp-performance"

export PATH="$PG_BIN:$PATH"
export LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-}:${LD_LIB}"
export PGPASSWORD=""

bench() {
    local label="$1" clients="$2" scale="$3" txns="$4" extra="$5"
    echo ""
    echo "=== Benchmark: $label (c=$clients s=$scale t=$txns) ==="
    pgbench -i -q -s "$scale" -h 127.0.0.1 -p "$PORT" -U postgres postgres 2>&1 | tail -1

    # Background wait-event collector
    (
        for i in $(seq 1 999); do
            sleep 1
            psql -h 127.0.0.1 -p "$PORT" -U postgres -d postgres -Atqc \
                "SELECT '$label', extract(epoch from now())::bigint,
                        COALESCE(wait_event_type,'none'), COALESCE(wait_event,'none'), count(*)
                 FROM pg_catalog.pg_stat_activity
                 WHERE backend_type = 'client_backend' AND state = 'active'
                 GROUP BY wait_event_type, wait_event
                 ORDER BY count(*) DESC" 2>/dev/null >> "$RESULT/waits-$label.csv" || true
        done
    ) &
    local wp=$!

    pgbench -c "$clients" -t "$txns" -h 127.0.0.1 -p "$PORT" -U postgres postgres $extra 2>&1 | tee -a "$RESULT/pgbench-all.log"
    kill $wp 2>/dev/null || true
    wait $wp 2>/dev/null || true

    # Cleanup
    psql -h 127.0.0.1 -p "$PORT" -U postgres -d postgres -Atqc \
        "DROP TABLE IF EXISTS pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers CASCADE" 2>/dev/null || true
}

echo "=== OLTP Performance Benchmark ==="
rm -f "$RESULT/pgbench-all.log"
rm -f "$RESULT/waits-"*.csv

# Start server
rm -rf "$DATA_DIR"
mkdir -p "$DATA_DIR"
"$GOOPG" init -D "$DATA_DIR" 2>&1 | tail -1
"$GOOPG" start -D "$DATA_DIR" -listen "127.0.0.1:$PORT" > "$DATA_DIR/server.log" 2>&1 &
sleep 3

if ! pg_isready -h 127.0.0.1 -p "$PORT" -q 2>/dev/null; then
    echo "Server failed to start"; cat "$DATA_DIR/server.log"; exit 1
fi

# Run benchmarks
# Select-only: 1/4/16 clients, scale=10
bench "select-1"   1  10  5000  "-S"
bench "select-4"   4  10  2000  "-S"
bench "select-16" 16  10  500   "-S"

# Simple update (-N): 1/4/16 clients, scale=10
bench "update-1"   1  10  200   "-N"
bench "update-4"   4  10  100   "-N"
bench "update-16" 16  10  50    "-N"

# Default TPC-B: 1/4/16 clients, scale=10
bench "default-1"   1  10  50   ""
bench "default-4"   4  10  50   ""
bench "default-16" 16  10  20   ""

# Scale factor effect: clients=4, scale=1/10/100
bench "default-s1-4"   4  1   50   ""
bench "default-s10-4"  4  10  50   ""
bench "default-s100-4" 4  100 10   ""

# Stop
kill %1 2>/dev/null || true
wait 2>/dev/null || true
echo "=== Done ==="
