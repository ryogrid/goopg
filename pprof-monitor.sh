#!/bin/bash
set -euo pipefail

BASE="http://localhost:6060/debug/pprof"
OUTDIR="${1:-pprof-data}"
mkdir -p "$OUTDIR"

echo "=== goopg memory monitor ==="
echo "Output dir: $OUTDIR"
echo ""

trap 'echo "monitor stopped"; exit 0' INT TERM

seq=0
while true; do
  ts=$(date +%Y%m%d_%H%M%S)
  seqp=$(printf "%03d" $seq)

  # Heap profile
  if curl -sSL --max-time 10 "$BASE/heap" > "${OUTDIR}/heap_${ts}_${seqp}.prof" 2>/dev/null; then
    sz=$(stat -c%s "${OUTDIR}/heap_${ts}_${seqp}.prof" 2>/dev/null || echo "0")
    echo "[$ts] heap #${seqp} saved (${sz} bytes)"
  fi

  # Goroutine dump
  curl -sSL --max-time 10 "$BASE/goroutine?debug=2" > "${OUTDIR}/goroutine_${ts}.txt" 2>/dev/null

  seq=$((seq + 1))
  sleep 30
done
