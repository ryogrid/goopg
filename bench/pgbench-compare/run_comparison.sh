#!/bin/bash
# Performance comparison script: goopg vs PostgreSQL using pgbench
# Measures 3 workloads (standard, simple-update, select-only) alternating between systems

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PG_BIN_DIR="$REPO_ROOT/postgres/local_install/bin"
PG_LIB_DIR="$REPO_ROOT/postgres/local_install/lib"

export PATH="$PG_BIN_DIR:$PATH"
export LD_LIBRARY_PATH="$PG_LIB_DIR:$LD_LIBRARY_PATH"

# Configuration
GOOPG_PORT=5433
POSTGRES_PORT=5434
GOOPG_DATA_DIR="$REPO_ROOT/bench/pgbench-compare/goopg-data"
POSTGRES_DATA_DIR="$REPO_ROOT/bench/pgbench-compare/postgres-data"
GOOPG_BIN="$REPO_ROOT/bin/goopg"
SCALE_FACTOR=100
CLIENTS=100
THREADS=100
DURATION=180  # 3 minutes
RESULTS_DIR="$REPO_ROOT/bench/pgbench-compare/results"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Shared configuration for both systems
SHARED_BUFFERS="2560MB"  # 2.5GB
WAL_BUFFERS_MB="100MB"   # For PostgreSQL
WAL_BUFFERS_BYTES="104857600"  # 100MB in bytes for goopg (goopg doesn't accept unit suffix)
CHECKPOINT_TIMEOUT="24h"
MAX_WAL_SIZE="1024GB"  # 1TB

mkdir -p "$RESULTS_DIR"

echo "==================================================================="
echo "pgbench Performance Comparison: goopg vs PostgreSQL"
echo "==================================================================="
echo "Configuration:"
echo "  Scale factor: $SCALE_FACTOR"
echo "  Clients: $CLIENTS"
echo "  Threads: $THREADS"
echo "  Duration: ${DURATION}s (3 minutes)"
echo "  shared_buffers: $SHARED_BUFFERS"
echo "  wal_buffers: 100MB (goopg: $WAL_BUFFERS_BYTES bytes, PostgreSQL: $WAL_BUFFERS_MB)"
echo "  checkpoint_timeout: $CHECKPOINT_TIMEOUT"
echo "  max_wal_size: $MAX_WAL_SIZE"
echo "==================================================================="

# Function to check if port is in use
port_in_use() {
    netstat -tuln 2>/dev/null | grep -q ":$1 " || ss -tuln 2>/dev/null | grep -q ":$1 "
}

# Function to initialize goopg
init_goopg() {
    echo ""
    echo "--- Initializing goopg ---"

    if [ -d "$GOOPG_DATA_DIR" ]; then
        echo "goopg data directory already exists at $GOOPG_DATA_DIR"
        read -p "Remove and recreate? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            rm -rf "$GOOPG_DATA_DIR"
        else
            echo "Using existing data directory"
            return 0
        fi
    fi

    "$GOOPG_BIN" init -D "$GOOPG_DATA_DIR"

    # Configure goopg (note: wal_buffers requires bytes, no unit suffix)
    cat >> "$GOOPG_DATA_DIR/postgresql.conf" <<EOF

# Performance comparison settings
shared_buffers = $SHARED_BUFFERS
wal_buffers = $WAL_BUFFERS_BYTES
checkpoint_timeout = $CHECKPOINT_TIMEOUT
max_wal_size = $MAX_WAL_SIZE
EOF

    echo "goopg initialized at $GOOPG_DATA_DIR (port $GOOPG_PORT)"
}

# Function to initialize PostgreSQL
init_postgres() {
    echo ""
    echo "--- Initializing PostgreSQL ---"

    if [ -d "$POSTGRES_DATA_DIR" ]; then
        echo "PostgreSQL data directory already exists at $POSTGRES_DATA_DIR"
        read -p "Remove and recreate? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            rm -rf "$POSTGRES_DATA_DIR"
        else
            echo "Using existing data directory"
            return 0
        fi
    fi

    "$PG_BIN_DIR/initdb" -D "$POSTGRES_DATA_DIR" -U postgres --no-locale --encoding=UTF8

    # Configure PostgreSQL
    cat >> "$POSTGRES_DATA_DIR/postgresql.conf" <<EOF

# Performance comparison settings
port = $POSTGRES_PORT
shared_buffers = $SHARED_BUFFERS
wal_buffers = $WAL_BUFFERS_MB
checkpoint_timeout = $CHECKPOINT_TIMEOUT
max_wal_size = $MAX_WAL_SIZE
EOF

    echo "PostgreSQL initialized at $POSTGRES_DATA_DIR (port $POSTGRES_PORT)"
}

# Function to start goopg
start_goopg() {
    echo ""
    echo "--- Starting goopg ---"

    if port_in_use $GOOPG_PORT; then
        echo "Port $GOOPG_PORT already in use. goopg may already be running."
        return 0
    fi

    nohup "$GOOPG_BIN" start -D "$GOOPG_DATA_DIR" --listen "127.0.0.1:$GOOPG_PORT" \
        > "$GOOPG_DATA_DIR/server.log" 2>&1 &

    # Wait for server to be ready
    for i in $(seq 1 50); do
        if psql -h 127.0.0.1 -p $GOOPG_PORT -U postgres -d postgres -c "SELECT 1" >/dev/null 2>&1; then
            echo "goopg is ready on port $GOOPG_PORT"
            return 0
        fi
        sleep 0.2
    done

    echo "ERROR: goopg failed to start" >&2
    exit 1
}

# Function to start PostgreSQL
start_postgres() {
    echo ""
    echo "--- Starting PostgreSQL ---"

    if port_in_use $POSTGRES_PORT; then
        echo "Port $POSTGRES_PORT already in use. PostgreSQL may already be running."
        return 0
    fi

    "$PG_BIN_DIR/pg_ctl" -D "$POSTGRES_DATA_DIR" -l "$POSTGRES_DATA_DIR/server.log" start

    # Wait for server to be ready
    for i in $(seq 1 50); do
        if psql -h 127.0.0.1 -p $POSTGRES_PORT -U postgres -d postgres -c "SELECT 1" >/dev/null 2>&1; then
            echo "PostgreSQL is ready on port $POSTGRES_PORT"
            return 0
        fi
        sleep 0.2
    done

    echo "ERROR: PostgreSQL failed to start" >&2
    exit 1
}

# Function to initialize pgbench data
init_pgbench_data() {
    local port=$1
    local system=$2

    echo ""
    echo "--- Initializing pgbench data for $system (scale factor: $SCALE_FACTOR) ---"

    # Check if already initialized
    if psql -h 127.0.0.1 -p $port -U postgres -d postgres -c "\dt pgbench_accounts" 2>/dev/null | grep -q pgbench_accounts; then
        echo "pgbench data already initialized for $system"
        return 0
    fi

    pgbench -h 127.0.0.1 -p $port -U postgres -i -s $SCALE_FACTOR postgres
    echo "pgbench data initialized for $system"
}

# Function to run pgbench test
run_pgbench_test() {
    local port=$1
    local system=$2
    local workload=$3
    local workload_name=$4
    local output_file=$5

    echo ""
    echo "=== Running pgbench: $system - $workload_name ==="
    echo "Output: $output_file"

    local pgbench_opts="-h 127.0.0.1 -p $port -U postgres -c $CLIENTS -j $THREADS -T $DURATION -P 10"

    case $workload in
        standard)
            pgbench $pgbench_opts postgres > "$output_file" 2>&1
            ;;
        simple-update)
            pgbench $pgbench_opts -N postgres > "$output_file" 2>&1
            ;;
        select-only)
            pgbench $pgbench_opts -S postgres > "$output_file" 2>&1
            ;;
    esac

    echo "Completed: $system - $workload_name"

    # Extract TPS for summary
    local tps=$(grep "^tps = " "$output_file" | awk '{print $3}')
    echo "  TPS: $tps"
}

# Main execution flow

# Check if servers need initialization
if [ ! -d "$GOOPG_DATA_DIR" ] || [ ! -d "$POSTGRES_DATA_DIR" ]; then
    init_goopg
    init_postgres

    # Start servers
    start_goopg
    start_postgres

    # Initialize pgbench data
    init_pgbench_data $GOOPG_PORT "goopg"
    init_pgbench_data $POSTGRES_PORT "PostgreSQL"
else
    # Start servers if not running
    if ! port_in_use $GOOPG_PORT; then
        start_goopg
    else
        echo "goopg already running on port $GOOPG_PORT"
    fi

    if ! port_in_use $POSTGRES_PORT; then
        start_postgres
    else
        echo "PostgreSQL already running on port $POSTGRES_PORT"
    fi

    # Ensure pgbench data exists
    init_pgbench_data $GOOPG_PORT "goopg"
    init_pgbench_data $POSTGRES_PORT "PostgreSQL"
fi

echo ""
echo "==================================================================="
echo "Starting benchmark runs (3 workloads x 2 systems, alternating)"
echo "==================================================================="

# Run tests, alternating between systems for each workload
for workload in standard simple-update select-only; do
    case $workload in
        standard)
            workload_name="Standard (TPC-B like)"
            ;;
        simple-update)
            workload_name="Simple Update"
            ;;
        select-only)
            workload_name="Select Only"
            ;;
    esac

    # goopg first
    run_pgbench_test $GOOPG_PORT "goopg" "$workload" "$workload_name" \
        "$RESULTS_DIR/${TIMESTAMP}_goopg_${workload}.txt"

    # Then PostgreSQL
    run_pgbench_test $POSTGRES_PORT "PostgreSQL" "$workload" "$workload_name" \
        "$RESULTS_DIR/${TIMESTAMP}_postgres_${workload}.txt"
done

echo ""
echo "==================================================================="
echo "Benchmark completed!"
echo "==================================================================="
echo "Results saved in: $RESULTS_DIR"
echo ""
echo "Summary of results:"
echo ""

for workload in standard simple-update select-only; do
    case $workload in
        standard)
            workload_name="Standard (TPC-B like)"
            ;;
        simple-update)
            workload_name="Simple Update"
            ;;
        select-only)
            workload_name="Select Only"
            ;;
    esac

    echo "--- $workload_name ---"

    goopg_file="$RESULTS_DIR/${TIMESTAMP}_goopg_${workload}.txt"
    postgres_file="$RESULTS_DIR/${TIMESTAMP}_postgres_${workload}.txt"

    if [ -f "$goopg_file" ]; then
        goopg_tps=$(grep "^tps = " "$goopg_file" | awk '{print $3}')
        echo "  goopg:      $goopg_tps TPS"
    fi

    if [ -f "$postgres_file" ]; then
        postgres_tps=$(grep "^tps = " "$postgres_file" | awk '{print $3}')
        echo "  PostgreSQL: $postgres_tps TPS"
    fi

    if [ -n "$goopg_tps" ] && [ -n "$postgres_tps" ]; then
        ratio=$(echo "scale=2; $goopg_tps / $postgres_tps * 100" | bc)
        echo "  goopg/PostgreSQL: ${ratio}%"
    fi

    echo ""
done

echo "To generate the analysis report, run:"
echo "  ./bench/pgbench-compare/generate_report.sh $TIMESTAMP"
