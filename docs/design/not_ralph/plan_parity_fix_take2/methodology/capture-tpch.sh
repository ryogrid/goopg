#!/usr/bin/env bash
set -uo pipefail
PORT="$1"; DB="$2"; USER="$3"; OUT="$4"; HDR="$5"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
Q=/tmp/parity-r0/queries/tpch
tmp="/tmp/pp2/parity-capture-$$.sql"
: > "$OUT"; echo "# ${HDR}" >> "$OUT"
run(){ echo "=== $1" >> "$OUT"
  timeout 180 psql -h 127.0.0.1 -p "$PORT" -U "$USER" -d "$DB" -X \
    -c "SET work_mem='64MB'" -c "SET max_parallel_workers_per_gather=4" -f "$2" 2>&1 | grep -vx SET >> "$OUT" \
    || echo "(capture failed)" >> "$OUT"; }
for n in $(seq 1 22); do
  if [ "$n" = "15" ]; then run "Q15a-VIEWBODY" /tmp/parity-r0/q15a.sql; continue; fi
  f="$Q/Q${n}.sql"; [ -f "$f" ] || continue
  { echo -n "EXPLAIN "; cat "$f"; } > "$tmp"; run "Q${n}" "$tmp"
done
rm -f "$tmp"
