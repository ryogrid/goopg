#!/usr/bin/env bash
# bench_goopg_su50.sh — goopg-only c=50 simple-update headline + CPU/alloc/mutex
# profiles, for before/after fix comparison on the SAME data dir.
#
# Usage:
#   GOOPG_BIN=/path/to/goopg LABEL=before DATA_DIR=<dir> OUT_DIR=<dir> \
#     bash bench_goopg_su50.sh [--init]
#
# Conditions match analysis/perf-optimize2/scripts/run_su50.sh (uncapped,
# GOMEMLIMIT=18GiB, mutex/block rate=1, scale 100, -c50 -j50 -T180 -P10 -N).
# The PG side is unchanged from run 20260712_114859 (15,556 TPS) and is not
# re-measured here — this compares goopg before vs after the fixes.

set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
GOOPG_BIN="${GOOPG_BIN:?set GOOPG_BIN}"
LABEL="${LABEL:?set LABEL (before|after)}"
DATA_DIR="${DATA_DIR:?set DATA_DIR}"
OUT_DIR="${OUT_DIR:?set OUT_DIR}"
PG_BIN_DIR="${REPO_ROOT}/postgres/local_install/bin"
export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

PORT=5533
PPROF_PORT=6160
SCALE=100
DURATION="${DURATION:-180}"

export GOMEMLIMIT="${GOMEMLIMIT:-18GiB}"
export GOOPG_MUTEX_PROFILE_RATE="${GOOPG_MUTEX_PROFILE_RATE:-1}"
export GOOPG_BLOCK_PROFILE_RATE="${GOOPG_BLOCK_PROFILE_RATE:-1}"
export GOOPG_PPROF_ADDR="127.0.0.1:${PPROF_PORT}"

mkdir -p "$OUT_DIR"
log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

if [[ "${1:-}" == "--init" ]]; then
  log "init goopg data dir $DATA_DIR"
  rm -rf "$DATA_DIR"
  "$GOOPG_BIN" init -D "$DATA_DIR" > "$OUT_DIR/init_server.log" 2>&1 || { echo "init failed"; exit 1; }
  cat >> "$DATA_DIR/postgresql.conf" <<EOF
max_connections = 200
shared_buffers = 2560MB
wal_buffers = 134217728
checkpoint_timeout = 24h
max_wal_size = 1024GB
min_wal_size = 1024MB
checkpoint_completion_target = 0.9
EOF
fi

log "start goopg ($LABEL) bin=$GOOPG_BIN"
nohup "$GOOPG_BIN" start -D "$DATA_DIR" --listen "127.0.0.1:$PORT" > "$OUT_DIR/server_${LABEL}.log" 2>&1 &
SRV=$!
echo $SRV > "$OUT_DIR/goopg_${LABEL}.pid"
for i in $(seq 1 375); do
  "$PG_BIN_DIR/psql" -h 127.0.0.1 -p $PORT -U postgres -d postgres -c 'SELECT 1' >/dev/null 2>&1 && break
  sleep 0.4
done
sleep 1
curl -fsS --max-time 5 "http://127.0.0.1:$PPROF_PORT/debug/pprof/" >/dev/null 2>&1 && log "pprof OK" || log "WARN pprof not reachable"

if [[ "${1:-}" == "--init" ]]; then
  log "pgbench -i -s $SCALE"
  "$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p $PORT -U postgres -i -s $SCALE postgres \
    > "$OUT_DIR/init_${LABEL}.txt" 2>&1 || { tail -5 "$OUT_DIR/init_${LABEL}.txt"; }
fi

f0="$(awk '/walwriter flush/{n++} END{print n+0}' "$DATA_DIR/server.log" 2>/dev/null || echo 0)"

# CPU profile 120s, plus alloc/mutex/block snapshots after the run.
( sleep 20
  curl -fsS --max-time 130 -o "$OUT_DIR/${LABEL}.cpu.pb.gz" "http://127.0.0.1:$PPROF_PORT/debug/pprof/profile?seconds=120"
  curl -fsS --max-time 30 -o "$OUT_DIR/${LABEL}.allocs.pb.gz" "http://127.0.0.1:$PPROF_PORT/debug/pprof/allocs"
  curl -fsS --max-time 30 -o "$OUT_DIR/${LABEL}.mutex.pb.gz"  "http://127.0.0.1:$PPROF_PORT/debug/pprof/mutex"
  curl -fsS --max-time 30 -o "$OUT_DIR/${LABEL}.block.pb.gz"  "http://127.0.0.1:$PPROF_PORT/debug/pprof/block"
) > "$OUT_DIR/collect_${LABEL}.log" 2>&1 &
COLL=$!

log "pgbench -c50 -j50 -T$DURATION -N ($LABEL)"
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p $PORT -U postgres -c 50 -j 50 -T $DURATION -P 10 -N postgres \
  > "$OUT_DIR/pgbench_${LABEL}.txt" 2>&1
tps="$(awk '/^tps =/ {print $3; exit}' "$OUT_DIR/pgbench_${LABEL}.txt")"
log "  -> TPS=$tps"
wait "$COLL" 2>/dev/null || true

f1="$(awk '/walwriter flush/{n++} END{print n+0}' "$DATA_DIR/server.log" 2>/dev/null || echo 0)"
echo "flush_before=$f0 flush_after=$f1 delta=$((f1-f0))" > "$OUT_DIR/walflush_${LABEL}.txt"

"$GOOPG_BIN" stop -D "$DATA_DIR" >/dev/null 2>&1 || kill -TERM "$SRV" 2>/dev/null
for i in $(seq 1 60); do kill -0 "$SRV" 2>/dev/null || break; sleep 0.5; done
kill -0 "$SRV" 2>/dev/null && kill -9 "$SRV" 2>/dev/null
log "done ($LABEL): TPS=$tps flush_delta=$((f1-f0))"
