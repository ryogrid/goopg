#!/usr/bin/env bash
# run_su50.sh — perf-optimize2: goopg vs PG, 50 clients, simple-update (-N)
#
# Copy-derived from analysis/perf-optimize/scripts/run_perf_suite.sh
# (run 20260518_115032 conditions). Intentional deviations ONLY:
#   - single pattern: c=50, simple-update (-N)
#   - output under analysis/perf-optimize2/runs/<RUN_ID>/
#   - data under tmp/perf-optimize2/<RUN_ID>/
#   - GOOPG_BIN overridable (points at a clean-HEAD worktree build)
#   - provenance capture (env.txt)
#   - symmetric diagnostics: PG wait-event sampling + pg_stat_wal snapshots,
#     goopg WAL-flush counts from server.log, /proc CPU/IO deltas
#   - auxiliary attribution runs (aux/), clearly separated from the headline
#
# Everything else (ports, configs, env, scale, flags, PG fsync=on line,
# goopg wal_buffers in bytes) is byte-parity with the original. Known
# non-parity: the original ran c=50 select-only BEFORE simple-update on the
# same instance (warm shared_buffers); here simple-update is the first
# workload after the restart (cold cache) — disclosed in 00-methodology.md
# deviation 7.
#
# Both servers run UNCAPPED (no cgroup/systemd-run/taskset on either) per the
# user requirement — identical conditions for goopg and PG. GOMEMLIMIT=18GiB
# is the only (soft, Go-runtime) limit, matching the prior run.

set -uo pipefail

REPO_ROOT="${REPO_ROOT:-/home/ryo/work/goopg/goopg}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN_ID="${RUN_ID:-$(date +%Y%m%d_%H%M%S)}"

DATA_ROOT="${REPO_ROOT}/tmp/perf-optimize2/${RUN_ID}"
GOOPG_DATA="${DATA_ROOT}/goopg-data"
PG_DATA="${DATA_ROOT}/pg-data"
RUN_DIR="${REPO_ROOT}/analysis/perf-optimize2/runs/${RUN_ID}"
PROF_DIR="${RUN_DIR}/profiles"
AUX_DIR="${RUN_DIR}/aux"

GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/bin/goopg}"
PG_BIN_DIR="${REPO_ROOT}/postgres/local_install/bin"
PG_LIB_DIR="${REPO_ROOT}/postgres/local_install/lib"
PPROF_COLLECT="${REPO_ROOT}/analysis/perf-optimize/scripts/pprof_collect.sh"

export LD_LIBRARY_PATH="${PG_LIB_DIR}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

GOOPG_PORT=5533
PG_PORT=5534
PPROF_PORT=6160

SCALE=100
DURATION="${DURATION:-180}"
AUX_DURATION="${AUX_DURATION:-60}"
CLIENTS=50
RUN_AUX="${RUN_AUX:-1}"

SHARED_BUFFERS="2560MB"
WAL_BUFFERS_GOOPG_BYTES=134217728     # 128 MiB
WAL_BUFFERS_PG="128MB"
CHECKPOINT_TIMEOUT="24h"
MAX_WAL_SIZE="1024GB"
MIN_WAL_SIZE="1024MB"
MAX_CONNECTIONS=200

export GOMEMLIMIT="${GOMEMLIMIT:-18GiB}"
export GOOPG_MUTEX_PROFILE_RATE="${GOOPG_MUTEX_PROFILE_RATE:-1}"
export GOOPG_BLOCK_PROFILE_RATE="${GOOPG_BLOCK_PROFILE_RATE:-1}"
export GOOPG_PPROF_ADDR="${GOOPG_PPROF_ADDR:-127.0.0.1:${PPROF_PORT}}"

LOG_FILE="${RUN_DIR}/driver.log"

log() {
  local ts; ts="$(date +%H:%M:%S)"
  printf '[%s] %s\n' "$ts" "$*" | tee -a "$LOG_FILE"
}

die() {
  log "ERROR: $*"
  exit 1
}

ensure_dirs() {
  mkdir -p "$DATA_ROOT" "$RUN_DIR" "$PROF_DIR" "$AUX_DIR"
}

capture_provenance() {
  {
    echo "RUN_ID=$RUN_ID"
    echo "date=$(date -Is)"
    echo "git_head=$(git -C "$REPO_ROOT" rev-parse HEAD)"
    echo "git_dirty_files_main_tree=$(git -C "$REPO_ROOT" status --porcelain | wc -l)"
    echo "goopg_bin=$GOOPG_BIN"
    echo "goopg_bin_sha256=$(sha256sum "$GOOPG_BIN" | awk '{print $1}')"
    if [[ -n "${GOOPG_SRC_WORKTREE:-}" ]]; then
      echo "goopg_src_worktree=$GOOPG_SRC_WORKTREE"
      echo "goopg_src_head=$(git -C "$GOOPG_SRC_WORKTREE" rev-parse HEAD)"
      echo "goopg_src_dirty=$(git -C "$GOOPG_SRC_WORKTREE" status --porcelain | wc -l)"
    fi
    echo "go_version=$(go version)"
    echo "kernel=$(uname -r)"
    echo "nproc=$(nproc)"
    echo "GOMEMLIMIT=$GOMEMLIMIT"
    echo "GOOPG_MUTEX_PROFILE_RATE=$GOOPG_MUTEX_PROFILE_RATE"
    echo "GOOPG_BLOCK_PROFILE_RATE=$GOOPG_BLOCK_PROFILE_RATE"
    echo "pgbench_version=$("$PG_BIN_DIR/pgbench" --version)"
    echo "cgroup_caps=NONE (both servers uncapped; user requirement)"
    echo "--- free -h ---"; free -h
    echo "--- uptime ---"; uptime
  } > "$RUN_DIR/env.txt"
  log "provenance captured to env.txt"
}

preflight() {
  log "RUN_ID=$RUN_ID  (perf-optimize2, c=$CLIENTS simple-update only)"
  log "DATA_ROOT=$DATA_ROOT"
  log "RUN_DIR=$RUN_DIR"
  log "GOOPG_BIN=$GOOPG_BIN"
  log "GOOPG_PORT=$GOOPG_PORT  PG_PORT=$PG_PORT  PPROF_PORT=$PPROF_PORT"
  log "DURATION=${DURATION}s  AUX_DURATION=${AUX_DURATION}s  SCALE=$SCALE  RUN_AUX=$RUN_AUX"

  [[ -x "$GOOPG_BIN"          ]] || die "missing $GOOPG_BIN"
  [[ -x "$PG_BIN_DIR/initdb"  ]] || die "missing $PG_BIN_DIR/initdb"
  [[ -x "$PG_BIN_DIR/pg_ctl"  ]] || die "missing $PG_BIN_DIR/pg_ctl"
  [[ -x "$PG_BIN_DIR/pgbench" ]] || die "missing $PG_BIN_DIR/pgbench"
  [[ -x "$PG_BIN_DIR/psql"    ]] || die "missing $PG_BIN_DIR/psql"
  [[ -f "$PPROF_COLLECT"      ]] || die "missing $PPROF_COLLECT"

  for p in "$GOOPG_PORT" "$PG_PORT" "$PPROF_PORT"; do
    if ss -lnt 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${p}\$"; then
      die "port :$p already in use — refusing to start"
    fi
  done

  local avail_g; avail_g="$(free -g | awk '/^Mem:/ {print $7}')"
  log "free RAM (available): ${avail_g} GiB"
  (( avail_g < 22 )) && log "WARN: <22 GiB available"

  log "goopg sha256: $(sha256sum "$GOOPG_BIN" | awk '{print $1}')"
  log "pgbench: $("$PG_BIN_DIR/pgbench" --version)"
}

write_goopg_conf() {
  cat >> "$GOOPG_DATA/postgresql.conf" <<EOF

# perf-optimize2 suite (RUN_ID=$RUN_ID) — see analysis/perf-optimize2/
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

# perf-optimize2 suite (RUN_ID=$RUN_ID) — see analysis/perf-optimize2/
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
  log "starting goopg (GOMEMLIMIT=$GOMEMLIMIT, mutex_rate=$GOOPG_MUTEX_PROFILE_RATE, block_rate=$GOOPG_BLOCK_PROFILE_RATE, GOGC=${GOGC:-default})"
  nohup "$GOOPG_BIN" start -D "$GOOPG_DATA" --listen "127.0.0.1:$GOOPG_PORT" \
      > "$GOOPG_DATA/server.log" 2>&1 &
  echo $! > "$DATA_ROOT/goopg.pid"
  # 150 s window: goopg's startup over a scale-100 data dir takes ~30 s
  # (data-directory open / recovery), which overflowed the original 20 s.
  wait_for_pg "$GOOPG_PORT" "goopg" 375 0.4

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

# --- diagnostics -------------------------------------------------------------

psql_pg() {
  "$PG_BIN_DIR/psql" -h 127.0.0.1 -p "$PG_PORT" -U postgres -d postgres -X -Atc "$1" 2>/dev/null
}

snapshot_pg_stat_wal() {
  local tag="$1"
  psql_pg "SELECT now(), * FROM pg_stat_wal" > "$RUN_DIR/pg_stat_wal_${tag}.txt" || true
  psql_pg "SELECT now(), num_timed, num_requested FROM pg_stat_checkpointer" \
      > "$RUN_DIR/pg_stat_checkpointer_${tag}.txt" 2>/dev/null || true
}

start_pg_wait_sampler() {
  local out="$1"
  # NOTE: the subshell's stdout MUST be detached from the caller's stdout —
  # callers capture this function via $(...), and a background child holding
  # the command-substitution pipe open blocks the caller forever.
  (
    while :; do
      psql_pg "SELECT coalesce(wait_event_type,'CPU')||':'||coalesce(wait_event,'-') AS ev, count(*)
               FROM pg_stat_activity
               WHERE backend_type='client backend' AND pid<>pg_backend_pid()
               GROUP BY 1" >> "$out" || true
      echo "--" >> "$out"
      sleep 0.25
    done
  ) > /dev/null 2>&1 &
  echo $!
}

snapshot_proc() {
  # snapshot_proc <pid> <outfile>: cpu (utime/stime jiffies) + io counters
  local pid="$1" out="$2"
  {
    echo "ts=$(date +%s.%N)"
    awk '{print "stat:", $14, $15, $16, $17}' "/proc/$pid/stat" 2>/dev/null
    cat "/proc/$pid/io" 2>/dev/null
  } > "$out" || true
}

goopg_flush_count() {
  awk '/walwriter flush/{n++} END{print n+0}' "$GOOPG_DATA/server.log" 2>/dev/null || echo 0
}

# --- measurement -------------------------------------------------------------

# run_pgbench <target> <port> <clients> <flag-string> <label> <outdir> <duration> <collect_pprof:0|1> [extra pgbench args...]
run_pgbench() {
  local target="$1" port="$2" clients="$3" flag="$4" label="$5" outdir="$6" dur="$7" collect="$8"
  shift 8
  local out="$outdir/pgbench_${label}.txt"
  local prefix="$PROF_DIR/${label}"

  log "RUN ${label}  -c $clients -j $clients -T $dur $flag $*"

  local collector_pid="" sampler_pid=""
  if [[ "$target" == "goopg" ]]; then
    local f0; f0="$(goopg_flush_count)"
    echo "flush_count_before=$f0" > "$outdir/walflush_${label}.txt"
    snapshot_proc "$(cat "$DATA_ROOT/goopg.pid")" "$outdir/proc_${label}_before.txt"
    if [[ "$collect" == "1" ]]; then
      PPROF_URL="http://127.0.0.1:${PPROF_PORT}/debug/pprof" \
        bash "$PPROF_COLLECT" "$prefix" "$dur" \
          > "$PROF_DIR/${label}.collector.log" 2>&1 &
      collector_pid=$!
    fi
  else
    snapshot_pg_stat_wal "${label}_before"
    sampler_pid="$(start_pg_wait_sampler "$outdir/pg_waits_${label}.txt")"
  fi

  set +e
  "$PG_BIN_DIR/pgbench" -h 127.0.0.1 -p "$port" -U postgres \
      -c "$clients" -j "$clients" -T "$dur" -P 10 $flag "$@" postgres \
      > "$out" 2>&1
  local rc=$?
  set -e

  if [[ -n "$collector_pid" ]]; then
    wait "$collector_pid" 2>/dev/null || true
  fi
  if [[ -n "$sampler_pid" ]]; then
    kill "$sampler_pid" 2>/dev/null || true
    wait "$sampler_pid" 2>/dev/null || true
    snapshot_pg_stat_wal "${label}_after"
  fi
  if [[ "$target" == "goopg" ]]; then
    local f1; f1="$(goopg_flush_count)"
    echo "flush_count_after=$f1" >> "$outdir/walflush_${label}.txt"
    if [[ -f "$DATA_ROOT/goopg.pid" ]] && kill -0 "$(cat "$DATA_ROOT/goopg.pid")" 2>/dev/null; then
      snapshot_proc "$(cat "$DATA_ROOT/goopg.pid")" "$outdir/proc_${label}_after.txt"
    fi
  fi

  if (( rc != 0 )); then
    log "  → exit=$rc (failure on $target $label — see $out)"
    tail -20 "$out" | sed 's/^/  /' | tee -a "$LOG_FILE"
    if [[ "$target" == "goopg" ]]; then
      if ! kill -0 "$(cat "$DATA_ROOT/goopg.pid" 2>/dev/null)" 2>/dev/null; then
        log "  → goopg died; capturing server.log and restarting"
        cp "$GOOPG_DATA/server.log" "$outdir/server_crash_${label}.log" || true
        rm -f "$DATA_ROOT/goopg.pid"
        start_goopg
      fi
    fi
  else
    local tps; tps="$(awk '/^tps =/ {print $3; exit}' "$out")"
    log "  → TPS=$tps"
  fi
}

# --- aux runs ----------------------------------------------------------------

set_conf_line() {
  # set_conf_line <conf> <line>  (append; removed later by strip_conf_line)
  echo "$2" >> "$1"
}

strip_conf_line() {
  # strip_conf_line <conf> <pattern>
  sed -i "/$2/d" "$1"
}

aux_runs() {
  log "=== AUX runs (diagnostic only — NOT part of the headline comparison) ==="

  # A1: -M prepared (goopg) — parse/plan share
  restart_goopg
  run_pgbench goopg "$GOOPG_PORT" "$CLIENTS" "-N" "aux_goopg_c50_su_prepared" "$AUX_DIR" "$AUX_DURATION" 0 -M prepared

  # A2: GOGC=400 (goopg) — GC-attributable bound
  stop_goopg; sleep 1
  GOGC=400 start_goopg
  run_pgbench goopg "$GOOPG_PORT" "$CLIENTS" "-N" "aux_goopg_c50_su_gogc400" "$AUX_DIR" "$AUX_DURATION" 0
  stop_goopg; sleep 1

  # A3: synchronous_commit=off on BOTH — WAL-flush bound
  set_conf_line "$GOOPG_DATA/postgresql.conf" "synchronous_commit = off"
  set_conf_line "$PG_DATA/postgresql.conf"    "synchronous_commit = off"
  start_goopg
  run_pgbench goopg "$GOOPG_PORT" "$CLIENTS" "-N" "aux_goopg_c50_su_syncoff" "$AUX_DIR" "$AUX_DURATION" 0
  stop_pg; sleep 1; start_pg
  run_pgbench pg "$PG_PORT" "$CLIENTS" "-N" "aux_pg_c50_su_syncoff" "$AUX_DIR" "$AUX_DURATION" 0
  strip_conf_line "$GOOPG_DATA/postgresql.conf" "^synchronous_commit = off"
  strip_conf_line "$PG_DATA/postgresql.conf"    "^synchronous_commit = off"
  stop_pg; sleep 1; start_pg

  # A4: c=1 on both — per-txn service time without contention
  restart_goopg
  run_pgbench goopg "$GOOPG_PORT" 1 "-N" "aux_goopg_c1_su" "$AUX_DIR" "$AUX_DURATION" 0
  run_pgbench pg    "$PG_PORT"    1 "-N" "aux_pg_c1_su"    "$AUX_DIR" "$AUX_DURATION" 0

  # A5: profiling rates 0 (goopg) — profiler overhead in the headline
  stop_goopg; sleep 1
  GOOPG_MUTEX_PROFILE_RATE=0 GOOPG_BLOCK_PROFILE_RATE=0 start_goopg
  run_pgbench goopg "$GOOPG_PORT" "$CLIENTS" "-N" "aux_goopg_c50_su_noprof" "$AUX_DIR" "$AUX_DURATION" 0

  log "=== AUX runs complete ==="
}

trap '{ stop_goopg; stop_pg; } 2>/dev/null || true' EXIT

main() {
  ensure_dirs
  preflight
  capture_provenance
  init_clusters
  start_goopg
  start_pg

  pgbench_init "$GOOPG_PORT" "goopg"
  pgbench_init "$PG_PORT"    "pg"

  # --- HEADLINE (recorded comparison; byte-parity with perf-optimize) ---
  restart_goopg
  run_pgbench goopg "$GOOPG_PORT" "$CLIENTS" "-N" "goopg_c50_simple-update" "$RUN_DIR" "$DURATION" 1
  run_pgbench pg    "$PG_PORT"    "$CLIENTS" "-N" "pg_c50_simple-update"    "$RUN_DIR" "$DURATION" 0

  if [[ "$RUN_AUX" == "1" ]]; then
    aux_runs
  fi

  log "all patterns complete"
  log "artefacts under $RUN_DIR"
}

# SU50_NO_MAIN=1 allows sourcing this file for its functions (e.g. to resume
# an interrupted run with the same RUN_ID) without re-running the full suite.
if [[ "${SU50_NO_MAIN:-0}" != "1" ]]; then
  main "$@"
fi
