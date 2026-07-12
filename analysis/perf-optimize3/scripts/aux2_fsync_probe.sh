#!/usr/bin/env bash
# aux2_fsync_probe.sh — corrected AUX attribution probe (perf-optimize3).
#
# Fixes vs aux_fsync_probe.sh (kept for provenance):
#   - goopg WAL bytes: pg_current_wal_lsn() is not wired at runtime on goopg
#     (catalog-only stub) → use pg_stat_wal_io wal_buffers_{flush,overflow}_
#     drain_bytes deltas instead (bytes actually drained to segment files).
#   - goopg fsync count: ptrace_scope=1 forbids strace attach → launch the
#     server AS strace's child (`strace -f -c -e trace=fdatasync,fsync goopg
#     start ...`); the -c summary lands in the -o file when the server exits.
#     Filtered -c tracing adds overhead only on the two traced syscalls
#     (fdatasync is O(kHz) max) — disclosed AUX, not headline.
#   - PG fsync count: PG 18 moved wal_sync to pg_stat_io → use
#     `pg_stat_io WHERE object='wal'` fsyncs delta; wal bytes still via
#     pg_current_wal_lsn() delta.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
RUN_ID="${RUN_ID:?set RUN_ID}"
DATA_ROOT="${REPO_ROOT}/tmp/perf-optimize3/${RUN_ID}"
GOOPG_DATA="${DATA_ROOT}/goopg-data"
PG_DATA="${DATA_ROOT}/pg-data"
AUX_DIR="${REPO_ROOT}/analysis/perf-optimize3/runs/${RUN_ID}/aux2"
GOOPG_BIN="${GOOPG_BIN:?set GOOPG_BIN}"
PG_BIN_DIR="${REPO_ROOT}/postgres/local_install/bin"
export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export GOMEMLIMIT="${GOMEMLIMIT:-18GiB}"

GOOPG_PORT=5533
PG_PORT=5534
DUR=60
CLIENTS=50

mkdir -p "$AUX_DIR"
log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
wait_for() { for i in $(seq 1 375); do "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$1" -U postgres -d postgres -c 'SELECT 1' >/dev/null 2>&1 && return 0; sleep 0.4; done; return 1; }
q() { "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$1" -U postgres -d postgres -Atc "$2" 2>/dev/null; }

# ---------------- goopg (server as strace child) ----------------
log "goopg aux2: start under strace -c (fdatasync/fsync only)"
nohup strace -f -c -e trace=fdatasync,fsync -o "$AUX_DIR/goopg.strace.txt" \
  "$GOOPG_BIN" start -D "$GOOPG_DATA" --listen "127.0.0.1:$GOOPG_PORT" \
  > "$GOOPG_DATA/server.aux2.log" 2>&1 &
wait_for "$GOOPG_PORT" || { echo "goopg did not start"; exit 1; }

D0=$(q "$GOOPG_PORT" "SELECT wal_buffers_flush_drain_bytes::int8 + wal_buffers_overflow_drain_bytes::int8 FROM pg_stat_wal_io")
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$GOOPG_PORT" -U postgres -c $CLIENTS -j $CLIENTS -T $DUR -N postgres > "$AUX_DIR/goopg_N.pgbench.txt" 2>&1
D1=$(q "$GOOPG_PORT" "SELECT wal_buffers_flush_drain_bytes::int8 + wal_buffers_overflow_drain_bytes::int8 FROM pg_stat_wal_io")
{
  echo "drain_bytes_before=$D0 after=$D1 delta=$((D1-D0))"
  grep -E 'number of transactions|tps =|latency average' "$AUX_DIR/goopg_N.pgbench.txt"
} > "$AUX_DIR/goopg.walbytes.txt"
"$GOOPG_BIN" stop -D "$GOOPG_DATA" >/dev/null 2>&1 || true
# wait for the server (strace's child) to exit so strace flushes its summary
for i in $(seq 1 60); do ss -lnt 2>/dev/null | grep -q ":$GOOPG_PORT " || break; sleep 0.5; done
sleep 2
log "goopg aux2 done"

# ---------------- PG ----------------
log "PG aux2: start"
"$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" -l "$PG_DATA/server.aux2.log" start >/dev/null 2>&1
wait_for "$PG_PORT" || { echo "PG did not start"; exit 1; }

LSN0=$(q "$PG_PORT" "SELECT pg_current_wal_lsn()")
F0=$(q "$PG_PORT" "SELECT coalesce(sum(fsyncs),0)||' '||coalesce(sum(writes),0) FROM pg_stat_io WHERE object='wal'")
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$PG_PORT" -U postgres -c $CLIENTS -j $CLIENTS -T $DUR -N postgres > "$AUX_DIR/pg_N.pgbench.txt" 2>&1
LSN1=$(q "$PG_PORT" "SELECT pg_current_wal_lsn()")
F1=$(q "$PG_PORT" "SELECT coalesce(sum(fsyncs),0)||' '||coalesce(sum(writes),0) FROM pg_stat_io WHERE object='wal'")
DELTA=$(q "$PG_PORT" "SELECT pg_wal_lsn_diff('$LSN1'::pg_lsn, '$LSN0'::pg_lsn)")
FPI=$(q "$PG_PORT" "SELECT wal_fpi||' '||wal_records||' '||wal_bytes FROM pg_stat_wal")
{
  echo "lsn0=$LSN0 lsn1=$LSN1 wal_bytes_delta=$DELTA"
  echo "wal_io fsyncs/writes before: $F0   after: $F1"
  echo "pg_stat_wal (lifetime fpi records bytes): $FPI"
  grep -E 'number of transactions|tps =|latency average' "$AUX_DIR/pg_N.pgbench.txt"
} > "$AUX_DIR/pg.walbytes.txt"
"$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" stop -m fast >/dev/null 2>&1 || true
log "PG aux2 done"

echo "=== goopg ==="; cat "$AUX_DIR/goopg.walbytes.txt"; echo; sed -n '1,12p' "$AUX_DIR/goopg.strace.txt" 2>/dev/null
echo "=== PG ===";   cat "$AUX_DIR/pg.walbytes.txt"
