#!/bin/bash
set -e
PORT=${1:-15901}
SCALE=${2:-1}
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
PG_BIN="$REPO/postgres/local_install/bin"
GOOPG="$REPO/bin/goopg"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export PGPASSWORD=""
TMPDIR=$(mktemp -d)
trap "kill %1 2>/dev/null; wait 2>/dev/null; rm -rf $TMPDIR" EXIT

$GOOPG init -D "$TMPDIR/data" 2>/dev/null
$GOOPG start -D "$TMPDIR/data" -listen "127.0.0.1:$PORT" > /dev/null 2>&1 &
sleep 3

$PG_BIN/pgbench -i -s $SCALE -h 127.0.0.1 -p $PORT -U postgres postgres 2>&1 | tail -1
echo "=== Per-statement timing (-r) scale=$SCALE ==="
$PG_BIN/pgbench -c 1 -t 20 -h 127.0.0.1 -p $PORT -U postgres postgres -r -n 2>&1 | tail -15

echo "=== Collect goroutine profile during pgbench ==="
$PG_BIN/pgbench -c 4 -t 500 -h 127.0.0.1 -p $PORT -U postgres postgres -N &
PID=$!
sleep 3
# Get goroutine dump from pprof endpoint
curl -s "http://127.0.0.1:6060/debug/pprof/goroutine?debug=2" > /tmp/goroutines.txt 2>/dev/null || echo "pprof not available"
echo "Goroutine count: $(grep -c "goroutine" /tmp/goroutines.txt 2>/dev/null || echo 'N/A')"
wait $PID 2>/dev/null || true
