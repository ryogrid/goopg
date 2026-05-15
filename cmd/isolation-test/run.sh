#!/usr/bin/env bash
# run.sh — Run each dedicated isolation test (isolation_port_test.go line 83+)
# individually and sequentially, writing timestamped output to a single log file
# under tmp/.
#
# Usage:
#   ./cmd/isolation-test/run.sh [--timeout <duration>]
#
# Flags:
#   --timeout  Per-test timeout passed to "go test -timeout". Default: 20m.
#
# Output:
#   tmp/isolation-test-YYYYMMDD_HHMMSS.log
#
# Each test creates its own fresh goopg cluster via newCluster+mustInitStart
# (using t.TempDir()), so a clean server is guaranteed before every test
# without any additional setup from this script.

set -euo pipefail

# ---------------------------------------------------------------------------
# Resolve paths
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------
TIMEOUT="20m"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --timeout)
            TIMEOUT="$2"
            shift 2
            ;;
        *)
            echo "Unknown flag: $1" >&2
            exit 1
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Prepare output file
# ---------------------------------------------------------------------------
mkdir -p "$REPO_ROOT/tmp"
RUN_TS="$(date '+%Y%m%d_%H%M%S')"
LOG_FILE="$REPO_ROOT/tmp/isolation-test-${RUN_TS}.log"

log() {
    local msg="[$(date '+%Y-%m-%d %H:%M:%S')] $*"
    echo "$msg"
    echo "$msg" >> "$LOG_FILE"
}

log_raw() {
    # Write a line to both stdout and the log file without a timestamp prefix.
    echo "$*"
    echo "$*" >> "$LOG_FILE"
}

# ---------------------------------------------------------------------------
# List of individual isolation tests (isolation_port_test.go line 83+)
# ---------------------------------------------------------------------------
TESTS=(
    "TestPort_IsolationReadWriteUnique"
    "TestPort_IsolationLockCommittedUpdate"
    "TestPort_IsolationEvalPlanQual"
    "TestPort_IsolationEvalPlanQualTrigger"
    "TestPort_IsolationLockCommittedKeyupdate"
    "TestPort_IsolationInsertConflictDoUpdate"
    "TestPort_IsolationInsertConflictDoUpdate2"
    "TestPort_IsolationInsertConflictDoUpdate3"
    "TestPort_IsolationInsertConflictDoUpdate4"
    "TestPort_IsolationInsertConflictDoNothing"
    "TestPort_IsolationInsertConflictSpecconflict"
    "TestPort_IsolationDropIndexConcurrently1"
    "TestPort_IsolationFkSnapshot"
    "TestPort_IsolationPartitionKeyUpdate1"
    "TestPort_IsolationPartitionKeyUpdate2"
    "TestPort_IsolationPartitionKeyUpdate3"
    "TestPort_IsolationPartitionKeyUpdate4"
    "TestPort_IsolationMergeUpdate"
    "TestPort_IsolationMergeDelete"
    "TestPort_IsolationMergeInsertUpdate"
    "TestPort_IsolationMergeMatchRecheck"
    "TestPort_IsolationMergeJoin"
)

# ---------------------------------------------------------------------------
# Run each test individually
# ---------------------------------------------------------------------------
PASS=0
FAIL=0
SKIP=0

log "=== Isolation test run started ==="
log "Output: $LOG_FILE"
log "Per-test timeout: $TIMEOUT"
log "Total tests: ${#TESTS[@]}"
log_raw ""

cd "$REPO_ROOT"

for TEST in "${TESTS[@]}"; do
    log_raw "$(printf '=%.0s' {1..72})"
    log "BEGIN $TEST"
    log_raw "$(printf '=%.0s' {1..72})"

    # Capture exit code without triggering set -e.
    EXIT_CODE=0
    go test ./internal/testport/ \
        -run "^${TEST}$" \
        -v \
        -count=1 \
        -timeout "$TIMEOUT" \
        2>&1 | tee -a "$LOG_FILE" || EXIT_CODE="${PIPESTATUS[0]}"

    log_raw "$(printf '=%.0s' {1..72})"

    # Determine result from exit code and the last "--- PASS/FAIL/SKIP" line.
    if [[ "$EXIT_CODE" -eq 0 ]]; then
        # Check whether the test was actually skipped.
        if grep -q "^--- SKIP: ${TEST}" "$LOG_FILE" 2>/dev/null; then
            log "END   $TEST  [SKIP]"
            (( SKIP++ )) || true
        else
            log "END   $TEST  [PASS]"
            (( PASS++ )) || true
        fi
    else
        log "END   $TEST  [FAIL] (exit code $EXIT_CODE)"
        (( FAIL++ )) || true
    fi
    log_raw ""
done

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
log_raw "$(printf '=%.0s' {1..72})"
log "=== Summary ==="
log_raw "$(printf '=%.0s' {1..72})"
log "PASS: $PASS  SKIP: $SKIP  FAIL: $FAIL  TOTAL: ${#TESTS[@]}"
log "Log file: $LOG_FILE"

if [[ "$FAIL" -gt 0 ]]; then
    log "Result: FAILED ($FAIL test(s) failed)"
    exit 1
else
    log "Result: OK"
    exit 0
fi
