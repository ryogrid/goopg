#!/bin/bash
set -euo pipefail

OUTDIR="${1:-pprof-data}"
mkdir -p "$OUTDIR"

trap 'echo "monitor stopped"; exit 0' INT TERM

echo "PID,VmRSS_kB,HeapInuse_kB,HeapIdle_kB,HeapReleased_kB,HeapAlloc_kB,Time" > "${OUTDIR}/memory.csv"

while true; do
  ts=$(date +%Y%m%d_%H%M%S)

  # Find goopg PID
  PID=$(pgrep -f "goopg-bench-bin start" 2>/dev/null || echo "")
  if [ -n "$PID" ]; then
    # RSS from /proc
    if [ -f "/proc/$PID/status" ]; then
      RSS=$(grep VmRSS "/proc/$PID/status" | awk '{print $2}')
    else
      RSS="?"
    fi

    # Go runtime memory stats from pprof
    HEAP_INFO=$(curl -sSL --max-time 5 "http://localhost:6060/debug/pprof/heap?debug=1" 2>/dev/null | grep -E "^#.*(HeapInuse|HeapIdle|HeapReleased|HeapAlloc)=" | sed 's/# //' | tr '\n' ' ' || echo "? ? ? ?")

    echo "$PID,$RSS,$HEAP_INFO,$ts" >> "${OUTDIR}/memory.csv"
    echo "[$ts] PID=$PID RSS=${RSS}kB $HEAP_INFO"
  else
    echo "[$ts] goopg not running"
  fi

  sleep 10
done
