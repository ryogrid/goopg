#!/usr/bin/env bash
# pgbench-wait-sample.sh — sample pg_stat_activity client backends during a
# pgbench simple-update run and aggregate wait events at the end.
#
# Companion harness for docs/design/not_ralph/pg-stat-activity-probes/.
# Samples every --interval ms (default 500), captures a concurrent pprof CPU
# profile from the server's debug endpoint, then prints the observed
# (state, wait_event_type, wait_event) distribution and a per-sample matrix.
#
# Usage:
#   scripts/pgbench-wait-sample.sh [--port 5533] [--db postgres] [--user postgres]
#       [--clients 50] [--jobs 8] [--duration 60] [--scale 10]
#       [--interval-ms 500] [--psql PATH] [--pgbench PATH] [--outdir DIR]
#       [--profile-addr 127.0.0.1:6060] [--no-profile]
set -euo pipefail

PORT=5533; DB=postgres; USER_=postgres
CLIENTS=50; JOBS=8; DURATION=60; SCALE=10; INTERVAL_MS=500
PSQL=psql; PGBENCH=pgbench
PROFILE_ADDR=127.0.0.1:6060
DO_PROFILE=1
OUTDIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT=$2; shift 2;;
    --db) DB=$2; shift 2;;
    --user) USER_=$2; shift 2;;
    --clients) CLIENTS=$2; shift 2;;
    --jobs) JOBS=$2; shift 2;;
    --duration) DURATION=$2; shift 2;;
    --scale) SCALE=$2; shift 2;;
    --interval-ms) INTERVAL_MS=$2; shift 2;;
    --psql) PSQL=$2; shift 2;;
    --pgbench) PGBENCH=$2; shift 2;;
    --outdir) OUTDIR=$2; shift 2;;
    --profile-addr) PROFILE_ADDR=$2; shift 2;;
    --no-profile) DO_PROFILE=0; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

[ -n "$OUTDIR" ] || OUTDIR="tmp/psa-sample-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUTDIR"
SAMPLES="$OUTDIR/samples.csv"

export PGHOST=127.0.0.1 PGPORT="$PORT" PGUSER="$USER_" PGDATABASE="$DB"

query() {
  "$PSQL" -X -q -A -t -v ON_ERROR_STOP=1 -c "$1"
}

echo "== pg_stat_activity probe sanity =="
query "select count(*) from pg_catalog.pg_stat_activity where application_name='pgbench'" >/dev/null \
  || { echo "pg_stat_activity not queryable" >&2; exit 1; }
echo "ok"

echo "header: sample,pid,state,wait_event_type,wait_event" > "$SAMPLES"

# --- launch CPU profile capture (runs for the whole benchmark) -------------
if [ "$DO_PROFILE" = 1 ]; then
  (
    curl -sS --max-time $((DURATION + 30)) \
      "http://${PROFILE_ADDR}/debug/pprof/profile?seconds=${DURATION}" \
      -o "$OUTDIR/cpu.pb.gz"
  ) &
  PROFILE_PID=$!
fi

# --- run pgbench simple-update in background -------------------------------
"$PGBENCH" -N -c "$CLIENTS" -j "$JOBS" -T "$DURATION" -s "$SCALE" \
  >"$OUTDIR/pgbench.out" 2>&1 &
PGBENCH_PID=$!

# --- sampling loop ----------------------------------------------------------
n=0
while kill -0 "$PGBENCH_PID" 2>/dev/null; do
  n=$((n + 1))
  query "select coalesce(pid::text,'-'),state,coalesce(wait_event_type,'NULL'),coalesce(wait_event,'NULL')
         from pg_catalog.pg_stat_activity
         where application_name='pgbench' and backend_type='client backend'" \
    | awk -v s="$n" -F'|' '{print s","$1","$2","$3","$4}' >> "$SAMPLES"
  sleep "$(awk "BEGIN{print $INTERVAL_MS/1000}")"
done

wait "$PGBENCH_PID"; PGBENCH_RC=$?
if [ "${PROFILE_PID:-}" ]; then
  # profile finishes ~ when the run does; give it a grace period
  for _ in $(seq 1 40); do kill -0 "$PROFILE_PID" 2>/dev/null || break; sleep 1; done
  kill "$PROFILE_PID" 2>/dev/null || true
fi

# --- aggregate --------------------------------------------------------------
echo
echo "== pgbench summary ($CLIENTS clients, -T ${DURATION}s, -s $SCALE) =="
grep -E 'number of transactions|tps|failed' "$OUTDIR/pgbench.out" | head -5 || true

echo
echo "== wait-event distribution over $n samples (client backends only) =="
( echo "samples|state|wait_event_type|wait_event"
  tail -n +2 "$SAMPLES" | cut -d',' -f3- | sort | uniq -c | sort -rn \
    | while IFS=' ' read -r cnt rest; do printf '%s|%s\n' "$cnt" "$(echo "$rest" | tr ',' '|')"; done
) | column -t -s'|'

echo
echo "== per-state share of backend-samples =="
total=$(tail -n +2 "$SAMPLES" | wc -l)
tail -n +2 "$SAMPLES" | cut -d',' -f3 | sort | uniq -c | sort -rn \
  | while read -r c st; do
      [ -n "$st" ] && awk "BEGIN{printf \"%-24s %6d / $total  (%.1f%%)\n\",\"$st\",$c,$c*100/$total}"
    done

echo
echo "raw samples: $SAMPLES"
[ "$DO_PROFILE" = 1 ] && echo "cpu profile: $OUTDIR/cpu.pb.gz  (go tool pprof bin/goopg $OUTDIR/cpu.pb.gz)"
exit "$PGBENCH_RC"
