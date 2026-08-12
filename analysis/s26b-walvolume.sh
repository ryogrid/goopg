#!/usr/bin/env bash
# M0131-S26b WAL-volume A/B: what do the bare-block first-touch FPIs cost on a
# split-heavy workload?
#
#   analysis/s26b-walvolume.sh <goopg-binary> <label> [port]
#
# Workload: a scattered-key btree load (every insert can split an INTERIOR page,
# which is the sibling-block case the change touches) plus an UPDATE pass. The
# key order is a deterministic multiplicative hash, NOT random(), so the two
# arms perform the same splits and the WAL totals are comparable.
#
# Measurement is `pg_waldump --stats` from the PG 18.3 oracle over the whole
# stream (goopg's WAL is PG-identical), which reports record bytes and FPI bytes
# separately — the number this change moves is the FPI column.
set -euo pipefail

BIN=${1:?binary}
LABEL=${2:?label}
PORT=${3:-5533}
ROOT=/tmp/s26b-wal-$LABEL
DATA=$ROOT/data
PSQL=./postgres/local_install/bin/psql
WALDUMP=./postgres/local_install/bin/pg_waldump

rm -rf "$ROOT"
mkdir -p "$ROOT"
"$BIN" init -D "$DATA" >/dev/null 2>&1

GOOPG_CG_UNIT="s26b-$LABEL" scripts/goopg-test-run.sh "$BIN" start -D "$DATA" \
	--listen 127.0.0.1:"$PORT" >"$ROOT/server.log" 2>&1 &
for _ in $(seq 1 60); do
	if "$PSQL" -h 127.0.0.1 -p "$PORT" -U postgres -d postgres -c 'select 1' >/dev/null 2>&1; then break; fi
	sleep 1
done

run() { "$PSQL" -h 127.0.0.1 -p "$PORT" -U postgres -d postgres -v ON_ERROR_STOP=1 -c "$1" >/dev/null; }

run "CREATE TABLE s26b(id int, v int)"
run "CREATE INDEX s26b_id ON s26b(id)"
run "INSERT INTO s26b SELECT (g * 7919) % 2000000, g FROM generate_series(1,40000) g"
run "UPDATE s26b SET v = v + 1 WHERE v % 7 = 0"

"$BIN" stop -D "$DATA" >/dev/null 2>&1 || true
sleep 2

"$WALDUMP" -p "$DATA/pg_wal" --stats 000000010000000000000001 2>"$ROOT/waldump.err" |
	tee "$ROOT/waldump.stats" | grep -E "^(Btree|XLOG|Heap|Total)" || true
echo "--- $LABEL: full stats in $ROOT/waldump.stats"
