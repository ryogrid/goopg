#!/bin/bash
# pprof-all.sh — capture cpu / heap / mutex / block / goroutine /
# allocs profiles from a running goopg process at 127.0.0.1:6060.
#
# M0054-0004: this is the canonical capture script for the TPC-H
# bottleneck survey. Mutex and block profiles only return non-empty
# samples when goopg was started with:
#
#   GOOPG_MUTEX_PROFILE_RATE=1 GOOPG_BLOCK_PROFILE_RATE=1 \
#     bin/goopg start -D <data> --listen 127.0.0.1:65433
#
# (See cmd/goopg/main.go — the env-var hooks call
# runtime.SetMutexProfileFraction / runtime.SetBlockProfileRate.)
#
# Usage:
#   ./pprof-all.sh [TAG] [CPU_SECONDS]
#     TAG          — optional label appended to filenames (default: timestamp)
#     CPU_SECONDS  — optional CPU profile window in seconds (default: 30)
#
# Environment overrides:
#   PPROF_BASE     — base URL (default: http://localhost:6060/debug/pprof)
#   PPROF_OUT_DIR  — output directory (default: current directory)

set -euo pipefail

BASE="${PPROF_BASE:-http://localhost:6060/debug/pprof}"
TAG="${1:-$(date +%Y%m%d_%H%M%S)}"
CPU_SECONDS="${2:-30}"
OUT_DIR="${PPROF_OUT_DIR:-.}"

mkdir -p "$OUT_DIR"

echo "Capturing profiles (tag=$TAG cpu=${CPU_SECONDS}s) → $OUT_DIR/"
curl -sS "$BASE/profile?seconds=${CPU_SECONDS}" -o "$OUT_DIR/cpu_${TAG}.prof" &
CPU_PID=$!

curl -sS "$BASE/heap"               -o "$OUT_DIR/heap_${TAG}.prof"
curl -sS "$BASE/mutex"              -o "$OUT_DIR/mutex_${TAG}.prof"
curl -sS "$BASE/block"              -o "$OUT_DIR/block_${TAG}.prof"
curl -sS "$BASE/allocs"             -o "$OUT_DIR/allocs_${TAG}.prof"
curl -sS "$BASE/goroutine?debug=2"  -o "$OUT_DIR/goroutine_${TAG}.txt"

wait "$CPU_PID"
echo "Captured: cpu_${TAG}.prof heap_${TAG}.prof mutex_${TAG}.prof block_${TAG}.prof allocs_${TAG}.prof goroutine_${TAG}.txt"
echo
echo "Quick analysis (top 10 cumulative):"
echo "  go tool pprof -top -cum $OUT_DIR/cpu_${TAG}.prof | head -20"
echo "  go tool pprof -top -cum $OUT_DIR/heap_${TAG}.prof | head -20"
echo "  go tool pprof -top -cum $OUT_DIR/mutex_${TAG}.prof | head -20"
echo "  go tool pprof -top -cum $OUT_DIR/block_${TAG}.prof | head -20"
echo "  go tool pprof -top      $OUT_DIR/allocs_${TAG}.prof | head -20"
