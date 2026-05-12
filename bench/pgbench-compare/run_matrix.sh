#!/bin/bash
# M0099-0005/0006: pgbench client/thread matrix validation for goopg.
# Runs selected configurations against the goopg server only (not PostgreSQL).
# Results archived to bench/pgbench-compare/results/ with timestamp prefix.
#
# Usage: ./run_matrix.sh [--canonical-only]
#   --canonical-only  Run only the (100,100) canonical condition.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PG_BIN_DIR="$REPO_ROOT/postgres/local_install/bin"
PG_LIB_DIR="$REPO_ROOT/postgres/local_install/lib"

export PATH="$PG_BIN_DIR:$PATH"
export LD_LIBRARY_PATH="$PG_LIB_DIR:$LD_LIBRARY_PATH"

GOOPG_PORT=5433
GOOPG_DATA_DIR="$REPO_ROOT/bench/pgbench-compare/goopg-data"
GOOPG_BIN="$REPO_ROOT/bin/goopg"
SCALE_FACTOR=100
DURATION=180
RESULTS_DIR="$REPO_ROOT/bench/pgbench-compare/results"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

CANONICAL_ONLY="${1:-}"

mkdir -p "$RESULTS_DIR"

log() { echo "[$(date '+%H:%M:%S')] $*"; }

port_in_use() {
    ss -tuln 2>/dev/null | grep -q ":$1 "
}

stop_goopg() {
    if port_in_use "$GOOPG_PORT"; then
        log "Stopping goopg on port $GOOPG_PORT..."
        "$GOOPG_BIN" stop -D "$GOOPG_DATA_DIR" 2>/dev/null || true
        sleep 2
        # Force-kill if still running
        if port_in_use "$GOOPG_PORT"; then
            pkill -f "goopg start" 2>/dev/null || true
            sleep 2
        fi
    fi
}

start_goopg_cold() {
    stop_goopg
    log "Starting goopg (cold pool) on port $GOOPG_PORT..."
    GOGC=200 nohup "$GOOPG_BIN" start -D "$GOOPG_DATA_DIR" --listen "127.0.0.1:$GOOPG_PORT" \
        > "$GOOPG_DATA_DIR/server.log" 2>&1 &
    for i in $(seq 1 100); do
        if psql -h 127.0.0.1 -p "$GOOPG_PORT" -U postgres -d postgres \
                -c "SELECT 1" >/dev/null 2>&1; then
            log "goopg ready on port $GOOPG_PORT"
            return 0
        fi
        sleep 0.2
    done
    log "ERROR: goopg failed to start" >&2
    exit 1
}

run_pgbench() {
    local clients=$1
    local threads=$2
    local workload=$3       # standard | simple-update | select-only
    local workload_flag=$4  # "" | -N | -S
    local pool=$5           # cold | warm
    local outfile="$RESULTS_DIR/${TIMESTAMP}_goopg_c${clients}_j${threads}_${workload}.txt"

    log "Running: clients=$clients threads=$threads workload=$workload pool=$pool"
    # shellcheck disable=SC2086
    pgbench -h 127.0.0.1 -p "$GOOPG_PORT" -U postgres \
        -c "$clients" -j "$threads" -T "$DURATION" -P 30 \
        $workload_flag postgres > "$outfile" 2>&1
    local tps
    tps=$(grep "^tps = " "$outfile" | tail -1 | awk '{print $3}')
    local failures
    failures=$(grep "^number of failed" "$outfile" | awk '{print $NF}' || echo "0")
    log "  → TPS=$tps  failures=$failures  file=$(basename "$outfile")"
    echo "$tps"
}

echo ""
echo "========================================================"
echo "M0099-0005/0006 pgbench Matrix Validation — $(date)"
echo "Binary: $GOOPG_BIN ($(ls -la "$GOOPG_BIN" | awk '{print $5, $6, $7, $8}'))"
echo "Config: scale=$SCALE_FACTOR duration=${DURATION}s"
echo "========================================================"

# ── Phase 1: Canonical condition (100,100) — cold pool ────────────────────────

log "=== Phase 1: Canonical (100,100) — cold pool ==="

start_goopg_cold
TPS_STD=$(run_pgbench 100 100 "standard" "" "cold")

start_goopg_cold
TPS_SIMPLE=$(run_pgbench 100 100 "simple-update" "-N" "cold")

start_goopg_cold
TPS_SELECT=$(run_pgbench 100 100 "select-only" "-S" "cold")

echo ""
echo "=== Phase 1 Results (canonical -c100 -j100) ==="
echo "  Standard TPC-B:  $TPS_STD TPS  (target ≥1500)"
echo "  Simple Update:   $TPS_SIMPLE TPS  (target ≥1500)"
echo "  Select Only:     $TPS_SELECT TPS  (target ≥10000)"

if [ "$CANONICAL_ONLY" = "--canonical-only" ]; then
    stop_goopg
    log "Done (canonical-only mode)."
    exit 0
fi

# ── Phase 2: Matrix survey (warm pool after canonical run) ─────────────────────

log ""
log "=== Phase 2: Matrix survey (warm pool) ==="

# Server is still running from Phase 1 (warm pool) — reuse it.
for cfg in "10 10" "25 25" "50 50"; do
    c=$(echo "$cfg" | awk '{print $1}')
    j=$(echo "$cfg" | awk '{print $2}')
    run_pgbench "$c" "$j" "standard"      ""   "warm"
    run_pgbench "$c" "$j" "simple-update" "-N" "warm"
    run_pgbench "$c" "$j" "select-only"   "-S" "warm"
done

stop_goopg

echo ""
echo "=== All matrix runs complete. Results in $RESULTS_DIR/ ==="
