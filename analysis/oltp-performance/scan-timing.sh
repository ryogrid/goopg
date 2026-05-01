#!/bin/bash
set -e
PORT=${1:-15931}
SCALE=${2:-3}
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

echo "=== SeqScan: SELECT count(*)==="
time $PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atqc "SELECT count(*) FROM pgbench_accounts" 2>&1

echo "=== IndexScan: SELECT abalance WHERE aid=50000 ==="
time $PG_BIN/psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -Atqc "SELECT abalance FROM pgbench_accounts WHERE aid=50000" 2>&1

echo "=== Single UPDATE timing ==="
time $PG_BIN/pgbench -c 1 -t 1 -h 127.0.0.1 -p $PORT -U postgres postgres -N -n 2>&1 | tail -3
