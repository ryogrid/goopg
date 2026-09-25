#!/usr/bin/env bash
set -uo pipefail
PORT="$1"; OUT="$2"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
: > "$OUT"
for n in $(seq 1 22); do
  f=/tmp/parity-r0/queries/tpch/Q${n}.sql; [ -f "$f" ] || continue
  d=$(timeout 900 psql -h 127.0.0.1 -p "$PORT" -U tpch -d tpch -X -A -t -f "$f" 2>&1); rc=$?
  printf 'Q%-3s rc=%d rows=%d md5=%s\n' "$n" "$rc" "$(printf '%s' "$d"|grep -c .)" "$(printf '%s' "$d"|md5sum|cut -d' ' -f1)" >> "$OUT"
done
