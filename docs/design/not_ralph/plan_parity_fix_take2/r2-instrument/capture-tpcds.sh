#!/usr/bin/env bash
# Capture TPC-DS plan sections (sf05_capture_plans format, normalised to
# `=== Qn`), GUCs pinned as in capture-tpch.sh.
# usage: capture-tpcds.sh <port> <db> <user> <outfile> <header>
set -uo pipefail
PORT="$1"; DB="$2"; USER="$3"; OUT="$4"; HDR="$5"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
QDIR=/home/ryo/work/goopg/goopg/bench/tpcds/runtime_goopg/tpcds-data/queries
PIN=(-c "SET work_mem='64MB'" -c "SET max_parallel_workers_per_gather=4")
tmp=$(mktemp)
{ echo "# ${HDR}"; echo "# EXPLAIN only (no ANALYZE)."; } > "$OUT"
for q in $(seq 1 99); do
    f="${QDIR}/query${q}.sql"; [ -f "$f" ] || continue
    echo "=== Q${q}" >> "$OUT"
    python3 - "$f" > "$tmp" <<'PY'
import sys
src = open(sys.argv[1]).read()
for stmt in src.split(';'):
    if stmt.strip():
        print("EXPLAIN " + stmt.strip() + ";")
PY
    if ! timeout 120 psql -h 127.0.0.1 -p "$PORT" -U "$USER" -d "$DB" -X \
            "${PIN[@]}" -f "$tmp" 2>&1 | grep -vx SET >> "$OUT"; then
        echo "(explain failed)" >> "$OUT"
    fi
done
rm -f "$tmp"
