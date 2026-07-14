#!/usr/bin/env bash
# aux_fsync_probe.sh — perf-optimize3 auxiliary attribution probe.
#
# For each engine (on the data dirs left by run_rw50.sh): restart, run a 60 s
# c=50 simple-update, and capture
#   - WAL bytes per txn:   pg_current_wal_lsn() delta / pgbench xacts (both engines)
#   - fsync rate:          goopg: strace -f -c -e trace=fdatasync,fsync attached
#                          to the server for a 20 s mid-run window;
#                          PG: pg_stat_wal wal_sync delta (authoritative) plus
#                          the same strace window on the walwriter+checkpointer
#                          is NOT attempted (backends fsync themselves; the
#                          wal_sync counter already covers them).
#   - group-commit width:  xacts/s ÷ fsync/s.
# Deliberately a separate, disclosed AUX run: strace perturbs the traced
# process, so these numbers attribute mechanisms and are NOT the headline TPS.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
RUN_ID="${RUN_ID:?set RUN_ID (existing run_rw50 run)}"
DATA_ROOT="${REPO_ROOT}/tmp/perf-optimize3/${RUN_ID}"
GOOPG_DATA="${DATA_ROOT}/goopg-data"
PG_DATA="${DATA_ROOT}/pg-data"
RUN_DIR="${REPO_ROOT}/analysis/perf-optimize3/runs/${RUN_ID}"
AUX_DIR="${RUN_DIR}/aux"
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

# ---------------- goopg ----------------
log "goopg aux: start"
nohup "$GOOPG_BIN" start -D "$GOOPG_DATA" --listen "127.0.0.1:$GOOPG_PORT" > "$GOOPG_DATA/server.aux.log" 2>&1 &
GPID=$!
wait_for "$GOOPG_PORT" || { echo "goopg did not start"; exit 1; }

LSN0=$(q "$GOOPG_PORT" "SELECT pg_current_wal_lsn()")
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$GOOPG_PORT" -U postgres -c $CLIENTS -j $CLIENTS -T $DUR -N postgres > "$AUX_DIR/goopg_N.pgbench.txt" 2>&1 &
PGB=$!
sleep 20
log "goopg aux: strace 20s window (fdatasync/fsync counts)"
timeout 25 strace -f -c -e trace=fdatasync,fsync -p "$GPID" > "$AUX_DIR/goopg.strace.txt" 2>&1 || true
wait "$PGB" 2>/dev/null || true
LSN1=$(q "$GOOPG_PORT" "SELECT pg_current_wal_lsn()")
DELTA=$(q "$GOOPG_PORT" "SELECT pg_wal_lsn_diff('$LSN1'::pg_lsn, '$LSN0'::pg_lsn)")
{
  echo "lsn0=$LSN0 lsn1=$LSN1 wal_bytes_delta=$DELTA"
  grep -E 'number of transactions|tps =|latency average' "$AUX_DIR/goopg_N.pgbench.txt"
} > "$AUX_DIR/goopg.walbytes.txt"
"$GOOPG_BIN" stop -D "$GOOPG_DATA" >/dev/null 2>&1 || kill -TERM "$GPID" 2>/dev/null
for i in $(seq 1 60); do kill -0 "$GPID" 2>/dev/null || break; sleep 0.5; done
kill -9 "$GPID" 2>/dev/null || true
log "goopg aux done"

# ---------------- PG ----------------
log "PG aux: start"
"$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" -l "$PG_DATA/server.aux.log" start >/dev/null 2>&1
wait_for "$PG_PORT" || { echo "PG did not start"; exit 1; }

LSN0=$(q "$PG_PORT" "SELECT pg_current_wal_lsn()")
W0=$(q "$PG_PORT" "SELECT wal_records||' '||wal_fpi||' '||wal_bytes||' '||wal_write||' '||wal_sync FROM pg_stat_wal")
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$PG_PORT" -U postgres -c $CLIENTS -j $CLIENTS -T $DUR -N postgres > "$AUX_DIR/pg_N.pgbench.txt" 2>&1
LSN1=$(q "$PG_PORT" "SELECT pg_current_wal_lsn()")
W1=$(q "$PG_PORT" "SELECT wal_records||' '||wal_fpi||' '||wal_bytes||' '||wal_write||' '||wal_sync FROM pg_stat_wal")
DELTA=$(q "$PG_PORT" "SELECT pg_wal_lsn_diff('$LSN1'::pg_lsn, '$LSN0'::pg_lsn)")
{
  echo "lsn0=$LSN0 lsn1=$LSN1 wal_bytes_delta=$DELTA"
  echo "pg_stat_wal before: $W0"
  echo "pg_stat_wal after:  $W1"
  grep -E 'number of transactions|tps =|latency average' "$AUX_DIR/pg_N.pgbench.txt"
} > "$AUX_DIR/pg.walbytes.txt"
"$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" stop -m fast >/dev/null 2>&1 || true
log "PG aux done"

cat "$AUX_DIR"/goopg.walbytes.txt "$AUX_DIR"/pg.walbytes.txt
log "aux probe complete: $AUX_DIR"
