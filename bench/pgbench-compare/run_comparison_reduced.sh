#!/bin/bash
# Reduced concurrency version for goopg compatibility
# Uses 50 clients/threads instead of 100

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "Running comparison with REDUCED concurrency (50 clients, 50 threads)"
echo "This avoids the 'short read at block' error seen with 100 clients"
echo ""

# Temporarily modify the variables
export PGBENCH_CLIENTS=50
export PGBENCH_THREADS=50

# Run the main script with modifications
exec ./run_comparison.sh
