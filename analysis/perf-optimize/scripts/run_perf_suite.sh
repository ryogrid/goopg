#!/usr/bin/env bash
# run_perf_suite.sh — goopg pgbench performance analysis suite
#
# Drives the 9-pattern (3 client counts × 3 workloads) pgbench comparison
# between goopg and upstream PostgreSQL 18.3, with full pprof telemetry
# captured on the goopg side. Output goes to:
#   - tmp/perf-optimize/<RUN_ID>/       : ephemeral data dirs + server logs
#   - analysis/perf-optimize/runs/<RUN_ID>/ : pgbench stdout + .prof artefacts
#
# Designed to coexist with a running Ralph loop:
#   ports 5533 (goopg) / 5534 (PG) — avoid bench/pgbench-compare/'s 5433/5434
#   data root tmp/perf-optimize/<RUN_ID>/ — distinct from tmp/pgbench-compare/
#
# Per plan: pprof is hardcoded to 127.0.0.1:6060 in cmd/goopg/main.go.
# Pre-flight aborts if :6060 is already in use.
#
# Requirements (per user spec):
#   shared_buffers   = 2.5 GiB (2560 MB)
#   wal_buffers      = 128 MiB (134217728 bytes for goopg; "128MB" for PG)
#   GOMEMLIMIT       = 18 GiB
#   checkpoint suppressed (timeout=24h, max_wal_size=1024GB)
#   scale            = 100
#   client counts    = {100, 50, 10}
#   if c=100 errors → skip that combo, do not modify goopg, continue
#   capture every pprof profile available and cross-reference with source

set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_ID="${RUN_ID:-$(date +%Y%m%d_%H%M%S)}"

DATA_ROOT="${REPO_ROOT}/tmp/perf-optimize/${RUN_ID}"
GOOPG_DATA="${DATA_ROOT}/goopg-data"
PG_DATA="${DATA_ROOT}/pg-data"
RUN_DIR="${REPO_ROOT}/analysis/perf-optimize/runs/${RUN_ID}"
PROF_DIR="${RUN_DIR}/profiles"

GOOPG_BIN="${REPO_ROOT}/bin/goopg"
PG_BIN_DIR="${REPO_ROOT}/postgres/local_install/bin"
PG_LIB_DIR="${REPO_ROOT}/postgres/local_install/lib"

# Ensure pgbench/psql/initdb/pg_ctl find local libpq.so.5.18 (PQsendPipelineSync, etc.)
export LD_LIBRARY_PATH="${PG_LIB_DIR}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

GOOPG_PORT=5533
PG_PORT=5534
PPROF_PORT=6160                       # 6160 (not default 6060) — leaves Ralph's tests on 6060

SCALE=100
DURATION="${DURATION:-180}"           # seconds per pgbench run
CLIENT_COUNTS=(${CLIENT_COUNTS:-10 50 100})
WORKLOADS=(select-only simple-update standard)

SHARED_BUFFERS="2560MB"
WAL_BUFFERS_GOOPG_BYTES=134217728     # 128 MiB
WAL_BUFFERS_PG="128MB"
CHECKPOINT_TIMEOUT="24h"
MAX_WAL_SIZE="1024GB"
MIN_WAL_SIZE="1024MB"
MAX_CONNECTIONS=200

# goopg startup environment
export GOMEMLIMIT="${GOMEMLIMIT:-18GiB}"
export GOOPG_MUTEX_PROFILE_RATE="${GOOPG_MUTEX_PROFILE_RATE:-1}"
export GOOPG_BLOCK_PROFILE_RATE="${GOOPG_BLOCK_PROFILE_RATE:-1}"
export GOOPG_PPROF_ADDR="${GOOPG_PPROF_ADDR:-127.0.0.1:${PPROF_PORT}}"

# Logging
LOG_FILE="${RUN_DIR}/driver.log"

declare -A WL_FLAG=(
  [select-only]="-S"
  [simple-update]="-N"
  [standard]=""
)

log() {
  local ts; ts="$(date +%H:%M:%S)"
  printf '[%s] %s\n' "$ts" "$*" | tee -a "$LOG_FILE"
}

die() {
  log "ERROR: $*"
  exit 1
}

ensure_dirs() {
  mkdir -p "$DATA_ROOT" "$RUN_DIR" "$PROF_DIR"
}

preflight() {
  log "RUN_ID=$RUN_ID"
  log "DATA_ROOT=$DATA_ROOT"
  log "RUN_DIR=$RUN_DIR"
  log "GOOPG_PORT=$GOOPG_PORT  PG_PORT=$PG_PORT  PPROF_PORT=$PPROF_PORT"
  log "DURATION=${DURATION}s  CLIENT_COUNTS=${CLIENT_COUNTS[*]}  SCALE=$SCALE"

  [[ -x "$GOOPG_BIN"             ]] || die "missing $GOOPG_BIN — run 'go build -o bin/goopg ./cmd/goopg' first"
  [[ -x "$PG_BIN_DIR/initdb"     ]] || die "missing $PG_BIN_DIR/initdb"
  [[ -x "$PG_BIN_DIR/pg_ctl"     ]] || die "missing $PG_BIN_DIR/pg_ctl"
  [[ -x "$PG_BIN_DIR/pgbench"    ]] || die "missing $PG_BIN_DIR/pgbench"
  [[ -x "$PG_BIN_DIR/psql"       ]] || die "missing $PG_BIN_DIR/psql"

  for p in "$GOOPG_PORT" "$PG_PORT" "$PPROF_PORT"; do
    if ss -lnt 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${p}\$"; then
      die "port :$p already in use — refusing to start"
    fi
  done

  local avail_g; avail_g="$(free -g | awk '/^Mem:/ {print $7}')"
  log "free RAM (available): ${avail_g} GiB"
  (( avail_g < 22 )) && log "WARN: <22 GiB available; GOMEMLIMIT=18GiB + shared_buffers=2.5GiB may be tight"

  local avail_disk_g; avail_disk_g="$(df -BG "$DATA_ROOT" 2>/dev/null || df -BG /tmp)"
  log "disk: $avail_disk_g"

  # Snapshot Ralph status (no edit)
  if [[ -f "$REPO_ROOT/.ralph/status.json" ]]; then
    log "ralph status (no edit): $(stat -c '%y' "$REPO_ROOT/.ralph/status.json")"
  fi

  log "goopg sha256: $(sha256sum "$GOOPG_BIN" | awk '{print $1}')"
  log "pgbench: $("$PG_BIN_DIR/pgbench" --version)"
  log "psql:    $("$PG_BIN_DIR/psql" --version)"
}

write_goopg_conf() {
  cat >> "$GOOPG_DATA/postgresql.conf" <<EOF

# perf-optimize suite (RUN_ID=$RUN_ID) — see analysis/perf-optimize/
max_connections = $MAX_CONNECTIONS
shared_buffers = $SHARED_BUFFERS
wal_buffers = $WAL_BUFFERS_GOOPG_BYTES
checkpoint_timeout = $CHECKPOINT_TIMEOUT
max_wal_size = $MAX_WAL_SIZE
min_wal_size = $MIN_WAL_SIZE
checkpoint_completion_target = 0.9
EOF
}

write_pg_conf() {
  cat >> "$PG_DATA/postgresql.conf" <<EOF

# perf-optimize suite (RUN_ID=$RUN_ID) — see analysis/perf-optimize/
port = $PG_PORT
max_connections = $MAX_CONNECTIONS
shared_buffers = $SHARED_BUFFERS
wal_buffers = $WAL_BUFFERS_PG
checkpoint_timeout = $CHECKPOINT_TIMEOUT
max_wal_size = $MAX_WAL_SIZE
min_wal_size = $MIN_WAL_SIZE
checkpoint_completion_target = 0.9
fsync = on
EOF
}

init_clusters() {
  log "init goopg cluster at $GOOPG_DATA"
  "$GOOPG_BIN" init -D "$GOOPG_DATA" >> "$LOG_FILE" 2>&1 || die "goopg init failed"
  write_goopg_conf

  log "init PG cluster at $PG_DATA"
  "$PG_BIN_DIR/initdb" -D "$PG_DATA" -U postgres --no-locale --encoding=UTF8 \
      >> "$LOG_FILE" 2>&1 || die "PG initdb failed"
  write_pg_conf

  cp -p "$GOOPG_BIN" "$RUN_DIR/goopg.bin"
  log "archived goopg binary to $RUN_DIR/goopg.bin"
}

start_goopg() {
  log "starting goopg (GOMEMLIMIT=$GOMEMLIMIT, mutex_rate=$GOOPG_MUTEX_PROFILE_RATE, block_rate=$GOOPG_BLOCK_PROFILE_RATE)"
  nohup "$GOOPG_BIN" start -D "$GOOPG_DATA" --listen "127.0.0.1:$GOOPG_PORT" \
      > "$GOOPG_DATA/server.log" 2>&1 &
  echo $! > "$DATA_ROOT/goopg.pid"
  wait_for_pg "$GOOPG_PORT" "goopg" 50 0.4

  # Verify pprof actually bound (main.go swallows bind errors at Debug level).
  sleep 1
  if ! curl -fsS --max-time 5 "http://127.0.0.1:$PPROF_PORT/debug/pprof/" > /dev/null; then
    log "WARN: pprof endpoint http://127.0.0.1:$PPROF_PORT/debug/pprof/ not reachable"
  else
    log "pprof endpoint OK on :$PPROF_PORT"
  fi
}

stop_goopg() {
  if [[ -f "$DATA_ROOT/goopg.pid" ]]; then
    local pid; pid="$(cat "$DATA_ROOT/goopg.pid")"
    if kill -0 "$pid" 2>/dev/null; then
      log "stopping goopg (pid=$pid)"
      "$GOOPG_BIN" stop -D "$GOOPG_DATA" >> "$LOG_FILE" 2>&1 || kill -TERM "$pid" 2>/dev/null || true
      for i in $(seq 1 30); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.5
      done
      kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$DATA_ROOT/goopg.pid"
  fi
}

restart_goopg() {
  log "restart goopg (clears mutex/block sampling history)"
  stop_goopg
  sleep 1
  start_goopg
}

start_pg() {
  log "starting PG on port $PG_PORT"
  "$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" -l "$PG_DATA/server.log" start \
      >> "$LOG_FILE" 2>&1 || die "pg_ctl start failed"
  wait_for_pg "$PG_PORT" "postgres" 50 0.4
}

stop_pg() {
  log "stopping PG"
  "$PG_BIN_DIR/pg_ctl" -D "$PG_DATA" stop -m fast >> "$LOG_FILE" 2>&1 || true
}

wait_for_pg() {
  local port="$1" name="$2" tries="${3:-50}" delay="${4:-0.4}"
  for i in $(seq 1 "$tries"); do
    if "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$port" -U postgres -d postgres -c 'SELECT 1' \
        >/dev/null 2>&1; then
      log "$name ready on port $port (after $i tries)"
      return 0
    fi
    sleep "$delay"
  done
  die "$name did not become ready on port $port after $tries tries"
}

pgbench_init() {
  local port="$1" name="$2"
  log "pgbench -i -s $SCALE against $name (port $port)"
  local out="$RUN_DIR/init_${name}.txt"
  if ! "$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$port" -U postgres -i -s "$SCALE" \
      postgres > "$out" 2>&1; then
    log "WARN: pgbench -i against $name returned non-zero — see $out"
    tail -5 "$out" | sed 's/^/  /' | tee -a "$LOG_FILE"
  fi
}

# Run one pgbench measurement. Profile only when target is goopg.
run_pgbench() {
  local target="$1"   # goopg | pg
  local port="$2"
  local clients="$3"
  local wl="$4"
  local flag="${WL_FLAG[$wl]:-}"
  local label="${target}_c${clients}_${wl}"
  local out="$RUN_DIR/pgbench_${label}.txt"
  local prefix="$PROF_DIR/${label}"

  log "RUN ${label}  -c $clients -j $clients -T $DURATION $flag"

  local collector_pid=""
  if [[ "$target" == "goopg" ]]; then
    PPROF_URL="http://127.0.0.1:${PPROF_PORT}/debug/pprof" \
      bash "$SCRIPT_DIR/pprof_collect.sh" "$prefix" "$DURATION" \
        > "$PROF_DIR/${label}.collector.log" 2>&1 &
    collector_pid=$!
  fi

  set +e
  "$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$port" -U postgres \
      -c "$clients" -j "$clients" -T "$DURATION" -P 10 $flag postgres \
      > "$out" 2>&1
  local rc=$?
  set -e

  if [[ -n "$collector_pid" ]]; then
    # Allow collector to finish its remaining snapshots
    wait "$collector_pid" 2>/dev/null || true
  fi

  if (( rc != 0 )); then
    log "  → exit=$rc"
    if [[ "$clients" == "100" && "$target" == "goopg" ]]; then
      log "  → c=100 failure on goopg — marking SKIPPED, continuing per plan"
      echo "SKIPPED (c=100 failure; do not modify goopg)" > "$RUN_DIR/SKIPPED_${label}.txt"
      tail -20 "$out" >> "$RUN_DIR/SKIPPED_${label}.txt"
      # Check goopg still alive; if not, restart for the next workload
      if ! kill -0 "$(cat "$DATA_ROOT/goopg.pid")" 2>/dev/null; then
        log "  → goopg died; restarting"
        rm -f "$DATA_ROOT/goopg.pid"
        start_goopg
      fi
    else
      log "  → non-fatal failure on $target c=$clients $wl"
    fi
  else
    # Extract TPS for the driver log
    local tps; tps="$(awk '/^tps =/ {print $3; exit}' "$out")"
    log "  → TPS=$tps"
  fi
}

trap '{ stop_goopg; stop_pg; } 2>/dev/null || true' EXIT

main() {
  ensure_dirs
  preflight
  init_clusters
  start_goopg
  start_pg

  pgbench_init "$GOOPG_PORT" "goopg"
  pgbench_init "$PG_PORT"    "pg"

  for clients in "${CLIENT_COUNTS[@]}"; do
    restart_goopg
    for wl in "${WORKLOADS[@]}"; do
      run_pgbench "goopg" "$GOOPG_PORT" "$clients" "$wl"
      run_pgbench "pg"    "$PG_PORT"    "$clients" "$wl"
    done
  done

  log "all patterns complete"
  log "artefacts under $RUN_DIR"
}

main "$@"
