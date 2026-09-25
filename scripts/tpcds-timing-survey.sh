#!/usr/bin/env bash
# tpcds-timing-survey.sh PORT OUT — TPC-DS SF0.25 timing survey (goopg).
# Per OK query: 3 timed runs (client wall-clock), values checksum vs PG
# oracle on the first run, PG oracle secs alongside. TIMEOUT=300/run.
# usage: scripts/tpcds-timing-survey.sh 5561 /tmp/timing.txt
# (R97 timing survey; harness committed per request 2026-09-13.)
set -uo pipefail
PORT="$1"; OUT="$2"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
QDIR=$REPO/bench/tpcds/runtime_goopg/tpcds-data/queries
ORACLE=$REPO/bench/tpcds/runtime_goopg/tpcds-results-sf025/oracle.txt
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
: > "$OUT"
echo "# goopg TPC-DS SF0.25 timing survey — $(date -Iseconds)" >> "$OUT"
echo "# format: q|goopg_med_s|goopg_runs|pg_s|rows|ck|verdict" >> "$OUT"
for q in $(seq 1 99); do
  line=$(grep -E "^${q}\|" "$ORACLE" || true)
  st=$(cut -d'|' -f2 <<<"$line")
  if [[ "$st" != "OK" ]]; then echo "Q${q}|SKIP" >> "$OUT"; continue; fi
  orows=$(cut -d'|' -f3 <<<"$line"); ock=$(cut -d'|' -f4 <<<"$line"); pg=$(cut -d'|' -f5 <<<"$line")
  qf="$QDIR/query${q}.sql"
  times=(); ok=1; grows=0; gck=""
  for i in 1 2 3; do
    res=$(mktemp)
    t0=$(date +%s.%N)
    if ! timeout 300 psql "host=127.0.0.1 port=$PORT dbname=postgres user=postgres" -X -q -f "$qf" > "$res" 2>&1; then
      echo "Q${q}|TIMEOUT" >> "$OUT"; ok=0; rm -f "$res"; break
    fi
    t1=$(date +%s.%N)
    times+=($(echo "$t1 $t0" | awk '{printf "%.2f", $1-$2}'))
    if [[ $i == 1 ]]; then
      ckout=$(python3 "$REPO/scripts/tpcds-result-checksum.py" "$res" 2>/dev/null || echo "rows=0 ck=ck-err")
      grows=$(sed -n 's/.*rows=\([0-9]*\).*/\1/p' <<<"$ckout" | head -1)
      gck=$(sed -n 's/.*ck=\([^ ]*\).*/\1/p' <<<"$ckout" | head -1)
      grows=${grows:-0}; gck=${gck:-ck-err}
    fi
    rm -f "$res"
  done
  [[ $ok == 0 ]] && continue
  med=$(printf '%s\n' "${times[@]}" | sort -n | sed -n '2p')
  verdict="PASS"
  [[ "$grows" != "$orows" ]] && verdict="ROWMISMATCH"
  if [[ "$ock" != "n/a" && "$gck" != "$ock" ]]; then verdict="CKMISMATCH"; fi
  echo "Q${q}|${med}|${times[0]},${times[1]},${times[2]}|${pg}|${grows}/${orows}|${gck}/${ock}|${verdict}" >> "$OUT"
  echo "Q${q} med=${med}s pg=${pg}s rows=${grows}/${orows} ${verdict}"
done
