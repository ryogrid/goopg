#!/usr/bin/env bash
# pprof_collect.sh <out_prefix> <window_seconds>
#
# Captures all 8 pprof profile types within the given window:
#   cpu, trace, heap, allocs, goroutine, mutex (base + final), block (base + final), threadcreate
#
# Schedule (T = window start):
#   T+0     : sleep warmup (30s for window >= 120s, else 2s)
#   T+warmup: snapshot mutex/block as baseline (for `pprof -base`)
#   T+warmup: launch CPU profile (window - 2*warmup) and trace (min(30,...))
#   T+warmup+trace_secs: snapshot goroutine (debug=2)
#   T+window-warmup: snapshot heap, allocs, mutex.final, block.final, threadcreate
#
# Requires `curl`. Endpoint is the hardcoded http://127.0.0.1:6060/debug/pprof in cmd/goopg/main.go.

set -uo pipefail

PREFIX="${1:?usage: pprof_collect.sh <out_prefix> <window_seconds>}"
WIN="${2:?usage: pprof_collect.sh <out_prefix> <window_seconds>}"
URL="${PPROF_URL:-http://127.0.0.1:6160/debug/pprof}"

if (( WIN >= 120 )); then
  WARMUP=30
  TRACE_SECS=30
elif (( WIN >= 60 )); then
  WARMUP=10
  TRACE_SECS=20
else
  WARMUP=2
  TRACE_SECS=$(( WIN / 3 ))
fi
# CPU profile spans warmup..(WIN-warmup); minimum 5s
CPU_SECS=$(( WIN - 2*WARMUP ))
(( CPU_SECS < 5 )) && CPU_SECS=5

mkdir -p "$(dirname "$PREFIX")"

log() { printf '[pprof_collect %s] %s\n' "$(basename "$PREFIX")" "$*" >&2; }

log "warmup=${WARMUP}s cpu=${CPU_SECS}s trace=${TRACE_SECS}s win=${WIN}s"
sleep "$WARMUP"

# Baseline mutex/block (cumulative since process start)
curl -fsS --max-time 30 -o "${PREFIX}.mutex_base.pb.gz" "${URL}/mutex"  || log "mutex_base failed"
curl -fsS --max-time 30 -o "${PREFIX}.block_base.pb.gz" "${URL}/block"  || log "block_base failed"

# Long-running captures in parallel
curl -fsS --max-time "$(( CPU_SECS + 30 ))" -o "${PREFIX}.cpu.pb.gz" \
  "${URL}/profile?seconds=${CPU_SECS}" &
CPU_PID=$!

curl -fsS --max-time "$(( TRACE_SECS + 30 ))" -o "${PREFIX}.trace.out" \
  "${URL}/trace?seconds=${TRACE_SECS}" &
TRACE_PID=$!

# After trace ends, dump goroutines (full stacks)
if wait "$TRACE_PID"; then
  log "trace done"
else
  log "trace failed (exit $?)"
fi
curl -fsS --max-time 30 -o "${PREFIX}.goroutine.txt" "${URL}/goroutine?debug=2" \
  || log "goroutine snap failed"

# Wait for CPU profile
if wait "$CPU_PID"; then
  log "cpu profile done"
else
  log "cpu profile failed (exit $?)"
fi

# Final snapshots
curl -fsS --max-time 30 -o "${PREFIX}.heap.pb.gz"          "${URL}/heap"          || log "heap snap failed"
curl -fsS --max-time 30 -o "${PREFIX}.allocs.pb.gz"        "${URL}/allocs"        || log "allocs snap failed"
curl -fsS --max-time 30 -o "${PREFIX}.mutex.pb.gz"         "${URL}/mutex"         || log "mutex snap failed"
curl -fsS --max-time 30 -o "${PREFIX}.block.pb.gz"         "${URL}/block"         || log "block snap failed"
curl -fsS --max-time 30 -o "${PREFIX}.threadcreate.txt"    "${URL}/threadcreate?debug=1" || log "threadcreate snap failed"

log "done"
