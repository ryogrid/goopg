#!/usr/bin/env bash
# run_rw50.sh — perf-optimize3: goopg vs PG, read path vs write path, c=50.
#
# Derived from analysis/perf-optimize2/scripts/run_su50.sh (same configs, same
# uncapped-parity conditions, scale 100). Intentional deviations ONLY:
#   - TWO workloads per engine: select-only (-S, read path) and simple-update
#     (-N, write path), each on a fresh server restart (clears goopg
#     mutex/block sampling history; symmetric restart on PG).
#   - pgbench -r on every run (per-statement latency attribution).
#   - Symmetric diagnostics on BOTH engines: pg_stat_activity wait-event
#     sampling (5 Hz), pg_stat_wal before/after snapshots (fsync batch width =
#     xacts / wal_sync delta), pgbench-relation sizes before/after the write
#     run (bloat/no-HOT evidence), pg_stat_user_tables n_tup_hot_upd (PG;
#     attempted on goopg and allowed to fail).
#   - goopg pprof: 90 s CPU profile inside each workload + allocs/mutex/block
#     snapshots after it.
#
# Both servers UNCAPPED (no cgroup), GOMEMLIMIT=18GiB on goopg only (Go soft
# limit, matches every prior perf-optimize run). Engines run SEQUENTIALLY
# (goopg fully first, then PG) so they never compete for CPU/disk.
set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
RUN_ID="${RUN_ID:-$(date +%Y%m%d_%H%M%S)}"

DATA_ROOT="${REPO_ROOT}/tmp/perf-optimize3/${RUN_ID}"
GOOPG_DATA="${DATA_ROOT}/goopg-data"
PG_DATA="${DATA_ROOT}/pg-data"
RUN_DIR="${REPO_ROOT}/analysis/perf-optimize3/runs/${RUN_ID}"
PROF_DIR="${RUN_DIR}/profiles"

GOOPG_BIN="${GOOPG_BIN:?set GOOPG_BIN (clean-HEAD build)}"
PG_BIN_DIR="${REPO_ROOT}/postgres/local_install/bin"
export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

GOOPG_PORT=5533
PG_PORT=5534
PPROF_PORT=6160

SCALE=100
DURATION="${DURATION:-120}"
CLIENTS=50

export GOMEMLIMIT="${GOMEMLIMIT:-18GiB}"
export GOOPG_MUTEX_PROFILE_RATE=1
export GOOPG_BLOCK_PROFILE_RATE=1
export GOOPG_PPROF_ADDR="127.0.0.1:${PPROF_PORT}"

mkdir -p "$DATA_ROOT" "$RUN_DIR" "$PROF_DIR"
LOG_FILE="${RUN_DIR}/driver.log"
log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$LOG_FILE"; }
die() { log "ERROR: $*"; exit 1; }

# ---------- provenance ----------
{
  echo "RUN_ID=$RUN_ID"; echo "date=$(date -Is)"
  echo "git_head=$(git -C "$REPO_ROOT" rev-parse HEAD)"
  echo "goopg_bin=$GOOPG_BIN"
  echo "goopg_bin_sha256=$(sha256sum "$GOOPG_BIN" | awk '{print $1}')"
  echo "go_version=$(go version 2>/dev/null)"
  echo "kernel=$(uname -r)"; echo "nproc=$(nproc)"
  echo "GOMEMLIMIT=$GOMEMLIMIT"
  echo "pgbench_version=$("$PG_BIN_DIR/pgbench" --version)"
  echo "cgroup_caps=NONE (both engines uncapped; parity requirement)"
  echo "--- free -h ---"; free -h; echo "--- uptime ---"; uptime
} > "$RUN_DIR/env.txt"

for p in "$GOOPG_PORT" "$PG_PORT" "$PPROF_PORT"; do
  ss -lnt 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${p}\$" && die "port :$p in use"
done

wait_for() { # port name tries sleep
  for i in $(seq 1 "$3"); do
    "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$1" -U postgres -d postgres -c 'SELECT 1' >/dev/null 2>&1 && { log "$2 up"; return 0; }
    sleep "$4"
  done
  die "$2 did not come up on :$1"
}

psq() { # port sql outfile
  "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$1" -U postgres -d postgres -Atc "$2" > "$3" 2>&1 || true
}

sample_waits() { # port outfile pidfile — 5 Hz wait-event sampling until pidfile removed
  local port="$1" out="$2" flag="$3"
  while [[ -e "$flag" ]]; do
    "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$port" -U postgres -d postgres -Atc \
      "SELECT coalesce(wait_event_type,'CPU')||':'||coalesce(wait_event,'-') FROM pg_stat_activity WHERE state='active' AND application_name='pgbench'" \
      >> "$out" 2>/dev/null
    sleep 0.2
  done
}

snap_sizes() { # port outfile
  psq "$1" "SELECT relname, pg_relation_size(oid) FROM pg_class WHERE relname LIKE 'pgbench%' ORDER BY relname" "$2"
}

snap_walstat() { # port outfile
  psq "$1" "SELECT * FROM pg_stat_wal" "$2"
}

# =====================================================================
# run_workload ENGINE MODE PORT  — one pgbench run with full diagnostics
#   ENGINE: goopg|pg   MODE: S|N
# =====================================================================
run_workload() {
  local eng="$1" mode="$2" port="$3"
  local tag="${eng}_${mode}"
  local flags="-${mode}"

  snap_walstat "$port" "$RUN_DIR/${tag}.walstat.before"
  [[ "$mode" == "N" ]] && snap_sizes "$port" "$RUN_DIR/${tag}.sizes.before"

  # wait-event sampler
  touch "$DATA_ROOT/${tag}.sampling"
  sample_waits "$port" "$RUN_DIR/${tag}.waits" "$DATA_ROOT/${tag}.sampling" &
  local SAMP=$!

  # goopg pprof capture (90 s CPU window inside the run)
  if [[ "$eng" == "goopg" ]]; then
    ( sleep 15
      curl -fsS --max-time 100 -o "$PROF_DIR/${tag}.cpu.pb.gz" "http://127.0.0.1:$PPROF_PORT/debug/pprof/profile?seconds=90"
      curl -fsS --max-time 30 -o "$PROF_DIR/${tag}.allocs.pb.gz" "http://127.0.0.1:$PPROF_PORT/debug/pprof/allocs"
      curl -fsS --max-time 30 -o "$PROF_DIR/${tag}.mutex.pb.gz"  "http://127.0.0.1:$PPROF_PORT/debug/pprof/mutex"
      curl -fsS --max-time 30 -o "$PROF_DIR/${tag}.block.pb.gz"  "http://127.0.0.1:$PPROF_PORT/debug/pprof/block"
    ) >> "$LOG_FILE" 2>&1 &
  fi

  log "pgbench $flags c=$CLIENTS T=$DURATION ($tag)"
  "$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$port" -U postgres \
      -c "$CLIENTS" -j "$CLIENTS" -T "$DURATION" -P 30 -r $flags postgres \
      > "$RUN_DIR/${tag}.pgbench.txt" 2>&1 || log "WARN pgbench $tag nonzero exit"

  rm -f "$DATA_ROOT/${tag}.sampling"; wait "$SAMP" 2>/dev/null || true

  snap_walstat "$port" "$RUN_DIR/${tag}.walstat.after"
  if [[ "$mode" == "N" ]]; then
    snap_sizes "$port" "$RUN_DIR/${tag}.sizes.after"
    psq "$port" "SELECT relname, n_tup_upd, n_tup_hot_upd FROM pg_stat_user_tables WHERE relname LIKE 'pgbench%'" "$RUN_DIR/${tag}.hot.txt"
  fi
  grep -E 'tps =|latency average' "$RUN_DIR/${tag}.pgbench.txt" | while read -r l; do log "  $tag: $l"; done
}

# =====================================================================
# goopg phase
# =====================================================================
log "=== goopg init + load (scale $SCALE) ==="
"$GOOPG_BIN" init -D "$GOOPG_DATA" >> "$LOG_FILE" 2>&1 || die "goopg init failed"
cat >> "$GOOPG_DATA/postgresql.conf" <<EOF
max_connections = 200
shared_buffers = 2560MB
wal_buffers = 134217728
checkpoint_timeout = 24h
max_wal_size = 1024GB
min_wal_size = 1024MB
checkpoint_completion_target = 0.9
EOF

start_goopg() {
  nohup "$GOOPG_BIN" start -D "$GOOPG_DATA" --listen "127.0.0.1:$GOOPG_PORT" \
      > "$GOOPG_DATA/server.log" 2>&1 &
  echo $! > "$DATA_ROOT/goopg.pid"
  wait_for "$GOOPG_PORT" goopg 375 0.4
}
stop_goopg() {
  local pid; pid="$(cat "$DATA_ROOT/goopg.pid" 2>/dev/null)" || return 0
  "$GOOPG_BIN" stop -D "$GOOPG_DATA" >> "$LOG_FILE" 2>&1 || kill -TERM "$pid" 2>/dev/null || true
  for i in $(seq 1 60); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
  kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
  rm -f "$DATA_ROOT/goopg.pid"
}

start_goopg
t0=$(date +%s)
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$GOOPG_PORT" -U postgres -i -s $SCALE postgres \
    > "$RUN_DIR/goopg.load.txt" 2>&1 || log "WARN goopg load nonzero exit"
log "goopg load took $(( $(date +%s) - t0 )) s"

log "--- goopg restart (fresh stats) then SELECT-ONLY ---"
stop_goopg; start_goopg
run_workload goopg S "$GOOPG_PORT"

log "--- goopg restart then SIMPLE-UPDATE ---"
stop_goopg; start_goopg
run_workload goopg N "$GOOPG_PORT"

stop_goopg
log "=== goopg phase done ==="

# =====================================================================
# PG phase
# =====================================================================
log "=== PG init + load (scale $SCALE) ==="
"$PG_BIN_DIR/initdb" -D "$PG_DATA" -U postgres --no-locale --encoding=UTF8 >> "$LOG_FILE" 2>&1 || die "PG initdb failed"
cat >> "$PG_DATA/postgresql.conf" <<EOF
port = $PG_PORT
max_connections = 200
shared_buffers = 2560MB
wal_buffers = 128MB
checkpoint_timeout = 24h
max_wal_size = 1024GB
min_wal_size = 1024MB
checkpoint_completion_target = 0.9
fsync = on
EOF

start_pg() { "$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" -l "$PG_DATA/server.log" start >> "$LOG_FILE" 2>&1; wait_for "$PG_PORT" PG 75 0.4; }
stop_pg()  { "$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" stop -m fast >> "$LOG_FILE" 2>&1 || true; }

start_pg
t0=$(date +%s)
"$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$PG_PORT" -U postgres -i -s $SCALE postgres \
    > "$RUN_DIR/pg.load.txt" 2>&1 || log "WARN pg load nonzero exit"
log "PG load took $(( $(date +%s) - t0 )) s"

log "--- PG restart then SELECT-ONLY ---"
stop_pg; start_pg
run_workload pg S "$PG_PORT"

log "--- PG restart then SIMPLE-UPDATE ---"
stop_pg; start_pg
run_workload pg N "$PG_PORT"

stop_pg
log "=== PG phase done ==="

# headline summary
{
  echo "RUN_ID=$RUN_ID  scale=$SCALE c=$CLIENTS T=$DURATION"
  for tag in goopg_S goopg_N pg_S pg_N; do
    echo "--- $tag ---"
    grep -E 'tps =|latency average|latency stddev' "$RUN_DIR/${tag}.pgbench.txt" 2>/dev/null
  done
} > "$RUN_DIR/SUMMARY.txt"
cat "$RUN_DIR/SUMMARY.txt" | tee -a "$LOG_FILE"
log "run complete: $RUN_DIR"
