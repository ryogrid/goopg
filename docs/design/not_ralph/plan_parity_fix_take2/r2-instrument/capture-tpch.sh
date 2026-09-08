#!/usr/bin/env bash
# Capture TPC-H plan sections from a goopg or PG server, with the
# planner-visible session GUCs PINNED so a cluster's ambient config cannot
# decide the comparison (R2: the TPC-DS pair was running 512MB vs 4MB).
# The `SET` command tags are stripped — they parse as a plan node named SET.
# usage: capture-tpch.sh <port> <db> <user> <outfile> <header>
set -uo pipefail
PORT="$1"; DB="$2"; USER="$3"; OUT="$4"; HDR="$5"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
Q=/tmp/parity-r0/queries/tpch
PIN=(-c "SET work_mem='64MB'" -c "SET max_parallel_workers_per_gather=4")
: > "$OUT"
echo "# ${HDR}" >> "$OUT"
run() {
    echo "=== $1" >> "$OUT"
    if ! timeout 180 psql -h 127.0.0.1 -p "$PORT" -U "$USER" -d "$DB" -X \
            "${PIN[@]}" -f "$2" 2>&1 | grep -vx SET >> "$OUT"; then
        echo "(capture failed)" >> "$OUT"
    fi
}
# Fixed name, not mktemp: the temp path appears in psql ERROR text for
# the unplannable queries, so a random name fakes a plan diff between
# two otherwise identical captures (seen in R9).
tmp="${TMPDIR:-/tmp}/parity-capture-$$.sql"
for n in $(seq 1 22); do
    if [ "$n" = "15" ]; then run "Q15a-VIEWBODY" /tmp/parity-r0/q15a.sql; continue; fi
    f="$Q/Q${n}.sql"
    if [ ! -f "$f" ]; then echo "=== Q${n}" >> "$OUT"; echo "MISSING QUERY FILE" >> "$OUT"; continue; fi
    { echo -n "EXPLAIN "; cat "$f"; } > "$tmp"
    run "Q${n}" "$tmp"
done
rm -f "$tmp"
