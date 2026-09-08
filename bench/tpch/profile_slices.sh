#!/usr/bin/env bash
# profile_slices.sh — EX0-04 per-operator timing harness collection.
#
# Collects, for one query shape, the full EX0-02 §4 capture set plus the
# EXPLAIN ANALYZE text the slice mapping cross-checks against:
#   heap/block/mutex before+after (window deltas), cpu.pb.gz over SECS,
#   perf stat (user-mode, no trailing `--`), 500 ms wait-event sampling,
#   pinned value-identity check (diff with `set -e`, stricter than ab.sh).
#
# Design: docs/design/executor-ex0-04-harness/DESIGN.md. Protocol header
# fields are echoed into header.txt by the caller (see EX0-02 DESIGN §2);
# this script captures numbers only, never claims.
#
# Usage:
#   TAG=q6-serial SECS=40 QSQL=tmp/take4/q06.sql SERIAL=1 PPROF=6161 \
#     bench/tpch/profile_slices.sh OUTDIR
# Env:
#   TAG      run label (required)
#   QSQL     file with the query text (required)
#   OUTDIR   positional: output directory (required)
#   SECS     capture window, default 40
#   SERIAL   1 → `SET max_parallel_workers_per_gather = 0`, default 1
#   PGPORT   default 65433
#   PPROF    pprof port, default 6161
#   VALUEPIN optional file with the pinned single-row value; when set the
#            run fails loudly on mismatch (harness self-check, not a gate)
set -euo pipefail
TAG="${TAG:?set TAG}"; QSQL="${QSQL:?set QSQL}"; OUTDIR="${1:?outdir}"
SECS="${SECS:-40}"; SERIAL="${SERIAL:-1}"
PGPORT="${PGPORT:-65433}"; PPROF="${PPROF:-6161}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export PATH="$REPO/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$REPO/postgres/local_install/lib"
mkdir -p "$OUTDIR"
Q="$(cat "$QSQL")"
if [[ "$SERIAL" == "1" ]]; then
  PSQLOPTS=(-c "SET max_parallel_workers_per_gather = 0;")
else
  PSQLOPTS=()
fi
PSQL=(psql -h 127.0.0.1 -p "$PGPORT" -U tpch -d tpch -At "${PSQLOPTS[@]}")
PID="$(head -1 "$REPO/bench/tpch/runtime_goopg/data/postmaster.pid")"

echo "tag=$TAG secs=$SECS serial=$SERIAL server_pid=$PID" | tee "$OUTDIR/header.txt"
date -u +%FT%TZ >>"$OUTDIR/header.txt"

# Value identity (harness self-check): single execution, pinned diff.
"${PSQL[@]}" -c "$Q" >"$OUTDIR/value.txt"
if [[ -n "${VALUEPIN:-}" ]]; then
  diff -u "$VALUEPIN" "$OUTDIR/value.txt"
fi

# Node-time cross-check source (serial only per DESIGN §4).
"${PSQL[@]}" -c "EXPLAIN (ANALYZE, TIMING ON) $Q" >"$OUTDIR/explain-analyze.txt"

# Warmup so the capture window is steady-state (uncounted).
"${PSQL[@]}" -c "$Q" >/dev/null

curl -s "http://127.0.0.1:$PPROF/debug/pprof/heap" -o "$OUTDIR/heap.before.pb.gz"
curl -s "http://127.0.0.1:$PPROF/debug/pprof/block" -o "$OUTDIR/block.before.pb.gz"
curl -s "http://127.0.0.1:$PPROF/debug/pprof/mutex" -o "$OUTDIR/mutex.before.pb.gz"

( end=$((SECS+8)); t0=$SECONDS
  while (( SECONDS-t0 < end )); do "${PSQL[@]}" -c "$Q" >/dev/null 2>&1; done ) &
LOADPID=$!
sleep 2

( echo "ts,pid,state,wait_event_type,wait_event,query"
  t0=$SECONDS
  while (( SECONDS-t0 < SECS )); do
    "${PSQL[@]}" -F, -c "select now(),pid,state,coalesce(wait_event_type,''),coalesce(wait_event,''),left(replace(query,',',' '),40) from pg_stat_activity where pid <> pg_backend_pid()" 2>/dev/null
    sleep 0.5
  done ) >"$OUTDIR/waitevents.csv" 2>&1 &
WEPID=$!

if command -v perf >/dev/null; then
  perf stat -p "$PID" -e task-clock:u,cycles:u,instructions:u,branch-misses:u,cache-misses:u \
    sleep "$SECS" >"$OUTDIR/perf-stat.txt" 2>&1 &
  PERFPID=$!
fi

curl -s "http://127.0.0.1:$PPROF/debug/pprof/profile?seconds=$SECS" -o "$OUTDIR/cpu.pb.gz"

wait "$WEPID" 2>/dev/null
kill "$LOADPID" 2>/dev/null; wait "$LOADPID" 2>/dev/null || true
[ -n "${PERFPID:-}" ] && wait "$PERFPID" 2>/dev/null || true

curl -s "http://127.0.0.1:$PPROF/debug/pprof/heap" -o "$OUTDIR/heap.after.pb.gz"
curl -s "http://127.0.0.1:$PPROF/debug/pprof/block" -o "$OUTDIR/block.after.pb.gz"
curl -s "http://127.0.0.1:$PPROF/debug/pprof/mutex" -o "$OUTDIR/mutex.after.pb.gz"
echo "COLLECTED $TAG → $OUTDIR"
