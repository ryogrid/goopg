#!/bin/bash
set -e
PORT=15705
SCALE=3
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
PG_BIN="$REPO/postgres/local_install/bin"
GOOPG="$REPO/bin/goopg"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export PGPASSWORD=""
TMPDIR=$(mktemp -d)
PPROF_FILE="/tmp/goopg-cpu.pprof"
trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" EXIT

$GOOPG init -D "$TMPDIR/data" 2>/dev/null
$GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" > /dev/null 2>&1 &
sleep 3
$PG_BIN/pgbench -i -s $SCALE -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1

echo "=== Capturing CPU profile during pgbench ==="
rm -f "$PPROF_FILE"
# Start pprof in background
curl -s -o /dev/null localhost:$PORT 2>/dev/null || true # just to check

# Use runtime/pprof via environment
GODEBUG=cpuprofile=$PPROF_FILE \
$GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" 2>/dev/null &
sleep 2
$PG_BIN/pgbench -c 4 -t 200 -h 127.0.0.1 -p $PORT -U postgres postgres -N 2>&1 | grep -E "^tps|^latency"

echo "=== Profile saved to $PPROF_FILE ==="
ls -la "$PPROF_FILE" 2>/dev/null || echo "no profile (GODEBUG may not work with 'go run')"

# Try with built binary CPU profile
$REPO/bin/goopg start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" 2>/dev/null &
sleep 2
# Use curl if pprof endpoint exists
$PG_BIN/pgbench -c 4 -t 200 -h 127.0.0.1 -p $PORT -U postgres postgres -N 2>&1 | grep -E "^tps|^latency"
