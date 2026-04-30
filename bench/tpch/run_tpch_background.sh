#!/bin/bash
# run-tpch-background.sh — Start TPC-H build + power test in background.
# Output goes to bench/tpch/logs/ with timestamps.
# Check progress: tail -f bench/tpch/logs/build_*.log
set -euo pipefail

cd "$(dirname "$0")/../.."
REPO_ROOT=$(pwd)
LOG_DIR="bench/tpch/logs"
BUILD_LOG="${LOG_DIR}/build_$(date +%Y%m%d-%H%M%S).log"
RUN_LOG="${LOG_DIR}/run_$(date +%Y%m%d-%H%M%S).log"

mkdir -p "${LOG_DIR}"

echo "=== TPC-H Benchmark Start: $(date) ===" | tee -a "${BUILD_LOG}"

# 1. Reset and start cluster
echo "--- Setup cluster ---" | tee -a "${BUILD_LOG}"
bash bench/tpch/setup_goopg.sh 2>&1 | tee -a "${BUILD_LOG}"

# 2. Build schema + bulk-load
echo "--- Build schema ---" | tee -a "${BUILD_LOG}"
bash bench/tpch/build_schema_goopg.sh 2>&1 | tee -a "${BUILD_LOG}"
echo "--- Build complete: $(date) ---" | tee -a "${BUILD_LOG}"

# 3. Run power test
echo "--- Run power test ---" | tee -a "${RUN_LOG}"
bash bench/tpch/run_power_test_goopg.sh 2>&1 | tee -a "${RUN_LOG}"
echo "--- Power test complete: $(date) ---" | tee -a "${RUN_LOG}"

# 4. Summary
echo "=== TPC-H Benchmark Complete: $(date) ===" | tee -a "${BUILD_LOG}" "${RUN_LOG}"
