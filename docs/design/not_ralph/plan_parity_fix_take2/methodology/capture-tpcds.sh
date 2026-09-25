#!/usr/bin/env bash
set -uo pipefail
PORT="$1"; OUT="$2"; HDR="$3"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
QDIR=/home/ryo/work/goopg/goopg/bench/tpcds/runtime_goopg/tpcds-data/queries
tmp="/tmp/pp2/parity-capture-$$.sql"
{ echo "# ${HDR}"; echo "# EXPLAIN only."; } > "$OUT"
for q in $(seq 1 99); do
  f="${QDIR}/query${q}.sql"; [ -f "$f" ] || continue
  echo "===== Q${q} =====" >> "$OUT"
  python3 - "$f" > "$tmp" <<'PY'
import sys
for s in open(sys.argv[1]).read().split(';'):
    if s.strip(): print("EXPLAIN "+s.strip()+";")
PY
  timeout 120 psql -h 127.0.0.1 -p "$PORT" -U postgres -d postgres -X \
    -c "SET work_mem='64MB'" -c "SET max_parallel_workers_per_gather=4" \
    -f "$tmp" 2>&1 | grep -vx SET >> "$OUT" || echo "(explain failed)" >> "$OUT"
done
rm -f "$tmp"
