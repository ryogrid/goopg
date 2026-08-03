#!/usr/bin/env bash
# Full 99-query SF0.5 gate at HEAD, run as four contiguous QUERIES= chunks on ONE
# private binary (the nightly CI batch owns tmp/goopg-bench-bin — see
# sf05_ensure_bin's refusal). Chunk boundaries are sound for the same reason the
# 2026-07-29 run documented: every chunk is sf05_goopg_start-ed fresh and runs
# S-cold, which is the gate's own determinism contract.
#
# FORCE=1 is legitimate here and ONLY here: this is a row-count/checksum verdict.
# Per-query SECONDS from this run are void (load ~10 from the nightly).
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
OUT="analysis/m0125-sf05-fullgate-20260730"
export GOOPG_BIN="$PWD/tmp/goopg-sf05-bin"
export SF05_RESULTS_DIR="$OUT"
export FORCE=1 TIMEOUT_SEC=300 RESTART_AFTER_TIMEOUT=1

for range in "1 30" "31 53" "54 72" "73 99"; do
    set -- $range
    echo "===== CHUNK Q$1-Q$2 begins $(date -Iseconds) ====="
    QUERIES="$(seq "$1" "$2")" scripts/tpcds-sf05-regression.sh sweep
    echo "===== CHUNK Q$1-Q$2 rc=$? ends $(date -Iseconds) ====="
done
echo "ALL CHUNKS DONE $(date -Iseconds)"
